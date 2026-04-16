# Cortex Recall-First and Full-Thread Ingest Design

**Date:** 2026-04-16

## Goal

Two targeted improvements to Felix's Cortex integration:

1. **Recall-first** — query Cortex exactly once per user message, before the think-act loop starts, instead of once per LLM turn.
2. **Full-thread ingest** — save the complete conversation thread (user message, tool calls, tool results, final reply) to Cortex in the background, on every exit path.

## Background

Cortex is already wired into the agent runtime (`internal/agent/runtime.go`). The current behaviour:

- **Recall**: called at the top of the `for turn` loop — so a conversation requiring 3 tool uses queries Cortex 4 times with the same message. Wasteful and semantically wrong ("first" means before any LLM work).
- **Ingest**: fires in the background but only on the `len(toolCalls) == 0` branch (final text-only turn). Error exits, max-turns exits, and abort paths never ingest. Only the initial `userMsg` + final `assistantReply` are saved, discarding all tool context.

## Architecture

Two files change: `internal/agent/runtime.go` and `internal/cortex/cortex.go`. No other files are affected.

### Change 1 — Recall before the loop (`runtime.go`)

Move the `r.Cortex.Recall(...)` call out of the `for turn` loop to immediately after the user message is appended to the session. Store the formatted result in a local `cortexContext string`. Inside the loop, inject `cortexContext` into the system prompt on every turn without re-querying.

```
// Before
for turn := 0; turn < maxTurns; turn++ {
    ...
    if r.Cortex != nil {
        systemPrompt += CortexHint
        results, _ := r.Cortex.Recall(ctx, userMsg, ...)
        systemPrompt += FormatResults(results)
    }
    ...
}

// After
cortexContext := ""
if r.Cortex != nil {
    results, err := r.Cortex.Recall(ctx, userMsg, cortex.WithLimit(5))
    if err == nil {
        cortexContext = cortexadapter.CortexHint + cortexadapter.FormatResults(results)
    } else {
        slog.Debug("cortex recall error", "error", err)
        cortexContext = cortexadapter.CortexHint
    }
}

for turn := 0; turn < maxTurns; turn++ {
    ...
    systemPrompt += cortexContext   // inject cached result, no new query
    ...
}
```

### Change 2 — Full-thread accumulation and deferred ingest (`runtime.go`)

At the start of the goroutine, initialise a `thread []conversation.Message` with the user message. Use `defer` to guarantee ingest fires on every exit path.

**Thread accumulation rules:**

| Event | Appended entry |
|---|---|
| Start of run | `{Role: "user", Content: userMsg}` |
| Assistant text (any turn) | `{Role: "assistant", Content: text}` |
| Tool call | `{Role: "assistant", Content: "[tool: name]\n" + string(input)}` |
| Tool result (success) | `{Role: "user", Content: output}` |
| Tool result (error) | `{Role: "user", Content: "[error] " + errorMsg}` |

**Deferred ingest:**

```go
if r.Cortex != nil {
    cx := r.Cortex
    defer func() {
        if len(thread) > 1 {
            go cortexadapter.IngestThread(context.Background(), cx, thread)
        }
    }()
}
```

`len(thread) > 1` guards against ingesting a user message with no response (e.g. immediate cancellation).

### Change 3 — `IngestThread` replaces `IngestConversation` (`cortex.go`)

Rename and re-signature:

```go
// Old
func IngestConversation(ctx context.Context, cx *cortex.Cortex, userMsg, assistantReply string)

// New
func IngestThread(ctx context.Context, cx *cortex.Cortex, thread []conversation.Message)
```

The body passes `thread` directly to `conversation.Ingest` — a 1:1 mapping.

Update `ShouldIngest` to accept `[]conversation.Message` and check:
- Thread has at least one assistant message (role == "assistant")
- Total content length across all messages >= `minIngestLen` (100 chars)
- First user message is not a trivial phrase

```go
func ShouldIngest(thread []conversation.Message) bool {
    if len(thread) == 0 {
        return false
    }
    // Check trivial first user message
    if trivialPhrases[strings.ToLower(strings.TrimSpace(thread[0].Content))] {
        return false
    }
    // Check combined length
    total := 0
    hasAssistant := false
    for _, m := range thread {
        total += len(strings.TrimSpace(m.Content))
        if m.Role == "assistant" {
            hasAssistant = true
        }
    }
    return hasAssistant && total >= minIngestLen
}
```

## Data Flow

```
User message arrives
       │
       ▼
Append to session
       │
       ▼
Cortex recall (once) ──► store as cortexContext
       │
       ▼
┌─────────────────────────────────┐
│  for turn := 0; turn < max; turn++ │
│                                 │
│  assemble system prompt         │
│  + inject cortexContext         │
│                                 │
│  call LLM                       │
│                                 │
│  if text ──► append to thread   │
│  if tool ──► append to thread   │
│           ──► execute tool      │
│           ──► append result     │
│                                 │
│  if no tool calls: break        │
└─────────────────────────────────┘
       │
       ▼ (defer fires on any exit)
IngestThread(thread) in goroutine
```

## Error Handling

- Recall errors: logged at DEBUG, `cortexContext` falls back to `CortexHint` only (no results section). Agent still runs normally.
- Ingest errors: `IngestThread` logs at WARN and returns. Never blocks or fails the caller.
- Deferred ingest with empty thread (`len <= 1`): skipped silently.

## Files Changed

| File | Change |
|---|---|
| `internal/agent/runtime.go` | Move recall before loop; add thread accumulation; add deferred ingest; remove old inline ingest |
| `internal/cortex/cortex.go` | Rename `IngestConversation` → `IngestThread`; change signature to `[]conversation.Message`; update `ShouldIngest` |

## Testing

- `internal/cortex/cortex_test.go` (new): unit tests for `ShouldIngest` with thread slices; test empty thread, trivial thread, short thread, valid thread.
- `internal/agent/agent_test.go`: existing tests should still pass; if Cortex is nil the new code paths are skipped entirely.
