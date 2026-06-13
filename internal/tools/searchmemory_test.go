package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sausheong/felix/internal/memory"
	"github.com/stretchr/testify/require"
)

// fakeSearcher is a MemorySearcher stub. It records the last query/limit and
// returns canned entries. callCount lets tests assert Search was NOT called on
// the empty-query path.
type fakeSearcher struct {
	entries   []memory.Entry
	lastQuery string
	lastLimit int
	callCount int
}

func (f *fakeSearcher) Search(query string, maxResults int) []memory.Entry {
	f.callCount++
	f.lastQuery = query
	f.lastLimit = maxResults
	return f.entries
}

func TestSearchMemoryTool_Metadata(t *testing.T) {
	tool := &SearchMemoryTool{Searcher: &fakeSearcher{}}
	require.Equal(t, "search_memory", tool.Name())
	require.NotEmpty(t, tool.Description())
	require.True(t, tool.IsConcurrencySafe(nil))

	// Parameters must be valid JSON declaring a required "query".
	var schema map[string]any
	require.NoError(t, json.Unmarshal(tool.Parameters(), &schema))
	req, _ := schema["required"].([]any)
	require.Contains(t, req, "query")
}

func exec(t *testing.T, tool *SearchMemoryTool, in searchMemoryInput) ToolResult {
	t.Helper()
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	res, err := tool.Execute(context.Background(), raw)
	require.NoError(t, err)
	return res
}

func TestSearchMemory_Hits(t *testing.T) {
	fs := &fakeSearcher{entries: []memory.Entry{
		{ID: "abc", Title: "Coffee order", Content: "double espresso, no sugar"},
		{ID: "def", Title: "", Content: "untitled note body"},
	}}
	res := exec(t, &SearchMemoryTool{Searcher: fs}, searchMemoryInput{Query: "coffee"})
	require.Empty(t, res.Error)
	require.Contains(t, res.Output, "abc")
	require.Contains(t, res.Output, "Coffee order")
	require.Contains(t, res.Output, "double espresso")
	// Empty-title entry renders without a dangling em-dash.
	require.Contains(t, res.Output, "def")
	require.NotContains(t, res.Output, "def — :")
	require.Equal(t, "coffee", fs.lastQuery)
}

func TestSearchMemory_EmptyQuery(t *testing.T) {
	fs := &fakeSearcher{}
	res := exec(t, &SearchMemoryTool{Searcher: fs}, searchMemoryInput{Query: "   "})
	require.NotEmpty(t, res.Error)
	require.Equal(t, 0, fs.callCount, "Search must not be called on empty query")
}

func TestSearchMemory_NoMatches(t *testing.T) {
	fs := &fakeSearcher{entries: nil}
	res := exec(t, &SearchMemoryTool{Searcher: fs}, searchMemoryInput{Query: "nothing"})
	require.Empty(t, res.Error)
	require.Contains(t, res.Output, "no matching memory entries")
}

func TestSearchMemory_Clamping(t *testing.T) {
	fs := &fakeSearcher{}
	exec(t, &SearchMemoryTool{Searcher: fs}, searchMemoryInput{Query: "q"}) // default
	require.Equal(t, 5, fs.lastLimit)
	exec(t, &SearchMemoryTool{Searcher: fs}, searchMemoryInput{Query: "q", MaxResults: 0})
	require.Equal(t, 5, fs.lastLimit)
	exec(t, &SearchMemoryTool{Searcher: fs}, searchMemoryInput{Query: "q", MaxResults: 99})
	require.Equal(t, 20, fs.lastLimit)
	exec(t, &SearchMemoryTool{Searcher: fs}, searchMemoryInput{Query: "q", MaxResults: 3})
	require.Equal(t, 3, fs.lastLimit)
}

func TestSearchMemory_SnippetUTF8Safe(t *testing.T) {
	// 1 ASCII byte then many 3-byte runes: a naive byte-slice at the rune cap
	// would split a rune. Guard rune-safe truncation.
	long := "x" + strings.Repeat("好", 400)
	fs := &fakeSearcher{entries: []memory.Entry{{ID: "u", Title: "t", Content: long}}}
	res := exec(t, &SearchMemoryTool{Searcher: fs}, searchMemoryInput{Query: "q"})
	require.True(t, utf8.ValidString(res.Output), "snippet must not split a rune")
}

func TestSearchMemory_SnippetSingleLine(t *testing.T) {
	fs := &fakeSearcher{entries: []memory.Entry{
		{ID: "m1", Title: "Note", Content: "first line\nsecond line\n\nthird"},
	}}
	res := exec(t, &SearchMemoryTool{Searcher: fs}, searchMemoryInput{Query: "q"})
	require.Empty(t, res.Error)
	// Exactly one output line for one hit — no embedded newline in the snippet.
	lines := strings.Split(strings.TrimRight(res.Output, "\n"), "\n")
	require.Len(t, lines, 1, "each hit must render on a single line")
	require.Contains(t, res.Output, "first line second line third")
}
