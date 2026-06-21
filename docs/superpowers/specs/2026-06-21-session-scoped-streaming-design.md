# Session-scoped streaming Design

**Date:** 2026-06-21
**Status:** Approved
**Repo:** `felix` only (`internal/gateway`).

## Problem

When a chat turn is generating in one session and the user switches to another
session, the first turn's streamed output renders into the second session's
message pane ("spill-over"). The response that starts in one session must stay
in that session.

## Root cause (verified against source)

The live streaming path carries **no session scope**. `wsSubscriber.OnEvent`
(`websocket.go`) writes each event as a JSON-RPC Response tagged only with the
opaque chat.send request `id`; `eventToResult` (`websocket.go`) emits only the
event type + payload. The client's `onmessage` streaming cases
(`chat.go:3812-3883`) render blindly into global turn state — `currentAssistant`
(the DOM bubble), `sending` (composer lock), `toolEls` (tool DOM nodes) — with
no check of which `(agent, session)` the event belongs to.

Sequence that breaks:
1. Send in session A → run starts; `currentAssistant` points at A's bubble.
2. Switch to session B (`sessionSelect` change, `chat.go:3347`) → pane wiped,
   `currentAssistant=null`. The in-flight A turn is **not** detached and the
   server is not told the view left A.
3. A's run keeps streaming over the same WebSocket; the dispatcher finds
   `currentAssistant` null, creates a **new bubble in B**, and pours A's
   response into it.

Additional latent gaps found:
- `liveRunIdBySession` (`chat.go:1891`) is set/read using the *displayed*
  scope at event time (`chat.go:3818-3819`, `3906-3907`), so it mislabels runs
  after a switch.
- The client **never calls `chat.subscribe`** — the server's live re-attach +
  gap-fill subsystem (`handleChatSubscribe`/`forwardEvents`) is unused by the
  client; only read-only `chat.replay` is used. So switching back to a
  still-running session currently shows nothing live.

The server is otherwise correct: `runs.Registry` enforces at-most-one active
run per session scope, and `maxConcurrentRuns = 8` (`limits.go`) already allows
concurrent turns across different sessions on one connection.

## Decisions (resolved with user)

1. **Switch-away behavior:** the in-flight turn keeps running server-side;
   its events stop rendering into the wrong session; a **sidebar running badge**
   marks sessions generating in the background.
2. **Composer lock:** **per-session**. The composer is locked only for the
   displayed session when that session has a live run; switching to an idle
   session frees the composer so a new turn can start there.
3. **Re-attach on switch-back:** use **`chat.subscribe`** (`fromSeq=0`) — the
   server replays the run's events so far, then streams the remaining live
   events.
4. **Approach:** stamp scope on every streamed event (both envelopes) and route
   by scope on the client (chosen over client-only rpcID correlation and over
   unifying everything onto chat.subscribe).

## Design

### Part 1 — Server: stamp scope on every streamed event

`internal/gateway/websocket.go`.

- Add `scope runs.SessionScope` to `wsSubscriber` (currently `{conn, rpcID}` at
  `websocket.go:706-708`); set it in `handleChatSend` where `scope` is already
  computed (`websocket.go:422`): `sub := &wsSubscriber{conn: conn, rpcID: rpcID,
  scope: scope}`.
- `wsSubscriber.OnEvent`: after `res := eventToResult(e)` and the nil-check,
  inject the scope before writing:
  ```go
  res["agentId"] = s.scope.AgentID
  res["sessionKey"] = s.scope.SessionKey
  ```
- `wsSubscriber.OnAttached` (the `run_attached` first response): include the
  same two fields in its result map so the client binds the run to the right
  scope from the first frame.
- `forwardEvents` (the `chat.subscribe` path, `websocket.go:675`): thread the
  scope in (known at the `handleChatSubscribe` call site, `websocket.go:636`,
  `667`) and stamp `agentId`/`sessionKey` into the `chat.event` params, so the
  re-attach stream is scoped identically to the send stream.
- The `handleChatSubscribe` synchronous response already returns `runId` and
  `past`; add `agentId`/`sessionKey` to its result so the client can confirm
  the scope of the replayed `past` events (the per-event payloads inside `past`
  do not need stamping — the enclosing response identifies them).
- The trace-mark forwarder (`makeTraceMarkForwarder`, `websocket.go:491`) also
  stamps the two fields, so trace rows route by scope too.

No change to `runs.Event` on disk, event types, or the protocol shape beyond two
additive string fields. Backward compatible: an older client ignores them.

### Part 2 — Client: per-scope render state

`internal/gateway/chat.go`.

Replace the global turn state (`currentAssistant`, `sending`, `toolEls`) with a
per-scope map keyed by `runsKey(agent, session)`:

```
turnState = new Map();   // scopeKey → { assistant, sending, toolEls }
function stateFor(scopeKey) {
  var s = turnState.get(scopeKey);
  if (!s) { s = { assistant: null, sending: false, toolEls: {} }; turnState.set(scopeKey, s); }
  return s;
}
function displayedScope() { return runsKey(agentSelect.value, sessionSelect.value); }
function eventScope(r) { return runsKey(r.agentId, r.sessionKey); }
```

Each streaming case in the `onmessage` dispatcher (`chat.go:3814-3917`):
`text_delta`, `tool_call_start`, `tool_result`, `done`, `aborted`, `error`,
`compaction.start`, `compaction.done`, `compaction.skipped`, `run_attached`,
`run_terminal`, `trace`:

- Resolve `sk = eventScope(r)` and `st = stateFor(sk)`.
- Update `st` unconditionally (set/clear `st.sending`, manage `st.assistant`,
  `st.toolEls`, track live runId via `liveRunIdBySession`).
- **Touch the DOM only when `sk === displayedScope()`.** Otherwise the event
  updates background state + the sidebar badge but never the message pane.

**No background partial-text buffering.** Background scopes keep only
lightweight state (`sending`, live runId). The response *content* for a
background scope is never retained client-side; on switch-back the pane is
wiped and rebuilt from the server via `chat.subscribe` (Part 3). This keeps a
single source of truth (the server's run log) and avoids unbounded client
buffers.

Composer lock becomes per-scope: `updateSendBtn()` reads
`stateFor(displayedScope()).sending`. `sendMessage` sets
`stateFor(displayedScope()).sending = true` before sending and keys the new
turn's state to the send scope.

Switch handlers (`agentSelect`/`sessionSelect` change, `chat.go:3327`, `3347`):
stop nulling global turn state. Instead: clear the message pane, set the new
displayed scope, then either re-attach (Part 3, if the target scope is live) or
load `session.history` (as today, if idle), and call `updateSendBtn()`.

### Part 3 — Client: re-attach on switch-back via chat.subscribe

`liveRunIdBySession` becomes the single source of truth for "scope is live":
- Set on `run_attached` using the **event's** scope (`eventScope(r)`), not the
  displayed scope. Cleared on `run_terminal` using the event's scope.

When a switch makes `displayedScope()` a key present in `liveRunIdBySession`:
1. Clear the message pane and `stateFor(scope).toolEls`.
2. Send `chat.subscribe {agentId, sessionKey, fromSeq: 0}` with id
   `subscribe-<scopeKey>`.
3. Dispatcher branch for `subscribe-` ids: render the response `past` array via
   the existing per-event replay rendering (the read-only replay path already
   does this at `chat.go:2975-3032`) to rebuild the response-so-far, then live
   `chat.event` notifications continue appending through the same scoped
   streaming cases (Part 2).

Safety: `Run.Subscribe` closes any prior subscriber for the conn (the
double-subscribe guard) and — per the v0.9.2 fix — returns an already-closed
live channel if the run finished between switch and subscribe (so a
just-finished run replays full `past` then ends cleanly; no hang, no duplicate
terminal). If the run finished and is no longer active, `handleChatSubscribe`
returns `{active:false}`; the client then falls back to `session.history`.

`chat.event` is a server-initiated notification (no `id`); the dispatcher gains
a `resp.method === 'chat.event'` branch (next to the existing `session_titled`
branch at `chat.go:3641`) that runs the same scoped streaming-case logic on
`resp.params`.

### Part 4 — Client: sidebar running badge

- `renderSessions` (`chat.go:2743`) adds `<span class="ses-running"></span>` to
  each row; shown when `liveRunIdBySession.has(runsKey(aid, s.key))`.
- New `updateSessionBadges()` toggles the `.ses-running` class on existing rows
  (by `data-session-key`) without a full re-render; called whenever
  `liveRunIdBySession` changes (on `run_attached`/`run_terminal`, any scope).
- CSS: `.ses-running` is a small colored dot with a subtle pulse, independent of
  the `.active` selected-row highlight (a background-running, non-selected
  session still shows the dot).

## Testing

Server (Go, `internal/gateway`, extend `websocket_chat_test.go`'s ws-pair +
scripted-provider harness):

1. **OnEvent stamps scope:** a `chat.send` for scope `(A, k)` produces streamed
   result frames each carrying `agentId:"A"` and `sessionKey:"k"`.
2. **run_attached stamps scope:** the first `run_attached` response carries the
   scope fields.
3. **forwardEvents stamps scope:** a `chat.subscribe` re-attach emits
   `chat.event` notifications whose params carry the scope fields.
4. **Concurrent-scope isolation (core guarantee):** two `chat.send`s on
   different sessions over one conn produce frames that are each correctly
   scoped — no frame from run A is labeled with B's scope. This is the
   regression guard for the spill-over bug, now assertable because scope is on
   the wire.

Client (JS — no JS test harness exists in the repo):
- Keep scope logic in small pure helpers (`stateFor`, `displayedScope`,
  `eventScope`) so the routing decision is inspectable.
- Manual verification steps (documented in the plan):
  1. Send in A; while generating, switch to B → B pane stays clean; A's sidebar
     row shows the running dot.
  2. Switch back to A → full response present (replayed), continues streaming if
     still live, finalizes normally; dot clears on completion.
  3. While A generates, send in idle B → B's turn runs concurrently; both rows
     badge; each renders only its own output.
  4. Switch away mid-turn and back after completion → A shows the completed
     turn (via `active:false` → history fallback).

Gates: `go build ./...`, `go vet ./...`, `go test ./...`,
`go test -race ./internal/gateway/...`.

## Out of scope (YAGNI)

- Cross-tab synchronization.
- Persisting live-run state across a full page reload (on reload the client
  re-derives via `chat.runs`/`chat.subscribe` as needed).
- Per-scope unread counts or partial-text previews beyond the running dot.
- Changing the `chat.send` streaming contract (we keep the dual-envelope shape;
  we only add scope fields to it).
- Any server change beyond the additive scope fields.
