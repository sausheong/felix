package compaction

import (
	"log/slog"
	"sync"
	"time"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
)

// BuildManager constructs a compaction Manager pinned to the *default* agent's
// model (or cfg.Agents.Defaults.Compaction.Model if explicitly set). Use
// Provider.For(agentModel) instead when you can — it builds per-chat-agent
// Managers so compaction uses the same LLM as the conversation.
//
// Returns nil when compaction is disabled or no provider is configured —
// callers must treat nil as "compaction off".
func BuildManager(cfg *config.Config) *Manager {
	if cfg == nil || !cfg.Agents.Defaults.Compaction.Enabled {
		return nil
	}
	modelStr := cfg.Agents.Defaults.Compaction.Model
	if modelStr == "" && len(cfg.Agents.List) > 0 {
		modelStr = cfg.Agents.List[0].Model
	}
	return buildManagerForModel(cfg, modelStr)
}

// Provider builds and caches per-agent compaction Managers, keyed by the
// chatting agent's "provider/model". The cache matters because Manager has
// per-session locks — two requests on the same session must hit the same
// Manager instance to serialize correctly.
type Provider struct {
	cfg *config.Config

	mu    sync.Mutex
	cache map[string]*Manager // key: "provider/model"
}

// NewProvider returns a per-agent compaction Manager factory. Returns nil
// when compaction is globally disabled.
func NewProvider(cfg *config.Config) *Provider {
	if cfg == nil || !cfg.Agents.Defaults.Compaction.Enabled {
		return nil
	}
	return &Provider{cfg: cfg, cache: make(map[string]*Manager)}
}

// For returns the Manager for the given chat-agent model. If compaction.model
// is explicitly pinned in config, that overrides agentModel. Returns nil if
// the resolved provider is missing — callers must handle nil.
func (p *Provider) For(agentModel string) *Manager {
	if p == nil {
		return nil
	}
	modelStr := p.cfg.Agents.Defaults.Compaction.Model
	if modelStr == "" {
		modelStr = agentModel
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if m, ok := p.cache[modelStr]; ok {
		return m
	}
	m := buildManagerForModel(p.cfg, modelStr)
	p.cache[modelStr] = m // cache nil too — avoid retrying a failed build per request
	return m
}

// buildManagerForModel builds a single Manager wired to the given
// "provider/model" string. Returns nil when the provider is unconfigured.
func buildManagerForModel(cfg *config.Config, modelStr string) *Manager {
	c := cfg.Agents.Defaults.Compaction
	provider, model := llm.ParseProviderModel(modelStr)
	if provider == "" {
		provider = "local"
	}
	pcfg, ok := cfg.Providers[provider]
	// "Configured enough to talk to" means we have either an API key (native
	// SDKs like anthropic/openai/gemini work without a baseURL) or a baseURL
	// (local Ollama, openai-compatible proxies). Requiring baseURL alone
	// silently disabled compaction for native anthropic.
	if !ok || (pcfg.APIKey == "" && pcfg.BaseURL == "") {
		slog.Warn("compaction disabled: provider not configured", "provider", provider, "model", modelStr)
		return nil
	}
	llmProv, err := llm.NewProvider(provider, llm.ProviderOptions{
		APIKey:  pcfg.APIKey,
		BaseURL: pcfg.BaseURL,
		Kind:    pcfg.Kind,
	})
	if err != nil {
		slog.Warn("compaction disabled: failed to build provider", "error", err, "model", modelStr)
		return nil
	}
	timeout := time.Duration(c.TimeoutSec) * time.Second
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	slog.Info("compaction manager built", "provider", provider, "model", model)
	return &Manager{
		Summarizer: &Summarizer{
			Provider: llmProv,
			Model:    model,
			Timeout:  timeout,
		},
		PreserveTurns: c.PreserveTurns,
		Threshold:     c.Threshold,
		MessageCap:    c.MessageCap,
	}
}
