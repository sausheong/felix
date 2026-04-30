package agent

import (
	"strings"
	"testing"

	"github.com/sausheong/felix/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBuildConfigSummaryWithAgentsAndCLI(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:    "alpha",
			Name:  "Alpha",
			Model: "anthropic/claude-sonnet-4-5",
			Tools: config.ToolPolicy{Allow: []string{"read_file", "bash"}},
		},
		{ID: "beta", Name: "Beta", Model: "openai/gpt-4o"},
	}
	cfg.Channels.CLI.Enabled = true

	got := BuildConfigSummary(cfg)
	require.Contains(t, got, "Configured agents:")
	require.Contains(t, got, "Alpha (id: alpha, model: anthropic/claude-sonnet-4-5, tools: read_file, bash)")
	require.Contains(t, got, "Beta (id: beta, model: openai/gpt-4o)")
	require.Contains(t, got, "Configured channels: cli")
}

func TestBuildConfigSummaryEmptyConfig(t *testing.T) {
	got := BuildConfigSummary(&config.Config{})
	require.Equal(t, "", strings.TrimSpace(got))
}

func TestBuildConfigSummaryNilSafe(t *testing.T) {
	got := BuildConfigSummary(nil)
	require.Equal(t, "", got)
}
