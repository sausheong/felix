package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/skill"
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

func TestBuildStaticSystemPromptWithIdentityFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "IDENTITY.md"),
		[]byte("CUSTOM IDENTITY"),
		0o644,
	))

	got := BuildStaticSystemPrompt(
		dir, "", "alpha", "Alpha",
		[]string{"read_file"},
		"Configured channels: cli",
		"\n\n## Skills Index\n\n- foo",
	)
	require.Contains(t, got, "CUSTOM IDENTITY")
	require.Contains(t, got, `"Alpha" agent (id: alpha)`)
	require.Contains(t, got, "Configured channels: cli")
	require.Contains(t, got, "## Skills Index")
}

func TestBuildStaticSystemPromptConfigOverride(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("FROM_IDENTITY_FILE"), 0o644))

	got := BuildStaticSystemPrompt(dir, "FROM CONFIG", "id", "Name", nil, "", "")
	require.Contains(t, got, "FROM CONFIG")
	require.NotContains(t, got, "FROM_IDENTITY_FILE")
}

func TestBuildStaticSystemPromptDefaultIdentity(t *testing.T) {
	dir := t.TempDir() // no IDENTITY.md
	got := BuildStaticSystemPrompt(dir, "", "id", "Name", []string{"read_file", "bash"}, "", "")
	require.Contains(t, got, defaultIdentityBase)
	require.Contains(t, got, "read files")
	require.Contains(t, got, "bash commands")
}

func TestBuildStaticSystemPromptByteStableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	a := BuildStaticSystemPrompt(dir, "", "id", "Name", []string{"read_file"}, "summary", "index")
	b := BuildStaticSystemPrompt(dir, "", "id", "Name", []string{"read_file"}, "summary", "index")
	require.Equal(t, a, b, "BuildStaticSystemPrompt must be deterministic")
}

func TestBuildDynamicSystemPromptSuffixAllSources(t *testing.T) {
	skills := []skill.Skill{{Name: "foo", Body: "FOO BODY"}}
	mem := []memory.Entry{{ID: "m1", Title: "Mem One", Content: "memory body"}}
	cortex := "\n\n## Cortex hint\n..."

	got := buildDynamicSystemPromptSuffix(skills, mem, cortex)
	require.Contains(t, got, "FOO BODY")
	require.Contains(t, got, "Mem One")
	require.Contains(t, got, "memory body")
	require.Contains(t, got, "Cortex hint")
}

func TestBuildDynamicSystemPromptSuffixEmpty(t *testing.T) {
	got := buildDynamicSystemPromptSuffix(nil, nil, "")
	require.Equal(t, "", got)
}

func TestBuildDynamicSystemPromptSuffixCortexOnly(t *testing.T) {
	got := buildDynamicSystemPromptSuffix(nil, nil, "\n\nCORTEX")
	require.Equal(t, "\n\nCORTEX", got)
}
