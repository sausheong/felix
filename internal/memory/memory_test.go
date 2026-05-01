package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	results := mgr.Search("programming language", 5)
	assert.NotEmpty(t, results)
	// Both golang and python should match
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	assert.Contains(t, ids, "golang")
	assert.Contains(t, ids, "python")
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

func TestFormatIndexListsEntriesSortedByID(t *testing.T) {
	m := NewManager(t.TempDir())
	m.entries = map[string]Entry{
		"zebra":  {ID: "zebra", Title: "Z", Content: "# Z\n\nlast line about zebras."},
		"apple":  {ID: "apple", Title: "A", Content: "# A\n\nfirst line about apples."},
		"banana": {ID: "banana", Title: "B", Content: "# B\n\nbody about bananas."},
	}
	got := m.FormatIndex()
	require.Contains(t, got, "## Memory Index")
	// Order must be apple, banana, zebra. Sorting matters: the index
	// goes into the cached static prompt, so any non-deterministic
	// ordering would invalidate the prompt cache every turn.
	a := strings.Index(got, "**apple**")
	b := strings.Index(got, "**banana**")
	z := strings.Index(got, "**zebra**")
	require.True(t, a >= 0 && b > a && z > b,
		"entries must be sorted by id; got positions %d %d %d", a, b, z)
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
	// 250 entries with sortable IDs (e_001 .. e_250) so the cap is
	// observable: the LAST entry (e_250) must be elided when the cap
	// is 200.
	for i := 1; i <= 250; i++ {
		id := fmt.Sprintf("e_%03d", i)
		m.entries[id] = Entry{ID: id, Title: id, Content: ""}
	}
	got := m.FormatIndex()
	require.Contains(t, got, "**e_001**")
	require.Contains(t, got, "**e_200**")
	require.NotContains(t, got, "**e_201**")
	require.NotContains(t, got, "**e_250**")
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
