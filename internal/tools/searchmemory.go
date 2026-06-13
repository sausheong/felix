package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sausheong/felix/internal/memory"
)

const (
	searchMemoryDefaultResults = 5
	searchMemoryMaxResults     = 20
	searchMemorySnippetRunes   = 200
)

// MemorySearcher is the narrow read contract SearchMemoryTool needs.
// *memory.Manager satisfies it via its existing
// Search(query string, maxResults int) []memory.Entry method.
type MemorySearcher interface {
	Search(query string, maxResults int) []memory.Entry
}

// SearchMemoryTool lets the agent retrieve saved memory entries by meaning or
// keyword. It calls memory.Manager.Search (vector search when an embedder is
// configured, BM25 otherwise) and returns matching entry IDs with snippets;
// the agent then uses load_memory to read a full entry body. This is the
// search counterpart to the static, capped memory index in the system prompt.
type SearchMemoryTool struct {
	Searcher MemorySearcher
}

func (t *SearchMemoryTool) Name() string { return "search_memory" }

func (t *SearchMemoryTool) Description() string {
	return "Search your saved memory entries by meaning or keyword. Returns " +
		"matching entry IDs with snippets; use load_memory to read a full " +
		"entry. Use this when the memory index in your system prompt doesn't " +
		"show what you need."
}

func (t *SearchMemoryTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "What to search for — natural language or keywords."
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of entries to return (default 5, max 20)."
			}
		},
		"required": ["query"]
	}`)
}

// IsConcurrencySafe: pure read. Manager.Search takes an RLock.
func (t *SearchMemoryTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

type searchMemoryInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func (t *SearchMemoryTool) Execute(_ context.Context, input json.RawMessage) (ToolResult, error) {
	var in searchMemoryInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}

	query := strings.TrimSpace(in.Query)
	if query == "" {
		return ToolResult{Error: "query is required"}, nil
	}

	limit := in.MaxResults
	if limit <= 0 {
		limit = searchMemoryDefaultResults
	}
	if limit > searchMemoryMaxResults {
		limit = searchMemoryMaxResults
	}

	entries := t.Searcher.Search(query, limit)
	if len(entries) == 0 {
		return ToolResult{Output: "no matching memory entries"}, nil
	}

	var b strings.Builder
	for _, e := range entries {
		// Collapse newlines/tabs/space-runs to single spaces so each entry stays
		// on one line (Content is markdown and routinely contains newlines),
		// then truncate so the rune budget isn't spent on whitespace.
		snippet := truncateRunes(strings.Join(strings.Fields(e.Content), " "), searchMemorySnippetRunes)
		if title := strings.TrimSpace(e.Title); title != "" {
			fmt.Fprintf(&b, "- %s — %s: %s\n", e.ID, title, snippet)
		} else {
			fmt.Fprintf(&b, "- %s: %s\n", e.ID, snippet)
		}
	}
	return ToolResult{Output: strings.TrimRight(b.String(), "\n")}, nil
}

// RegisterSearchMemoryTool registers the search_memory tool backed by the
// given searcher. Pass a *internal/memory.Manager — it satisfies
// MemorySearcher. A nil searcher is a no-op (mirrors the memMgr != nil guard
// at the startup registration sites), so callers need not branch twice.
func RegisterSearchMemoryTool(reg *Registry, searcher MemorySearcher) {
	if searcher == nil {
		return
	}
	reg.Register(&SearchMemoryTool{Searcher: searcher})
}

// truncateRunes returns s truncated to at most n runes, never splitting a
// multi-byte rune. Local copy — memory.truncateRunes is unexported.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
