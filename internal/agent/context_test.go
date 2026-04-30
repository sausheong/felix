package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		"", // memoryFiles
	)
	require.Contains(t, got, "CUSTOM IDENTITY")
	require.Contains(t, got, `"Alpha" agent (id: alpha)`)
	require.Contains(t, got, "Configured channels: cli")
	require.Contains(t, got, "## Skills Index")
}

func TestBuildStaticSystemPromptConfigOverride(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("FROM_IDENTITY_FILE"), 0o644))

	got := BuildStaticSystemPrompt(dir, "FROM CONFIG", "id", "Name", nil, "", "", "")
	require.Contains(t, got, "FROM CONFIG")
	require.NotContains(t, got, "FROM_IDENTITY_FILE")
}

func TestBuildStaticSystemPromptDefaultIdentity(t *testing.T) {
	dir := t.TempDir() // no IDENTITY.md
	got := BuildStaticSystemPrompt(dir, "", "id", "Name", []string{"read_file", "bash"}, "", "", "")
	require.Contains(t, got, defaultIdentityBase)
	require.Contains(t, got, "read files")
	require.Contains(t, got, "bash commands")
}

func TestBuildStaticSystemPromptByteStableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	a := BuildStaticSystemPrompt(dir, "", "id", "Name", []string{"read_file"}, "summary", "index", "")
	b := BuildStaticSystemPrompt(dir, "", "id", "Name", []string{"read_file"}, "summary", "index", "")
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

func TestLoadAgentMemoryFilesEmpty(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	got := LoadAgentMemoryFiles(workspace)
	require.Equal(t, "", got)
}

func TestLoadAgentMemoryFilesWorkspaceOnly(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "FELIX.md"),
		[]byte("PROJECT_INSTRUCTIONS_SENTINEL"),
		0o644,
	))
	got := LoadAgentMemoryFiles(workspace)
	require.Contains(t, got, "## Project memory: ")
	require.Contains(t, got, "FELIX.md")
	require.Contains(t, got, "PROJECT_INSTRUCTIONS_SENTINEL")
	require.True(t, strings.HasPrefix(got, "\n\n"),
		"output must start with \\n\\n so it composes after skillsIndex")
}

func TestLoadAgentMemoryFilesBothLocations(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte("WORKSPACE_FELIX_CONTENT"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, "AGENTS.md"),
		[]byte("HOME_AGENTS_CONTENT"), 0o644))
	got := LoadAgentMemoryFiles(workspace)
	require.Contains(t, got, "## Project memory: ")
	require.Contains(t, got, "WORKSPACE_FELIX_CONTENT")
	require.Contains(t, got, "## User memory: ")
	require.Contains(t, got, "HOME_AGENTS_CONTENT")
	wsIdx := strings.Index(got, "WORKSPACE_FELIX_CONTENT")
	homeIdx := strings.Index(got, "HOME_AGENTS_CONTENT")
	require.Less(t, wsIdx, homeIdx, "workspace must appear before home")
}

func TestLoadAgentMemoryFilesAllFour(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte("WS_FELIX"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"),
		[]byte("WS_AGENTS"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, "FELIX.md"),
		[]byte("HOME_FELIX"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, "AGENTS.md"),
		[]byte("HOME_AGENTS"), 0o644))

	got := LoadAgentMemoryFiles(workspace)

	wsFelix := strings.Index(got, "WS_FELIX")
	wsAgents := strings.Index(got, "WS_AGENTS")
	homeFelix := strings.Index(got, "HOME_FELIX")
	homeAgents := strings.Index(got, "HOME_AGENTS")
	require.True(t, wsFelix >= 0 && wsAgents > wsFelix &&
		homeFelix > wsAgents && homeAgents > homeFelix,
		"order must be ws-felix < ws-agents < home-felix < home-agents; got %d %d %d %d",
		wsFelix, wsAgents, homeFelix, homeAgents)
}

func TestLoadAgentMemoryFilesSkipsEmptyFile(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"), []byte{}, 0o644))
	got := LoadAgentMemoryFiles(workspace)
	require.Equal(t, "", got, "empty file produces no section header")
}

func TestLoadAgentMemoryFilesSkipsWhitespaceOnly(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte("\n  \t\n"), 0o644))
	got := LoadAgentMemoryFiles(workspace)
	require.Equal(t, "", got, "whitespace-only file produces no section header")
}

func TestLoadAgentMemoryFilesTruncatesOverCap(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	huge := strings.Repeat("Lorem ipsum dolor sit amet.\n", 2000) // ~54 KB
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte(huge), 0o644))
	got := LoadAgentMemoryFiles(workspace)
	require.LessOrEqual(t, len(got), MaxAgentMemoryBytes+200,
		"output must respect cap (allowing some slack for header + truncation marker)")
	require.Contains(t, got, "[truncated — over 40 KB total agent memory]")
}

func TestLoadAgentMemoryFilesSkipsAfterTruncation(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	near := strings.Repeat("x\n", 25000) // ~50 KB; pushes past the cap
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte(near), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "AGENTS.md"),
		[]byte("AGENTS_SHOULD_NOT_APPEAR"), 0o644))
	got := LoadAgentMemoryFiles(workspace)
	require.Contains(t, got, "[truncated")
	require.NotContains(t, got, "AGENTS_SHOULD_NOT_APPEAR",
		"files after truncation must be fully skipped")
}

func TestLoadAgentMemoryFilesDedupSameDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // workspace == home
	require.NoError(t, os.WriteFile(filepath.Join(dir, "FELIX.md"),
		[]byte("UNIQUE_CONTENT_DEDUP_TEST"), 0o644))
	got := LoadAgentMemoryFiles(dir)
	occurrences := strings.Count(got, "UNIQUE_CONTENT_DEDUP_TEST")
	require.Equal(t, 1, occurrences, "same file at same path must appear exactly once")
}

func TestLoadAgentMemoryFilesEmptyHome(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("HOME", "")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "FELIX.md"),
		[]byte("WORKSPACE_STILL_LOADS"), 0o644))
	require.NotPanics(t, func() {
		got := LoadAgentMemoryFiles(workspace)
		require.Contains(t, got, "WORKSPACE_STILL_LOADS")
	})
}

func TestLoadAgentMemoryFilesEmptyWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "FELIX.md"),
		[]byte("HOME_STILL_LOADS"), 0o644))
	require.NotPanics(t, func() {
		got := LoadAgentMemoryFiles("")
		require.Contains(t, got, "HOME_STILL_LOADS")
	})
}

func TestBuildStaticSystemPromptIncludesMemoryFiles(t *testing.T) {
	dir := t.TempDir()
	got := BuildStaticSystemPrompt(
		dir, "", "id", "Name",
		[]string{"read_file"},
		"",                  // configSummary
		"",                  // skillsIndex
		"\n\n## Project memory: /tmp/x\n\nUNIQUE_MEM_FILES_SENTINEL", // memoryFiles
	)
	require.Contains(t, got, "UNIQUE_MEM_FILES_SENTINEL")
	require.Contains(t, got, "## Project memory:")
}

func TestFormatDateLine(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"may day", time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), "Today's date is 2026-05-01."},
		{"new year", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "Today's date is 2027-01-01."},
		{"single digit month", time.Date(2026, 3, 9, 23, 59, 59, 0, time.UTC), "Today's date is 2026-03-09."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatDateLine(tc.in)
			require.Equal(t, tc.want, got)
		})
	}
}
