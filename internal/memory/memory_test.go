package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBM25IndexBasic(t *testing.T) {
	idx := NewBM25Index()
	idx.Add("doc1", "the quick brown fox jumps over the lazy dog")
	idx.Add("doc2", "machine learning and artificial intelligence")
	idx.Add("doc3", "the quick brown fox and the lazy cat")

	results := idx.Search("quick fox", 5)
	require.NotEmpty(t, results)
	// doc1 and doc3 mention both "quick" and "fox"
	assert.Contains(t, []string{"doc1", "doc3"}, results[0].ID)
}

func TestBM25IndexEmpty(t *testing.T) {
	idx := NewBM25Index()
	results := idx.Search("test", 5)
	assert.Empty(t, results)
}

func TestBM25IndexNoMatch(t *testing.T) {
	idx := NewBM25Index()
	idx.Add("doc1", "hello world")
	results := idx.Search("quantum physics", 5)
	assert.Empty(t, results)
}

func TestBM25IndexMaxResults(t *testing.T) {
	idx := NewBM25Index()
	for i := 0; i < 10; i++ {
		idx.Add("doc"+string(rune('0'+i)), "test document about golang programming")
	}

	results := idx.Search("golang", 3)
	assert.Len(t, results, 3)
}

func TestTokenize(t *testing.T) {
	tokens := tokenize("Hello, World! This is a test 123.")
	assert.Contains(t, tokens, "hello")
	assert.Contains(t, tokens, "world")
	assert.Contains(t, tokens, "this")
	assert.Contains(t, tokens, "test")
	assert.Contains(t, tokens, "123")
	// Single char tokens should be filtered
	assert.NotContains(t, tokens, "a")
}

func TestMemoryManagerSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	// Save an entry
	err := mgr.Save("test-entry", "# Test Entry\n\nThis is a test memory about Go programming.")
	require.NoError(t, err)

	// Check it exists
	entry, ok := mgr.Get("test-entry")
	assert.True(t, ok)
	assert.Equal(t, "Test Entry", entry.Title)
	assert.Contains(t, entry.Content, "Go programming")

	// Verify file was written
	path := filepath.Join(dir, "entries", "test-entry.md")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Go programming")

	// Reload and verify persistence
	mgr2 := NewManager(dir)
	require.NoError(t, mgr2.Load())
	entry2, ok := mgr2.Get("test-entry")
	assert.True(t, ok)
	assert.Equal(t, "Test Entry", entry2.Title)
}

func TestMemoryManagerSearch(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	mgr.Save("golang", "# Go Programming\n\nGo is a statically typed, compiled language designed at Google.")
	mgr.Save("python", "# Python Programming\n\nPython is a high-level interpreted programming language.")
	mgr.Save("recipes", "# Favorite Recipes\n\nChocolate cake recipe with vanilla frosting.")

	// Search for programming
	results := mgr.Search(context.Background(), "programming language", 5)
	assert.NotEmpty(t, results)
	// Both golang and python should match
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	assert.Contains(t, ids, "golang")
	assert.Contains(t, ids, "python")
}

// toggleEmbedder embeds normally until hang is set, after which Embed blocks
// until its ctx is cancelled and then returns ctx.Err(). It models a black-holed
// embedder endpoint so the test can prove Search bounds the vector query.
type toggleEmbedder struct {
	dim  int
	hang atomic.Bool
}

func (e *toggleEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e.hang.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	out := make([][]float32, len(texts))
	for i := range out {
		v := make([]float32, e.dim)
		// Non-zero so chromem's cosine similarity is defined (zero vectors NaN).
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

// TestMemoryManagerSearch_BoundsHungEmbedder proves Search does not hold the
// RLock indefinitely when the embedder black-holes: the internal 5s timeout
// fires, Search falls back to BM25 (or returns nil) and RETURNS rather than
// hanging. Without the ctx-timeout fix this test would wait the full 15s and
// fail.
func TestMemoryManagerSearch_BoundsHungEmbedder(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	emb := &toggleEmbedder{dim: 8}
	mgr.SetEmbedder(emb)
	mgr.SetEmbedderModel("fake")

	// Seed an entry on disk, then Load so initVectorCollection builds vecColl
	// via the fast (non-hanging) embedder path.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "entries"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "entries", "golang.md"),
		[]byte("# Go Programming\n\nGo is a compiled language."), 0o600))
	require.NoError(t, mgr.Load())
	require.NotNil(t, mgr.vecColl, "vector collection must be built so the hung path is exercised")

	// Now black-hole the embedder: every Query embed call blocks until cancelled.
	emb.hang.Store(true)

	done := make(chan []Entry, 1)
	start := time.Now()
	go func() {
		done <- mgr.Search(context.Background(), "programming", 5)
	}()

	select {
	case res := <-done:
		// Returned — the 5s bound fired. res is BM25 fallback or nil; either is
		// acceptable. The point is that Search returned at all.
		require.Less(t, time.Since(start), 10*time.Second,
			"Search returned but only after the internal timeout; bound too loose")
		_ = res
	case <-time.After(15 * time.Second):
		t.Fatal("Search did not return; embedder timeout not enforced (RLock held on hung endpoint)")
	}

	// The write lock must be acquirable immediately — Search must have released
	// its RLock. This is the actual harm being prevented.
	saveDone := make(chan error, 1)
	go func() { saveDone <- mgr.Save("after", "# After\n\nbody") }()
	select {
	case err := <-saveDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Save blocked — Search did not release the read lock")
	}
}

func TestMemoryManagerDelete(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	mgr.Save("to-delete", "# Delete Me\n\nThis will be deleted.")

	_, ok := mgr.Get("to-delete")
	assert.True(t, ok)

	err := mgr.Delete("to-delete")
	require.NoError(t, err)

	_, ok = mgr.Get("to-delete")
	assert.False(t, ok)

	// File should be gone
	path := filepath.Join(dir, "entries", "to-delete.md")
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestMemoryManagerDeleteNonexistent(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	require.NoError(t, mgr.Load())

	err := mgr.Delete("nonexistent")
	assert.Error(t, err)
}

func TestFormatMemoryForPrompt(t *testing.T) {
	entries := []Entry{
		{ID: "test", Title: "Test Entry", Content: "Content here"},
	}

	result := FormatForPrompt(entries)
	assert.Contains(t, result, "## Relevant Memory")
	assert.Contains(t, result, "### Test Entry")
	assert.Contains(t, result, "Content here")
}

func TestFormatMemoryForPromptEmpty(t *testing.T) {
	result := FormatForPrompt(nil)
	assert.Equal(t, "", result)
}

func TestFormatMemoryForPromptTruncation(t *testing.T) {
	// Create an entry with > 2000 chars
	longContent := ""
	for i := 0; i < 300; i++ {
		longContent += "This is line number " + string(rune('0'+i%10)) + " of a very long memory entry.\n"
	}

	entries := []Entry{
		{ID: "long", Title: "Long Entry", Content: longContent},
	}

	result := FormatForPrompt(entries)
	assert.Contains(t, result, "[truncated]")
}

// --- FormatIndex (sub-project 5: index injection, on-demand load) ---

func TestFormatIndexEmptyManagerReturnsEmpty(t *testing.T) {
	m := NewManager(t.TempDir())
	require.Equal(t, "", m.FormatIndex())
}

func TestFormatIndexListsEntriesNewestFirst(t *testing.T) {
	m := NewManager(t.TempDir())
	// Entries are sorted by ModTime descending (tie-break id desc) so the
	// newest survives the cap. Give distinct ModTimes to assert ordering.
	m.entries = map[string]Entry{
		"apple":  {ID: "apple", Title: "A", Content: "# A\n\nfirst line about apples.", ModTime: time.Unix(100, 0)},
		"banana": {ID: "banana", Title: "B", Content: "# B\n\nbody about bananas.", ModTime: time.Unix(200, 0)},
		"zebra":  {ID: "zebra", Title: "Z", Content: "# Z\n\nlast line about zebras.", ModTime: time.Unix(300, 0)},
	}
	got := m.FormatIndex()
	require.Contains(t, got, "## Memory Index")
	// Order must be zebra (newest), banana, apple (oldest). Sorting is
	// deterministic so the index stays cache-stable across turns.
	z := strings.Index(got, "**zebra**")
	b := strings.Index(got, "**banana**")
	a := strings.Index(got, "**apple**")
	require.True(t, z >= 0 && b > z && a > b,
		"entries must be sorted newest-first; got positions z=%d b=%d a=%d", z, b, a)
}

func TestFormatIndexIncludesTitleAndDescription(t *testing.T) {
	m := NewManager(t.TempDir())
	m.entries = map[string]Entry{
		"e1": {
			ID:      "e1",
			Title:   "First entry title",
			Content: "# First entry title\n\nThis is the one-line description.",
		},
	}
	got := m.FormatIndex()
	require.Contains(t, got, "**e1**")
	require.Contains(t, got, "First entry title")
	require.Contains(t, got, "This is the one-line description.")
}

func TestFormatIndexSkipsTitleInDescription(t *testing.T) {
	m := NewManager(t.TempDir())
	m.entries = map[string]Entry{
		"e1": {
			ID:      "e1",
			Title:   "Has only title",
			Content: "# Has only title\n",
		},
	}
	got := m.FormatIndex()
	require.Contains(t, got, "**e1**")
	require.Contains(t, got, "Has only title")
	// No body beyond the title means no description suffix —
	// indexDescription must walk past the H1 line.
	require.NotContains(t, got, ": # ")
}

func TestFormatIndexCapsAtMax(t *testing.T) {
	m := NewManager(t.TempDir())
	m.entries = make(map[string]Entry)
	// 250 entries; ModTime ascends with i so the NEWEST (e_250 .. e_051)
	// survive the cap and the OLDEST (e_001 .. e_050) are elided. This is
	// the R8 fix: newest-first ordering keeps recent memories discoverable.
	for i := 1; i <= 250; i++ {
		id := fmt.Sprintf("e_%03d", i)
		m.entries[id] = Entry{ID: id, Title: id, Content: "", ModTime: time.Unix(int64(i), 0)}
	}
	got := m.FormatIndex()
	require.Contains(t, got, "**e_250**")
	require.Contains(t, got, "**e_051**")
	require.NotContains(t, got, "**e_050**")
	require.NotContains(t, got, "**e_001**")
}

func TestFormatIndexTrimsLongDescription(t *testing.T) {
	m := NewManager(t.TempDir())
	long := strings.Repeat("x", 500)
	m.entries = map[string]Entry{
		"e1": {ID: "e1", Title: "T", Content: "# T\n" + long},
	}
	got := m.FormatIndex()
	// 120-char cap + ellipsis. Anything close to 500 chars on the
	// description line means the cap silently failed.
	require.Contains(t, got, "…")
	// The description segment must end with the ellipsis, not the full string.
	require.NotContains(t, got, long)
}

func TestFormatIndex_ShowsNewestWhenOverCap(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	total := MaxMemoryIndexEntries + 5
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("agent-%010d-x", i)
		require.NoError(t, m.Save(id, "# T"+id+"\n\nbody"))
		m.mu.Lock()
		e := m.entries[id]
		e.ModTime = time.Unix(int64(1000+i), 0)
		m.entries[id] = e
		m.mu.Unlock()
	}
	idx := m.FormatIndex()
	require.Contains(t, idx, fmt.Sprintf("agent-%010d-x", total-1), "newest must be listed")
	require.NotContains(t, idx, fmt.Sprintf("agent-%010d-x", 0), "oldest must fall off")
	require.Contains(t, idx, "and ", "truncation notice must appear")
}

func TestTruncateRunes_UTF8Safe(t *testing.T) {
	s := strings.Repeat("é", 100)
	out := truncateRunes(s, 10)
	require.True(t, utf8.ValidString(out), "must not split a rune")
	require.Equal(t, 10, utf8.RuneCountInString(out))
}

func TestSave_ConcurrentWithLoadNoRace(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = m.Save(fmt.Sprintf("agent-%d", i), "# t\n\nbody")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = m.Load()
		}
	}()
	wg.Wait()
}

func TestWriteFileAtomic_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.md")
	require.NoError(t, writeFileAtomic(p, []byte("hello"), 0o600))
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "hello", string(b))
}

func TestSave_DoesNotBlockOnVectorSemaphore(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	// Without an embedder, vecColl is nil so the goroutine path is skipped;
	// this guards that Save returns promptly regardless. (Structural guard for
	// the moved-acquire fix — the acquire is no longer under m.mu.)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			_ = m.Save(fmt.Sprintf("agent-%d", i), "# t\n\nbody")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Save calls blocked unexpectedly")
	}
}
