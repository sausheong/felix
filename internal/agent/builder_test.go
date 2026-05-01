package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sausheong/felix/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBuildRuntimeForAgentSetsProviderAndStaticPrompt(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "a", Name: "A", Model: "anthropic/claude-sonnet-4-5"},
	}
	cfg.Channels.CLI.Enabled = true

	a := &cfg.Agents.List[0]
	deps := RuntimeDeps{Config: cfg}
	inputs := RuntimeInputs{}

	rt, err := BuildRuntimeForAgent(deps, inputs, a)
	require.NoError(t, err)
	require.Equal(t, "anthropic", rt.Provider)
	require.Equal(t, "claude-sonnet-4-5", rt.Model)
	require.NotEmpty(t, rt.StaticSystemPrompt)
	require.Contains(t, rt.StaticSystemPrompt, `"A" agent (id: a)`)
	require.Contains(t, rt.StaticSystemPrompt, "Configured channels: cli")
}

func TestBuildRuntimeForAgentLocalProvider(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "x", Name: "X", Model: "local/qwen2.5:3b"},
	}
	rt, err := BuildRuntimeForAgent(RuntimeDeps{Config: cfg}, RuntimeInputs{}, &cfg.Agents.List[0])
	require.NoError(t, err)
	require.Equal(t, "local", rt.Provider)
}

func TestBuildRuntimeForAgentNilConfigSafe(t *testing.T) {
	a := &config.AgentConfig{ID: "a", Name: "A", Model: "anthropic/claude-sonnet-4-5"}
	rt, err := BuildRuntimeForAgent(RuntimeDeps{}, RuntimeInputs{}, a)
	require.NoError(t, err)
	require.Equal(t, "anthropic", rt.Provider)
	require.NotEmpty(t, rt.StaticSystemPrompt)
}

func TestBuildRuntimeForAgentLoadsMemoryFilesIntoStaticPrompt(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "FELIX.md"),
		[]byte("MEMFILE_END_TO_END_SENTINEL"),
		0o644,
	))

	a := &config.AgentConfig{
		ID:        "a",
		Name:      "A",
		Workspace: workspace,
		Model:     "anthropic/claude-sonnet-4-5",
	}
	rt, err := BuildRuntimeForAgent(RuntimeDeps{}, RuntimeInputs{}, a)
	require.NoError(t, err)
	require.Contains(t, rt.StaticSystemPrompt, "MEMFILE_END_TO_END_SENTINEL")
	require.Contains(t, rt.StaticSystemPrompt, "## Project memory:")
}
