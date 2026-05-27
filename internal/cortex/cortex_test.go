package cortex

import (
	"testing"

	"github.com/sausheong/felix/internal/config"
)

func TestResolveCortexModelMirrorsAgentWhenEmpty(t *testing.T) {
	cfg := config.CortexConfig{Enabled: true} // Provider and LLMModel both empty
	provider, model := resolveCortexModel(cfg, "local/gemma4:latest")
	if provider != "local" {
		t.Errorf("auto-mirror provider = %q, want \"local\"", provider)
	}
	if model != "gemma4:latest" {
		t.Errorf("auto-mirror model = %q, want \"gemma4:latest\"", model)
	}
}

func TestResolveCortexModelMirrorsChatAgentNotDefault(t *testing.T) {
	// Same empty config but a different chat-agent model — cortex follows
	// whoever's actually chatting, not a single startup-time choice.
	cfg := config.CortexConfig{Enabled: true}
	provider, model := resolveCortexModel(cfg, "anthropic/claude-sonnet-4-6")
	if provider != "anthropic" || model != "claude-sonnet-4-6" {
		t.Errorf("expected mirror of chat agent; got (%q, %q)", provider, model)
	}
}

func TestResolveCortexModelPreservesExplicitConfig(t *testing.T) {
	cfg := config.CortexConfig{Enabled: true, Provider: "openai", LLMModel: "gpt-4o"}
	provider, model := resolveCortexModel(cfg, "local/gemma4:latest")
	if provider != "openai" {
		t.Errorf("explicit provider should be preserved; got %q", provider)
	}
	if model != "gpt-4o" {
		t.Errorf("explicit model should be preserved; got %q", model)
	}
}

func TestResolveCortexModelMirrorsWhenPartial(t *testing.T) {
	// Only one of Provider/LLMModel set isn't a real "pin" — fall back to
	// mirroring the chat agent so cortex doesn't ship a half-configured client.
	cfg := config.CortexConfig{Enabled: true, Provider: "anthropic", LLMModel: ""}
	provider, model := resolveCortexModel(cfg, "local/gemma4:latest")
	if provider != "local" || model != "gemma4:latest" {
		t.Errorf("partial config should mirror chat agent; got (%q, %q)", provider, model)
	}
}
