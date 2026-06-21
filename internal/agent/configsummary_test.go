package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/config"
)

func TestBuildConfigSummary_Nil(t *testing.T) {
	require.Equal(t, "", buildConfigSummary(nil))
}

func TestBuildConfigSummary_Empty(t *testing.T) {
	// No agents, no channels → nothing to summarize, so no config-path footer.
	require.Equal(t, "", buildConfigSummary(&config.Config{}))
}

func TestBuildConfigSummary_ListsAgents(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{ID: "main", Name: "Main", Model: "local/gemma3"},
			},
		},
	}
	got := buildConfigSummary(cfg)
	require.Contains(t, got, "Configured agents:")
	require.Contains(t, got, "Main")
	require.Contains(t, got, "id: main")
	require.Contains(t, got, "model: local/gemma3")
	// A non-empty summary appends the config-path / data-dir footer.
	require.Contains(t, got, "configuration file is at")
}

func TestBuildConfigSummary_IncludesAllowedTools(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{ID: "a", Name: "A", Model: "m", Tools: config.ToolPolicy{Allow: []string{"bash", "read_file"}}},
			},
		},
	}
	got := buildConfigSummary(cfg)
	require.Contains(t, got, "tools: bash, read_file")
}

func TestBuildConfigSummary_NoToolsSectionWhenAllowEmpty(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{{ID: "a", Name: "A", Model: "m"}},
		},
	}
	require.NotContains(t, buildConfigSummary(cfg), "tools:")
}

func TestBuildConfigSummary_CLIChannel(t *testing.T) {
	cfg := &config.Config{}
	cfg.Channels.CLI.Enabled = true
	got := buildConfigSummary(cfg)
	require.Contains(t, got, "Configured channels: cli")
}

func TestConfigSummaryFor_CachedAndInvalidated(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{{ID: "x", Name: "X", Model: "m1"}},
		},
	}
	first := ConfigSummaryFor(cfg)
	require.Contains(t, first, "model: m1")

	// Mutate the config WITHOUT bumping the generation: the cached summary
	// for this generation must be returned unchanged.
	cfg.Agents.List[0].Model = "m2"
	require.Equal(t, first, ConfigSummaryFor(cfg), "same generation must return cached summary")

	// Bumping the generation invalidates the cache; the new model appears.
	BumpConfigGeneration()
	require.Contains(t, ConfigSummaryFor(cfg), "model: m2")
}

func TestEffectiveMaxToolResultLen_NegativeFallsBackToDefault(t *testing.T) {
	// Only configured > 0 wins; zero and negative both fall back to 64K.
	require.Equal(t, 65536, effectiveMaxToolResultLen(-1))
}

func TestCortexStaticHint_DisabledIsEmpty(t *testing.T) {
	require.Equal(t, "", cortexStaticHint(nil))
	require.Equal(t, "", cortexStaticHint(&config.Config{}))
}

func TestCortexStaticHint_EnabledReturnsHint(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cortex.Enabled = true
	require.NotEmpty(t, cortexStaticHint(cfg))
}

func TestFelixEnvHint_DescribesDataDirAndGuardrails(t *testing.T) {
	got := felixEnvHint()
	require.Contains(t, got, "Felix execution environment")
	require.Contains(t, got, "~/.felix/")
	require.Contains(t, got, "brain.db")
	require.Contains(t, got, "Bash guardrails")
	require.True(t, strings.HasPrefix(got, "\n"), "hint should lead with a blank line for clean concatenation")
}
