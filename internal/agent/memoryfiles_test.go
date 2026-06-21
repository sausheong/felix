package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// withTempHome points os.UserHomeDir at a fresh temp dir for the duration of
// the test so the $HOME-sourced FELIX.md/AGENTS.md candidates are isolated
// from the developer's real home directory.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestLoadAgentMemoryFiles_Empty(t *testing.T) {
	withTempHome(t)
	// Empty workspace + empty home → no files → empty string.
	require.Equal(t, "", loadAgentMemoryFilesImpl(t.TempDir()))
}

func TestLoadAgentMemoryFiles_ProjectAndUser(t *testing.T) {
	home := withTempHome(t)
	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "FELIX.md"), []byte("project felix body"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("user agents body"), 0o600))

	got := loadAgentMemoryFilesImpl(ws)

	require.Contains(t, got, "Project memory:")
	require.Contains(t, got, "project felix body")
	require.Contains(t, got, "User memory:")
	require.Contains(t, got, "user agents body")
	// Project memory must precede user memory (discovery order).
	require.Less(t, strings.Index(got, "Project memory"), strings.Index(got, "User memory"))
}

func TestLoadAgentMemoryFiles_DiscoveryOrder(t *testing.T) {
	home := withTempHome(t)
	ws := t.TempDir()
	// Write all four candidates; FELIX.md must precede AGENTS.md within each tier.
	require.NoError(t, os.WriteFile(filepath.Join(ws, "FELIX.md"), []byte("WS_FELIX"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("WS_AGENTS"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, "FELIX.md"), []byte("HOME_FELIX"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("HOME_AGENTS"), 0o600))

	got := loadAgentMemoryFilesImpl(ws)
	order := []string{"WS_FELIX", "WS_AGENTS", "HOME_FELIX", "HOME_AGENTS"}
	prev := -1
	for _, marker := range order {
		idx := strings.Index(got, marker)
		require.GreaterOrEqual(t, idx, 0, "marker %q missing", marker)
		require.Greater(t, idx, prev, "marker %q out of discovery order", marker)
		prev = idx
	}
}

func TestLoadAgentMemoryFiles_WhitespaceOnlySkipped(t *testing.T) {
	withTempHome(t)
	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "FELIX.md"), []byte("   \n\t\n  "), 0o600))
	require.Equal(t, "", loadAgentMemoryFilesImpl(ws), "whitespace-only file must be skipped")
}

func TestLoadAgentMemoryFiles_DedupesSamePath(t *testing.T) {
	// When workspace == $HOME, FELIX.md resolves to the same absolute path for
	// the project and user candidates — it must appear exactly once.
	home := withTempHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, "FELIX.md"), []byte("DEDUP_BODY"), 0o600))

	got := loadAgentMemoryFilesImpl(home) // workspace == home
	require.Equal(t, 1, strings.Count(got, "DEDUP_BODY"), "same path must be deduped")
}

func TestLoadAgentMemoryFiles_TruncatesOverCap(t *testing.T) {
	withTempHome(t)
	ws := t.TempDir()
	// One file larger than the 40 KB cap forces truncation.
	big := strings.Repeat("abcdefghij\n", 6000) // ~66 KB
	require.NoError(t, os.WriteFile(filepath.Join(ws, "FELIX.md"), []byte(big), 0o600))

	got := loadAgentMemoryFilesImpl(ws)
	require.Contains(t, got, "[truncated", "over-cap content must carry the truncation marker")
	require.LessOrEqual(t, len(got), 41*1024, "output must stay near the 40 KB cap")
}

func TestLoadAgentMemoryFiles_SkipsSubsequentAfterTruncation(t *testing.T) {
	home := withTempHome(t)
	ws := t.TempDir()
	big := strings.Repeat("x", 50*1024)
	require.NoError(t, os.WriteFile(filepath.Join(ws, "FELIX.md"), []byte(big), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("SHOULD_NOT_APPEAR"), 0o600))

	got := loadAgentMemoryFilesImpl(ws)
	require.NotContains(t, got, "SHOULD_NOT_APPEAR", "files after truncation must be skipped")
}
