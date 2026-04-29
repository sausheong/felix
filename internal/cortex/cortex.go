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

// resolveCortexModel chooses the (provider, model) for a given chat agent.
// If cfg.Provider AND cfg.LLMModel are both set, that's a hard pin and is
// used regardless of the agent. Otherwise cortex mirrors the chatting
// agent's model so its LLM extraction stays consistent with the conversation
// (e.g. chatting with anthropic/sonnet → cortex extracts via Sonnet, not
// whatever model the *default* agent happens to use).
func resolveCortexModel(cfg config.CortexConfig, agentModel string) (provider, model string) {
	if cfg.Provider != "" && cfg.LLMModel != "" {
		return cfg.Provider, cfg.LLMModel
	}
	return llm.ParseProviderModel(agentModel)
}

// Provider builds and caches per-agent *cortex.Cortex clients, all sharing
// the same SQLite DB path. SQLite WAL mode permits multiple connection pools
// against the same file, so each agent gets a client wired to its own LLM
// extractor without interfering with the others.
type Provider struct {
	dbPath      string
	cfg         config.CortexConfig
	memCfg      config.MemoryConfig
	getProvider func(string) config.ProviderConfig

	mu      sync.Mutex
	clients map[string]*cortex.Cortex // key: "provider/model"
}

// NewProvider returns a factory that lazily builds a *cortex.Cortex per
// chatting agent. Call For(agentModel) at chat time; the same agentModel
// always returns the same client (so per-instance caches stay warm).
func NewProvider(cfg config.CortexConfig, memCfg config.MemoryConfig, getProvider func(string) config.ProviderConfig) *Provider {
	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(config.DefaultDataDir(), "brain.db")
	}
	return &Provider{
		dbPath:      dbPath,
		cfg:         cfg,
		memCfg:      memCfg,
		getProvider: getProvider,
		clients:     make(map[string]*cortex.Cortex),
	}
}

// For returns the cortex client for the given chat-agent model
// (e.g. "anthropic/claude-sonnet-4-6-asia-southeast1"). On first call for
// a given (resolved provider, model) it opens a new cortex client; later
// calls return the cached one.
func (p *Provider) For(agentModel string) (*cortex.Cortex, error) {
	provider, model := resolveCortexModel(p.cfg, agentModel)
	key := provider + "/" + model

	p.mu.Lock()
	defer p.mu.Unlock()
	if cx, ok := p.clients[key]; ok {
		return cx, nil
	}
	cx, err := p.build(provider, model)
	if err != nil {
		return nil, err
	}
	p.clients[key] = cx
	slog.Info("cortex client built",
		"agent_model", agentModel,
		"resolved_provider", provider,
		"resolved_model", model,
		"db", p.dbPath)
	return cx, nil
}

// Close closes every cached cortex client. Safe to call multiple times.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var firstErr error
	for k, cx := range p.clients {
		if err := cx.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(p.clients, k)
	}
	return firstErr
}

// build opens a single *cortex.Cortex wired with the right LLM, extractor,
// and embedder. The LLM/extractor mirror the chat agent's provider; the
// embedder always mirrors memory's configured embedder so cortex and memory
// share one vector space (no per-agent index drift, no model-not-found
// errors when memory routes through a non-local provider). All builds share
// p.dbPath.
func (p *Provider) build(provider, model string) (*cortex.Cortex, error) {
	pcfg := p.getProvider(provider)
	apiKey := pcfg.APIKey
	baseURL := pcfg.BaseURL

	var opts []cortex.Option
	detExt := deterministic.New()

	// LLM + extractor: depend on the chat agent's provider. "local" stays on
	// deterministic-only — small local models frequently return malformed
	// JSON for the structured-extraction prompt and tie up Ollama for 30–90s
	// per failure. "anthropic" / "openai" get hybrid (deterministic + LLM)
	// when an API key is present.
	//
	// Note: we intentionally do NOT call cortex.WithLLM(...). The cortex
	// library only uses cfg.llm in Recall.decomposeQuery (an LLM round-trip
	// to split the query into sub-queries), and that adds 1–3 s of latency
	// to every chat turn — well beyond the 800 ms recall budget the agent
	// runtime allows. Cortex's recall fallback (keyword + memory lookup)
	// gives near-equivalent results without the round-trip. The extractor
	// still embeds its own LLM client for ingest, which runs async.
	switch provider {
	case "local":
		opts = append(opts, cortex.WithExtractor(detExt))

	case "anthropic":
		if apiKey != "" {
			if model == "" {
				model = "claude-sonnet-4-5-20250929"
			}
			anthOpts := []cortexanthropic.LLMOption{cortexanthropic.WithModel(model)}
			if baseURL != "" {
				anthOpts = append(anthOpts, cortexanthropic.WithBaseURL(baseURL))
			}
			llmClient := cortexanthropic.NewLLM(apiKey, anthOpts...)
			opts = append(opts, cortex.WithExtractor(hybrid.New(detExt, llmext.New(llmClient))))
		} else {
			opts = append(opts, cortex.WithExtractor(detExt))
		}

	default: // "openai" and any unknown provider
		if apiKey != "" {
			if model == "" {
				model = "gpt-5.4-mini"
			}
			oaiOpts := []cortexoai.LLMOption{cortexoai.WithModel(model)}
			if baseURL != "" {
				oaiOpts = append(oaiOpts, cortexoai.WithBaseURL(baseURL))
			}
			llmClient := cortexoai.NewLLM(apiKey, oaiOpts...)
			opts = append(opts, cortex.WithExtractor(hybrid.New(detExt, llmext.New(llmClient))))
		} else {
			opts = append(opts, cortex.WithExtractor(detExt))
		}
	}

	// Embedder: mirror memory's configuration so cortex's vector index lives
	// in the same embedding space the user picked for memory. Skipped if
	// memory has no embedding provider configured (cortex falls back to
	// keyword search).
	if p.memCfg.EmbeddingProvider != "" {
		embPcfg := p.getProvider(p.memCfg.EmbeddingProvider)
		embModel, embDims := localpkg.EmbeddingDims(p.memCfg.EmbeddingModel)
		embOpts := []cortexoai.EmbedderOption{
			cortexoai.WithEmbeddingModel(goopenai.EmbeddingModel(embModel), embDims),
		}
		if embPcfg.BaseURL != "" {
			embOpts = append(embOpts, cortexoai.WithEmbedderBaseURL(embPcfg.BaseURL))
		}
		opts = append(opts, cortex.WithEmbedder(cortexoai.NewEmbedder(embPcfg.APIKey, embOpts...)))
	}

	cx, err := cortex.Open(p.dbPath, opts...)
	if err != nil {
		return nil, fmt.Errorf("cortex init: %w", err)
	}
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

// ingestTimeout is the hard cap on a single IngestThread call. Async, so
// it doesn't block the user — but it does need to be long enough for an
// LLM-backed extractor to finish. Sonnet/GPT-class extraction over a multi-
// chunk thread can take 30–60 s; bumped from 30s after seeing repeated
// "ingest timed out" warnings against anthropic-mirrored cortex.
const ingestTimeout = 90 * time.Second

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
