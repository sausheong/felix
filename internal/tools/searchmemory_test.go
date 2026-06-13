package tools

import (
	"encoding/json"
	"testing"

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
