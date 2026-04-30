# Phase D — Agent Loop: Streaming Tool Execution

**Status:** Draft
**Date:** 2026-04-30
**Scope:** `internal/agent/`
**Builds on:** Phase A (`docs/superpowers/specs/2026-04-29-agent-loop-phase-a-design.md`, merged `c5bbcf9`), Phase B (`docs/superpowers/specs/2026-04-29-agent-loop-phase-b-design.md`, merged `45ab904`), Phase C (`docs/superpowers/specs/2026-04-29-agent-loop-phase-c-design.md`, merged `a762ad1`).

## Context

Felix today consumes the LLM's `ChatStream` to completion before dispatching any tool calls. The stream loop in `Runtime.Run` collects every tool_use block into a slice, then runs `partitionToolCalls` + `runBatch` after the stream ends. For tool-heavy responses (`safe_read foo.go; safe_read bar.go; web_fetch ...`) every tool sits idle until the LLM stops talking.

Claude Code addresses this with `StreamingToolExecutor` (`src/services/tools/StreamingToolExecutor.ts`), kicked off when its `streamingToolExecution` feature flag is on. As each `tool_use` block completes mid-stream, the executor starts the tool in parallel with the rest of the stream.

Phase D ports this pattern to Felix using the seams Phases A (`dispatchTool`), B (`partitionToolCalls`/`runBatch`), and C (`Runtime.emit`) already provide.

## Goals

- When `FELIX_STREAMING_TOOLS=1`, kick off concurrency-safe tool calls as their `EventToolCallDone` arrives mid-stream, instead of waiting for the LLM stream to end.
- Stop kickoff at the first unsafe call; the unsafe call and any later calls go through the existing post-stream `partitionToolCalls`/`runBatch` path. Preserves Phase B's "unsafe runs alone" invariant.
- Emit `EventToolResult` to the runtime's events channel as each kicked-off tool completes — possibly mid-stream, before `EventDone`.
- All Phase A, B, C invariants preserved: paired tool_call/tool_result session entries, FilterToolDefs / Permission gating, Session RWMutex, subagent event forwarding via `Runtime.emit`.
- Default off (env-gated). When unset/empty/non-`1`, behavior is byte-identical to today.

## Non-Goals

- Speculative tool kickoff before the `tool_use` JSON is fully streamed. We wait for `EventToolCallDone`, which fires only after the provider has assembled the complete tool call.
- Cross-turn streaming (kicking off tools from turn N+1 before turn N's tools complete).
- Partial tool input parsing.
- Default-on rollout. Phase D ships opt-in; flipping the default is a follow-up after CLI/WebSocket handlers are confirmed to render mid-stream tool results correctly.
- Per-tool override: streaming kickoff is all-or-nothing (gated by env), not per-tool.
- Bounding kickoff concurrency. Phase B's `FELIX_MAX_TOOL_CONCURRENCY` does NOT apply to streaming kickoffs; in practice <10 tool_use blocks per response makes a cap unnecessary.

## Design

### 1. Env helper

`internal/agent/streaming.go` (new):

```go
package agent

import "os"

// streamingToolsEnabled reports whether streaming tool kickoff is on for
// this process. Reads FELIX_STREAMING_TOOLS; only the literal "1" enables
// the feature. Anything else (unset, "0", "true", "garbage") is off.
//
// Strict "1" rather than truthy parsing keeps the env contract simple and
// matches Claude Code's binary-feature-gate posture.
func streamingToolsEnabled() bool {
    return os.Getenv("FELIX_STREAMING_TOOLS") == "1"
}

// kickoffResult is the channel payload sent by a streaming-kickoff goroutine
// once dispatchTool returns. The result fields mirror dispatchTool's return
// shape; aborted=true signals the caller to break out of the post-stream
// await loop and emit EventAborted.
type kickoffResult struct {
    aborted bool
}
```

`kickoffResult` only needs the `aborted` bool because:
- The session entries are already paired by `dispatchTool` itself.
- The `EventToolResult` is already emitted by the kickoff goroutine via `r.emitToolResult`.
- The post-stream await loop only needs to know whether to abort.

### 2. Run loop changes

`internal/agent/runtime.go`. The current stream-handling block (~lines 356-390 in current main):

```go
for event := range stream {
    switch event.Type {
    case llm.EventTextDelta: ...
    case llm.EventToolCallStart: ...
    case llm.EventToolCallDone:
        if event.ToolCall != nil {
            toolCalls = append(toolCalls, *event.ToolCall)
        }
    case llm.EventDone: ...
    case llm.EventError: ...
    }
}
```

Becomes:

```go
streamingOn := streamingToolsEnabled()
kickoffs := map[string]chan kickoffResult{}
kickoffStopped := false

for event := range stream {
    switch event.Type {
    case llm.EventTextDelta: // unchanged
    case llm.EventToolCallStart: // unchanged
    case llm.EventToolCallDone:
        if event.ToolCall == nil {
            continue
        }
        tc := *event.ToolCall
        toolCalls = append(toolCalls, tc)
        if !streamingOn || kickoffStopped {
            continue
        }
        if !isCallConcurrencySafe(tc, r.Tools) {
            kickoffStopped = true
            continue
        }
        ch := make(chan kickoffResult, 1)
        kickoffs[tc.ID] = ch
        go func() {
            result, aborted := r.dispatchTool(ctx, tc, cortexThreadOrNil(r.Cortex, &thread))
            r.emitToolResult(turn, tc, result, aborted)
            ch <- kickoffResult{aborted: aborted}
        }()
    case llm.EventDone: // unchanged
    case llm.EventError:
        drainKickoffs(kickoffs)  // wait for in-flight goroutines to settle
        r.emit(AgentEvent{Type: EventError, Error: event.Error})
        return
    }
}
```

Then the existing post-stream "save assistant text + dispatch tool calls" block becomes:

```go
// Save assistant response (unchanged).
if textContent.Len() > 0 { ... }
if len(toolCalls) == 0 {
    if len(kickoffs) > 0 {
        // Defensive: shouldn't happen — kickoff implies a tool_use was added
        // to toolCalls. Drain anyway.
        drainKickoffs(kickoffs)
    }
    r.emit(AgentEvent{Type: EventDone})
    return
}

// Resolve kickoffs in stream order; collect non-kicked-off tools for the batcher.
var pending []llm.ToolCall
for _, tc := range toolCalls {
    if ch, ok := kickoffs[tc.ID]; ok {
        kr := <-ch
        if kr.aborted {
            drainKickoffsExcept(kickoffs, tc.ID)
            r.emit(AgentEvent{Type: EventAborted})
            return
        }
        continue
    }
    pending = append(pending, tc)
}

// Pending tail goes through Phase B's batcher unchanged.
batches := partitionToolCalls(pending, r.Tools)
for _, b := range batches {
    if r.runBatch(ctx, b, cortexThreadOrNil(r.Cortex, &thread), turn, tr) {
        r.emit(AgentEvent{Type: EventAborted})
        return
    }
}
```

Helpers:

```go
// drainKickoffs blocks until every kickoff channel has received a value, then
// returns. Used on early-return paths (LLM error, abort) so kickoff goroutines
// fully settle before Run() returns and r.events closes.
func drainKickoffs(kickoffs map[string]chan kickoffResult) {
    for _, ch := range kickoffs {
        <-ch
    }
}

// drainKickoffsExcept is drainKickoffs but skips the channel keyed by skipID
// (the caller has already consumed that one).
func drainKickoffsExcept(kickoffs map[string]chan kickoffResult, skipID string) {
    for id, ch := range kickoffs {
        if id == skipID {
            continue
        }
        <-ch
    }
}
```

These helpers live in `streaming.go` alongside `kickoffResult`.

### 3. Why dispatchTool stays untouched

`dispatchTool` (Phase A) already handles ctx cancellation pre/post-execute, appends paired session entries, and routes through `r.cortexMu` for cortex updates (Phase B). Calling it from a kickoff goroutine satisfies all those invariants without any changes:

- Session.Append is RWMutex-guarded (Phase B) — multiple kickoff goroutines + the post-stream batcher can append concurrently without races.
- `r.cortexMu` serializes cortex thread appends (Phase B) — same protection.
- `dispatchTool`'s pre-execute and post-execute `ctx.Err()` checks (Phase A) fire on user abort, so a cancelled run pairs every in-flight kickoff with an aborted ToolResultEntry.

`r.emitToolResult` (Phase C T3 — now a method on Runtime) routes through `r.emit`, which is goroutine-safe (channel send is, and the non-blocking parent forward in `emit()` uses select+default).

### 4. Cancellation & abort sequencing

**LLM error mid-stream**: `EventError` from the stream → drain all kickoff channels (each kickoff goroutine sees ctx.Err() if ctx was already cancelled, otherwise completes its tool normally) → emit EventError → return.

**User abort (ctx cancel)**:
1. The LLM stream goroutine sees ctx cancellation and ends.
2. Each kickoff goroutine's `dispatchTool` sees `ctx.Err()` in the pre- or post-execute check, writes the AbortedToolResultEntry, returns `aborted=true`.
3. The post-stream await loop receives the first `aborted=true`, drains remaining kickoff channels (so they fully settle before Run returns), emits one `EventAborted`, returns.

This produces exactly one `EventAborted` and pairs every tool_use with a tool_result — matching today's behavior.

**Backpressure**: `r.events` is buffered at 100. Kickoff goroutines write `EventToolResult` via `r.emit`. If the consumer is slow and the channel fills, the kickoff goroutine blocks on the send. Same backpressure model as today's post-stream emits.

### 5. Subagent integration (Phase C)

A subagent runtime running with `FELIX_STREAMING_TOOLS=1` gets streaming kickoff just like a top-level runtime — it inherits the same Run loop. Each kickoff goroutine's `r.emit` still routes through `r.Parent.events` non-blockingly when `r.Parent != nil`. So a subagent's mid-stream tool results forward to the parent's stream automatically.

The Phase C test `TestRun_SubagentEventsForwardedToParent` asserts forwarded events appear in the parent's stream. With streaming kickoff enabled, those forwarded events may arrive earlier and more interleaved, but the test asserts presence, not strict ordering — should still pass. The Phase D test suite includes a streaming + subagent integration test (Test 6 below) to make this explicit.

### 6. No call-site changes

The Phase D feature is fully internal to `internal/agent/runtime.go`. No change to `cmd/felix/main.go`, `internal/startup/startup.go`, `internal/gateway/websocket.go`, or any tool implementation. Default-off env gate means existing deployments are unaffected.

## Testing

`internal/agent/streaming_test.go` (new):

### Unit tests

1. **`TestStreamingToolsEnabled_Default`** — env unset → false.
2. **`TestStreamingToolsEnabled_Override`** — env "1" → true.
3. **`TestStreamingToolsEnabled_InvalidFallsBack`** — env "0", "true", "garbage", " 1 " → false (strict-"1" contract).

### Integration tests

4. **`TestRun_StreamingKickoffOverlapsWithLLMStream`** — fake LLM emits text → tool_use(safe) → text → done with `time.Sleep(50ms)` between blocks. Tool execution must START (recorded via atomic timestamp) before the second text delta arrives. Without streaming, tool starts AFTER `done`.

5. **`TestRun_StreamingStopsAtFirstUnsafe`** — stream `[safe1, safe2, unsafe1, safe3]`. Assert: safe1+safe2 ran in parallel mid-stream (overlapping atomic timestamps); unsafe1 ran post-stream alone; safe3 ran after unsafe1 (single-call batch, since it's the only remaining safe call after partition).

6. **`TestRun_StreamingDisabledMatchesNonStreaming`** — same scripted stream with FELIX_STREAMING_TOOLS unset. Behavior identical to today: no tool runs until after stream ends. Pin the default-off contract.

7. **`TestRun_StreamingAbortMidKickoffPairsAllEntries`** — kick off 3 safe tools mid-stream; cancel ctx after the first completes. Assert: session has 3 paired tool_call/tool_result entries (one real, two aborted); exactly one `EventAborted` event in the stream; no goroutine leaks (use `runtime.NumGoroutine` before/after with a small grace period).

8. **`TestRun_StreamingResultEmittedBeforeStreamEnds`** — tool finishes mid-stream. Assert: `EventToolResult` lands on the events channel BEFORE the LLM's `EventDone` does. Validates the live-emit contract from Section 3 ("Result timing").

9. **`TestRun_SubagentStreamingForwardsToParent`** — subagent with streaming enabled emits a tool_use mid-stream; parent receives forwarded `EventToolResult` with `AgentID` set to subagent's ID. Validates Phase C interaction.

All tests run under `go test -race ./internal/agent/...`.

## Migration

No data migration. No config schema change. No JSONL change. New env var defaults to off.

## Risks

- **R1: Mid-stream EventToolResult breaks UI ordering assumptions.** Today consumers see all tool results AFTER `EventDone`. Phase D enables `EventToolResult` arriving mid-stream. Felix's CLI/WebSocket renderers may not handle this. **Mitigation**: env gate. CLI/WS handlers can be audited separately and updated before flipping the default.
- **R2: Kickoff goroutine leaks on early Run return.** If Run returns without draining kickoffs, goroutines leak. **Mitigation**: `drainKickoffs` called on every early-return path (LLM error, abort, after-stream return when toolCalls is empty).
- **R3: Cortex thread ordering differs from session ordering.** Kickoff goroutines append to `cortexThread` in completion order, not stream order. Cortex sees `[asst, call1, result2, result1, call2, ...]` instead of `[asst, call1, result1, call2, result2, ...]`. **Mitigation**: pair-by-id is preserved (call and result still reference the same tool_call_id). If observed to confuse Cortex, follow-up: defer cortex appends to post-stream and append in stream order.
- **R4: Tool with hidden dependency on a parallel sibling's result.** Two `safe` tools depending on each other are a tool bug (already a Phase B concern), not Phase D. The `IsConcurrencySafe` contract is the truth.
- **R5: `dispatchTool` panics in a kickoff goroutine.** A panic in the goroutine would terminate the process. **Mitigation**: existing Phase A code doesn't recover; this isn't a new exposure. If it becomes one, wrap kickoffs in `defer recover()` and surface as an error result.
- **R6: Channel-fill deadlock between LLM goroutine and kickoff goroutine.** Both write to `r.events`. Consumer is single-threaded. If consumer stalls and channel fills, both block. Same model as today; no new deadlock.

## Out of scope (deferred)

- Default-on rollout (after CLI/WS handler audit).
- Per-tool concurrency-safety hints from MCP servers.
- Speculative kickoff before `EventToolCallDone`.
- Cross-turn tool streaming.
- Wrapping kickoffs in Phase B's semaphore (`FELIX_MAX_TOOL_CONCURRENCY`).
- Defer-cortex-append-to-post-stream fix for R3 (only if observed).
