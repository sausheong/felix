# Memory Search Wire-Up (P7) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `search_memory` agent tool that exposes the already-built `(*memory.Manager).Search` (vector→BM25 fallback) so the agent can retrieve curated memory by meaning/keyword, returning entry IDs + snippets.

**Architecture:** A Felix-native tool in `internal/tools` (mirroring `SendMessageTool`/`RegisterMemoryTool`) calls `(*memory.Manager).Search` through a one-method `MemorySearcher` interface. Registered at the three existing memory-registration sites in `startup.go`, each guarded by `memMgr != nil`. No harness changes.

**Tech Stack:** Go 1.25; `github.com/sausheong/harness/tool` (Tool/ToolResult via Felix aliases); `internal/memory` (Manager.Search, Entry); `testify`.

**Decision resolved from spec:** Direct import of `internal/memory` into `internal/tools` for the `Entry` type. Rationale: `internal/tools` already imports Felix-internal packages (`internal/tools/cortextools`, `github.com/sausheong/cortex`), and `startup.go` already imports both packages, so no adapter is needed and this is not a layering violation. `truncateRunes` is unexported in `internal/memory`, so the tool carries its own small copy.

---

## File Structure

- **Create:** `internal/tools/searchmemory.go` — `SearchMemoryTool`, `MemorySearcher` interface, `RegisterSearchMemoryTool`, local `truncateRunes` helper.
- **Create:** `internal/tools/searchmemory_test.go` — unit tests against a fake searcher.
- **Modify:** `internal/startup/startup.go` — three `RegisterSearchMemoryTool` calls next to the existing `RegisterMemoryTool` calls (~lines 642, 862, 928).

---

## Task 1: SearchMemoryTool type, interface, and Name/Description/Parameters/IsConcurrencySafe

**Files:**
- Create: `internal/tools/searchmemory.go`
- Test: `internal/tools/searchmemory_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/tools/searchmemory_test.go`. Import only what Task 1 uses
(`context`, `strings`, `unicode/utf8` are added in Task 2 when the tests that
use them are appended — adding them now would fail Go's unused-import check):

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/ -run TestSearchMemoryTool_Metadata`
Expected: FAIL — `undefined: SearchMemoryTool`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/tools/searchmemory.go`:

```go
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
	// Implemented in Task 2.
	return ToolResult{}, nil
}

// truncateRunes returns s truncated to at most n runes, never splitting a
// multi-byte rune. Local copy — memory.truncateRunes is unexported.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/ -run TestSearchMemoryTool_Metadata`
Expected: PASS.

Note: the imports `fmt` and `strings` are unused until Task 2. To keep this task compiling on its own, omit `fmt` and `strings` from the import block in Step 3 and add them in Task 2. (If you implement Task 2 immediately after, add them now.)

- [ ] **Step 5: Commit**

```bash
git add internal/tools/searchmemory.go internal/tools/searchmemory_test.go
git commit -m "feat(tools): add SearchMemoryTool scaffold (metadata + schema)"
```

---

## Task 2: Execute — query validation, clamping, formatting

**Files:**
- Modify: `internal/tools/searchmemory.go`
- Test: `internal/tools/searchmemory_test.go`

- [ ] **Step 1: Write the failing tests**

First, extend the import block in `internal/tools/searchmemory_test.go` to add
the imports these new tests use — `context`, `strings`, and `unicode/utf8` — so
it reads:

```go
import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sausheong/felix/internal/memory"
	"github.com/stretchr/testify/require"
)
```

Then append to `internal/tools/searchmemory_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tools/ -run TestSearchMemory_`
Expected: FAIL — Execute returns empty `ToolResult` (no Error on empty query, no Output on hits, lastLimit is 0).

- [ ] **Step 3: Implement Execute**

In `internal/tools/searchmemory.go`, ensure the import block includes `fmt` and `strings` (add them now if omitted in Task 1), then replace the stub `Execute` with:

```go
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
		snippet := truncateRunes(strings.TrimSpace(e.Content), searchMemorySnippetRunes)
		if title := strings.TrimSpace(e.Title); title != "" {
			fmt.Fprintf(&b, "- %s — %s: %s\n", e.ID, title, snippet)
		} else {
			fmt.Fprintf(&b, "- %s: %s\n", e.ID, snippet)
		}
	}
	return ToolResult{Output: strings.TrimRight(b.String(), "\n")}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tools/ -run TestSearchMemory`
Expected: PASS (all subtests).

- [ ] **Step 5: Vet and full package test**

Run: `go vet ./internal/tools/ && go test ./internal/tools/`
Expected: clean vet; all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/tools/searchmemory.go internal/tools/searchmemory_test.go
git commit -m "feat(tools): implement search_memory Execute (validate, clamp, format)"
```

---

## Task 3: RegisterSearchMemoryTool + registration test

**Files:**
- Modify: `internal/tools/searchmemory.go`
- Test: `internal/tools/searchmemory_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tools/searchmemory_test.go`:

```go
func TestRegisterSearchMemoryTool(t *testing.T) {
	reg := NewRegistry()
	RegisterSearchMemoryTool(reg, &fakeSearcher{})
	require.Contains(t, reg.Names(), "search_memory")
}

func TestRegisterSearchMemoryTool_NilSearcher(t *testing.T) {
	reg := NewRegistry()
	RegisterSearchMemoryTool(reg, nil) // defensive no-op
	require.NotContains(t, reg.Names(), "search_memory")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/ -run TestRegisterSearchMemoryTool`
Expected: FAIL — `undefined: RegisterSearchMemoryTool`.

- [ ] **Step 3: Implement the registrar**

Add to `internal/tools/searchmemory.go` (after the type methods):

```go
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
```

Note on the nil check: a typed-nil `*memory.Manager` passed as `MemorySearcher`
would NOT be caught by `searcher == nil`. The startup sites all guard with
`memMgr != nil` before calling, so the registrar only ever receives a non-nil
concrete value in production; the nil check defends against an untyped-nil
literal (as in the test). Do not over-engineer a reflect-based nil check.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/ -run TestRegisterSearchMemoryTool`
Expected: PASS.

- [ ] **Step 5: Full package test + vet**

Run: `go vet ./internal/tools/ && go test ./internal/tools/`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/tools/searchmemory.go internal/tools/searchmemory_test.go
git commit -m "feat(tools): add RegisterSearchMemoryTool"
```

---

## Task 4: Wire registration into startup.go (three sites)

**Files:**
- Modify: `internal/startup/startup.go` (~lines 642, 862, 928)

No new test — this is wiring; correctness is covered by Task 3's registration
test plus the build. Verification is `go build ./...` and the full suite.

- [ ] **Step 1: Site 1 — main toolReg (~line 642)**

Find:

```go
		tools.RegisterMemoryTool(toolReg, memory.NewHarnessAdapter(memMgr))
	}
```

(the block inside `if cfg.Memory.Enabled {`). Add the search tool registration
immediately after the `RegisterMemoryTool` line, inside the same block:

```go
		tools.RegisterMemoryTool(toolReg, memory.NewHarnessAdapter(memMgr))
		// Read-side companion to the memory write tool: semantic/BM25
		// search over the same store (memMgr satisfies MemorySearcher).
		tools.RegisterSearchMemoryTool(toolReg, memMgr)
	}
```

- [ ] **Step 2: Site 2 — per-agent reg (~line 862)**

Find:

```go
		if memMgr != nil {
			tools.RegisterMemoryTool(reg, memory.NewHarnessAdapter(memMgr))
		}
```

Replace with:

```go
		if memMgr != nil {
			tools.RegisterMemoryTool(reg, memory.NewHarnessAdapter(memMgr))
			tools.RegisterSearchMemoryTool(reg, memMgr)
		}
```

- [ ] **Step 3: Site 3 — cron cronToolReg (~line 928)**

Find:

```go
			if memMgr != nil {
				tools.RegisterMemoryTool(cronToolReg, memory.NewHarnessAdapter(memMgr))
			}
```

Replace with:

```go
			if memMgr != nil {
				tools.RegisterMemoryTool(cronToolReg, memory.NewHarnessAdapter(memMgr))
				tools.RegisterSearchMemoryTool(cronToolReg, memMgr)
			}
```

- [ ] **Step 4: Build both binaries**

Run: `go build ./... && go build -o /tmp/felix ./cmd/felix && go build -o /tmp/felix-app ./cmd/felix-app`
Expected: clean build, no errors.

- [ ] **Step 5: Full test suite + vet + race**

Run: `go vet ./... && go test ./... && go test -race ./internal/tools/ ./internal/startup/`
Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add internal/startup/startup.go
git commit -m "feat(startup): register search_memory at all three memory sites"
```

---

## Final verification (controller, after all tasks)

- [ ] `go build ./...` clean; both `cmd/felix` and `cmd/felix-app` build.
- [ ] `go vet ./...` clean.
- [ ] `go test ./...` green.
- [ ] `go test -race ./...` green.
- [ ] Confirm `search_memory` appears in a registry alongside `load_memory` and `memory` when memory is enabled, and is absent when disabled (Task 3 tests cover the registrar; spot-check the startup guards are inside the `memMgr != nil` / `cfg.Memory.Enabled` blocks).
