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
	"time"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/connector/conversation"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	"github.com/sausheong/cortex/extractor/llmext"
	cortexanthropic "github.com/sausheong/cortex/llm/anthropic"
	cortexoai "github.com/sausheong/cortex/llm/openai"
	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
	localpkg "github.com/sausheong/felix/internal/local"
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

// resolveCortexModel returns the (provider, model) cortex.Init should use.
// When both cfg.Provider and cfg.LLMModel are empty, it mirrors the default
// agent's model (e.g. "local/gemma4:latest" → "local", "gemma4:latest").
// Otherwise it returns the explicit values verbatim — no half-mirroring.
func resolveCortexModel(cfg config.CortexConfig, defaultAgentModel string) (provider, model string) {
	if cfg.Provider == "" && cfg.LLMModel == "" {
		return llm.ParseProviderModel(defaultAgentModel)
	}
	return cfg.Provider, cfg.LLMModel
}

// Init opens (or creates) a Cortex knowledge graph using the provided config.
// When cfg.Provider and cfg.LLMModel are both empty, the function mirrors
// defaultAgentModel: e.g. "local/gemma4:latest" wires cortex through bundled
// Ollama with the same model the default agent uses. getProvider is used to
// look up the resolved provider's API key + base URL.
func Init(cfg config.CortexConfig, memCfg config.MemoryConfig, defaultAgentModel string, getProvider func(name string) config.ProviderConfig) (*cortex.Cortex, error) {
	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(config.DefaultDataDir(), "brain.db")
	}

	provider, model := resolveCortexModel(cfg, defaultAgentModel)
	pcfg := getProvider(provider)
	apiKey := pcfg.APIKey
	baseURL := pcfg.BaseURL
	slog.Info("cortex auto-mirror",
		"agent_model", defaultAgentModel,
		"resolved_provider", provider,
		"resolved_model", model)

	var opts []cortex.Option

	// "local" needs no API key (bundled Ollama). Other providers require one
	// to enable LLM-backed extraction; without a key cortex runs deterministic-
	// only via cortex.Open's default extractor.
	if provider == "local" || apiKey != "" {
		detExt := deterministic.New()

		switch provider {
		case "local":
			// Deterministic-only extraction for local. Small local models
			// (gemma4, qwen, llama3.2) frequently return malformed JSON for
			// the structured-extraction prompt, and each failed call can tie
			// up Ollama for 30–90s — blocking both /api/embeddings and the
			// next user chat turn since Ollama serializes per-model. Until
			// the cortex library learns Ollama JSON-mode constraint, this
			// is the right trade-off for the bundled deployment.
			//
			// Users who want LLM-augmented extraction can set
			// cortex.provider = "anthropic" or "openai" in felix.json5.
			embModel, embDims := localpkg.EmbeddingDims(memCfg.EmbeddingModel)
			embedder := cortexoai.NewEmbedder("",
				cortexoai.WithEmbedderBaseURL(baseURL),
				cortexoai.WithEmbeddingModel(goopenai.EmbeddingModel(embModel), embDims))
			opts = append(opts,
				cortex.WithEmbedder(embedder),
				cortex.WithExtractor(detExt),
			)

		case "anthropic":
			if model == "" {
				model = "claude-sonnet-4-5-20250929"
			}
			llmClient := cortexanthropic.NewLLM(apiKey, cortexanthropic.WithModel(model))
			extractor := hybrid.New(detExt, llmext.New(llmClient))
			opts = append(opts,
				cortex.WithLLM(llmClient),
				cortex.WithExtractor(extractor),
			)

		default: // "openai" and any unknown provider
			if model == "" {
				model = "gpt-5.4-mini"
			}
			llmClient := cortexoai.NewLLM(apiKey, cortexoai.WithModel(model))
			embedder := cortexoai.NewEmbedder(apiKey)
			extractor := hybrid.New(detExt, llmext.New(llmClient))
			opts = append(opts,
				cortex.WithLLM(llmClient),
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
// required to trigger ingestion. Short exchanges (one-line acknowledgements,
// "OK", "thanks", brief confirmations) are skipped because they cost an
// embed call apiece without producing recall-worthy content. Bumped from
// 100 → 300 after observing low-value ingests crowding the graph.
const minIngestLen = 300

// minRecallLen is the minimum trimmed user message length to trigger Cortex
// recall. Short messages either are trivial phrases (filtered separately) or
// don't carry enough context for a useful semantic match.
const minRecallLen = 12

// maxIngestLen caps the total characters per ingest. nomic-embed-text has
// an 8192-token context window (~30 KB chars). We reject larger threads
// to avoid the "input length exceeds the context length" embed error and
// to bound the time the embedder is tied up.
const maxIngestLen = 28000

// ingestTimeout is the hard cap on a single IngestThread call. Even with
// deterministic-only extraction the embedder still runs N HTTP calls per
// chunk, so a runaway thread shouldn't be able to block forever.
const ingestTimeout = 30 * time.Second

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

// ShouldRecall returns true if the user message is substantial enough to
// be worth a Cortex recall. Trivial phrases ("ok", "thanks", greetings) and
// very short messages are skipped so they don't cost an embed call apiece.
// Symmetric to ShouldIngest on the write side.
func ShouldRecall(userMsg string) bool {
	trimmed := strings.TrimSpace(userMsg)
	if trimmed == "" {
		return false
	}
	if trivialPhrases[strings.ToLower(trimmed)] {
		return false
	}
	if len(trimmed) < minRecallLen {
		return false
	}
	return true
}

// ShouldIngest returns true if the conversation thread contains enough
// substance to be worth storing in the knowledge graph, but isn't so large
// that the embedder would reject it.
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
	if !hasAssistant || total < minIngestLen {
		return false
	}
	if total > maxIngestLen {
		slog.Debug("cortex: skipping oversized thread", "chars", total, "cap", maxIngestLen)
		return false
	}
	return true
}

// IngestThread feeds a completed conversation thread into the Cortex knowledge
// graph. The thread should contain all messages for the exchange: user message,
// tool calls (as assistant messages), tool results (as user messages), and the
// final assistant reply. It skips trivial, short, or oversized threads.
// A hard ingestTimeout caps each call so a stuck extractor or embedder
// can't tie up Ollama capacity indefinitely. It runs synchronously;
// callers should run it in a goroutine if they don't want to block.
func IngestThread(ctx context.Context, cx *cortex.Cortex, thread []conversation.Message) {
	if !ShouldIngest(thread) {
		slog.Debug("cortex: skipping ingest (trivial, too small, or too large)", "len", len(thread))
		return
	}
	ingestCtx, cancel := context.WithTimeout(ctx, ingestTimeout)
	defer cancel()
	start := time.Now()
	conn := conversation.New()
	if err := conn.Ingest(ingestCtx, cx, thread); err != nil {
		if ingestCtx.Err() == context.DeadlineExceeded {
			slog.Warn("cortex: thread ingest timed out", "after_ms", time.Since(start).Milliseconds(), "cap_ms", ingestTimeout.Milliseconds())
		} else {
			slog.Warn("cortex: thread ingest failed", "error", err, "dur_ms", time.Since(start).Milliseconds())
		}
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
