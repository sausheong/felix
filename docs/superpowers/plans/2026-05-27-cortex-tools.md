# Cortex Native Tools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `recall`, `remember`, `find_entities`, `get_relationships` into native Felix tools, replacing the existing automatic-per-turn `Recall` and automatic-end-of-run `Ingest` paths.

**Architecture:** New `internal/tools/cortextools/` subpackage holds the four tools. A `tools.RegisterCortexTools(reg, cx)` helper (mirroring `RegisterMemoryTool`) wires them. `internal/startup/startup.go` calls it whenever cortex is enabled. The existing `cortexKGAdapter` in `internal/agent/agent.go` is deleted, along with `hdeps.KGFn` wiring.

**Tech Stack:** Go 1.25, `github.com/sausheong/cortex` library, `github.com/sausheong/harness/tool` interfaces.

**Spec:** `docs/superpowers/specs/2026-05-27-cortex-tools-design.md`

---

## File structure

| Path | Action | Responsibility |
|------|--------|----------------|
| `internal/tools/cortextools/tools.go` | Create | Package doc + shared types |
| `internal/tools/cortextools/recall.go` | Create | RecallTool struct + Execute |
| `internal/tools/cortextools/remember.go` | Create | RememberTool struct + Execute |
| `internal/tools/cortextools/find_entities.go` | Create | FindEntitiesTool struct + Execute |
| `internal/tools/cortextools/get_relationships.go` | Create | GetRelationshipsTool struct + Execute |
| `internal/tools/cortextools/format.go` | Create | formatEntities, formatRelationships helpers |
| `internal/tools/cortextools/tools_test.go` | Create | Table-driven tests against ephemeral cortex |
| `internal/tools/tools.go` | Modify | Add `RegisterCortexTools(reg, cx)` |
| `internal/startup/startup.go` | Modify | Call `RegisterCortexTools` in 3 places (per-agent, subagent builder, cron-registry builder) |
| `internal/agent/agent.go` | Modify | Delete `cortexKGAdapter` struct + methods, delete `hdeps.KGFn = ...` in 2 places, prune imports |
| `internal/cortex/cortex.go` | Modify | Delete `ShouldRecall`, `IngestThread`, `IngestThreadAsync` |
| `internal/cortex/cortex_test.go` | Modify | Delete tests for the three removed functions |
| `internal/config/config.go` | Modify | Add auto-add for cortex tool names (mirroring MCP auto-add) |

No harness changes. No HTTP-route changes. No on-disk DB changes.

---

## Pre-flight context for the implementer

You are adding four new built-in tools to Felix that wrap a `*cortex.Cortex` instance. Felix's existing automatic-per-turn cortex recall and automatic-end-of-run ingestion are being REMOVED. The agent now decides when to query and write to its knowledge graph.

**Cortex API surface you'll touch** (from `go doc github.com/sausheong/cortex Cortex`):

```
func (c *Cortex) Recall(ctx, query string, opts ...RecallOption) ([]Result, error)
func (c *Cortex) Remember(ctx, content string, opts ...RememberOption) error
func (c *Cortex) FindEntities(ctx, f EntityFilter) ([]Entity, error)
func (c *Cortex) GetRelationships(ctx, entityID string, filters ...RelFilter) ([]Relationship, error)
func (c *Cortex) GetEntity(ctx, id string) (*Entity, error)  // used by formatRelationships to resolve IDs to names

cortex.WithLimit(int) RecallOption
cortex.WithMinConfidence(float64) RecallOption
cortex.WithSource(string) RememberOption
```

Run `go doc github.com/sausheong/cortex EntityFilter` / `RelFilter` / `Entity` / `Relationship` if you need field names — don't guess.

**Tool interface** (`github.com/sausheong/harness/tool`):

```go
type Tool interface {
    Name() string
    Description() string
    Schema() any        // JSON Schema
    Execute(ctx context.Context, args map[string]any) (string, error)
}
```

The exact method names may differ slightly (check by reading another tool — `harness/tools/web/fetch.go` is a good reference). Follow whatever convention the existing tools use.

**Per-agent cortex resolution:** `internal/startup/startup.go` already has `resolveCortex(agentModel string) *cortex.Cortex` at line 600. You can call it directly when building per-agent registries. For the global `toolReg` (line 489) you do NOT have a single agent's cortex — that registry exists for tool-listing / settings purposes; pass `nil` for cortex there and skip registration.

**Auto-add precedent:** look at `internal/config/config.go` for `mcpAutoAddedNames`, `ApplyMCPToolNamesToAllowlists`, `StripMCPAutoAdded`. The cortex auto-add follows the same pattern. Spec recommends renaming the field to `runtimeAutoAddedNames` and threading both MCP and cortex names through one list. If that rename touches more than a handful of call sites, KEEP the existing MCP field and add a parallel `cortexAutoAddedNames` field with mirror helpers — it's not worth a sprawling rename.

---

## Task 1: Add the cortextools package

This task creates the package with all 4 tools + format helpers + tests. Single commit.

**Files:**
- Create: `internal/tools/cortextools/tools.go`
- Create: `internal/tools/cortextools/recall.go`
- Create: `internal/tools/cortextools/remember.go`
- Create: `internal/tools/cortextools/find_entities.go`
- Create: `internal/tools/cortextools/get_relationships.go`
- Create: `internal/tools/cortextools/format.go`
- Create: `internal/tools/cortextools/tools_test.go`

- [ ] **Step 1: Verify clean working tree**

```bash
cd ~/projects/felix && git status
```

Expected: only untracked `docs/superpowers/plans/*.md` and `docs/superpowers/specs/*.md`. Stop if anything in `internal/` is modified.

- [ ] **Step 2: Read a reference tool for the interface convention**

```bash
cat /Users/sausheong/go/pkg/mod/github.com/sausheong/harness@v0.3.0/tools/web/fetch.go | head -60
```

Note the receiver pattern (struct + methods), schema format, Execute signature. The cortex tools must match this pattern exactly. If the interface uses pointer receivers, use pointer receivers. If it returns `(string, error)`, return `(string, error)`. Mimic exactly.

- [ ] **Step 3: Check the actual cortex.EntityFilter and RelFilter shapes**

```bash
cd ~/projects/felix && go doc github.com/sausheong/cortex EntityFilter
cd ~/projects/felix && go doc github.com/sausheong/cortex RelFilter
cd ~/projects/felix && go doc github.com/sausheong/cortex Entity
cd ~/projects/felix && go doc github.com/sausheong/cortex Relationship
```

Note exact field names. The plan below uses placeholder names — replace with the real ones in your code.

- [ ] **Step 4: Create `internal/tools/cortextools/tools.go`**

```go
// Package cortextools exposes the cortex knowledge graph as four native
// Felix tools: recall, remember, find_entities, get_relationships. Each
// tool wraps a single *cortex.Cortex instance captured at construction
// time. Used in place of the older automatic-per-turn recall + ingest
// pathway that ran via the harness KnowledgeGraph interface.
package cortextools

import (
	"github.com/sausheong/cortex"
	"github.com/sausheong/harness/tool"
)

// BuildTools returns the four cortex-backed tools wired against cx.
// Returns nil when cx is nil so callers can pass through without a
// per-call nil check.
func BuildTools(cx *cortex.Cortex) []tool.Tool {
	if cx == nil {
		return nil
	}
	return []tool.Tool{
		&RecallTool{cx: cx},
		&RememberTool{cx: cx},
		&FindEntitiesTool{cx: cx},
		&GetRelationshipsTool{cx: cx},
	}
}
```

- [ ] **Step 5: Create `internal/tools/cortextools/recall.go`**

```go
package cortextools

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex"
	cortexfmt "github.com/sausheong/felix/internal/cortex"
)

// RecallTool searches the knowledge graph for context relevant to a query.
type RecallTool struct {
	cx *cortex.Cortex
}

func (t *RecallTool) Name() string { return "recall" }

func (t *RecallTool) Description() string {
	return "Search the knowledge graph for context relevant to a query. " +
		"Returns entities, memories, and document chunks ranked by relevance. " +
		"Use at the start of a conversation, or whenever you need to check what " +
		"you already know about a person, project, or topic before asking the user."
}

func (t *RecallTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Natural-language search query — keywords or a short phrase.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Max number of results. Default 5.",
			},
			"min_confidence": map[string]any{
				"type":        "number",
				"description": "Filter out results with confidence below this (0.0–1.0). Omit to include all.",
			},
		},
		"required": []string{"query"},
	}
}

func (t *RecallTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "error: 'query' is required", nil
	}

	var opts []cortex.RecallOption
	limit := 5
	if v, ok := args["limit"]; ok {
		switch n := v.(type) {
		case float64:
			limit = int(n)
		case int:
			limit = n
		}
	}
	opts = append(opts, cortex.WithLimit(limit))

	if v, ok := args["min_confidence"]; ok {
		if f, ok := v.(float64); ok {
			opts = append(opts, cortex.WithMinConfidence(f))
		}
	}

	results, err := t.cx.Recall(ctx, query, opts...)
	if err != nil {
		return fmt.Sprintf("error: %s", err.Error()), nil
	}
	if len(results) == 0 {
		return "No results.", nil
	}
	return cortexfmt.FormatResults(results), nil
}
```

- [ ] **Step 6: Create `internal/tools/cortextools/remember.go`**

```go
package cortextools

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex"
)

type RememberTool struct {
	cx *cortex.Cortex
}

func (t *RememberTool) Name() string { return "remember" }

func (t *RememberTool) Description() string {
	return "Save a fact, preference, decision, or note to the knowledge graph " +
		"for future recall. Cortex will extract entities and relationships from " +
		"the content automatically. Use when the user shares information worth " +
		"remembering across conversations — preferences, decisions, project " +
		"context, biographical facts."
}

func (t *RememberTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "The fact, preference, or note to remember. Phrase it as a standalone statement (e.g. 'User prefers Go over Python for backend work').",
			},
			"source": map[string]any{
				"type":        "string",
				"description": "Optional source tag (default: 'agent'). Use to distinguish facts told by the user vs. inferred from context.",
			},
		},
		"required": []string{"content"},
	}
}

func (t *RememberTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	content, _ := args["content"].(string)
	if content == "" {
		return "error: 'content' is required", nil
	}
	source, _ := args["source"].(string)
	if source == "" {
		source = "agent"
	}

	if err := t.cx.Remember(ctx, content, cortex.WithSource(source)); err != nil {
		return fmt.Sprintf("error: %s", err.Error()), nil
	}
	return "Remembered.", nil
}
```

- [ ] **Step 7: Create `internal/tools/cortextools/find_entities.go`**

NOTE: Use the actual `cortex.EntityFilter` field names you got from Step 3. The example below assumes `Name string`, `Type string`, `Limit int` — if the real names differ, substitute.

```go
package cortextools

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex"
)

type FindEntitiesTool struct {
	cx *cortex.Cortex
}

func (t *FindEntitiesTool) Name() string { return "find_entities" }

func (t *FindEntitiesTool) Description() string {
	return "Look up entities in the knowledge graph by name or type. " +
		"Use when the user mentions a specific person, project, organization, " +
		"or concept and you want to surface what cortex already knows about it."
}

func (t *FindEntitiesTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Filter by entity name (substring match).",
			},
			"type": map[string]any{
				"type":        "string",
				"description": "Filter by entity type (e.g. 'person', 'project', 'organization', 'concept').",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Max results. Default 10.",
			},
		},
	}
}

func (t *FindEntitiesTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	filter := cortex.EntityFilter{}
	if v, ok := args["name"].(string); ok {
		filter.Name = v  // adjust field name if needed
	}
	if v, ok := args["type"].(string); ok {
		filter.Type = v  // adjust field name if needed
	}
	limit := 10
	if v, ok := args["limit"]; ok {
		switch n := v.(type) {
		case float64:
			limit = int(n)
		case int:
			limit = n
		}
	}
	filter.Limit = limit  // adjust field name if needed

	entities, err := t.cx.FindEntities(ctx, filter)
	if err != nil {
		return fmt.Sprintf("error: %s", err.Error()), nil
	}
	if len(entities) == 0 {
		return "No entities found.", nil
	}
	return formatEntities(entities), nil
}
```

- [ ] **Step 8: Create `internal/tools/cortextools/get_relationships.go`**

NOTE: Check `cortex.RelFilter` for how to express direction (in/out/both). The example uses a placeholder — substitute the real RelFilter constructors.

```go
package cortextools

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex"
)

type GetRelationshipsTool struct {
	cx *cortex.Cortex
}

func (t *GetRelationshipsTool) Name() string { return "get_relationships" }

func (t *GetRelationshipsTool) Description() string {
	return "Get edges connected to an entity in the knowledge graph. " +
		"Use after find_entities to explore how an entity is connected to " +
		"other people, projects, or concepts."
}

func (t *GetRelationshipsTool) Schema() any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"entity_id": map[string]any{
				"type":        "string",
				"description": "Entity ID from find_entities or a previous recall.",
			},
			"direction": map[string]any{
				"type":        "string",
				"enum":        []string{"in", "out", "both"},
				"description": "Direction of edges to include. Default 'both'.",
			},
		},
		"required": []string{"entity_id"},
	}
}

func (t *GetRelationshipsTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	entityID, _ := args["entity_id"].(string)
	if entityID == "" {
		return "error: 'entity_id' is required", nil
	}
	direction, _ := args["direction"].(string)
	if direction == "" {
		direction = "both"
	}

	// Build the cortex.RelFilter slice based on direction. Check the
	// actual RelFilter API (Step 3) — it may be functional options like
	// cortex.WithOutgoing(true), or a struct with a Direction field.
	// The skeleton below assumes functional options; substitute.
	var filters []cortex.RelFilter
	// switch direction {
	// case "in":   filters = append(filters, cortex.WithIncoming())
	// case "out":  filters = append(filters, cortex.WithOutgoing())
	// case "both": // default: no filter
	// }

	rels, err := t.cx.GetRelationships(ctx, entityID, filters...)
	if err != nil {
		return fmt.Sprintf("error: %s", err.Error()), nil
	}
	if len(rels) == 0 {
		return fmt.Sprintf("No relationships found for %s.", entityID), nil
	}
	return formatRelationships(ctx, t.cx, rels), nil
}
```

- [ ] **Step 9: Create `internal/tools/cortextools/format.go`**

NOTE: Use real `cortex.Entity` and `cortex.Relationship` field names from Step 3.

```go
package cortextools

import (
	"context"
	"fmt"
	"strings"

	"github.com/sausheong/cortex"
)

// formatEntities renders a markdown bullet list of entities.
func formatEntities(es []cortex.Entity) string {
	var b strings.Builder
	for _, e := range es {
		fmt.Fprintf(&b, "- **%s** (%s)", e.Name, e.Type)  // adjust field names
		if e.Summary != "" {
			fmt.Fprintf(&b, " — %s", e.Summary)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// formatRelationships renders a markdown bullet list of relationships.
// Resolves subject/object IDs to entity names via cx.GetEntity for
// human-readable output.
func formatRelationships(ctx context.Context, cx *cortex.Cortex, rs []cortex.Relationship) string {
	var b strings.Builder
	for _, r := range rs {
		subjectName := r.SubjectID  // fallback to ID
		if e, err := cx.GetEntity(ctx, r.SubjectID); err == nil && e != nil {
			subjectName = e.Name
		}
		objectName := r.ObjectID
		if e, err := cx.GetEntity(ctx, r.ObjectID); err == nil && e != nil {
			objectName = e.Name
		}
		fmt.Fprintf(&b, "- %s → **%s** → %s\n", subjectName, r.Predicate, objectName)
	}
	return b.String()
}
```

- [ ] **Step 10: Create `internal/tools/cortextools/tools_test.go`**

```go
package cortextools_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/felix/internal/tools/cortextools"
)

// openTestCortex opens a fresh *cortex.Cortex on a temp file.
// Returns the cx + a cleanup that the test should defer.
func openTestCortex(t *testing.T) *cortex.Cortex {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	cx, err := cortex.Open(dbPath)
	if err != nil {
		t.Fatalf("cortex.Open: %v", err)
	}
	t.Cleanup(func() { cx.Close() })
	return cx
}

func findTool(tools []cortexTool, name string) cortexTool {
	for _, t := range tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

// cortexTool is a local alias for the harness tool.Tool interface so
// the test file doesn't have to import harness/tool directly.
type cortexTool interface {
	Name() string
	Execute(ctx context.Context, args map[string]any) (string, error)
}

func TestBuildTools_NilCortex(t *testing.T) {
	if tools := cortextools.BuildTools(nil); tools != nil {
		t.Fatalf("expected nil for nil cortex, got %d tools", len(tools))
	}
}

func TestBuildTools_FourTools(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(tools))
	}
	want := map[string]bool{"recall": false, "remember": false, "find_entities": false, "get_relationships": false}
	for _, tool := range tools {
		if _, ok := want[tool.Name()]; ok {
			want[tool.Name()] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("missing tool: %s", n)
		}
	}
}

func TestRememberThenRecall(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	var remember, recall cortexTool
	for _, tool := range tools {
		switch tool.Name() {
		case "remember":
			remember = tool
		case "recall":
			recall = tool
		}
	}

	ctx := context.Background()
	out, err := remember.Execute(ctx, map[string]any{"content": "User prefers oat milk in coffee"})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if out != "Remembered." {
		t.Fatalf("remember output: got %q want %q", out, "Remembered.")
	}

	out, err = recall.Execute(ctx, map[string]any{"query": "milk preference"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "oat") {
		t.Fatalf("recall output missing 'oat': %q", out)
	}
}

func TestRecall_EmptyResults(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	recall := findTool(tools, "recall")
	out, err := recall.Execute(context.Background(), map[string]any{"query": "xyzzy-nonexistent"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if out != "No results." {
		t.Fatalf("expected 'No results.', got %q", out)
	}
}

func TestRecall_RequiresQuery(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	recall := findTool(tools, "recall")
	out, _ := recall.Execute(context.Background(), map[string]any{})
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("expected error prefix, got %q", out)
	}
}

func TestRemember_RequiresContent(t *testing.T) {
	cx := openTestCortex(t)
	tools := cortextools.BuildTools(cx)
	remember := findTool(tools, "remember")
	out, _ := remember.Execute(context.Background(), map[string]any{})
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("expected error prefix, got %q", out)
	}
}
```

If cortex extraction needs an extractor (deterministic / hybrid / LLM) to actually produce results that `Recall` can find, the test cortex won't return hits from a bare `Open`. Two options:

1. **Skip the round-trip test** if no extractor is configured — convert `TestRememberThenRecall` to just call `Remember` and assert no error, then call `Recall` and accept either hits OR "No results.".
2. **Configure the deterministic extractor** in `openTestCortex`. Look at how `internal/cortex/cortex.go` builds its cortex (it uses `cortex.WithExtractor(deterministic.New())`) — mirror that minimal setup in the test helper.

Pick option 2 if it's a quick win; option 1 if cortex Init turns out to require more config.

- [ ] **Step 11: Build the package**

```bash
cd ~/projects/felix && go build ./internal/tools/cortextools/...
```

Expected: clean. If build errors mention `cortex.EntityFilter` field names, you used a placeholder name from the plan that didn't match the real API — adjust per the `go doc` output from Step 3.

- [ ] **Step 12: Run the tests**

```bash
cd ~/projects/felix && go test -count=1 ./internal/tools/cortextools/...
```

Expected: all tests pass. If `TestRememberThenRecall` fails because Recall returns no hits, fall back to option 1 above (or option 2). Don't paper over genuine failures.

- [ ] **Step 13: Stage and commit**

```bash
cd ~/projects/felix
git add internal/tools/cortextools/
git commit -m "feat(cortextools): native recall/remember/find_entities/get_relationships tools

New internal/tools/cortextools package wrapping a *cortex.Cortex with
four tool.Tool implementations. Each tool captures cx at construction
and exposes its method via the harness tool.Tool interface. Tests use
an ephemeral *cortex.Cortex on t.TempDir()."
```

---

## Task 2: Register the tools via tools.RegisterCortexTools

This task adds the registration helper and wires it in the three startup call sites.

**Files:**
- Modify: `internal/tools/tools.go` (add `RegisterCortexTools`)
- Modify: `internal/startup/startup.go` (3 call sites)

- [ ] **Step 1: Add RegisterCortexTools to internal/tools/tools.go**

Open `internal/tools/tools.go`. Find the `RegisterMemoryTool` function (around line 124 — confirm with `grep -n RegisterMemoryTool internal/tools/tools.go`). Add a new helper directly below it:

```go
// RegisterCortexTools registers the four cortex-backed tools (recall,
// remember, find_entities, get_relationships) wired against cx. Pass
// nil cx to skip — useful when cortex is globally disabled. Mirrors
// RegisterMemoryTool's shape.
func RegisterCortexTools(reg *Registry, cx *cortex.Cortex) {
	for _, t := range cortextools.BuildTools(cx) {
		reg.Register(t)
	}
}
```

You'll need new imports in this file:

```go
"github.com/sausheong/cortex"
"github.com/sausheong/felix/internal/tools/cortextools"
```

- [ ] **Step 2: Build to confirm import path**

```bash
cd ~/projects/felix && go build ./internal/tools/...
```

Expected: clean.

- [ ] **Step 3: Wire the call in the per-agent registry builder (chat path)**

Open `internal/startup/startup.go`. Find line ~727 (the `tools.RegisterCoreToolsWithSearch` call inside the chat-agent registry construction):

```bash
cd ~/projects/felix && grep -n "tools.RegisterCoreToolsWithSearch" internal/startup/startup.go
```

There are TWO call sites: one for the chat agent (~line 727 inside a loop), and one for the cron registry (~line 786). For now, find the chat one. Below the `tools.RegisterMemoryTool` call in the same block, add:

```go
// Cortex tools: gated on cfg.Cortex.Enabled. resolveCortex returns nil
// when cortex isn't configured for this agent's provider/model, so a
// nil cx falls through to a no-op registration.
if cfg.Cortex.Enabled {
    tools.RegisterCortexTools(reg, resolveCortex(a.Model))
}
```

- [ ] **Step 4: Wire the call in the subagent registry builder**

In `internal/startup/startup.go`, find `buildSubagentInputs` (line ~720). After its `tools.RegisterMemoryTool` line, add the same 3-line block:

```go
if cfg.Cortex.Enabled {
    tools.RegisterCortexTools(reg, resolveCortex(a.Workspace))  // NOTE: subagent uses a.Workspace OR a.Model — check what's in scope
}
```

If `resolveCortex` is defined later in the file (after `buildSubagentInputs`), declare it as a forward-referenced closure or restructure — but most likely it's already defined before. Verify with:

```bash
cd ~/projects/felix && grep -n "resolveCortex" internal/startup/startup.go
```

`resolveCortex` should appear first (line ~600), then both `buildSubagentInputs` (line ~720) and the chat-agent loop reference it.

- [ ] **Step 5: Wire the call in the cron registry builder**

Find the cron-tool registry construction at line ~786 (`cronToolReg := tools.NewRegistry()` followed by `tools.RegisterCoreToolsWithSearch(cronToolReg, ...)`). After its core/MCP/sendmessage/memory registrations, add:

```go
if cfg.Cortex.Enabled {
    tools.RegisterCortexTools(cronToolReg, resolveCortex(agentCfg.Model))
}
```

If the cron registry doesn't already register memory, also skip cortex (cron tools may intentionally be a smaller set). Check whether `RegisterMemoryTool` appears in that block; if it does, cortex matches the pattern; if not, ask before adding.

- [ ] **Step 6: Build**

```bash
cd ~/projects/felix && go build ./... 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 7: Run tests**

```bash
cd ~/projects/felix && go test -count=1 ./internal/startup/... ./internal/tools/... 2>&1 | tail -10
```

Expected: green.

- [ ] **Step 8: Commit**

```bash
cd ~/projects/felix
git add internal/tools/tools.go internal/startup/startup.go
git commit -m "feat(tools): RegisterCortexTools + wire into per-agent registries

Adds the helper that registers the four cortextools (recall, remember,
find_entities, get_relationships) against a *cortex.Cortex. Wired into
internal/startup/startup.go in three places: the chat-agent registry
builder, the subagent builder, and the cron-tool registry. All three
guard on cfg.Cortex.Enabled."
```

---

## Task 3: Auto-add cortex tool names to agent allowlists

Mirrors the existing MCP auto-add mechanism so users don't have to manually allow `recall`/`remember`/etc on every agent.

**Files:**
- Modify: `internal/config/config.go` (add helpers)
- Modify: `internal/startup/startup.go` (call the helper after registry construction)

- [ ] **Step 1: Read the existing MCP auto-add code**

```bash
cd ~/projects/felix && grep -n "mcpAutoAddedNames\|ApplyMCPToolNames\|StripMCPAutoAdded" internal/config/config.go internal/startup/startup.go
```

Read the body of `ApplyMCPToolNamesToAllowlists` and `StripMCPAutoAdded`. The cortex auto-add will follow the same shape.

- [ ] **Step 2: Decide field naming**

If the rename `mcpAutoAddedNames` → `runtimeAutoAddedNames` (per the spec's recommendation) touches more than 5 call sites, ABORT the rename and add a parallel `cortexAutoAddedNames` field instead. The simpler thing wins.

```bash
cd ~/projects/felix && grep -rn "mcpAutoAddedNames" --include="*.go"
```

If the count is small (≤5), proceed with rename. Otherwise, parallel field.

- [ ] **Step 3a (parallel-field path): Add `cortexAutoAddedNames` + helpers**

Add to the `Config` struct near `mcpAutoAddedNames`:

```go
// cortexAutoAddedNames tracks tool names that ApplyCortexToolNamesToAllowlists
// added to agent allowlists at startup. StripCortexAutoAdded removes them
// from a Config clone before persisting to disk (e.g. via the Settings UI).
cortexAutoAddedNames []string
```

Add methods near the MCP equivalents:

```go
// ApplyCortexToolNamesToAllowlists adds the given tool names to every
// agent's Tools.Allow list, in-memory only. Tracked in
// cortexAutoAddedNames so StripCortexAutoAdded can remove them before
// persisting.
func (c *Config) ApplyCortexToolNamesToAllowlists(names []string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.cortexAutoAddedNames = append([]string(nil), names...)
    for i := range c.Agents.List {
        existing := map[string]bool{}
        for _, n := range c.Agents.List[i].Tools.Allow {
            existing[n] = true
        }
        for _, n := range names {
            if !existing[n] {
                c.Agents.List[i].Tools.Allow = append(c.Agents.List[i].Tools.Allow, n)
            }
        }
    }
}

// StripCortexAutoAdded mutates out by removing names listed in
// c.cortexAutoAddedNames from every agent's Tools.Allow.
func (c *Config) StripCortexAutoAdded(out *Config) {
    c.mu.RLock()
    skip := map[string]bool{}
    for _, n := range c.cortexAutoAddedNames {
        skip[n] = true
    }
    c.mu.RUnlock()
    for i := range out.Agents.List {
        filtered := out.Agents.List[i].Tools.Allow[:0]
        for _, n := range out.Agents.List[i].Tools.Allow {
            if !skip[n] {
                filtered = append(filtered, n)
            }
        }
        out.Agents.List[i].Tools.Allow = filtered
    }
}
```

(If `mcpAutoAddedNames`'s implementation differs in any way from this skeleton, follow IT rather than this skeleton — the goal is to mirror the existing pattern exactly.)

- [ ] **Step 3b (rename path): Adapt MCP helpers to runtime-generic shape**

Rename `mcpAutoAddedNames` → `runtimeAutoAddedNames` and `ApplyMCPToolNamesToAllowlists` / `StripMCPAutoAdded` to `Apply{Runtime}` / `Strip{Runtime}`. Update all call sites. The helper takes a slice of names; callers pass the union of MCP names + cortex names.

- [ ] **Step 4: Update SaveConfig to strip cortex names too**

In `internal/gateway/settings.go`, find where `StripMCPAutoAdded` is called (the SaveConfig handler before writing to disk). Add `cfg.StripCortexAutoAdded(&clone)` next to it.

```bash
cd ~/projects/felix && grep -n "StripMCPAutoAdded" internal/gateway/settings.go
```

- [ ] **Step 5: Call ApplyCortexToolNamesToAllowlists at startup**

In `internal/startup/startup.go`, find where `ApplyMCPToolNamesToAllowlists` is called (after MCP tools are loaded). Right after that call, add:

```go
if cfg.Cortex.Enabled {
    cfg.ApplyCortexToolNamesToAllowlists([]string{
        "recall", "remember", "find_entities", "get_relationships",
    })
}
```

- [ ] **Step 6: Build**

```bash
cd ~/projects/felix && go build ./... 2>&1 | tail -10
```

- [ ] **Step 7: Tests**

```bash
cd ~/projects/felix && go test -count=1 ./internal/config/... ./internal/gateway/... ./internal/startup/... 2>&1 | tail -10
```

If config tests assert on the in-memory shape of `Agents.List[i].Tools.Allow` after construction, those may now fail because of the cortex auto-add. Update assertions to use a test config with `Cortex.Enabled = false` OR explicitly account for the auto-added names.

- [ ] **Step 8: Commit**

```bash
cd ~/projects/felix
git add internal/config/config.go internal/startup/startup.go internal/gateway/settings.go
git commit -m "feat(config): auto-add cortex tool names to agent allowlists

Mirrors the existing MCP auto-add mechanism. When cfg.Cortex.Enabled,
recall/remember/find_entities/get_relationships are added to every
agent's Tools.Allow at startup; StripCortexAutoAdded removes them
from the on-disk persistence path so user-edited allow lists stay
clean."
```

---

## Task 4: Delete the auto-recall / auto-ingest paths

The new tools fully replace the harness `KnowledgeGraph` wiring. Remove the now-dead code.

**Files:**
- Modify: `internal/agent/agent.go` (delete `cortexKGAdapter`, `hdeps.KGFn = ...` blocks, prune imports)
- Modify: `internal/cortex/cortex.go` (delete `ShouldRecall`, `IngestThread`, `IngestThreadAsync`)
- Modify: `internal/cortex/cortex_test.go` (delete tests for removed functions)

- [ ] **Step 1: Delete the KGFn block in BuildRuntimeForAgent**

In `internal/agent/agent.go`, find:

```go
if deps.CortexFn != nil {
    hdeps.KGFn = func(model string) hrt.KnowledgeGraph {
        cx := deps.CortexFn(model)
        if cx == nil {
            return nil
        }
        return &cortexKGAdapter{cx: cx}
    }
}
```

Delete the entire `if` block (about 9 lines).

- [ ] **Step 2: Delete the same block in MakeSubagentFactory**

There's a structurally identical block in `MakeSubagentFactory` (line ~207). Delete it.

- [ ] **Step 3: Delete cortexKGAdapter struct and its three methods**

Around line 245-285 — `cortexKGAdapter`, its `ShouldRecall`, `Recall`, `Ingest` methods. Delete all of it.

- [ ] **Step 4: Prune unused imports**

After steps 1-3, the imports `"github.com/sausheong/cortex"` and `conv "github.com/sausheong/cortex/connector/conversation"` may be unused. Check:

```bash
cd ~/projects/felix && go build ./internal/agent/... 2>&1
```

If errors say "imported and not used", remove the offending imports. Note that `cortexadapter` (alias for `internal/cortex`) IS still needed because `cortexStaticHint` references `cortexadapter.CortexHint`.

- [ ] **Step 5: Delete ShouldRecall, IngestThread, IngestThreadAsync from internal/cortex/cortex.go**

```bash
cd ~/projects/felix && grep -n "^func ShouldRecall\|^func IngestThread\|^func IngestThreadAsync" internal/cortex/cortex.go
```

Delete each function body. Also delete any package-level vars they reference (e.g., a `recallStopwords` slice). Check what becomes unused after the deletions.

- [ ] **Step 6: Delete the corresponding tests**

```bash
cd ~/projects/felix && grep -n "TestShouldRecall\|TestIngestThread\|TestIngestThreadAsync" internal/cortex/cortex_test.go
```

Delete those test functions. If their helpers (`buildTestCortex` or similar) become unused, delete those too.

- [ ] **Step 7: Build the world**

```bash
cd ~/projects/felix && go build ./... 2>&1 | tail -10
```

Expected: clean. If something references a deleted helper, decide whether to (a) restore the helper, or (b) delete the reference too — usually (b) for now-dead code.

- [ ] **Step 8: Run all tests**

```bash
cd ~/projects/felix && go test -count=1 ./... 2>&1 | grep -E "FAIL|ok " | tail -20
```

Expected: all green.

- [ ] **Step 9: Commit**

```bash
cd ~/projects/felix
git add internal/agent/agent.go internal/cortex/cortex.go internal/cortex/cortex_test.go
git commit -m "refactor(agent): remove auto-recall/auto-ingest cortex wiring

The cortex KnowledgeGraph adapter that ran ShouldRecall + Recall every
turn and Ingest at end-of-run is fully replaced by the new explicit
recall/remember tools. Drops:

- cortexKGAdapter struct + ShouldRecall/Recall/Ingest methods in
  internal/agent/agent.go
- hdeps.KGFn assignment in BuildRuntimeForAgent and MakeSubagentFactory
- cortexadapter.ShouldRecall + IngestThread + IngestThreadAsync

The agent now sees CortexHint in the static system prompt (added
earlier today) and decides explicitly when to call recall/remember."
```

---

## Task 5: End-to-end smoke test

- [ ] **Step 1: Start felix with cortex enabled**

Use the smoke config from earlier ports, but enable cortex:

```bash
mkdir -p /tmp/felix-cortex-smoke && rm -rf /tmp/felix-cortex-smoke/.felix
cat > /tmp/felix-cortex-smoke/felix.json5 <<'EOF'
{
  "gateway": {"host": "127.0.0.1", "port": 18891},
  "providers": {
    "anthropic": {"kind": "anthropic", "base_url": "https://api.anthropic.com", "api_key": "STUB"}
  },
  "agents": {"list": [{"id": "smoke", "name": "Smoke", "model": "anthropic/claude-sonnet-4-6", "workspace": "/tmp/felix-cortex-smoke/ws", "sandbox": "none", "tools": {"allow": ["recall", "remember"]}}]},
  "memory": {"enabled": false},
  "cortex": {"enabled": true}
}
EOF
mkdir -p /tmp/felix-cortex-smoke/ws
cd ~/projects/felix && go build -o /tmp/felix-cortex-smoke/felix-bin ./cmd/felix
```

- [ ] **Step 2: Verify the tools register and appear in the available-tools list**

```bash
cd /tmp/felix-cortex-smoke && FELIX_HOME=/tmp/felix-cortex-smoke/.felix HOME=/tmp/felix-cortex-smoke ./felix-bin start --config /tmp/felix-cortex-smoke/felix.json5 &
sleep 2
curl -s http://127.0.0.1:18891/settings/api/tools | grep -E "recall|remember|find_entities|get_relationships"
kill %1 2>/dev/null
```

Expected: all four tool names in the output. If any are missing, the registration didn't happen — back to Task 2.

- [ ] **Step 3: Verify cortex tools are auto-added to the agent's allow list**

Reload the page or curl `/settings/api/config` and confirm:
- `agents.list[0].tools.allow` contains all four names (in addition to "recall" and "remember" already declared in the smoke config).

If `find_entities` / `get_relationships` are missing, Task 3 step 5 didn't fire. Debug.

- [ ] **Step 4: Cleanup**

```bash
rm -rf /tmp/felix-cortex-smoke
```

---

## Self-review checklist

After all 5 tasks:

1. `go build ./...` clean.
2. `go test -count=1 ./...` all green.
3. `go vet ./...` quiet.
4. `grep -rn "cortexKGAdapter\|ShouldRecall\|IngestThreadAsync" internal/` returns nothing.
5. New package `internal/tools/cortextools/` has 7 files, ~600-800 LOC total.
6. Five commits, one per task.
7. Smoke test confirms tools are visible AND auto-added to allow list.

---

## If a step blocks

- **`cortex.EntityFilter` / `RelFilter` API doesn't match the placeholder in the plan** → use the real shape from `go doc` Step 3 of Task 1. The plan placeholders are educated guesses.
- **`TestRememberThenRecall` fails because Recall returns no hits without an extractor** → fall back to the simpler "remember succeeds, recall result either has hits or is 'No results.'" assertion. Don't fake success.
- **The MCP auto-add rename touches dozens of files** → use the parallel-field path (Step 3a) and skip the rename.
- **`resolveCortex` is not in scope where you need it** → restructure: move the closure declaration earlier in the file, or pass it via a struct. Don't duplicate the cortex client building.
- **A `cron-tool registry` doesn't already register memory tool** → don't add cortex there either. Cron tools are intentionally a smaller set; ask before expanding.
