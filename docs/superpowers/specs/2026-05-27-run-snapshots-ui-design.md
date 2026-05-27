# Wave 2 — Run snapshots UI + Wave 1 polish

**Status:** design
**Date:** 2026-05-27
**Predecessor:** [Wave 1 — runs+chatexec port](2026-05-27-runs-chatexec-port-design.md), merged as `f7d97e0`.

## Goal

Close the user-visible loop on Wave 1's durable-run work. Wave 1 made every chat turn a `Run` that persists to disk with a JSONL event log and is replayable by ID, but nothing in the UI surfaces that history. Wave 2 adds a per-session run list (view + delete) so users can browse and prune the runs that have already accumulated under `~/.felix/sessions/<agent>/<key>.runs/`.

Wave 2 also discharges the carry-forward debt the Wave 1 final review flagged: unit tests for the new gateway handlers, the un-wired `OverlayMetrics`, a comment block explaining the chat.send vs chat.subscribe wire-format asymmetry, and a stale `chatexec.go` docstring referencing an inbox worker that doesn't exist in felix.

## Scope

**In — Run snapshots (per-session, view + delete):**

- New JSON-RPC method `chat.runs(agentId, sessionKey) → {runs: [RunSummary]}`. Lists past runs for a session by calling `runs.Registry.Snapshot(scope)`. Server sorts newest-first.
- New JSON-RPC method `chat.deleteRun(agentId, sessionKey, runId) → {deleted: bool}`. Removes the run from `index.json` and deletes the `<runID>.jsonl` log. Refuses if the run is still in-flight.
- New method on `runs.Registry`: `DeleteRun(scope SessionScope, runID string) error`. Encapsulates the file-then-index delete order so a failed file delete doesn't leave the index referencing a missing log.
- Frontend (`internal/gateway/chat.go`): each session row in the sidebar grows a `▸` chevron. Click expands the row to a sub-list of past runs (timestamp · status · event count · `[×]`). Click a run row body fires `chat.replay` and renders the returned events into the chat pane in a read-only mode with a "← Back to live" banner. The `[×]` opens a confirm dialog and fires `chat.deleteRun`.

**In — Wave 1 polish:**

- Unit tests for `handleChatSend`, `handleChatAbort`, `handleChatSubscribe`, `handleChatReplay`. Cover happy path + the abort-with-empty-params fallback that the smoke test caught in Wave 1.
- Wire `chatexec.OverlayMetrics` so chat-path tool calls increment `felix_tool_calls_total`. Cloudcat's overlay had the field; felix's `RunTurn` constructed the overlay without populating it.
- Doc comment above `wsSubscriber` / `forwardEvents` in `internal/gateway/websocket.go` explaining the intentional wire-format asymmetry: `chat.send` events arrive as `Result` messages on the shared rpcID for backward compatibility with the existing felix HTML chat client; `chat.subscribe` live events arrive as `chat.event` notifications. Future authors must not "unify" the two without breaking the client.
- Trim `internal/chatexec/chatexec.go` package docstring — currently says "consumed both by the WebSocket chat handler and by the inbox worker". Felix has no inbox worker. Drop the inbox half.

## Out of scope

- **Forking / "continue from this run".** Re-loading session state from a past run and resuming a new turn from there is a separate, larger wave (touches harness session-state rewriting). Defer.
- **Cross-session run browsing.** A dedicated "Runs" tab in the sidebar Tools section that lists runs across all sessions is overkill for the current need. YAGNI.
- **Run list pagination.** Felix sessions in practice accumulate dozens, not thousands, of runs. Add pagination only if a user reports the list is slow.
- **Persist sidebar expand state across reloads.** Each session's chevron state is per-session-per-page-load. localStorage backing is a follow-up if asked.
- **The cloudcat-context `felix_*` → `cloudcat_*` Prometheus rename** mentioned in Wave 1's final review. Felix is felix; the metric prefix is correct as-is. Reviewer carried a cloudcat note into felix context by mistake.

## Architecture

```
Sidebar session row
    │  click ▸
    ▼
chat.runs {agentId, sessionKey}
    │
    ▼
WebSocketHandler.handleChatRuns
    │
    ▼
h.runs.Snapshot(scope) ──► reads index.json ──► [RunSummary, ...]
    │  sorted newest-first
    ▼
JSON-RPC response, rendered as nested rows under the session
    │
    ├── click run row body ──► chat.replay (existing from Wave 1)
    │                              │
    │                              ▼
    │                          runs.ReadLog(<runID>.jsonl, fromSeq=0)
    │                              │
    │                              ▼
    │                          read-only chat view + "Back to live" banner
    │
    └── click [×] ──► confirm dialog ──► chat.deleteRun {agentId, sessionKey, runId}
                                              │
                                              ▼
                                          WebSocketHandler.handleChatDeleteRun
                                              │
                                              ▼
                                          h.runs.DeleteRun(scope, runID)
                                              │
                                              ├─ refuse if not Completed
                                              ├─ delete <runID>.jsonl
                                              └─ rewrite index.json without the row
```

### Why this shape

The sidebar-expandable layout is the most discoverable: runs are conceptually children of their session, and a chevron is a near-universal signal that "click reveals more." Putting runs under each session also scales naturally — a user with 20 sessions sees the same compact session list they have today; runs only appear when they ask.

Adding `DeleteRun` to `runs.Registry` rather than letting the handler manipulate disk directly keeps the package's invariants intact: the registry remains the single owner of `<runID>.jsonl` and `index.json` writes, so a future change to those paths (e.g. adding a hash subdirectory) ripples through one place.

The two RPC methods reuse the same `{agentId, sessionKey}` parameter shape as every other chat-related handler, including the standard explicit → `activeSessionKeys[conn][agentID]` → `"ws_default"` fallback chain. No new parameter conventions.

## Components and contracts

### `runs.Registry.DeleteRun(scope SessionScope, runID string) error`

```go
// DeleteRun removes a completed run from disk. Returns an error if:
//   - the run is currently in-flight (Get returns a non-Completed run)
//   - the index file cannot be rewritten
// File-delete failures are non-fatal: a missing <runID>.jsonl after this
// returns nil leaves the index entry in place but with no log to replay
// from; ReadLog on the missing path returns (nil, nil) so replay shows
// an empty past. (See risk 1.)
func (reg *Registry) DeleteRun(scope SessionScope, runID string) error
```

Order of operations:
1. Lock `reg.mu`; check `reg.runs[runID]`. If found AND not `Completed.Load()`, unlock and return `fmt.Errorf("cannot delete in-flight run %s", runID)`.
2. Unlock.
3. `os.Remove(<runID>.jsonl)` — log warning on error, do not fail.
4. `loadIndex` → filter out the row matching runID → `saveIndex` via `WriteFileAtomic`.
5. Return any error from the index rewrite.

Step 4's atomic rewrite means a crash mid-delete leaves either the old index (with the row present) or the new index (without it). The orphan file from step 3 is harmless (ReadLog tolerates missing files).

### `chat.runs` handler

```go
// handleChatRuns: GET past run summaries for a session.
// Request:  {agentId: string, sessionKey: string}
// Response: {runs: [{id, started_at, ended_at?, status, last_seq, superseded_by?}]}
// Sorted by started_at descending (newest first).
```

Session-key fallback: explicit → `activeSessionKeys[conn][agentID]` → `"ws_default"`.
Errors: `-32602` on param parse, `-32000` on registry-not-configured.

### `chat.deleteRun` handler

```go
// handleChatDeleteRun: remove a completed run.
// Request:  {agentId: string, sessionKey: string, runId: string}
// Response: {deleted: bool}
// Errors:   -32602 if runId empty
//           -32000 if registry not configured OR if the run is in-flight
```

Refusing in-flight runs is a server-side guarantee; the frontend should also hide `[×]` next to whichever run matches the live `runID` (if any), but the server enforcement is the contract.

### Frontend additions to `internal/gateway/chat.go`

State additions (per-conn JS state, in the chat client):

- `runsBySession: { [scope]: { runs: [RunSummary], expanded: bool } }` — per-session run cache + expand state. Scope key is `${agentId}::${sessionKey}`.
- `viewingRunID: string | null` — when non-null, the chat pane is in read-only mode showing this run; the input box is hidden, the Stop button is hidden, a "← Back to live" banner is shown above the messages.

DOM additions to each session row:
- `▸` / `▾` chevron at the start of the row (24px, click to toggle).
- When expanded, a `<ul>` of run rows beneath the session row, each row showing:
  - `HH:MM:SS` of `started_at`
  - status badge (color-coded: completed=green, cancelled=gray, failed=red, interrupted=orange, running=blue with pulse)
  - event count `N events` (from `last_seq`)
  - `[×]` delete button (hidden when this is the live run for the session)

Click handlers:
- Chevron click → if no cached runs, fire `chat.runs` then render; if cached, toggle visibility.
- Run row body click → fire `chat.replay({agentId, sessionKey, runId: row.id, fromSeq: 0})`. Set `viewingRunID = row.id`. Render the returned `past` events into the chat pane in read-only mode. Show the banner.
- `[×]` click → `confirm("Delete this run? The conversation history stays; only the per-turn event log is removed.")`. On accept, fire `chat.deleteRun({agentId, sessionKey, runId})`. On success, remove the row from `runsBySession[scope].runs` and re-render.
- "← Back to live" banner click → clear `viewingRunID`, re-render the chat pane from the session's persisted message history (existing code path — same as session.switch's render).

### Polish item details

**Handler unit tests** — new file `internal/gateway/websocket_chat_test.go`. Test fixtures:

- A minimal in-memory `runs.Registry` (the real one works fine — tests use a `t.TempDir()` for `sessionsDir`).
- A faked `*websocket.Conn` for capturing JSON-RPC writes. `server_test.go` already has a similar pattern; reuse.
- A test-only `chatexec` driver — for handleChatSend we can use the real `chatexec.RunTurn` with `llmtest.NewScriptedProvider` (already in tree from Wave 1).

Tests:

- `TestHandleChatSend_HappyPath` — scripted provider returns "hi", verify `run_attached` response followed by `text_delta` and `done`.
- `TestHandleChatSend_NilRegistry` — `h.runs == nil`, verify `-32000` error.
- `TestHandleChatAbort_NoActiveRun` — `{aborted: false}` reply.
- `TestHandleChatAbort_AbortsActiveRun` — register a run, send abort, verify `{aborted: true, runId}`, verify the run's `Done` channel closes.
- `TestHandleChatAbort_FallbackResolvesViaActiveSessionKeys` — send abort with empty params, with `activeSessionKeys[conn][agentID]` populated for a different agent than `default`. Verify the fallback walks the map and finds the run. (Regression guard for the bug smoke test caught in Wave 1.)
- `TestHandleChatSubscribe_NoActiveRun` — `{active: false}` reply.
- `TestHandleChatSubscribe_AttachesAndReceivesLive` — register a run, subscribe, Append an event, verify `chat.event` notification arrives.
- `TestHandleChatReplay_HappyPath` — write a runID's jsonl to disk, replay, verify the `past` array matches.
- `TestHandleChatReplay_MissingRun` — replay an unknown runID, verify empty `past` array and no error.

**OverlayMetrics wiring** — in `internal/chatexec/chatexec.go`, the overlay construction line:

```go
overlay := &ChatToolOverlay{Base: deps.Tools}
```

becomes:

```go
overlay := &ChatToolOverlay{Base: deps.Tools, Metrics: deps.Metrics}
```

The `ChatToolOverlay.Metrics` field's type must accept `deps.Metrics` (which is `chatexec.MetricsLike`, an interface). If `ChatToolOverlay.Metrics` was typed as a different interface (the audit found this is `OverlayMetrics` in cloudcat), unify the two interfaces in this commit: define `MetricsLike` with both `IncChatTurns()` and `IncToolCalls(name string)`, drop `OverlayMetrics` as a separate type. `gateway.Metrics` already implements both.

**Wire-format comment** — block above `wsSubscriber` (currently `internal/gateway/websocket.go` ~line 619):

> // Wire-format note: this codebase has two paths for sending event payloads to a WebSocket conn, intentionally asymmetric:
> //
> //   1. chat.send → wsSubscriber.OnEvent — writes each event as a JSONRPCResponse with Result set, ID = the original chat.send request ID. The existing felix HTML chat client treats multiple Results sharing one rpcID as a stream; do NOT change this without updating the client.
> //
> //   2. chat.subscribe → forwardEvents — writes each event as a JSON-RPC notification (method = "chat.event", no ID). Newer clients that attach to existing runs (post-disconnect, multi-tab) consume this shape.
> //
> // Same underlying runs.Event; two different envelopes. The asymmetry exists for backward-compatibility with the chat.send-as-stream pattern the felix HTML client was designed around before the durable-runs work landed.

**chatexec comment trim** — `internal/chatexec/chatexec.go` package docstring currently says:

> // Package chatexec runs a single chat turn end-to-end: derives the run
> // context, opens the session, drives the harness runtime, fans events
> // out to the runs.Registry (for durable replay) and to a live Subscriber
> // (for WebSocket clients).
> //
> // The package is consumed both by the WebSocket chat handler and by the
> // inbox worker that delivers agent-to-agent messages, so it must not
> // depend on any transport.

becomes:

> // Package chatexec runs a single chat turn end-to-end: derives the run
> // context, opens the session, drives the harness runtime, fans events
> // out to the runs.Registry (for durable replay) and to a live Subscriber
> // (for WebSocket clients).
> //
> // The package is consumed by the WebSocket chat handler. It is written to
> // be transport-agnostic; additional consumers (e.g., a future inbox or
> // cron-driven turn dispatcher) can plug in by implementing the Subscriber
> // interface.

## Suggested phasing

One spec, three sequential commits. Each is independently buildable and tests stay green between them.

1. **P1 — Backend.** Add `Registry.DeleteRun`, `handleChatRuns`, `handleChatDeleteRun`, register the dispatcher cases, and write the unit test file covering BOTH the new methods AND the Wave 1 carry-forward handler tests. ~250 LOC. Pure addition.

2. **P2 — Wave 1 polish.** Wire `OverlayMetrics`. Add the wire-format comment block. Trim the chatexec docstring. ~30 LOC across `chatexec/chatexec.go`, `chatexec/overlay.go` (if `Metrics` field's type needs a rename), `gateway/websocket.go`.

3. **P3 — Frontend.** Sidebar chevron + sub-list, read-only mode, "Back to live" banner, delete-with-confirm. ~200 LOC in `internal/gateway/chat.go`. Manual smoke test: expand, click a run, see events, click Back to live, delete a run, confirm it's gone from disk (verify with `ls ~/.felix/sessions/*/*.runs/`).

Tests pass green after each phase.

## Risks and mitigations

1. **File-delete failure leaves orphan index entry.** Mitigation: delete the file FIRST, then rewrite the index. A failed file delete is logged and the function continues to rewrite the index without the row. On next read, `chat.replay` for the deleted ID returns an empty past — frontend renders "Run no longer available" (handled by risk 4's empty-past handling).

2. **Frontend complexity from sidebar state.** Mitigation: per-session expand state is plain in-memory, not persisted. If users complain, add localStorage in a single follow-up commit.

3. **Read-only mode UX confusion.** Mitigation: prominent yellow banner with explicit "← Back to live" affordance. Input box and Stop button hidden (not just disabled) so users don't try to interact. P3's smoke test confirms intuition end-to-end.

4. **Concurrent delete from another tab.** Race: user clicks a run row in tab A while tab B deletes the same run. `chat.replay` returns `{past: []}`. Mitigation: render an empty read-only view with a "Run no longer available — it may have been deleted." message, with the "← Back to live" banner still active.

5. **`OverlayMetrics` interface change is breaking if any test mocks it.** Mitigation: search `grep -rn "OverlayMetrics" internal/` before the rename. The only consumers should be chatexec internals and `gateway.Metrics`.

## Migration / compatibility

- No on-disk format changes. The runs `index.json` and `<runID>.jsonl` shapes are identical to Wave 1.
- Existing felix instances upgrading: empty `runsBySession` cache, no behaviour change until a user expands a chevron.
- The Wave 1 chat.replay wire format is unchanged; this wave just adds a new caller (the frontend run row click).
- Wire-format comment is documentation-only.

## Follow-ups (not in this wave)

- Forking ("continue from this run") — separate brainstorm.
- Cross-session "Runs" tab — separate brainstorm if/when users want it.
- localStorage-backed expand state — one-liner follow-up if asked.
- Bulk delete ("clear all completed runs older than N days") — useful once `~/.felix/sessions/*/*.runs/` directories accumulate.
