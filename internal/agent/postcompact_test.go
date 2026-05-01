package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sausheong/felix/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- providerSupportsMidLoopCompaction ---

func TestProviderSupportsMidLoopCompactionMatrix(t *testing.T) {
	cases := map[string]bool{
		"anthropic": true,
		"openai":    true,
		"gemini":    true,
		"local":     false,
		"ollama":    false,
		"":          false, // empty/unknown defaults to off (safety)
		"deepseek":  false, // not yet classified — opt-in via the switch
	}
	for provider, want := range cases {
		r := &Runtime{Provider: provider}
		assert.Equalf(t, want, r.providerSupportsMidLoopCompaction(),
			"provider=%q", provider)
	}
}

// --- recordFileTouch ordering + dedupe ---

func TestRecordFileTouchAppendsAndDedupesByMoveToBack(t *testing.T) {
	r := &Runtime{}
	r.recordFileTouch("a.go")
	r.recordFileTouch("b.go")
	r.recordFileTouch("c.go")
	r.recordFileTouch("a.go") // re-touch — should move to back, not duplicate

	got := r.snapshotTouchedFiles()
	assert.Equal(t, []string{"b.go", "c.go", "a.go"}, got)
}

func TestRecordFileTouchIgnoresEmptyPath(t *testing.T) {
	r := &Runtime{}
	r.recordFileTouch("")
	r.recordFileTouch("x.go")
	r.recordFileTouch("")

	assert.Equal(t, []string{"x.go"}, r.snapshotTouchedFiles())
}

func TestSnapshotTouchedFilesReturnsCopy(t *testing.T) {
	r := &Runtime{}
	r.recordFileTouch("a.go")
	snap := r.snapshotTouchedFiles()
	snap[0] = "mutated"
	// Mutation of the snapshot must not bleed back into the live slice;
	// otherwise concurrent post-compact restore reads could observe
	// torn paths under load.
	assert.Equal(t, []string{"a.go"}, r.snapshotTouchedFiles())
}

// --- extractPathFromInput ---

func TestExtractPathFromInputHappyPath(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"path": "/tmp/foo.go"})
	assert.Equal(t, "/tmp/foo.go", extractPathFromInput(in))
}

func TestExtractPathFromInputMissingFieldReturnsEmpty(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"command": "ls"})
	assert.Equal(t, "", extractPathFromInput(in))
}

func TestExtractPathFromInputBadJSONReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", extractPathFromInput([]byte("not-json{")))
	assert.Equal(t, "", extractPathFromInput(nil))
}

// --- buildPostCompactRestore ---

func TestBuildPostCompactRestoreFormatsLatestKFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	b := filepath.Join(dir, "b.go")
	c := filepath.Join(dir, "c.go")
	require.NoError(t, os.WriteFile(a, []byte("package a\n"), 0o644))
	require.NoError(t, os.WriteFile(b, []byte("package b\n"), 0o644))
	require.NoError(t, os.WriteFile(c, []byte("package c\n"), 0o644))

	// Touch order: a, b, c — newest is c. K=2 should pick c then b
	// (newest-first walk), and skip a entirely.
	msg := buildPostCompactRestore([]string{a, b, c}, 2, 1024)

	require.Equal(t, "user", msg.Role)
	assert.Contains(t, msg.Content, "<system-reminder>")
	assert.Contains(t, msg.Content, "</system-reminder>")
	assert.Contains(t, msg.Content, c, "newest file must be present")
	assert.Contains(t, msg.Content, b, "second-newest must be present")
	assert.NotContains(t, msg.Content, a, "K=2 cap must drop the oldest")
}

func TestBuildPostCompactRestoreSkipsMissingAndDir(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.go")
	require.NoError(t, os.WriteFile(good, []byte("ok\n"), 0o644))
	missing := filepath.Join(dir, "missing.go")
	subdir := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(subdir, 0o755))

	msg := buildPostCompactRestore([]string{missing, subdir, good}, 5, 1024)
	assert.Contains(t, msg.Content, good)
	assert.NotContains(t, msg.Content, missing)
	assert.NotContains(t, msg.Content, "<file path=\""+subdir)
}

func TestBuildPostCompactRestoreReturnsEmptyWhenNothingReadable(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "ghost.go")
	msg := buildPostCompactRestore([]string{missing}, 5, 1024)
	assert.Equal(t, "", msg.Content,
		"all-failed read must collapse to empty (caller skips empty)")
}

func TestBuildPostCompactRestoreTruncatesOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.txt")
	// 4 KB file, cap at 1 KB → should get truncation marker
	content := strings.Repeat("x", 4*1024)
	require.NoError(t, os.WriteFile(big, []byte(content), 0o644))

	msg := buildPostCompactRestore([]string{big}, 1, 1024)
	assert.Contains(t, msg.Content, "[truncated — over 1024 bytes]")
	// Sanity: the included content must be ≤ cap + marker overhead.
	// Anything close to 4 KB means the cap silently failed.
	assert.Less(t, len(msg.Content), 2*1024)
}

func TestBuildPostCompactRestoreEmptyInputs(t *testing.T) {
	assert.Equal(t, "", buildPostCompactRestore(nil, 5, 1024).Content)
	assert.Equal(t, "", buildPostCompactRestore([]string{"x"}, 0, 1024).Content)
	assert.Equal(t, "", buildPostCompactRestore([]string{"x"}, 5, 0).Content)
}

// --- prependPostCompactRestore (runtime-facing wrapper) ---

func TestPrependPostCompactRestoreInjectsBeforeExisting(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.go")
	require.NoError(t, os.WriteFile(f, []byte("hi\n"), 0o644))

	original := []llm.Message{
		{Role: "user", Content: "what next?"},
	}
	got := prependPostCompactRestore(original, []string{f})

	require.Len(t, got, 2)
	assert.Equal(t, "user", got[0].Role)
	assert.Contains(t, got[0].Content, "<system-reminder>")
	assert.Contains(t, got[0].Content, f)
	assert.Equal(t, original[0], got[1], "original messages must follow restore intact")
}

func TestPrependPostCompactRestoreReturnsUnchangedWhenNoFiles(t *testing.T) {
	original := []llm.Message{{Role: "user", Content: "hi"}}
	got := prependPostCompactRestore(original, nil)
	assert.Equal(t, original, got)
}

func TestPrependPostCompactRestoreReturnsUnchangedWhenAllUnreadable(t *testing.T) {
	original := []llm.Message{{Role: "user", Content: "hi"}}
	got := prependPostCompactRestore(original, []string{"/nonexistent/ghost.go"})
	assert.Equal(t, original, got, "unreadable touched files must not produce an empty restore wrapper")
}
