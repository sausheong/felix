// Package cortex provides an adapter between Felix and the Cortex knowledge
// graph library. It handles initialization, conversation ingestion, and
// formatting recall results for the agent system prompt.
package cortex

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	goopenai "github.com/sashabaranov/go-openai"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	"github.com/sausheong/cortex/extractor/llmext"
	cortexanthropic "github.com/sausheong/cortex/llm/anthropic"
	cortexoai "github.com/sausheong/cortex/llm/openai"
	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
	localpkg "github.com/sausheong/felix/internal/local"
)

// Drain is a no-op kept as a stable shutdown hook for callers that
// historically waited on background cortex ingest goroutines. The auto-
// ingest path was removed when the four explicit cortex tools (recall,
// remember, find_entities, get_relationships) replaced it; cortex now
// only writes synchronously from inside tool.Execute calls, so there is
// nothing to wait for. Kept (rather than deleted) so startup/cleanup
// wiring does not need touching.
func Drain() {}

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

// CortexHint is injected into the system prompt when Cortex is enabled so the
// agent knows it has a persistent knowledge graph backing its memory.
const CortexHint = `

You have access to cortex, my persistent memory. At the start of every conversation, call recall with keywords from my first message to check for relevant context. When I tell you something you should remember about me, my projects, my preferences, or my decisions, call remember. Use find_entities and get_relationships when I ask about people, projects, or how things connect.

Cortex results are ranked by relevance (best match first), but the ranker can still return loosely-related entries when nothing truly matches the query. ALWAYS judge each result by its content against the actual question — if none of the returned entries genuinely address it, treat the recall as a miss and do not cite them.`

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
