// Package cortex provides an adapter between Felix and the Cortex knowledge
// graph library. It handles initialization, conversation ingestion, and
// formatting recall results for the agent system prompt.
package cortex

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/connector/conversation"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	"github.com/sausheong/cortex/extractor/llmext"
	cortexanthropic "github.com/sausheong/cortex/llm/anthropic"
	cortexoai "github.com/sausheong/cortex/llm/openai"
	"github.com/sausheong/felix/internal/config"
)

// ingestWG tracks in-flight background ingest goroutines.
// Call Drain() before closing the Cortex database to ensure all writes complete.
var ingestWG sync.WaitGroup

// IngestThreadAsync queues a conversation thread for background ingestion into
// the Cortex knowledge graph. Call Drain() before closing the database to
// ensure all queued writes complete.
func IngestThreadAsync(ctx context.Context, cx *cortex.Cortex, thread []conversation.Message) {
	ingestWG.Go(func() {
		IngestThread(ctx, cx, thread)
	})
}

// Drain waits for all in-flight IngestThreadAsync calls to complete.
// Call this before closing the Cortex database (cx.Close()).
func Drain() {
	ingestWG.Wait()
}

// Init opens (or creates) a Cortex knowledge graph using the provided config.
// apiKey is the API key for the configured provider, looked up by the caller
// from cfg.Providers[cfg.Cortex.Provider].
func Init(cfg config.CortexConfig, apiKey string) (*cortex.Cortex, error) {
	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(config.DefaultDataDir(), "brain.db")
	}

	var opts []cortex.Option

	if apiKey != "" {
		model := cfg.LLMModel
		provider := cfg.Provider
		if provider == "" {
			provider = "openai"
		}

		detExt := deterministic.New()

		switch provider {
		case "anthropic":
			if model == "" {
				model = "claude-sonnet-4-5-20250929"
			}
			llm := cortexanthropic.NewLLM(apiKey, cortexanthropic.WithModel(model))
			extractor := hybrid.New(detExt, llmext.New(llm))
			opts = append(opts,
				cortex.WithLLM(llm),
				cortex.WithExtractor(extractor),
			)
		default: // "openai"
			if model == "" {
				model = "gpt-5.4-mini"
			}
			llm := cortexoai.NewLLM(apiKey, cortexoai.WithModel(model))
			embedder := cortexoai.NewEmbedder(apiKey)
			extractor := hybrid.New(detExt, llmext.New(llm))
			opts = append(opts,
				cortex.WithLLM(llm),
				cortex.WithEmbedder(embedder),
				cortex.WithExtractor(extractor),
			)
		}
	}

	cx, err := cortex.Open(dbPath, opts...)
	if err != nil {
		return nil, fmt.Errorf("cortex init: %w", err)
	}

	slog.Info("cortex knowledge graph initialized", "db", dbPath)
	return cx, nil
}

// minIngestLen is the minimum combined length of user+assistant text
// required to trigger ingestion. Short exchanges like "ok", "thanks",
// greetings, and simple confirmations are skipped.
const minIngestLen = 100

// trivialPhrases are exact-match messages (lowercased) that are never
// worth ingesting regardless of length.
var trivialPhrases = map[string]bool{
	"ok":           true,
	"okay":         true,
	"thanks":       true,
	"thank you":    true,
	"yes":          true,
	"no":           true,
	"sure":         true,
	"got it":       true,
	"understood":   true,
	"hi":           true,
	"hello":        true,
	"hey":          true,
	"bye":          true,
	"goodbye":      true,
	"good morning": true,
	"good night":   true,
}

// ShouldIngest returns true if the conversation thread contains enough
// substance to be worth storing in the knowledge graph.
func ShouldIngest(thread []conversation.Message) bool {
	if len(thread) == 0 {
		return false
	}
	// Skip if the first user message is a trivial phrase.
	if thread[0].Role == "user" && trivialPhrases[strings.ToLower(strings.TrimSpace(thread[0].Content))] {
		return false
	}
	// Require at least one assistant message and enough combined content.
	total := 0
	hasAssistant := false
	for _, m := range thread {
		total += len(strings.TrimSpace(m.Content))
		if m.Role == "assistant" {
			hasAssistant = true
		}
	}
	return hasAssistant && total >= minIngestLen
}

// IngestThread feeds a completed conversation thread into the Cortex knowledge
// graph. The thread should contain all messages for the exchange: user message,
// tool calls (as assistant messages), tool results (as user messages), and the
// final assistant reply. It skips trivial or short threads.
// It runs synchronously; callers should run it in a goroutine if they
// don't want to block.
func IngestThread(ctx context.Context, cx *cortex.Cortex, thread []conversation.Message) {
	if !ShouldIngest(thread) {
		slog.Debug("cortex: skipping trivial thread ingest", "len", len(thread))
		return
	}
	conn := conversation.New()
	if err := conn.Ingest(ctx, cx, thread); err != nil {
		slog.Warn("cortex: thread ingest failed", "error", err)
	}
}

// CortexHint is injected into the system prompt when Cortex is enabled so the
// agent knows it has a persistent knowledge graph backing its memory.
const CortexHint = `

You have access to Cortex, a persistent knowledge graph that automatically stores and retrieves knowledge across conversations. Cortex extracts entities (people, organizations, places, concepts), relationships between them, and factual memories from every conversation.

How Cortex works for you:
- AUTOMATIC STORAGE: After each conversation turn, entities, relationships, and facts are automatically extracted and stored. You do not need to do anything to save knowledge.
- AUTOMATIC RETRIEVAL: Before each response, Cortex searches its knowledge graph for information relevant to the user's message. Results appear below under "Cortex Knowledge Graph".
- CORTEX FIRST — ALWAYS: Before using any tool (web_fetch, web_search, bash, read_file, or any other), check whether the "Cortex Knowledge Graph" section below already contains the answer. Only reach for a tool if Cortex does not have sufficient information.
- USE THE CONTEXT: When Cortex results appear, incorporate that knowledge naturally into your response. Reference what you know about people, organizations, past conversations, and relationships.
- CONNECT THE DOTS: If a user mentions a person or topic that Cortex has data on, proactively surface relevant connections and context — don't wait to be asked.
- ACKNOWLEDGE MEMORY: When you use Cortex knowledge, you can say things like "From our previous conversations..." or "I recall that..." to indicate you remember.`

// FormatResults formats Cortex recall results for injection into the agent
// system prompt, similar to memory.FormatForPrompt.
func FormatResults(results []cortex.Result) string {
	if len(results) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n## Cortex Knowledge Graph\n\nThe following knowledge was retrieved from your knowledge graph and is relevant to the current message:\n\n")

	for _, r := range results {
		switch r.Type {
		case "entity":
			b.WriteString("- [entity] ")
		case "memory":
			b.WriteString("- [memory] ")
		case "chunk":
			b.WriteString("- [context] ")
		default:
			fmt.Fprintf(&b, "- [%s] ", r.Type)
		}
		content := r.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		b.WriteString(content)
		if r.Source != "" {
			b.WriteString(" (source: ")
			b.WriteString(r.Source)
			b.WriteString(")")
		}
		b.WriteString("\n")
	}

	return b.String()
}
