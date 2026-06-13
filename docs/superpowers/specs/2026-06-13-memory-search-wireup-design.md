# P7 — Memory Search Wire-Up Design

**Date:** 2026-06-13
**Status:** Approved
**Catalogue ref:** `optimisation.md` P7 (memory vector/embedding search)

## Problem

Felix's `internal/memory` package contains a complete, hardened semantic-search
stack — an `Embedder` (`embedder.go`), a persistent embed cache
(`embedcache.go`), a startup probe (`probe.go`), a `chromem-go` vector
collection, a bounded async vector-add path (`vecSem`, hardened in Round 5/R10),
and `(*Manager).Search(query, maxResults)` which prefers vector search and falls
back to BM25.

**None of it is reachable by the agent.** The agent's memory retrieval surface
today is index-then-fetch only:

- `FormatIndex()` — a capped (200-entry) markdown index baked into the system
  prompt.
- `load_memory` (harness tool) — fetch one entry body **by ID**.
- `memory` CRUD tool (harness `MemoryTool`) — `save`/`update`/`remove`/`list`
  (all or by tag) / `get` (by ID).

`(*Manager).Search` has **zero callers**. The harness `MemoryStore` /
`MemoryProvider` interfaces do not expose a search method, so the agent cannot
retrieve memory by meaning or keyword. The write-path embedding cost is paid on
every `Save`, but the read side that would make it pay off is dead.

This wires up the read side.

## Decision: wire up (not delete)

Resolved with the user. Wiring up is cheap and additive (the machinery exists and
was hardened last round); deleting is expensive and subtractive (rip out
embedder/embedcache/probe + vector branches across Load/Save/Delete/Search, drop
`chromem-go`, unwire Settings embedder config + bundled `nomic-embed-text`
pull/pre-warm). Semantic search over **curated** memory is distinct from Cortex's
recall over **auto-extracted** knowledge-graph content, so the capability is not
redundant.

## Approach: a Felix-native `search_memory` tool

A new tool in `internal/tools`, mirroring the existing `SendMessageTool` /
`RegisterMemoryTool` pattern, that calls `(*memory.Manager).Search`. No harness
changes, no widening of the `MemoryStore` interface.

**Rejected alternative — a harness `search` action:** would force a `Search`
method onto the harness `MemoryStore` interface and every implementation of it.
Large blast radius for a Felix-only need. `Manager.Search` already lives
Felix-side, so a Felix-native tool keeps the change local.

## Tool contract

| Field | Value |
|---|---|
| Name | `search_memory` |
| Params | `query` (string, required); `max_results` (integer, optional, default 5, clamped to 1–20) |
| Concurrency-safe | `true` — pure read; `Search` takes `RLock` |
| Output | Markdown list, one line per hit |

### Output format

On hits, one line per entry:

```
- <id> — <title>: <snippet>
```

- `snippet` is the entry `Content`, rune-safe-truncated to ~200 runes using the
  existing `truncateRunes` helper (the one added in Round 5/L6). Truncation must
  never split a multi-byte rune.
- Title may be empty (some entries have no title); render `- <id>: <snippet>`
  in that case rather than a dangling em-dash.

Edge cases:

- Empty/whitespace-only `query` → `ToolResult{Error: ...}`, no `Search` call.
- No matches → `ToolResult{Output: "no matching memory entries"}` (not an error).

### Why snippet + ID, not full bodies

Keeps the index-then-load pattern consistent: `search_memory` finds candidates
by meaning/keyword, the agent then `load_memory`s the chosen ID for the full
body. Bounds token cost; the agent already knows how to chase an ID.

### Description copy (what the LLM sees)

> Search your saved memory entries by meaning or keyword. Returns matching entry
> IDs with snippets; use `load_memory` to read a full entry. Use this when the
> memory index in your system prompt doesn't show what you need.

Positions the tool against the static capped index so the agent reaches for it
when the index misses.

## Wiring

A `RegisterSearchMemoryTool(reg *Registry, searcher memorySearcher)` in
`internal/tools/tools.go`, mirroring `RegisterMemoryTool`'s shape.

To keep `internal/tools` from importing `internal/memory` (the package stays
dependency-light), the tool depends on a one-method Felix-side interface defined
in `internal/tools`:

```go
type memorySearcher interface {
    Search(query string, maxResults int) []memory.Entry // see note
}
```

**Type note:** `Search` returns `[]memory.Entry`. To avoid importing
`internal/memory` into `internal/tools`, the tool reads only the fields it needs
(`ID`, `Title`, `Content`) via a minimal local interface/struct shape. The
cleanest form that compiles without the import is to define the searcher
interface in terms of a small result type the tool owns, and have the registrar
(in `startup.go`, which already imports both packages) pass an adapter that maps
`[]memory.Entry` to that shape. If an adapter adds more friction than value, the
acceptable fallback is a direct `internal/memory` import from `internal/tools`
for the `Entry` type only — `internal/tools` already imports several internal
packages, so this is not a layering violation, merely a preference. The plan
will pick whichever is simpler at implementation time and note the choice.

`(*memory.Manager)` satisfies the searcher contract directly via its existing
`Search(string, int) []Entry` method.

### Registration sites

Called at the three existing memory-registration sites in
`internal/startup/startup.go`, each already guarded by `cfg.Memory.Enabled` /
`memMgr != nil`:

1. Main `toolReg` (~line 642, alongside `RegisterMemoryTool`)
2. Per-agent `reg` (~line 862)
3. Cron `cronToolReg` (~line 928)

When memory is disabled (`memMgr == nil`), the tool is not registered — same as
the `memory` CRUD tool. `load_memory` remains available regardless (unchanged).

## Testing

Unit tests in `internal/tools` against a fake searcher (no real Manager needed):

- `Search` returns hits → output lists IDs + titles + snippets, one per line.
- Empty/whitespace query → error result; fake searcher's `Search` not invoked.
- `max_results` clamping: `0`→5, `99`→20, `3`→3.
- No matches (searcher returns nil/empty) → friendly "no matching memory
  entries" message, not an error.
- Snippet truncation is rune-safe: an entry whose `Content` is multi-byte runes
  arranged so a naive byte-slice at the cap would split a rune; assert
  `utf8.ValidString(output)` (mirrors the Round 5/R9 regression guard).
- Entry with empty `Title` renders without a dangling em-dash.
- `IsConcurrencySafe(...)` → `true`.
- Registration: tool present in registry after `RegisterSearchMemoryTool`;
  registrar is a no-op when the searcher is nil (defensive, matching the
  `memMgr != nil` guard at call sites).

No live/integration test required — `Manager.Search` and the vector path are
already covered by `internal/memory` tests; this layer is the tool wrapper.

## Out of scope

- No changes to the harness `MemoryStore`/`MemoryProvider` interfaces.
- No changes to the embedder, embed cache, probe, or vector index internals.
- No Settings-UI surface for search (the embedder config already exists under
  Settings → Memory).
- No change to `load_memory`, the `memory` CRUD tool, or Cortex tools.
