package compaction

import (
	"log/slog"
	"time"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
)

// BuildManager constructs a compaction Manager from config + the bundled
// local Ollama. Returns nil when compaction is disabled or no provider is
// configured — callers must treat nil as "compaction off".
func BuildManager(cfg *config.Config) *Manager {
	if cfg == nil {
		return nil
	}
	c := cfg.Agents.Defaults.Compaction
	if !c.Enabled {
		return nil
	}
	provider, model := llm.ParseProviderModel(c.Model)
	if provider == "" {
		provider = "local"
	}
	pcfg, ok := cfg.Providers[provider]
	if !ok || pcfg.BaseURL == "" {
		slog.Warn("compaction disabled: provider not configured", "provider", provider)
		return nil
	}
	llmProv, err := llm.NewProvider(provider, llm.ProviderOptions{
		APIKey:  pcfg.APIKey,
		BaseURL: pcfg.BaseURL,
		Kind:    pcfg.Kind,
	})
	if err != nil {
		slog.Warn("compaction disabled: failed to build provider", "error", err)
		return nil
	}
	timeout := time.Duration(c.TimeoutSec) * time.Second
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &Manager{
		Summarizer: &Summarizer{
			Provider: llmProv,
			Model:    model,
			Timeout:  timeout,
		},
		PreserveTurns: c.PreserveTurns,
		Threshold:     c.Threshold,
	}
}
