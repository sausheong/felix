// Package cortex provides an adapter between GoClaw and the Cortex knowledge
// graph library. It handles initialization, conversation ingestion, and
// formatting recall results for the agent system prompt.
package cortex

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/connector/conversation"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	"github.com/sausheong/cortex/extractor/llmext"
	cortexoai "github.com/sausheong/cortex/llm/openai"
	"github.com/sausheong/goclaw/internal/config"
)

// Init opens (or creates) a Cortex knowledge graph using the provided config
// and OpenAI API key. The API key is used for both LLM extraction and
// embedding via the cortex/llm/openai package.
func Init(cfg config.CortexConfig, openaiAPIKey string) (*cortex.Cortex, error) {
	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(config.DefaultDataDir(), "brain.db")
	}

	var opts []cortex.Option

	if openaiAPIKey != "" {
		model := cfg.LLMModel
		if model == "" {
			model = "gpt-5.4-mini"
		}

		llm := cortexoai.NewLLM(openaiAPIKey, cortexoai.WithModel(model))
		embedder := cortexoai.NewEmbedder(openaiAPIKey)
		detExt := deterministic.New()
		llmExt := llmext.New(llm)
		extractor := hybrid.New(detExt, llmExt)

		opts = append(opts,
			cortex.WithLLM(llm),
			cortex.WithEmbedder(embedder),
			cortex.WithExtractor(extractor),
		)
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

// ShouldIngest returns true if the conversation exchange contains enough
// substance to be worth storing in the knowledge graph.
func ShouldIngest(userMsg, assistantReply string) bool {
	// Skip if user message is a trivial phrase.
	if trivialPhrases[strings.ToLower(strings.TrimSpace(userMsg))] {
		return false
	}

	// Skip if combined text is too short.
	combined := len(strings.TrimSpace(userMsg)) + len(strings.TrimSpace(assistantReply))
	if combined < minIngestLen {
		return false
	}

	return true
}

// IngestConversation feeds a completed user+assistant exchange into the
// Cortex knowledge graph. It skips trivial or short exchanges.
// It runs synchronously; callers should run it in a goroutine if they
// don't want to block.
func IngestConversation(ctx context.Context, cx *cortex.Cortex, userMsg, assistantReply string) {
	if !ShouldIngest(userMsg, assistantReply) {
		slog.Debug("cortex: skipping trivial conversation ingest",
			"user_len", len(userMsg), "assistant_len", len(assistantReply))
		return
	}

	conn := conversation.New()
	msgs := []conversation.Message{
		{Role: "user", Content: userMsg},
		{Role: "assistant", Content: assistantReply},
	}
	if err := conn.Ingest(ctx, cx, msgs); err != nil {
		slog.Warn("cortex: conversation ingest failed", "error", err)
	}
}

// CortexHint is injected into the system prompt when Cortex is enabled so the
// agent knows it has a persistent knowledge graph backing its memory.
const CortexHint = `

You have access to Cortex, a persistent knowledge graph that automatically stores and retrieves knowledge across conversations. Cortex extracts entities (people, organizations, places, concepts), relationships between them, and factual memories from every conversation.

How Cortex works for you:
- AUTOMATIC STORAGE: After each conversation turn, entities, relationships, and facts are automatically extracted and stored. You do not need to do anything to save knowledge.
- AUTOMATIC RETRIEVAL: Before each response, Cortex searches its knowledge graph for information relevant to the user's message. Results appear below under "Cortex Knowledge Graph".
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
			b.WriteString(fmt.Sprintf("- [%s] ", r.Type))
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
