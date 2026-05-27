# Cortex as native Felix tools

**Status:** design
**Date:** 2026-05-27

## Goal

Make four cortex operations — `recall`, `remember`, `find_entities`, `get_relationships` — into native Felix built-in tools. Remove the existing automatic-per-turn `Recall` and automatic-end-of-run `Ingest` paths. The agent decides when to read from and write to its knowledge graph, just like it decides when to use any other tool.

## Why this change

Felix today wraps cortex via the harness `KnowledgeGraph` interface (`ShouldRecall`, `Recall`, `Ingest`). Recall fires automatically every turn whose query passes a heuristic; ingest fires automatically at end-of-run. The agent has no say. Symptoms:

- Recall injects context the agent may not want, costing tokens.
- The agent never sees a single mention of cortex unless recall happens to fire, so it can't reason about its memory.
- Ingest saves whole conversations indiscriminately, polluting the graph with chat noise.

The MCP-tool model is more honest: cortex is a memory backend, tools are how the agent uses it. The user now wants the same model embedded natively (no MCP server hop).

## Scope

**In:**

- Four tools registered as Felix native tools when `cfg.Cortex.Enabled`:
  - `recall(query, limit?, min_confidence?)`
  - `remember(content, source?)`
  - `find_entities(name?, type?, limit?)`
  - `get_relationships(entity_id, direction?)`
- Auto-add the four tool names to every agent's `Tools.Allow` list at startup, gated on `cfg.Cortex.Enabled` (mirrors the existing MCP auto-add mechanism). Per-agent `Tools.Deny` still wins.
- Wire the new tool factory into `BuildAgentRegistry` (`internal/tools/registry.go`).
- Remove the auto-recall + auto-ingest paths: delete `cortexKGAdapter` in `internal/agent/agent.go` and the `hdeps.KGFn = ...` assignment in both `BuildRuntimeForAgent` and `MakeSubagentFactory`.
- Delete now-orphan helpers: `cortexadapter.ShouldRecall`, `cortexadapter.IngestThreadAsync`. Keep `FormatResults` (the new `recall` tool uses it).
- Keep the existing `CortexHint` (the short tool-oriented version the user wrote) in `internal/cortex/cortex.go`. Keep `cortexStaticHint(cfg)` in `internal/agent/agent.go` so the hint flows into the static system prompt when cortex is enabled.

**Out:**

- Tools for `traverse`, `get_entity`, `get_memories_by_entity`, `forget`, `lint`, `merge_entities`. Add later if asked.
- An MCP server exposing the same tools. The user chose embedded.
- Backfill / migration from data ingested via the old auto-ingest path. Existing graph data stays usable — `recall` just queries the same SQLite DB.

## Architecture

```
LLM tool call: recall(query=...)
    │
    ▼
internal/tools/cortextools.NewRecallTool(cx)
    │
    ▼
*cortex.Cortex.Recall(ctx, query, WithLimit, WithMinConfidence)
    │
    ▼
cortexadapter.FormatResults(results) → markdown string → LLM
```

Same shape for `remember` (`cx.Remember`), `find_entities` (`cx.FindEntities`), `get_relationships` (`cx.GetRelationships`).

No harness changes. The four tools are pure additions to the Felix-side registry; the harness sees them just like `read_file` or `web_fetch`. Removing the KG wiring is felix-side only (the harness `KnowledgeGraph` interface and the runtime's per-turn recall hook stay, untouched, in the harness library — felix just stops handing in a KG).

### Components

- **`internal/tools/cortextools/`** — new package.
  - `tools.go` — `BuildTools(cx *cortex.Cortex) []tool.Tool` factory. Returns the 4 tools wired against a single Cortex instance.
  - `recall.go` — tool struct + `Execute(ctx, args) (string, error)`.
  - `remember.go` — ditto.
  - `find_entities.go` — ditto.
  - `get_relationships.go` — ditto.
  - `format.go` — shared markdown helpers for entity / relationship rendering (FormatResults stays in `internal/cortex/`).
  - `tools_test.go` — table-driven tests against an ephemeral `*cortex.Cortex`.

- **`internal/tools/registry.go`** — `BuildAgentRegistry` signature gains a `cortexFn func(model string) *cortex.Cortex` parameter (or, simpler: pass it through `RegistryDeps` if a deps struct exists). When `cfg.Cortex.Enabled` AND `cortexFn != nil`, resolve `cx := cortexFn(agent.Model)`; if non-nil, register the four tools.

- **`internal/config/`** — auto-add the four tool names to each agent's `Tools.Allow`. The existing `Config.mcpAutoAddedNames` / `ApplyMCPToolNamesToAllowlists` / `StripMCPAutoAdded` machinery handles exactly this pattern for MCP tools. Reuse the same field (rename to `runtimeAutoAddedNames`) so both MCP and cortex auto-adds share one list, OR add a parallel `cortexAutoAddedNames`. Recommend reuse: smaller surface, same semantics.

- **`internal/cortex/cortex.go`** — no changes beyond what landed earlier today. `CortexHint` stays as the short tool-oriented text. `FormatResults` stays (the recall tool uses it).

- **`internal/agent/agent.go`** — delete: the `hdeps.KGFn = ...` blocks (lines 207-215 in `BuildRuntimeForAgent` and the equivalent in `MakeSubagentFactory`), the `cortexKGAdapter` struct + its methods (lines 245-285), and the now-unused imports (`conv "github.com/sausheong/cortex/connector/conversation"`, `"github.com/sausheong/cortex"` if no other ref remains). Keep `cortexStaticHint`.

- **`internal/cortex/cortex.go`** — delete: `ShouldRecall`, `IngestThread`, `IngestThreadAsync`. Their tests in `cortex_test.go` go too.

### Data flow per turn

```
User: "what's my name?"
   │
   ▼
harness builds turn (no KG hook fires — KGFn was removed)
   │
   ▼
LLM sees static prompt:
   - IDENTITY.md
   - configured agents list
   - skills index
   - memory entries index
   - FELIX.md / AGENTS.md
   - CortexHint ("you have access to cortex; call recall/remember/...")
   │
   ▼
LLM decides: "I should check cortex first."
   │
   ▼
Tool call: recall({"query": "user's name"})
   │
   ▼
cortextools.recall.Execute → cx.Recall(...) → FormatResults → markdown
   │
   ▼
LLM uses the result. May call remember("user's name is X") if learning new info.
   │
   ▼
Turn ends. No auto-ingest. Graph only grows when agent explicitly calls remember.
```

## Components and contracts

### `cortextools.BuildTools`

```go
package cortextools

import (
    "github.com/sausheong/cortex"
    "github.com/sausheong/harness/tool"
)

// BuildTools returns the four cortex-backed tools wired against cx.
// Returns nil when cx is nil — caller (BuildAgentRegistry) decides
// whether to register the result based on its own gating.
func BuildTools(cx *cortex.Cortex) []tool.Tool {
    if cx == nil {
        return nil
    }
    return []tool.Tool{
        newRecallTool(cx),
        newRememberTool(cx),
        newFindEntitiesTool(cx),
        newGetRelationshipsTool(cx),
    }
}
```

### `recall` tool

**Name:** `recall`
**Description:** `Search the knowledge graph for context relevant to a query. Returns entities, memories, and document chunks ranked by relevance. Use at the start of a conversation, or whenever you need to check what you already know about a person, project, or topic before asking the user.`

**Schema:**
```json
{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Natural-language search query — keywords or a short phrase."},
    "limit": {"type": "integer", "description": "Max number of results. Default 5.", "default": 5},
    "min_confidence": {"type": "number", "description": "Filter out results with confidence below this (0.0–1.0). Omit to include all."}
  },
  "required": ["query"]
}
```

**Behavior:**
- Call `cx.Recall(ctx, query, WithLimit(limit), WithMinConfidence(min_confidence))`.
- Format with `cortexadapter.FormatResults`. Returns the existing `## Cortex Knowledge Graph\n...` markdown block.
- Empty results → return `"No results."` (lower-cased, no header).
- Errors → return `error: <message>`.

### `remember` tool

**Name:** `remember`
**Description:** `Save a fact, preference, decision, or note to the knowledge graph for future recall. Cortex will extract entities and relationships from the content automatically. Use when the user shares information worth remembering across conversations — preferences, decisions, project context, biographical facts.`

**Schema:**
```json
{
  "type": "object",
  "properties": {
    "content": {"type": "string", "description": "The fact, preference, or note to remember. Phrase it as a standalone statement (e.g. 'User prefers Go over Python for backend work')."},
    "source": {"type": "string", "description": "Optional source tag (default: 'agent'). Use to distinguish facts told by the user vs. inferred from context."}
  },
  "required": ["content"]
}
```

**Behavior:**
- Call `cx.Remember(ctx, content, WithSource(source))`. Source defaults to `"agent"` if not given.
- On success: return `"Remembered."` (one word — token-cheap).
- On error: return `error: <message>`.

### `find_entities` tool

**Name:** `find_entities`
**Description:** `Look up entities in the knowledge graph by name or type. Use when the user mentions a specific person, project, organization, or concept and you want to surface what cortex already knows about it.`

**Schema:**
```json
{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Filter by entity name (substring match)."},
    "type": {"type": "string", "description": "Filter by entity type (e.g. 'person', 'project', 'organization', 'concept')."},
    "limit": {"type": "integer", "description": "Max results. Default 10.", "default": 10}
  }
}
```
(No `required` — either filter is optional; both empty = return up to limit entities.)

**Behavior:**
- Build `cortex.EntityFilter` from args. Call `cx.FindEntities(ctx, filter)`.
- Format as a markdown list: `- **<name>** (<type>) — <summary>`.
- Empty results → `"No entities found."`.
- Errors → `error: <message>`.

### `get_relationships` tool

**Name:** `get_relationships`
**Description:** `Get edges connected to an entity in the knowledge graph. Use after find_entities to explore how an entity is connected to other people, projects, or concepts.`

**Schema:**
```json
{
  "type": "object",
  "properties": {
    "entity_id": {"type": "string", "description": "Entity ID from find_entities or a previous recall."},
    "direction": {"type": "string", "enum": ["in", "out", "both"], "description": "Direction of edges to include. Default 'both'.", "default": "both"}
  },
  "required": ["entity_id"]
}
```

**Behavior:**
- Call `cx.GetRelationships(ctx, entity_id)`. Cortex's `RelFilter` API supports direction filtering; check its signature and pass the appropriate `RelFilter`.
- Format as markdown: `- <subject_name> → **<predicate>** → <object_name>`.
- Empty results → `"No relationships found for <entity_id>."`.
- Errors → `error: <message>`.

### Wiring changes

1. **`internal/tools/registry.go`** — extend `BuildAgentRegistry` to accept (and use) a `cortexFn`:

   ```go
   // Before:
   func BuildAgentRegistry(agent *config.AgentConfig, deps RegistryDeps) *tool.Registry {
       // ...
   }

   // After (deps gains a CortexFn field — or it already has one; check):
   type RegistryDeps struct {
       // ... existing fields
       CortexFn  func(model string) *cortex.Cortex
       CortexCfg config.CortexConfig
   }
   ```

   Inside `BuildAgentRegistry`, after the existing tool registration:
   ```go
   if deps.CortexCfg.Enabled && deps.CortexFn != nil {
       if cx := deps.CortexFn(agent.Model); cx != nil {
           for _, t := range cortextools.BuildTools(cx) {
               reg.Register(t)
           }
       }
   }
   ```

2. **`internal/agent/agent.go`** — DELETE the KGFn assignment in `BuildRuntimeForAgent`:
   ```go
   // delete this whole if block (was lines 207-215):
   if deps.CortexFn != nil {
       hdeps.KGFn = func(model string) hrt.KnowledgeGraph { ... }
   }
   ```
   And the equivalent in `MakeSubagentFactory`. Then delete `cortexKGAdapter` and its three methods (lines ~245-285).

3. **`internal/config/config.go`** — make the auto-add mechanism polymorphic. Rename `mcpAutoAddedNames` to `runtimeAutoAddedNames`. Add a method like:
   ```go
   // ApplyRuntimeToolNamesToAllowlists adds the given tool names to every
   // agent's Tools.Allow at startup. The names are tracked in
   // runtimeAutoAddedNames so StripRuntimeAutoAdded can remove them
   // before persisting to disk (e.g. via the Settings UI's SaveConfig).
   func (c *Config) ApplyRuntimeToolNamesToAllowlists(names []string)
   func (c *Config) StripRuntimeAutoAdded(out *Config)
   ```
   Caller in `internal/startup/startup.go` invokes this with the union of MCP tool names + (if cortex enabled) the four cortex tool names.

4. **`internal/cortex/cortex.go`** — delete `ShouldRecall`, `IngestThread`, `IngestThreadAsync`. Keep `CortexHint`, `FormatResults`, `Provider`, all the Recall/Open plumbing.

5. **`internal/cortex/cortex_test.go`** — delete tests for the three removed functions. Keep everything else.

## Format helpers

In `internal/tools/cortextools/format.go`:

```go
func formatEntities(es []cortex.Entity) string  // markdown list
func formatRelationships(rs []cortex.Relationship, cx *cortex.Cortex, ctx context.Context) string  // resolves entity IDs to names via cx.GetEntity for readable output
```

The recall tool reuses `cortexadapter.FormatResults` directly — no wrapping needed.

## Error handling

- All four tools follow Felix convention: return a string. Errors prefixed with `error:` so the LLM sees them but the runtime doesn't treat them as fatal.
- `cx.Recall` / `cx.Remember` / etc. surface their own errors (typically DB-level). Pass through with the prefix.
- Cancel-aware: each tool's `Execute` honors `ctx` cancellation — cortex methods already take ctx.

## Testing

`internal/tools/cortextools/tools_test.go`:

```go
func TestRecallTool_HappyPath(t *testing.T) {
    cx := openTestCortex(t)  // helper: open *cortex.Cortex on t.TempDir()
    defer cx.Close()
    // Seed two memories.
    cx.Remember(ctx, "User likes oat milk")
    cx.Remember(ctx, "Project Felix uses Go")
    tools := cortextools.BuildTools(cx)
    recall := findTool(tools, "recall")
    out, err := recall.Execute(ctx, map[string]any{"query": "milk"})
    assert.NoError(t, err)
    assert.Contains(t, out, "oat milk")
}

func TestRememberTool_Persists(t *testing.T) { ... }
func TestFindEntitiesTool_FilterByType(t *testing.T) { ... }
func TestGetRelationshipsTool_BothDirections(t *testing.T) { ... }
func TestRecallTool_EmptyResults(t *testing.T) { ... }  // returns "No results."
func TestRememberTool_Error(t *testing.T) { ... }       // closed cortex
func TestBuildTools_NilCortex(t *testing.T) { ... }     // returns nil
```

Plus a registry test:

```go
func TestBuildAgentRegistry_CortexEnabledRegistersTools(t *testing.T) { ... }
func TestBuildAgentRegistry_CortexDisabledOmitsTools(t *testing.T) { ... }
```

## Risks and mitigations

1. **Graph stops growing if agent forgets to call remember.** Mitigation: the hint text explicitly tells the agent to call `remember` for preferences/decisions. If real-world usage shows it forgets, follow-up could re-introduce an opt-in auto-ingest for a per-turn summary (out of scope here).

2. **Tool name collision with user-added MCP servers.** Mitigation: MCP servers support `tool_prefix` in their config (e.g. set `prefix: "ext_"` on the user's external memory server). Document this in the spec.

3. **`*cortex.Cortex` per agent model.** `CortexFn(model)` caches per `provider/model`. Two agents on the same model share one client (safe — cortex.Cortex is concurrency-safe per its docs). Different models get different clients. Tools inherit this.

4. **Auto-add fights with the Settings UI save round-trip.** The MCP auto-add already solves this with `StripMCPAutoAdded`. Extending it to cover cortex names follows the same pattern. Test: enable cortex → save → reload → cortex tool names appear at runtime, but the on-disk allow list stays clean.

5. **Per-agent opt-out.** Some agents may want NO cortex tools even when cortex is globally enabled. The existing `Tools.Deny` list works — `deny: ["recall", "remember", "find_entities", "get_relationships"]` disables them per-agent. Document.

6. **Deleting `cortexKGAdapter` is a one-way trip.** If we ever want auto-recall back, it's a git revert away. Risk = nil.

## Migration / compatibility

- **Config schema:** unchanged. `cfg.Cortex.Enabled` keeps its semantics ("cortex is on for this Felix install").
- **On-disk graph data:** unchanged. `~/.felix/brain.db` is the same SQLite DB queried by both old auto-recall and new tools.
- **User-visible behavior:** the `## Cortex Knowledge Graph` block stops appearing automatically per turn. Either the agent calls `recall` explicitly (and the block appears as a tool result), or the user gets no recall. This is the intended outcome.
- **Older Felix conversations:** sessions persisted to disk include past auto-recall results. Reloading those sessions still works; they're just snapshots.

## Follow-ups (not in this wave)

- `traverse`, `forget`, `lint`, `merge_entities` as additional tools.
- Tool to expose `cx.GetMemoriesByEntity` for entity-anchored memory browsing.
- A Settings UI affordance to view auto-added runtime tool names (debug visibility).
- Decision on whether to bundle a tiny "auto-summarize-and-remember" cron job to keep the graph growing for users who don't want to rely on the agent's discretion.
