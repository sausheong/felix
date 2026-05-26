# Port cloudcat `runs` + `chatexec` + chat subscribe/replay to Felix

**Status:** design
**Date:** 2026-05-27
**Origin:** the in-process chat-execution refactor that landed in `github.com/sausheong/cloudcat` between 2026-05-12 and 2026-05-23, while Felix's `internal/gateway` chat path stayed on its older, WS-bound model.

## Goal

After this wave, Felix chats:

- survive WebSocket disconnects — the run keeps going server-side, the client can re-attach and replay
- recover from process crashes — the per-session index file is reconciled at startup and abandoned runs receive a synthetic `interrupted` terminal event
- emit a consistent on-disk event log per turn — JSONL, append-only, replayable by seq, suitable for future tooling (debugging, audit, exports)

Internal structure mirrors cloudcat's, which keeps the two codebases close enough for future cloudcat → felix syncs to remain mechanical.

## Scope

**In:**

- New package `internal/gateway/runs/` — verbatim port of cloudcat's package (module path rename only).
- New package `internal/chatexec/` — cloudcat's port with all fleet-only fields and helpers removed.
- Refactor `internal/gateway/websocket.go` `handleChatSend` to call `chatexec.RunTurn`.
- Add JSON-RPC methods to the gateway: `chat.subscribe`, `chat.replay`, `chat.abort`.
- Startup wiring in `internal/startup/`: construct `runs.NewRegistry(sessionsDir)`, call `RecoverInterruptedRuns` at boot, install the `OnNewRun` callback so background-run notifications reach open WS clients.

**Out:**

- Anything fleet, inbox, admin, artifacts, peer-grant, or cross-VM trace propagation — Felix is single-user.
- CLI chat path (`cmd/felix chat`). It keeps its current synchronous flow; runs would add bookkeeping for no user benefit.
- Frontend changes beyond the wire format that `chat.subscribe` / `chat.replay` need. The chat UI shell already came across from cloudcat in `c3e5243` and is expected to handle streaming notifications.

## Architecture

```
WebSocketHandler.handleChatSend
    │
    ▼
chatexec.RunTurn(ctx, deps, scope, text, wsSubscriber)
    │   ├── deps.Runs.SupersedeAndCreate(scope, runID, cancel) ──► runs.Registry
    │   │       └── Registry writes index.json, opens <runID>.jsonl, fires OnNewRun
    │   ├── load session, build runtime (existing felix code; unchanged)
    │   ├── drain harness events
    │   │       └─► run.Append(type, payload)
    │   │             ├── JSONL write (single writer, fsync per line)
    │   │             └── fan out to all subscribers (non-blocking; full channel drops sub)
    │   └── run.Finish(status, reason, supersededBy)
    │         ├── close drain channel, write terminal Done event
    │         ├── persist index (with EndedAt, LastSeq)
    │         └── close all subscribers
    │
    ▼  (independent paths)
WebSocketHandler.handleChatSubscribe → Registry.GetBySession → Run.Subscribe(conn, fromSeq)
                                                          └── past + live channel
WebSocketHandler.handleChatReplay    → Registry.GetBySession OR runs.ReadLog(...)
                                                          └── replay from disk
WebSocketHandler.handleChatAbort     → Registry.GetBySession → Run.Finish(cancelled, user_abort, "")
```

The `runs` package is the single source of truth for in-flight runs and on-disk replay. `chatexec` is the only writer to a `Run`. The WS handler is just transport (request routing + per-conn subscriber lifecycle).

### Package layout

| Path | Purpose | New / changed |
|---|---|---|
| `internal/gateway/runs/types.go` | `Event`, `Status`, `RunSummary`, `SessionScope`, `EventType` constants | new (verbatim) |
| `internal/gateway/runs/log.go` | `logWriter` (single-writer JSONL with fsync), `ReadLog(path, fromSeq)` | new (verbatim) |
| `internal/gateway/runs/index.go` | `IndexFile`, `loadIndex`/`saveIndex` (atomic via `config.WriteFileAtomic`) | new (verbatim) |
| `internal/gateway/runs/registry.go` | `Registry`, `Run`, `Create`/`SupersedeAndCreate`/`Append`/`Finish`/`Subscribe`/`Unsubscribe`/`Snapshot`/`Remove`/`OnNewRun` | new (verbatim) |
| `internal/gateway/runs/recovery.go` | `RecoverInterruptedRuns(sessionsDir)` — walk `*.runs/` at boot, reconcile abandoned `running` entries | new (verbatim) |
| `internal/gateway/runs/*_test.go` | unit + integration tests (5 files) | new (verbatim) |
| `internal/chatexec/chatexec.go` | `RunTurn` end-to-end driver | new (stripped of fleet) |
| `internal/chatexec/overlay.go` | `ChatToolOverlay` (per-call task + cron tools) | new (no SendToAgent field) |
| `internal/chatexec/*_test.go` | unit tests (2 files) | new (adapted) |
| `internal/gateway/websocket.go` | new methods, refactored `handleChatSend`, new RPC dispatches | edited |
| `internal/startup/startup.go` | construct registry, recovery, OnNewRun wiring | edited |

### Why mirror cloudcat's path

`internal/gateway/runs/` is verbatim cloudcat. Future syncs are a `cp -r` + sed on the import prefix. The cost of one extra path segment for a package that's only consumed by the gateway is negligible. The alternative (`internal/runs/`) is purer in package taxonomy terms but breaks the sync ergonomics for no compelling reason.

## What chatexec looks like in Felix (after stripping)

Cloudcat's `chatexec.TurnDeps` has 18+ fields. Felix's version drops these 7 cleanly:

| Cloudcat field | Why dropped |
|---|---|
| `FleetBaseDomain` | no fleet |
| `InboxSender`, `AgentExists` | no inbox |
| `ReplyTo` | inbox wake-loop only |
| `PeerGrant` | fleet peer-grant tool filtering |
| `TraceContext` | cross-VM W3C traceparent (single-process Felix has no upstream span to attach to) |
| `OverlayMetrics` | Felix gateway doesn't expose the per-tool counter — defer until Felix has equivalent Prometheus surface |

Result: `TurnDeps` shrinks to ~10 fields, all of which Felix already constructs in `internal/startup/`. Two cloudcat files are NOT ported:

- `internal/chatexec/peer_grant.go` — fleet-only `peerGrantChecker`
- `internal/chatexec/trace_context.go` — W3C traceparent extraction

`overlay.go` is ported without the `SendToAgent` field. In-process `send_to_agent` continues to work through the shared `tools` registry; the per-call overlay matters in cloudcat only because cloudcat's overlay sets `Self` from the calling agent's ID for fleet attribution, which Felix doesn't need.

### chat.send semantics

`chat.send` returns immediately once the run is registered (matching cloudcat). The JSON-RPC response carries `{runID, scope}`. All chat events arrive as notifications (`chat.event`, `chat.done`, etc.). The Felix chat UI shell that landed in `c3e5243` is expected to consume this — we verify during phase 3.

This is a wire-format change from Felix's current behaviour (where `chat.send` blocks until the run finishes). Old clients that wait for the response will hang until they receive the `chat.done` notification anyway — they'll just see the response arrive earlier than the final text. No code path actually depends on the response carrying the final reply, but phase 3 includes an explicit smoke test of the felix HTML chat client to confirm.

## Wire-up changes in `internal/startup/startup.go`

After `sessionsDir` is known and after `wsHandler` is constructed:

```go
runsReg := runs.NewRegistry(sessionsDir)
if n, err := runs.RecoverInterruptedRuns(sessionsDir); err != nil {
    slog.Warn("runs recovery failed", "error", err)
} else if n > 0 {
    slog.Info("runs recovery complete", "recovered", n)
}
wsHandler.SetRunsRegistry(runsReg)
runsReg.OnNewRun = wsHandler.BroadcastNewRun
```

`SetRunsRegistry` and `BroadcastNewRun` are new methods on `WebSocketHandler`. `BroadcastNewRun` walks open connections that match the run's scope and pushes a `run_started` notification so any client that was viewing the same session can attach via `chat.replay`.

## Suggested phasing for the implementation plan

The wave is one spec but the plan should land in three sequential commits (each independently shippable and reviewable):

1. **P1 — runs package only.** Copy `internal/gateway/runs/` from cloudcat, rename `github.com/sausheong/cloudcat` → `github.com/sausheong/felix` in imports. Bring all 5 test files. `go test ./internal/gateway/runs/...` passes. Pure addition; zero call sites changed. ~2,000 LOC.
2. **P2 — chatexec package only.** Port chatexec.go + overlay.go + tests with fleet bits removed. Bridge any interface drift between cloudcat's and felix's `tools`, `agent`, `compaction`, `cortex`, `memory`, `skill` packages. No gateway changes yet; this package is unwired. `go test ./internal/chatexec/...` passes. ~1,000 LOC.
3. **P3 — gateway integration + startup wiring.** Refactor `handleChatSend` to call `chatexec.RunTurn`. Add `handleChatSubscribe` / `handleChatReplay` / `handleChatAbort`. Add `SetRunsRegistry` / `BroadcastNewRun` methods. Wire registry construction + recovery + `OnNewRun` in startup. Manually smoke-test the felix HTML chat client (connect, send, disconnect mid-stream, reconnect, see `chat.replay` resume the run). ~500–800 LOC of edits.

Tests must be green after each phase.

## Risks and mitigations

1. **Interface drift between cloudcat and felix.** The packages chatexec imports (`internal/tools`, `internal/agent`, `internal/compaction`, `internal/cortex`, `internal/memory`, `internal/skill`, `internal/session`, `internal/llm`, `internal/config`) have evolved independently. Phase 2 starts with a diff of the public surface of each and decides on adapter shims vs. direct rewires. The plan should budget time for a few of these.
2. **`chat.send` wire-format change.** Mitigated by the smoke test in P3 and by the fact that the felix UI shell already came from cloudcat — it should already speak the new protocol. If not, P3 includes adapting one chat HTML file.
3. **Disk layout addition.** `<sessionsDir>/<agent>/<key>.runs/{index.json, <runID>.jsonl}` is a new directory layout under each session. Existing Felix instances have no `.runs/` directories — first boot after the upgrade does nothing (recovery walk finds zero matching dirs). No migration script needed.
4. **Subagent path.** Cloudcat's chatexec builds task overlays via `agent.MakeSubagentFactory`. Felix has subagents in `internal/agent` too. P2 verifies the factory signatures match before settling on whether the overlay branch needs adaptation.
5. **Tightly-coupled package + integration in one wave.** The wave is large (~4,000 LOC including tests). Mitigated by the three-phase split: each phase is small and shippable on its own. If P3 reveals a wire-format compatibility problem, P1 and P2 stay merged as inert improvements.

## Out of scope for follow-up waves

After this wave merges, the natural follow-ups (each its own spec) would be:

- **Per-agent peer access editor + send_to_agent UI bubble** — Felix already has multi-agent, but the cloudcat polish (`530223c`, `63a7b0c`) is fleet-tagged in commits; lifting the in-process pieces is a small wave.
- **Artifacts store** — `internal/artifacts` is per-VM but works fine in single-user. Adds two tools (`create_artifact`, `fetch_artifact`) and a TTL-swept blob store at `~/.felix/artifacts/`.
- **Run snapshots in the chat UI** — `Registry.Snapshot` already returns past run summaries; the UI could surface "previous runs" navigation. Independent of this wave.

Each is decided as its own brainstorm.
