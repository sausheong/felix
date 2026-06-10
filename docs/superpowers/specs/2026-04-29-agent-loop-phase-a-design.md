# Phase A — Agent Loop Foundations: Dispatch Invariant + Permission Gate

**Status:** Draft
**Date:** 2026-04-29
**Scope:** `internal/agent/runtime.go`, `internal/tools/`, `internal/session/session.go`, `internal/startup/startup.go`

## Context

Felix's agent loop (`internal/agent/runtime.go:Run`) is a single-goroutine think-act loop that streams an LLM response, executes any tool calls, and loops. Analysis of Claude Code's equivalent (`src/query.ts`) surfaced two structural gaps in Felix's dispatch path:

1. **Tool-use / tool-result invariant is not enforced on every exit path.** Lines 399–407 of `runtime.go` batch-save a `ToolCallEntry` for every tool the model emitted, *before* the per-tool execution loop. If the goroutine returns mid-loop (`ctx.Err()` checks at :412, :432), tool calls already saved to the session lack matching `ToolResultEntry` records. On `/resume`, the next API call can 400 because every `tool_use` block must be followed by a matching `tool_result`.

2. **No single permission gate at dispatch time.** Existing policy plumbing is fragmented:
   - `tools.Policy` (allow/deny by tool name) → applied at `FilteredRegistry` boundary
   - `tools.ExecPolicy` (bash command rules) → embedded in `BashTool`
   - `config.AgentConfig.Sandbox` → declared but enforcement is ad-hoc
   - `config.SecurityConfig.GroupPolicy` → channel-level, not tool-level

   No single seam exists where future phases (parallel tools, subagents, streaming tool execution) can plug in. Phase B/C/D would each have to re-derive what's allowed.

This phase is **foundations only**. It does not invent new policy syntax, does not move bash command policy out of `BashTool`, and does not implement the cascade described in `CLAUDE.md` (Global > Provider > Agent > Group > Sandbox). Those are deferred to a later phase.

## Goals

- Every emitted `ToolCallEntry` is paired with exactly one `ToolResultEntry`, on every exit path (clean run, tool error, permission deny, cancellation pre/mid/post-execute).
- A single `PermissionChecker` interface is consulted before every `tool.Execute`. Default impl wraps the existing per-agent `tools.Policy`.
- Aborted tool calls are distinguishable from real errors in the session (so `/resume`, the CLI, and any future UI can render them differently).
- The dispatch path becomes the single seam for Phases B (parallel), C (subagents), and D (streaming) to plug into.

## Non-Goals

- New policy syntax (per-input matching, path globs, command prefixes).
- Migrating bash `ExecPolicy` out of `BashTool`.
- Centralising sandbox enforcement.
- Implementing the full Global > Provider > Agent > Group > Sandbox cascade.
- Per-tool callback / interactive permission prompts.

## Design

### Architecture

The per-tool execution block in `Run`'s goroutine (currently `runtime.go:399–472`) is extracted into a single function:

```go
func (r *Runtime) dispatchTool(
    ctx context.Context,
    tc llm.ToolCall,
) (result tools.ToolResult, aborted bool)
```

`dispatchTool` is the only place that touches the session for tool entries. It guarantees one `ToolCallEntry` + one `ToolResultEntry` for every invocation, regardless of outcome. The session writes happen internally; the returned `tools.ToolResult` is for event emission. The Run loop's per-tool body becomes:

```go
for _, tc := range toolCalls {
    result, aborted := r.dispatchTool(ctx, tc)
    events <- AgentEvent{Type: EventToolResult, ToolCall: &tc, Result: &result}
    if aborted {
        events <- AgentEvent{Type: EventAborted}
        return
    }
}
```

For aborted dispatches, `result` carries the synthesised error (`Error: "aborted by user"`) so downstream consumers see the same shape as any other failure.

The pre-loop batch save (`runtime.go:399–407`) is **deleted**. Pairing becomes atomic per call.

This shape is what later phases need:
- Phase B (parallel) replaces the `for` with a partitioner + errgroup; each goroutine still calls `dispatchTool` and the pairing guarantee holds within each call.
- Phase C (subagents) adds an `AgentTool` whose `Execute` re-enters `Run()` — `dispatchTool` is unchanged.
- Phase D (streaming) hoists `dispatchTool` into the LLM stream loop; pairing still holds per call.

### Permission gate

New file `internal/tools/permission.go`:

```go
package tools

import (
    "context"
    "encoding/json"
    "fmt"
)

type DecisionBehavior int

const (
    DecisionAllow DecisionBehavior = iota
    DecisionDeny
)

type Decision struct {
    Behavior DecisionBehavior
    Reason   string // surfaced into tool result when Deny; empty when Allow
}

type PermissionChecker interface {
    Check(ctx context.Context, agentID, toolName string, input json.RawMessage) Decision
}

// StaticChecker is the default PermissionChecker. It wraps existing per-agent
// tools.Policy values. Input is accepted (and currently ignored) so future
// input-aware checkers can implement the same interface without a signature change.
type StaticChecker struct {
    perAgent map[string]Policy
}

func NewStaticChecker(perAgent map[string]Policy) *StaticChecker {
    return &StaticChecker{perAgent: perAgent}
}

func (c *StaticChecker) Check(_ context.Context, agentID, toolName string, _ json.RawMessage) Decision {
    p, ok := c.perAgent[agentID]
    if !ok || p.IsAllowed(toolName) {
        return Decision{Behavior: DecisionAllow}
    }
    return Decision{
        Behavior: DecisionDeny,
        Reason:   fmt.Sprintf("tool %q is not allowed for agent %q", toolName, agentID),
    }
}
```

`Runtime` gains one new field:

```go
type Runtime struct {
    // ...existing fields
    Permission tools.PermissionChecker // optional; nil = allow-all
}
```

`internal/startup/startup.go` builds a `StaticChecker` from `cfg.Agents[].Tools` once at startup and injects it into every `Runtime` it constructs.

The existing `FilteredRegistry`'s `Execute`-time check is retained as defence-in-depth. If the gate is misconfigured or bypassed, `FilteredRegistry` still blocks. The cost is one extra map lookup per tool call.

### Invariant enforcement

**Schema addition** (`internal/session/session.go`):

```go
type ToolResultData struct {
    ToolCallID string      `json:"tool_call_id"`
    Output     string      `json:"output"`
    Error      string      `json:"error,omitempty"`
    IsError    bool        `json:"is_error,omitempty"`
    Aborted    bool        `json:"aborted,omitempty"` // NEW
    Images     []ImageData `json:"images,omitempty"`
}

func AbortedToolResultEntry(toolCallID string) SessionEntry {
    // Sets Output="", Error="aborted by user", IsError=true, Aborted=true.
}
```

`omitempty` keeps old session JSONL backwards-compatible; the new field defaults to `false` when absent.

**`dispatchTool` exit-path matrix** — every row produces exactly one ToolCallEntry + one ToolResultEntry:

| Scenario | Result entry | Aborted | Loop continues |
|---|---|---|---|
| Tool runs cleanly | real output, IsError=false | false | yes |
| Tool returns `result.Error` | error string, IsError=true | false | yes |
| `Execute` returns Go error | error string, IsError=true | false | yes |
| Permission denies | denial reason, IsError=true | false | yes |
| ctx cancelled before Execute | "aborted by user" | true | no — break, emit EventAborted |
| ctx cancelled mid/post Execute (post-check sees it) | "aborted by user" | true | no — break, emit EventAborted |

**Post-execute cancel semantics:** if `Execute` completes successfully but `ctx.Err()` is non-nil afterwards, the real output is **discarded** in favour of an aborted marker. Rationale: the user pressed Ctrl-C; they don't want the output to count.

### Cortex thread accumulation

Currently scattered across `runtime.go:401–406` (tool calls) and `:459–465` (results). Both moves into `dispatchTool` so the Cortex thread is updated atomically alongside the session — either both call+result land or neither does.

### What disappears

- `runtime.go:399–407` — pre-loop batch save of ToolCallEntries
- `runtime.go:412–415` — pre-execute ctx check (now inside `dispatchTool`)
- `runtime.go:432–435` — post-execute ctx check (now inside `dispatchTool`)
- `runtime.go:438–446` — tool result logging (moves into `dispatchTool`)
- `runtime.go:449–471` — image conversion + result append (moves into `dispatchTool`)

The Run goroutine's per-tool body shrinks to ~6 lines.

## Testing

**Unit tests on `dispatchTool`** with a fake `PermissionChecker` and a fake `tools.Executor`. One test per row of the exit matrix:

- `TestDispatchTool_CleanResult` — Execute returns success → ToolResultEntry has output, IsError=false, Aborted=false
- `TestDispatchTool_ToolReturnsError` — `result.Error != ""` → IsError=true, Aborted=false
- `TestDispatchTool_ExecuteReturnsGoError` — Executor returns non-nil error → IsError=true
- `TestDispatchTool_PermissionDenied` — Checker returns Deny → IsError=true, Reason in Output, no Execute call
- `TestDispatchTool_CancelledBeforeExecute` — ctx cancelled, Executor never called → Aborted=true, returned `aborted=true`
- `TestDispatchTool_CancelledAfterExecute` — Executor completes then ctx cancelled → real output discarded, Aborted=true

**Integration tests:**

- `TestRun_AbortMidDispatchProducesPairedSession` — three tool calls in one turn; cancel ctx after tool[0] completes. Assert session ends with `[tc0, tr0(real), tc1, tr1(aborted)]` — tc2 was never dispatched (loop broke), so it is not in the session. Every saved tc has a paired tr.
- `TestRun_PermissionDenyProducesPairedSession` — agent policy denies `bash`; model emits a bash call. Assert session has `[tc, tr(deny)]` and the loop continues to subsequent tool calls in the same batch.
- `TestRun_ResumeAfterAbortIsValidAPIRequest` — abort mid-tool, persist session, reload, call `assembleMessages` on the reloaded view. Assert no orphan `tool_use` blocks (every `tool_use` has a matching `tool_result` in adjacent positions per the Anthropic API contract).

**Backwards compatibility test:**

- `TestSession_LoadOldJSONLWithoutAbortedField` — load a session JSONL written before this change, confirm `Aborted` defaults to false and parsing succeeds.

## Migration

No data migration required. The new `Aborted` field is additive. Existing sessions on disk continue to parse. New sessions start writing the field; old sessions reading new code see `Aborted=false` by default (which matches their pre-change behaviour).

## Risks

- **Per-call overhead:** one extra map lookup (PermissionChecker) and one extra ctx check per tool call. Negligible vs LLM latency.
- **Cortex thread atomicity** is now a hard invariant — if `Append` to session succeeds but the Cortex thread append fails (it shouldn't — it's an in-memory slice), state diverges. Acceptable; not a real failure mode.
- **Discarding real output on post-execute cancellation** could surprise users who pressed Ctrl-C late. Documented as "Ctrl-C means the output doesn't count." Reversible if it proves wrong.
- **`dispatchTool` is now the single place every Phase B/C/D change touches.** If the contract is wrong, churn cascades. Reviewed deliberately in this design.

## Out of scope (deferred to Phases B–D)

- Phase B: concurrency-safe partitioning + errgroup-based parallel dispatch
- Phase C: `AgentTool` that re-enters `Run()` for subagents
- Phase D: streaming tool execution (kicks off tools as `tool_use` blocks arrive)
- Bash `ExecPolicy` migration into the unified gate
- Per-input policy matching (file paths, command prefixes)
- Full cascade (Global > Provider > Agent > Group > Sandbox)
- Hook system (PreToolUse / PostToolUse / Stop)
