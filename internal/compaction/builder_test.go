package compaction

import (
	"testing"

	"github.com/sausheong/felix/internal/config"
)

// cfgWith returns a Config whose default-agent compaction block is enabled,
// with a single provider entry and optional agent list. Callers tweak the
// returned value for the specific case under test.
func cfgWith(compaction config.CompactionConfig, providers map[string]config.ProviderConfig, agents ...config.AgentConfig) *config.Config {
	return &config.Config{
		Providers: providers,
		Agents: config.AgentsConfig{
			List:     agents,
			Defaults: config.AgentsDefaults{Compaction: compaction},
		},
	}
}

// ptr returns a pointer to v, for setting pointer-typed config fields in tests.
func ptr[T any](v T) *T { return &v }

// localProvider is a provider that NewProvider can build without any network
// access (the "local" kind just constructs an OpenAI-compatible client).
var localProvider = map[string]config.ProviderConfig{
	"local": {Kind: "local", BaseURL: "http://127.0.0.1:18790"},
}

func TestBuildManager_NilConfig(t *testing.T) {
	if got := BuildManager(nil); got != nil {
		t.Fatalf("BuildManager(nil) = %v, want nil", got)
	}
}

func TestBuildManager_Disabled(t *testing.T) {
	cfg := cfgWith(config.CompactionConfig{Enabled: false}, localProvider)
	if got := BuildManager(cfg); got != nil {
		t.Fatalf("BuildManager(disabled) = %v, want nil", got)
	}
}

func TestBuildManager_PinnedModel(t *testing.T) {
	cfg := cfgWith(
		config.CompactionConfig{Enabled: true, Model: "local/qwen2.5:3b"},
		localProvider,
	)
	m := BuildManager(cfg)
	if m == nil {
		t.Fatal("BuildManager returned nil for a configured local provider")
	}
	if m.Summarizer == nil {
		t.Fatal("Manager.Summarizer is nil")
	}
	if got, want := m.Summarizer.Model, "qwen2.5:3b"; got != want {
		t.Fatalf("Summarizer.Model = %q, want %q (model id only, no provider prefix)", got, want)
	}
}

func TestBuildManager_FallsBackToFirstAgentModel(t *testing.T) {
	// No pinned compaction.Model — it should mirror the first agent's model.
	cfg := cfgWith(
		config.CompactionConfig{Enabled: true},
		localProvider,
		config.AgentConfig{Model: "local/gemma3"},
	)
	m := BuildManager(cfg)
	if m == nil {
		t.Fatal("BuildManager returned nil; expected fallback to first agent model")
	}
	if got, want := m.Summarizer.Model, "gemma3"; got != want {
		t.Fatalf("Summarizer.Model = %q, want %q", got, want)
	}
}

func TestBuildManager_UnconfiguredProvider(t *testing.T) {
	// Enabled, model points at a provider with neither APIKey nor BaseURL.
	cfg := cfgWith(
		config.CompactionConfig{Enabled: true, Model: "openai/gpt-4o"},
		map[string]config.ProviderConfig{"openai": {Kind: "openai"}}, // no key, no baseURL
	)
	if got := BuildManager(cfg); got != nil {
		t.Fatalf("BuildManager with unconfigured provider = %v, want nil", got)
	}
}

func TestBuildManager_MissingProvider(t *testing.T) {
	// Model references a provider key that isn't present at all.
	cfg := cfgWith(
		config.CompactionConfig{Enabled: true, Model: "anthropic/claude"},
		localProvider, // only "local" exists
	)
	if got := BuildManager(cfg); got != nil {
		t.Fatalf("BuildManager with absent provider = %v, want nil", got)
	}
}

func TestBuildManagerForModel_DefaultsProviderToLocal(t *testing.T) {
	// A bare model string (no "provider/" prefix) should default to "local".
	cfg := cfgWith(config.CompactionConfig{Enabled: true}, localProvider)
	m := buildManagerForModel(cfg, "gemma3") // no slash → provider "" → "local"
	if m == nil {
		t.Fatal("buildManagerForModel returned nil; expected local default")
	}
	if got, want := m.Summarizer.Model, "gemma3"; got != want {
		t.Fatalf("Summarizer.Model = %q, want %q", got, want)
	}
}

func TestBuildManagerForModel_APIKeyOnlyIsConfigured(t *testing.T) {
	// A native SDK provider with an API key but no BaseURL must count as
	// configured (regression: requiring BaseURL alone disabled anthropic).
	cfg := cfgWith(
		config.CompactionConfig{Enabled: true},
		map[string]config.ProviderConfig{"anthropic": {Kind: "anthropic", APIKey: "sk-test"}},
	)
	m := buildManagerForModel(cfg, "anthropic/claude-x")
	if m == nil {
		t.Fatal("buildManagerForModel(anthropic, api-key only) = nil, want a Manager")
	}
}

func TestBuildManagerForModel_DefaultTimeout(t *testing.T) {
	cfg := cfgWith(
		config.CompactionConfig{Enabled: true, TimeoutSec: 0}, // unset → default
		localProvider,
	)
	m := buildManagerForModel(cfg, "local/gemma3")
	if m == nil {
		t.Fatal("buildManagerForModel returned nil")
	}
	if got, want := m.Summarizer.Timeout.Seconds(), 300.0; got != want {
		t.Fatalf("Summarizer.Timeout = %vs, want %vs (default)", got, want)
	}
}

func TestBuildManagerForModel_ExplicitTimeoutAndFields(t *testing.T) {
	cfg := cfgWith(
		config.CompactionConfig{
			Enabled:       true,
			TimeoutSec:    42,
			PreserveTurns: 7,
			Threshold:     0.75,
			MessageCap:    ptr(99),
		},
		localProvider,
	)
	m := buildManagerForModel(cfg, "local/gemma3")
	if m == nil {
		t.Fatal("buildManagerForModel returned nil")
	}
	if got := m.Summarizer.Timeout.Seconds(); got != 42 {
		t.Fatalf("Timeout = %vs, want 42s", got)
	}
	if m.PreserveTurns != 7 {
		t.Fatalf("PreserveTurns = %d, want 7", m.PreserveTurns)
	}
	if m.Threshold != 0.75 {
		t.Fatalf("Threshold = %v, want 0.75", m.Threshold)
	}
	if m.MessageCap != 99 {
		t.Fatalf("MessageCap = %d, want 99", m.MessageCap)
	}
}

func TestNewProvider_NilWhenDisabled(t *testing.T) {
	if got := NewProvider(nil); got != nil {
		t.Fatalf("NewProvider(nil) = %v, want nil", got)
	}
	cfg := cfgWith(config.CompactionConfig{Enabled: false}, localProvider)
	if got := NewProvider(cfg); got != nil {
		t.Fatalf("NewProvider(disabled) = %v, want nil", got)
	}
}

func TestProviderFor_NilReceiver(t *testing.T) {
	var p *Provider
	if got := p.For("local/gemma3"); got != nil {
		t.Fatalf("(*Provider)(nil).For() = %v, want nil", got)
	}
}

func TestProviderFor_UsesAgentModelWhenUnpinned(t *testing.T) {
	cfg := cfgWith(config.CompactionConfig{Enabled: true}, localProvider)
	p := NewProvider(cfg)
	if p == nil {
		t.Fatal("NewProvider returned nil for enabled config")
	}
	m := p.For("local/gemma3")
	if m == nil {
		t.Fatal("Provider.For returned nil for configured local model")
	}
	if got, want := m.Summarizer.Model, "gemma3"; got != want {
		t.Fatalf("Summarizer.Model = %q, want %q", got, want)
	}
}

func TestProviderFor_PinnedModelOverridesAgentModel(t *testing.T) {
	cfg := cfgWith(
		config.CompactionConfig{Enabled: true, Model: "local/pinned-model"},
		localProvider,
	)
	p := NewProvider(cfg)
	m := p.For("local/agent-model") // should be ignored in favor of pinned
	if m == nil {
		t.Fatal("Provider.For returned nil")
	}
	if got, want := m.Summarizer.Model, "pinned-model"; got != want {
		t.Fatalf("Summarizer.Model = %q, want %q (pinned should win)", got, want)
	}
}

func TestProviderFor_CachesByModel(t *testing.T) {
	cfg := cfgWith(config.CompactionConfig{Enabled: true}, localProvider)
	p := NewProvider(cfg)

	a1 := p.For("local/gemma3")
	a2 := p.For("local/gemma3")
	if a1 != a2 {
		t.Fatal("Provider.For did not return the cached Manager for the same model")
	}
	b := p.For("local/qwen2.5")
	if b == a1 {
		t.Fatal("Provider.For returned the same Manager for a different model")
	}
}

func TestProviderFor_CachesNilToAvoidRetry(t *testing.T) {
	// Resolving an unconfigured provider yields nil; the nil must be cached
	// so subsequent calls don't rebuild (and re-log) every request.
	cfg := cfgWith(
		config.CompactionConfig{Enabled: true},
		localProvider, // "openai" deliberately absent
	)
	p := NewProvider(cfg)
	if got := p.For("openai/gpt-4o"); got != nil {
		t.Fatalf("Provider.For(unconfigured) = %v, want nil", got)
	}
	// Second call must also be nil and must come from the cache.
	if got := p.For("openai/gpt-4o"); got != nil {
		t.Fatalf("Provider.For(unconfigured) second call = %v, want nil", got)
	}
	p.mu.Lock()
	_, cached := p.cache["openai/gpt-4o"]
	p.mu.Unlock()
	if !cached {
		t.Fatal("nil Manager was not cached; would rebuild on every request")
	}
}
