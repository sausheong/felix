# Phase B — Agent Loop: Parallel Tool Dispatch + Permission Consolidation

**Status:** Draft
**Date:** 2026-04-29
**Scope:** `internal/agent/`, `internal/tools/`, `internal/session/session.go`, `internal/config/config.go`, `internal/startup/startup.go`, `internal/gateway/websocket.go`, `cmd/felix/main.go`
**Builds on:** Phase A (`docs/superpowers/specs/2026-04-29-agent-loop-phase-a-design.md`, merged commit `c5bbcf9`)

## Context

Phase A established the `Runtime.dispatchTool` seam — a single function that owns paired `ToolCallEntry`/`ToolResultEntry` writes and consults a `PermissionChecker` before each `Tool.Execute`. The Run loop currently calls `dispatchTool` sequentially for every tool the model emits.

Two side-effects of Phase A are still open:
1. `Session.Append` is not mutex-guarded; `dispatchTool`'s doc note explicitly defers parallel safety to Phase B.
2. Tool gating happens in two places (`FilteredRegistry` at advertisement + execute, `StaticChecker` at dispatch). Error messages diverge; tool advertisement is asymmetric across code paths.

Phase B addresses both, plus the originally-planned parallel concurrency-safe tool execution that the original Phase A spec deferred.

## Goals

- Consecutive concurrency-safe tool calls execute in parallel, capped by `FELIX_MAX_TOOL_CONCURRENCY` (default 10). Unsafe calls execute sequentially.
- Each tool declares its own concurrency-safety via a new `IsConcurrencySafe(input json.RawMessage) bool` method on the `Tool` interface.
- `Session` is safe for concurrent `Append`/`View`/`Entries`/`History` from N parallel `dispatchTool` goroutines.
- `PermissionChecker` becomes the single source of truth for both tool advertisement (`FilterToolDefs`) and dispatch enforcement (`Check`). `FilteredRegistry` is deleted.
- The duplicated `agentPolicies` build block is replaced by `Config.BuildPermissionChecker()`.

## Non-Goals

- Streaming tool execution (kicks off tools as `tool_use` blocks arrive). Deferred to Phase D.
- Subagents / Task tool. Deferred to Phase C.
- Per-input concurrency-safety classifications (e.g., `bash git status` safe vs `bash rm -rf` unsafe). Interface accepts input now for forward-compat; current impls ignore it.
- Mutex on `Session.Compact`. Compact still runs between turns (single-goroutine), so no Phase B race exists. Documented as a follow-up.
- MCP tool concurrency hints. All MCP tools default to unsafe.

## Design

### 1. Tool interface change + per-tool classifications

`internal/tools/tool.go`:

```go
type Tool interface {
    Name() string
    Description() string
    Parameters() json.RawMessage
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
    // NEW
    IsConcurrencySafe(input json.RawMessage) bool
}
```

The `input` parameter is plumbed but ignored by current impls. It exists so a future input-aware classifier can implement the same interface without a signature change.

Per-tool classifications (one new method on each existing tool struct):

| Tool | IsConcurrencySafe | Reason |
|---|---|---|
| `read_file` | `true` | pure read |
| `web_fetch` | `true` | pure remote read |
| `web_search` | `true` | pure read |
| `write_file` | `false` | concurrent writes to same path race |
| `edit_file` | `false` | same |
| `bash` | `false` | arbitrary side effects |
| `browser` | `false` | shared chromedp instance |
| `send_message` | `false` | message ordering matters |
| `cron` | `false` | mutates scheduler state |
| MCP tools | `false` (default) | conservative until per-server hints exist |

If a tool's `IsConcurrencySafe` panics on weird input, the partitioner's `recover()` treats it as unsafe.

### 2. Partitioner + parallel runner

New file `internal/agent/partition.go` (~80 lines):

```go
type batch struct {
    concurrencySafe bool
    calls           []llm.ToolCall
}

// partitionToolCalls groups consecutive concurrency-safe calls into one batch
// each, and emits a single-call batch for every unsafe call. Order within and
// across batches is preserved.
func partitionToolCalls(tcs []llm.ToolCall, ex tools.Executor) []batch

// maxToolConcurrency reads FELIX_MAX_TOOL_CONCURRENCY (default 10).
func maxToolConcurrency() int
```

The Run loop's per-tool body (added in Phase A) is replaced by a per-batch body in `runtime.go`:

```go
batches := partitionToolCalls(toolCalls, r.Tools)
for _, b := range batches {
    aborted := r.runBatch(ctx, b, &thread)
    if aborted {
        events <- AgentEvent{Type: EventAborted}
        return
    }
}
```

`runBatch` (also in `partition.go`):

- Single-call batch (safe or unsafe): direct call to `dispatchTool`. Same shape as Phase A's per-tool body.
- Concurrency-safe batch with `len > 1`: spawn `min(len(calls), maxConcurrency())` goroutines via `sync.WaitGroup` + a buffered channel as a semaphore. Each goroutine calls `r.dispatchTool(ctx, tc, &thread)` and writes its event to the events channel as it completes (completion order). On any goroutine's `aborted=true`, set a shared atomic flag and continue draining; return `aborted=true` once all goroutines join.

`errgroup` is intentionally NOT used — its `WithContext` semantics cancel siblings on error, which is the opposite of what we want (sibling reads are independent and should complete).

Sibling cancellation behavior: goroutines see the parent ctx. They are cancelled only by user abort (parent ctx cancel), never by sibling errors or sibling aborts.

### 3. Thread-safety

**`Session` gets a `sync.RWMutex`** (`internal/session/session.go`):

```go
type Session struct {
    ID, AgentID, Key string
    mu               sync.RWMutex // NEW
    entries          []SessionEntry
    entryMap         map[string]*SessionEntry
    leafID           string
    store            *Store
}
```

`Append` takes `mu.Lock`. `View`, `Entries`, `History` take `mu.RLock` AND return a copy of the internal slice (so callers don't hold a slice header into a backing array that may be reallocated).

`Compact` is intentionally NOT updated (see Non-Goals). It mutates `s.entries` directly, but only runs between turns from the single Run goroutine, so no Phase B race exists. If compaction ever moves into a parallel path, this becomes a follow-up.

The slice copy is O(N) per call; existing callers all run once-per-turn or once-per-rewrite, never in a hot inner loop. Verified by grep.

**`Runtime` gets a `cortexMu sync.Mutex`** (`internal/agent/runtime.go`):

The per-Run `thread []conversation.Message` slice is mutated from inside `dispatchTool`, which now runs in N parallel goroutines. A single `sync.Mutex` on Runtime protects the three append sites inside `dispatchTool` and the two helpers (`appendDenialResult`, `appendAbortedResult`):

```go
if cortexThread != nil {
    r.cortexMu.Lock()
    *cortexThread = append(*cortexThread, conversation.Message{...})
    r.cortexMu.Unlock()
}
```

The `dispatchTool` doc note from Phase A is updated to reflect that the function is now safe for concurrent invocation on the same Runtime.

### 4. PermissionChecker consolidation (I1)

**Interface gains one method** (`internal/tools/permission.go`):

```go
type PermissionChecker interface {
    Check(ctx context.Context, agentID, toolName string, input json.RawMessage) Decision
    // NEW
    FilterToolDefs(toolDefs []llm.ToolDef, agentID string) []llm.ToolDef
}
```

`StaticChecker.FilterToolDefs` impl: unknown agent returns the full list (matches `Check`'s allow-all default); known agent filters via `Policy.IsAllowed`. Order preserved.

**Runtime change** (`internal/agent/runtime.go`):

Replace the existing `toolDefs := r.Tools.ToolDefs()` site with:

```go
toolDefs := r.Tools.ToolDefs()
if r.Permission != nil {
    toolDefs = r.Permission.FilterToolDefs(toolDefs, r.AgentID)
}
```

This makes the LLM see only allowed tools, uniformly across all 6 Runtime sites. Today only WebSocket/CLI paths filter (via FilteredRegistry); cron/heartbeat advertise the full list. After Phase B, all paths advertise the same filtered set.

**Delete `FilteredRegistry`** (`internal/tools/policy.go`):
- Remove the `FilteredRegistry` type, `NewFilteredRegistry`, and its 4 methods.
- `tools.Policy` and `Policy.IsAllowed` stay — used by `StaticChecker`.

**Update callers** (`cmd/felix/main.go`, `internal/gateway/websocket.go`):
- Pass the bare `Registry` directly to `Runtime.Tools` (no FilteredRegistry wrapping).

### 5. F1: Config.BuildPermissionChecker helper

`internal/config/config.go`:

```go
// BuildPermissionChecker returns a tools.PermissionChecker covering every
// agent in the config. Single source of truth — used at startup and on
// hot-reload by both entry points.
func (c *Config) BuildPermissionChecker() tools.PermissionChecker {
    policies := map[string]tools.Policy{}
    for _, a := range c.Agents.List {
        policies[a.ID] = tools.Policy{
            Allow: a.Tools.Allow,
            Deny:  a.Tools.Deny,
        }
    }
    return tools.NewStaticChecker(policies)
}
```

No import cycle — `config` already imports `tools` for `ToolPolicy`.

Replaces the duplicated 11-line build block in:
- `internal/startup/startup.go:415-428` → `permission := cfg.BuildPermissionChecker()`
- `cmd/felix/main.go:340-353` → `permission := cfg.BuildPermissionChecker()`
- `internal/startup/startup.go:524` (hot-reload) → `wsHandler.SetPermission(newCfg.BuildPermissionChecker())`

## Testing

### Unit tests

1. **`internal/agent/partition_test.go`** (new) — pure-function tests for `partitionToolCalls`:
   - Empty input → empty batches
   - All safe → one batch
   - All unsafe → N single-call batches
   - Mixed `[safe, safe, unsafe, safe]` → 3 batches `[{safe,2}, {unsafe,1}, {safe,1}]`
   - Tool not found → batch as unsafe (defensive)
   - Tool whose `IsConcurrencySafe` panics → batch as unsafe (recovered)

2. **`internal/tools/permission_test.go`** — extend with `FilterToolDefs` cases:
   - Unknown agent → returns full list unchanged
   - Allow-list set → returns only listed tools
   - Deny-list set → returns all except denied
   - Empty input → empty output
   - Order preserved within the filtered output

3. **`internal/session/session_test.go`** — concurrency test:
   - 100 goroutines each call `Append` with a unique entry. After joins, assert `len(View()) == 100` and IDs unique. Run with `-race`.
   - `View()` returns a copy: mutate the returned slice (e.g., `out[0] = SessionEntry{}`); subsequent `View()` still sees the original entry at index 0.

### Integration tests (`internal/agent/agent_test.go`)

4. **`TestRun_ParallelReadsExecuteConcurrently`** — three `read_file`-style tool calls. Fake executor uses a `sync/atomic.Int32` counter for concurrent invocations and asserts the max-observed concurrency reaches at least 2. Each `Execute` blocks on a barrier (`<-chan struct{}`) until all three are in-flight, then closes the barrier. Session ends with 3 paired entries.

5. **`TestRun_UnsafeToolBreaksBatch`** — tool calls `[read, read, write, read]`. Fake executor records the timestamp of each `Execute` entry. Assert: reads 1 and 2 overlap; write starts AFTER read-1 and read-2 complete; read 4 starts AFTER write completes. Session has 4 paired entries.

6. **`TestRun_AbortDuringParallelBatch`** — three parallel reads; cancel ctx after the first completes. Remaining two see cancellation; session ends with 3 paired entries (one real, two with `Aborted=true`). Loop emits exactly one `EventAborted`.

7. **`TestRun_FilterToolDefsHidesDeniedTools`** — agent with `Deny: ["bash"]`. Capture the `ChatRequest.Tools` list passed to the LLM via the existing test stub. Assert `bash` is absent. Replaces FilteredRegistry's coverage and proves I1 consolidation works.

### Existing test compatibility

The Phase A integration tests (`TestRun_AbortMidDispatchProducesPairedSession`, `TestRun_ResumeAfterAbortIsValidAPIRequest`, `TestRun_DenyPolicyShortCircuitsExecution`) use a `noop` tool whose `IsConcurrencySafe` will return `false` (default for unknown). Behavior matches today's sequential dispatch. No changes expected.

### Config helper test

8. **`internal/config/config_test.go`** — `TestConfig_BuildPermissionChecker`: load a config with two agents (one allow-list, one deny-list). Assert the returned checker's `Check` produces the expected behavior for both agents and an unknown agent.

## Migration

No data migration. All changes are interface additions or pure refactors. Existing JSONL sessions and felix.json5 configs work unchanged.

## Risks

- **Slice-copy in Session.View/Entries/History** — O(N) per call. Verified no hot-path callers, but a future caller adding View() to an inner loop would be a slow-path surprise. Worth a doc comment on each method.
- **`runBatch` synchronization complexity** — WaitGroup + semaphore + atomic abort flag + completion-order event emission is more code than errgroup. Justified by the "let siblings complete" semantic, but the partition.go file becomes the load-bearing concurrency primitive — tests must cover abort, partial completion, and cap exhaustion.
- **`IsConcurrencySafe` panic recovery** — if a tool panics deterministically, batches degrade to sequential silently. The recover() logs at WARN so this is observable.
- **Tool advertisement now uniform across all paths** — cron/heartbeat agents that previously saw all tools now see only allowed ones. This is a behavior change. Worth calling out in a release note.
- **Removing `FilteredRegistry`** — public API surface of `internal/tools/`. No external Felix consumers, but any downstream forks would need to update.

## Out of scope (deferred)

- Streaming tool execution (Phase D).
- Subagents / Task tool (Phase C).
- Per-input concurrency-safety classification (e.g., MCP per-server safety hints).
- Mutex on `Session.Compact` (still single-goroutine; will become relevant if compaction ever moves into a parallel path).
- Hook system (PreToolUse / PostToolUse / Stop) — was item #4 in the original gap list, deliberately excluded by the user.
