# Chat message timestamps Design

**Date:** 2026-06-13
**Status:** Approved
**Repo:** `felix` only (`internal/gateway/chat.go`, `internal/gateway/websocket.go`).

## Problem

The chat web UI renders user and assistant message bubbles with no indication of
when each message was sent/completed. Users want a datetime on every chat bubble.

The data already exists: the harness `session.SessionEntry` carries a
`Timestamp int64` (Unix **seconds**, set at append time). It is persisted for
every entry and returned by `Session.History()`. But `handleSessionHistory`
(`internal/gateway/websocket.go`) does not forward it to the client, and the
front-end never displays a time.

## Decisions (resolved with user)

- **Format:** time-only for messages from *today* (e.g. `2:32 PM`); `Mon D, h:mm AM`
  for older messages (e.g. `Jun 12, 9:01 AM`). Locale-aware via
  `toLocaleTimeString` / `toLocaleDateString`.
- **Scope:** both **user** and **assistant** bubbles. Tool-call rows are left
  unchanged (they are not "messages").
- **Visibility:** always shown — a small muted caption beneath the bubble.

## Architecture

Two surfaces produce message bubbles:

1. **Live stream** — `addUserMsg(text)` on send; `addAssistantMsg()` + a later
   `done` event for the reply. These happen in real time, so the client's own
   clock (`Date.now()`) is the completion time.
2. **History replay** — the `history` response handler iterates persisted
   entries and calls `addUserMsg` / `addAssistantMsg`. Here the **persisted**
   timestamp must be used, not the current time.

A third surface, **run replay mode** (`renderReplayMode`), repaints a single
past run's *event log* (assistant side only, no message timestamps in that log).
It reuses `addAssistantMsg`. We deliberately **omit** captions there rather than
stamp a misleading "now" — replay bubbles get no time.

### Server change (`websocket.go`)

In `handleSessionHistory`, add `"timestamp": entry.Timestamp` to the
`message`-type entry maps (both user and assistant). Tool entries unchanged.
`entry.Timestamp` is Unix **seconds**.

### Client changes (`chat.go`)

- **`formatMsgTime(unixSeconds)`** helper: returns a string. `0`/falsy →
  empty string (caption omitted). Same calendar day as now → `toLocaleTimeString`
  with `{hour:'numeric', minute:'2-digit'}`. Otherwise →
  `toLocaleDateString({month:'short', day:'numeric'})` + `', '` + the same time.
- **`addUserMsg(text, tsSeconds)`** and **`addAssistantMsg(tsSeconds)`** gain an
  optional timestamp arg. When omitted (live path), default to
  `Math.floor(Date.now()/1000)`. A `.msg-time` `<div>` caption is appended **as a
  sibling after** the bubble's content — but since the bubble is itself a flex
  child of `#messages`, the caption must live *inside* the bubble's flow or as a
  wrapper. Chosen approach: append a `.msg-time` child to the `.msg` div; CSS
  aligns it (right for user, left for assistant) and styles it muted/small.
- The `history` handler passes `entry.timestamp` into the calls. Live `text_delta`
  → `addAssistantMsg('')` keeps no explicit ts (defaults to now); the caption is
  written when the bubble is created. (For a streamed assistant reply the bubble
  is created at first token; "completed" time ≈ first-token time within a couple
  seconds — acceptable, and avoids re-stamping on `done`.)
- **Replay mode** (`renderReplayMode`): calls `addAssistantMsg` with an explicit
  sentinel (`0`) so `formatMsgTime` returns empty and no caption renders.

### CSS

```
.msg-time {
    font-size: var(--fs-xs);
    color: var(--text-muted);
    margin-top: 0.35rem;
    font-variant-numeric: tabular-nums;
}
.msg.user .msg-time { text-align: right; }
.msg.assistant .msg-time { text-align: left; }
```

The caption sits inside the bubble at the bottom. Because `.msg.user` is
right-aligned and `.msg.assistant` is left-aligned as flex items, the
per-role text-align keeps the caption visually anchored to the bubble's
"outer" edge.

## Testing

- **Server (Go, hermetic):** a new `TestSessionHistoryIncludesTimestamp` in
  `internal/gateway/` constructs a session store in a temp dir, appends a user
  and an assistant message, calls the history path, and asserts each returned
  `message` entry map carries a `"timestamp"` matching the stored entry. If the
  existing test harness only exercises `handleSessionHistory` through a live
  WebSocket, instead unit-test the small mapping by asserting on the entries
  slice via a thin extraction — but prefer driving `handleSessionHistory` if a
  fixture conn exists. (No history test exists today; add the minimal viable
  one that compiles against the current handler signature.)
- **Client (JS):** no JS test harness in this repo; `formatMsgTime` correctness
  is verified by reasoning + the Go build. Keep the helper tiny and pure.

Final gates: `go build ./...`, `go vet ./...`, `go test ./...`.

## Out of scope

- Per-tool-call timestamps.
- Relative/auto-refreshing times.
- Re-stamping assistant bubbles at `done` with a precise completion time
  (first-token time is close enough and far simpler).
- Timezone configuration — uses the browser locale/zone.
