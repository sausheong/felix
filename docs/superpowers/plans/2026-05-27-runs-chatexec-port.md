# Runs + chatexec + chat.subscribe/replay/abort Port Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring Felix's chat path up to cloudcat's durable-run architecture: runs survive WebSocket disconnects, recover from process crashes, emit a replayable on-disk event log, and clients can attach via `chat.subscribe`/`chat.replay`.

**Architecture:** Three sequential phases — (1) port the standalone `internal/gateway/runs/` package verbatim, (2) port `internal/chatexec/` with fleet-only fields stripped, (3) refactor `internal/gateway/websocket.go` to drive everything through `chatexec.RunTurn` plus the new subscribe/replay/abort RPC methods. Each phase compiles, tests green, and ships independently.

**Tech Stack:** Go 1.25, gorilla/websocket v1.5.3, oklog/ulid/v2 v2.1.1 (both already in `go.mod`), existing felix packages (`internal/agent`, `internal/compaction`, `internal/cortex`, `internal/memory`, `internal/session`, `internal/skill`, `internal/tools`, `internal/llm`, `internal/config`).

**Source of truth:** Cloudcat repo at `~/projects/cloudcat`. All "copy from cloudcat" steps reference paths in that repo. Only Go import paths change: `github.com/sausheong/cloudcat` → `github.com/sausheong/felix`. The cloudcat code is tested and proven; the port is mechanical except for chatexec where fleet fields must be removed.

**Spec:** `docs/superpowers/specs/2026-05-27-runs-chatexec-port-design.md`

---

## Pre-flight checks

Before starting Phase 1, confirm these are true. Each is a one-line shell check.

- [ ] **Felix branch is clean and on main**
  ```bash
  cd ~/projects/felix && git status -s && git branch --show-current
  ```
  Expected: empty status, branch `main`.

- [ ] **gorilla/websocket and oklog/ulid/v2 already in go.mod**
  ```bash
  cd ~/projects/felix && grep -E "gorilla/websocket|oklog/ulid" go.mod
  ```
  Expected: both lines present. (If `oklog/ulid` is marked `// indirect`, that's fine — Phase 1 references promote it.)

- [ ] **Cloudcat repo exists at `~/projects/cloudcat`**
  ```bash
  ls ~/projects/cloudcat/internal/gateway/runs/ && ls ~/projects/cloudcat/internal/chatexec/
  ```
  Expected: both directories list files.

- [ ] **Cloudcat's runs + chatexec test suites currently green**
  ```bash
  cd ~/projects/cloudcat && go test ./internal/gateway/runs/... ./internal/chatexec/...
  ```
  Expected: `ok` for both packages. If anything fails here, fix in cloudcat first; do not port broken code.

---

# Phase 1 — Port the `runs` package

Pure addition. ~2,000 LOC of verbatim copy with one module rename. Phase ends with the package compiled, all tests passing, and no felix call site changed.

### Task 1.0: Prerequisite — port `config.WriteFileAtomic`

The `runs` package's `index.go` calls `config.WriteFileAtomic` which doesn't exist in Felix yet. Cloudcat has a 44-line implementation with a 61-line test file.

**Files:**
- Create: `internal/config/writefile.go`
- Create: `internal/config/writefile_test.go`

- [ ] **Step 1: Copy the implementation file from cloudcat**

  ```bash
  cp ~/projects/cloudcat/internal/config/writefile.go ~/projects/felix/internal/config/writefile.go
  ```

- [ ] **Step 2: Copy the test file**

  ```bash
  cp ~/projects/cloudcat/internal/config/writefile_test.go ~/projects/felix/internal/config/writefile_test.go
  ```

- [ ] **Step 3: Confirm no import rename is needed**

  Neither file imports any cloudcat-specific package — `WriteFileAtomic` uses only stdlib (`os`, `fmt`, `path/filepath`).

  ```bash
  cd ~/projects/felix && grep -l "sausheong/cloudcat" internal/config/writefile.go internal/config/writefile_test.go
  ```
  Expected: empty (no matches).

- [ ] **Step 4: Run the writefile test**

  ```bash
  cd ~/projects/felix && go test ./internal/config -run WriteFileAtomic -v
  ```
  Expected: PASS for all `TestWriteFileAtomic*` cases.

- [ ] **Step 5: Run full config test suite — confirm no regression**

  ```bash
  cd ~/projects/felix && go test ./internal/config/...
  ```
  Expected: `ok` (no other config tests broken).

- [ ] **Step 6: Commit**

  ```bash
  cd ~/projects/felix && git add internal/config/writefile.go internal/config/writefile_test.go
  git commit -m "feat(config): add WriteFileAtomic for crash-safe writes

  Ported from cloudcat. Temp+rename so concurrent readers never see a
  half-written file. Needed by the runs package coming in the next
  commit."
  ```

---

### Task 1.1: Copy `runs/types.go` and its test

**Files:**
- Create: `internal/gateway/runs/types.go`
- Create: `internal/gateway/runs/types_test.go`

- [ ] **Step 1: Make the package directory**

  ```bash
  mkdir -p ~/projects/felix/internal/gateway/runs
  ```

- [ ] **Step 2: Copy both files**

  ```bash
  cp ~/projects/cloudcat/internal/gateway/runs/types.go ~/projects/felix/internal/gateway/runs/types.go
  cp ~/projects/cloudcat/internal/gateway/runs/types_test.go ~/projects/felix/internal/gateway/runs/types_test.go
  ```

- [ ] **Step 3: Confirm no cloudcat imports**

  `types.go` only imports `encoding/json`. `types_test.go` only imports `testing` + stdlib.

  ```bash
  grep -l "sausheong/cloudcat" ~/projects/felix/internal/gateway/runs/types.go ~/projects/felix/internal/gateway/runs/types_test.go
  ```
  Expected: empty.

- [ ] **Step 4: Compile and test**

  ```bash
  cd ~/projects/felix && go test ./internal/gateway/runs/...
  ```
  Expected: `ok` for the runs package.

- [ ] **Step 5: Commit**

  ```bash
  cd ~/projects/felix && git add internal/gateway/runs/types.go internal/gateway/runs/types_test.go
  git commit -m "feat(runs): add Event, Status, RunSummary, SessionScope types

  First file of the runs package port from cloudcat. Defines the
  vocabulary used by the rest of the package: EventType discriminator,
  Status lifecycle states, RunSummary (the index.json row), and
  SessionScope (the (agent, session) key)."
  ```

---

### Task 1.2: Copy `runs/log.go` and its test

The `logWriter` is the single-writer append-only JSONL handle. `ReadLog` is the disk replay reader (truncated tails silently dropped).

**Files:**
- Create: `internal/gateway/runs/log.go`
- Create: `internal/gateway/runs/log_test.go`

- [ ] **Step 1: Copy both files**

  ```bash
  cp ~/projects/cloudcat/internal/gateway/runs/log.go ~/projects/felix/internal/gateway/runs/log.go
  cp ~/projects/cloudcat/internal/gateway/runs/log_test.go ~/projects/felix/internal/gateway/runs/log_test.go
  ```

- [ ] **Step 2: Confirm no cloudcat imports**

  Only stdlib (`bufio`, `encoding/json`, `errors`, `fmt`, `io`, `io/fs`, `os`).

  ```bash
  grep -l "sausheong/cloudcat" ~/projects/felix/internal/gateway/runs/log.go ~/projects/felix/internal/gateway/runs/log_test.go
  ```
  Expected: empty.

- [ ] **Step 3: Run tests**

  ```bash
  cd ~/projects/felix && go test ./internal/gateway/runs -run TestLog -v
  ```
  Expected: PASS for `TestLogWriter*` and `TestReadLog*` cases.

- [ ] **Step 4: Commit**

  ```bash
  cd ~/projects/felix && git add internal/gateway/runs/log.go internal/gateway/runs/log_test.go
  git commit -m "feat(runs): add logWriter + ReadLog for per-run JSONL log files

  Single-writer append-only logWriter with fsync per line. ReadLog
  drops truncated tails silently — the recovery path treats partial
  writes as 'process died mid-event'."
  ```

---

### Task 1.3: Copy `runs/index.go` and its test

`IndexFile` is the on-disk shape of `<key>.runs/index.json`. This is the only file that imports `config.WriteFileAtomic` from Task 1.0.

**Files:**
- Create: `internal/gateway/runs/index.go`
- Create: `internal/gateway/runs/index_test.go`

- [ ] **Step 1: Copy the implementation file**

  ```bash
  cp ~/projects/cloudcat/internal/gateway/runs/index.go ~/projects/felix/internal/gateway/runs/index.go
  ```

- [ ] **Step 2: Rename the import**

  Cloudcat imports `github.com/sausheong/cloudcat/internal/config`. Felix's path is `github.com/sausheong/felix/internal/config`.

  ```bash
  sed -i '' 's|github.com/sausheong/cloudcat|github.com/sausheong/felix|g' ~/projects/felix/internal/gateway/runs/index.go
  ```

- [ ] **Step 3: Copy the test file (no rename needed)**

  ```bash
  cp ~/projects/cloudcat/internal/gateway/runs/index_test.go ~/projects/felix/internal/gateway/runs/index_test.go
  ```

- [ ] **Step 4: Verify rename was the only cloudcat reference**

  ```bash
  grep -l "sausheong/cloudcat" ~/projects/felix/internal/gateway/runs/index.go ~/projects/felix/internal/gateway/runs/index_test.go
  ```
  Expected: empty.

- [ ] **Step 5: Run tests**

  ```bash
  cd ~/projects/felix && go test ./internal/gateway/runs -run TestIndex -v
  ```
  Expected: PASS for `TestIndexFile*` cases.

- [ ] **Step 6: Commit**

  ```bash
  cd ~/projects/felix && git add internal/gateway/runs/index.go internal/gateway/runs/index_test.go
  git commit -m "feat(runs): add IndexFile with atomic save via config.WriteFileAtomic

  Per-session index.json holds RunSummary rows for both in-flight
  and completed runs. Atomic write means partial-write crashes leave
  the previous index intact, and recovery rebuilds anyway when the
  file is unparseable."
  ```

---

### Task 1.4: Copy `runs/registry.go` and its test

The biggest file in the package: 447 lines covering `Registry`, `Run`, `Create`, `SupersedeAndCreate`, `Append`, `Finish`, `Subscribe`, `Unsubscribe`, `Snapshot`, `Remove`, `Done`. Test file is 527 lines.

**Files:**
- Create: `internal/gateway/runs/registry.go`
- Create: `internal/gateway/runs/registry_test.go`

- [ ] **Step 1: Copy both files**

  ```bash
  cp ~/projects/cloudcat/internal/gateway/runs/registry.go ~/projects/felix/internal/gateway/runs/registry.go
  cp ~/projects/cloudcat/internal/gateway/runs/registry_test.go ~/projects/felix/internal/gateway/runs/registry_test.go
  ```

- [ ] **Step 2: Confirm no cloudcat imports**

  `registry.go` imports only stdlib + `github.com/gorilla/websocket` (already in go.mod). The test file imports its own package + stdlib + websocket.

  ```bash
  grep "sausheong/cloudcat" ~/projects/felix/internal/gateway/runs/registry.go ~/projects/felix/internal/gateway/runs/registry_test.go
  ```
  Expected: empty.

- [ ] **Step 3: Run tests**

  ```bash
  cd ~/projects/felix && go test ./internal/gateway/runs -run TestRegistry -v
  ```
  Expected: PASS for all `TestRegistry*` and `TestRun*` cases. Watch for race-detector noise — re-run with `-race` if anything looks flaky:
  ```bash
  cd ~/projects/felix && go test -race ./internal/gateway/runs -run TestRegistry -count=3
  ```

- [ ] **Step 4: Commit**

  ```bash
  cd ~/projects/felix && git add internal/gateway/runs/registry.go internal/gateway/runs/registry_test.go
  git commit -m "feat(runs): add Registry and Run with subscribe/replay primitives

  Registry: process-wide map of in-flight runs keyed by both runID and
  (agent, session) scope. Create/SupersedeAndCreate are atomic over the
  on-disk index. OnNewRun callback lets the gateway broadcast
  run_started to other open conns viewing the same scope.

  Run: per-turn JSONL writer + non-blocking fan-out to WebSocket
  subscribers (slow subscribers get dropped, expected to reconnect +
  replay). Append holds Run.mu across LastSeq.Add, log.Append, and
  fanout so Subscribe's gap-fill is exact. Finish writes the terminal
  Done event under a Completed CAS and closes the done channel — safe
  to call once."
  ```

---

### Task 1.5: Copy `runs/recovery.go` and its test

`RecoverInterruptedRuns` walks `sessionsDir` for `*.runs/index.json` at boot, reconciles entries whose status is still `running` (the process must have crashed), and writes a synthetic `Done(status=interrupted)` event to the corresponding log.

**Files:**
- Create: `internal/gateway/runs/recovery.go`
- Create: `internal/gateway/runs/recovery_test.go`

- [ ] **Step 1: Copy both files**

  ```bash
  cp ~/projects/cloudcat/internal/gateway/runs/recovery.go ~/projects/felix/internal/gateway/runs/recovery.go
  cp ~/projects/cloudcat/internal/gateway/runs/recovery_test.go ~/projects/felix/internal/gateway/runs/recovery_test.go
  ```

- [ ] **Step 2: Confirm no cloudcat imports**

  ```bash
  grep "sausheong/cloudcat" ~/projects/felix/internal/gateway/runs/recovery.go ~/projects/felix/internal/gateway/runs/recovery_test.go
  ```
  Expected: empty.

- [ ] **Step 3: Run tests**

  ```bash
  cd ~/projects/felix && go test ./internal/gateway/runs -run TestRecover -v
  ```
  Expected: PASS for `TestRecoverInterruptedRuns*` cases.

- [ ] **Step 4: Commit**

  ```bash
  cd ~/projects/felix && git add internal/gateway/runs/recovery.go internal/gateway/runs/recovery_test.go
  git commit -m "feat(runs): add RecoverInterruptedRuns startup scan

  Walks sessionsDir/*/*.runs/index.json at boot. For each row whose
  Status is still 'running', reads the matching log file. If the tail
  is a Done event, trusts it (the index just wasn't persisted). If
  not, appends a synthetic Done(status=interrupted) event and updates
  the index. Missing sessionsDir → no-op."
  ```

---

### Task 1.6: Copy `runs/integration_test.go`

256-line integration test exercising the full registry+run+log+index+recovery interaction. No new source file — this is purely tests.

**Files:**
- Create: `internal/gateway/runs/integration_test.go`

- [ ] **Step 1: Copy the test file**

  ```bash
  cp ~/projects/cloudcat/internal/gateway/runs/integration_test.go ~/projects/felix/internal/gateway/runs/integration_test.go
  ```

- [ ] **Step 2: Confirm no cloudcat imports**

  ```bash
  grep "sausheong/cloudcat" ~/projects/felix/internal/gateway/runs/integration_test.go
  ```
  Expected: empty.

- [ ] **Step 3: Run integration tests**

  ```bash
  cd ~/projects/felix && go test ./internal/gateway/runs -run TestIntegration -v
  ```
  Expected: PASS for all integration cases.

- [ ] **Step 4: Commit**

  ```bash
  cd ~/projects/felix && git add internal/gateway/runs/integration_test.go
  git commit -m "test(runs): add full-stack integration test

  Exercises registry + run + log + index + recovery in one suite:
  create → append → finish → recovery round-trip, supersede flow,
  subscriber attach mid-run with gap-fill, slow-subscriber drop."
  ```

---

### Task 1.7: Full Phase 1 verification

- [ ] **Step 1: Run the whole runs package suite with race detector**

  ```bash
  cd ~/projects/felix && go test -race -count=2 ./internal/gateway/runs/...
  ```
  Expected: `ok` (no races, no flakes across 2 runs).

- [ ] **Step 2: Run `go vet` on the new package**

  ```bash
  cd ~/projects/felix && go vet ./internal/gateway/runs/...
  ```
  Expected: empty output.

- [ ] **Step 3: Confirm no other package broke (the runs package has no consumers yet, so this should be a no-op compile)**

  ```bash
  cd ~/projects/felix && go build ./...
  ```
  Expected: no output (clean build).

- [ ] **Step 4: Run the full felix test suite to confirm zero regression**

  ```bash
  cd ~/projects/felix && go test ./...
  ```
  Expected: `ok` for every package.

Phase 1 is done. The `runs` package is in tree and tested, but no felix code uses it yet. That's expected — Phase 2 brings in `chatexec`, the only consumer.

---

# Phase 2 — Port the `chatexec` package

Cloudcat's `chatexec.go` is 581 lines and depends on 7+ felix packages. The bulk is verbatim; the changes are removing fleet/inbox-specific fields and code paths.

### Task 2.0: Interface diff — produce the adaptation list

Before copying anything, diff each interface chatexec touches between cloudcat and felix. Adaptation needed where signatures differ.

**Files:**
- (Read-only investigation. No files created in this task.)

- [ ] **Step 1: Diff `agent.RuntimeDeps`**

  ```bash
  diff <(grep -A 30 "type RuntimeDeps struct" ~/projects/cloudcat/internal/agent/agent.go) \
       <(grep -A 30 "type RuntimeDeps struct" ~/projects/felix/internal/agent/agent.go)
  ```
  Expected diff: cloudcat has an extra `FleetPeersAddendum` function field. Felix does not. Action: chatexec must not set this field.

- [ ] **Step 2: Diff `agent.BuildRuntimeForAgent` signature**

  ```bash
  diff <(grep -A 5 "func BuildRuntimeForAgent" ~/projects/cloudcat/internal/agent/agent.go) \
       <(grep -A 5 "func BuildRuntimeForAgent" ~/projects/felix/internal/agent/agent.go)
  ```
  Note any differences. If signatures match, no adaptation needed.

- [ ] **Step 3: Diff `tools.Executor` and `tools.PermissionChecker`**

  ```bash
  diff <(grep -A 5 "type Executor\|type PermissionChecker" ~/projects/cloudcat/internal/tools/*.go) \
       <(grep -A 5 "type Executor\|type PermissionChecker" ~/projects/felix/internal/tools/*.go)
  ```
  Note any signature drift.

- [ ] **Step 4: Diff `tools.JobScheduler`, `tools.CronTool`, `tools.NewTaskTool`, `agent.MakeSubagentFactory`, `agent.SubagentBuildFn`**

  ```bash
  diff <(grep -A 3 "type JobScheduler\|type CronTool\|func NewTaskTool\|func MakeSubagentFactory\|type SubagentBuildFn" ~/projects/cloudcat/internal/tools/*.go ~/projects/cloudcat/internal/agent/*.go) \
       <(grep -A 3 "type JobScheduler\|type CronTool\|func NewTaskTool\|func MakeSubagentFactory\|type SubagentBuildFn" ~/projects/felix/internal/tools/*.go ~/projects/felix/internal/agent/*.go)
  ```

- [ ] **Step 5: Diff `session.Store`, `memory.Manager`, `skill.Loader`, `compaction.Provider`, `llm.ParseProviderModel`**

  ```bash
  for sym in "type Store struct" "type Manager struct" "type Loader struct" "type Provider struct" "func ParseProviderModel"; do
    echo "=== $sym ==="
    diff <(grep -A 3 "$sym" ~/projects/cloudcat/internal/session/*.go ~/projects/cloudcat/internal/memory/*.go ~/projects/cloudcat/internal/skill/*.go ~/projects/cloudcat/internal/compaction/*.go ~/projects/cloudcat/internal/llm/*.go 2>/dev/null) \
         <(grep -A 3 "$sym" ~/projects/felix/internal/session/*.go ~/projects/felix/internal/memory/*.go ~/projects/felix/internal/skill/*.go ~/projects/felix/internal/compaction/*.go ~/projects/felix/internal/llm/*.go 2>/dev/null)
  done
  ```

- [ ] **Step 6: Diff `config.Config`, `config.PeerGrant`, `config.GetAgent`, `config.AgentLoop`, `config.EligibleSubagents`**

  ```bash
  for sym in "func.*Config.*GetAgent" "func.*Config.*EligibleSubagents" "AgentLoop config" "type PeerGrant"; do
    echo "=== $sym ==="
    diff <(grep "$sym" ~/projects/cloudcat/internal/config/*.go) \
         <(grep "$sym" ~/projects/felix/internal/config/*.go)
  done
  ```
  Expected: `PeerGrant` is cloudcat-only. The chatexec port drops the `PeerGrant` field from `TurnDeps` and the entire `peer_grant.go` file (covered in spec).

- [ ] **Step 7: Write up findings in commit message of next task**

  Capture: every signature drift discovered. The implementer of Task 2.2 uses this list to know exactly what to adapt in chatexec.go.

This task produces no code — it produces knowledge. If everything matches, Task 2.2 is straight sed-and-strip. If anything differs, Task 2.2 includes the bridging.

---

### Task 2.1: Port `overlay.go` (no SendToAgent field)

Cloudcat's `ChatToolOverlay` has three optional fields: `Task`, `Cron`, `SendToAgent`. The Felix port drops `SendToAgent` — in-process `send_to_agent` works through the shared `tools` registry; the per-call overlay matters in cloudcat only for fleet `Self` attribution.

**Files:**
- Create: `internal/chatexec/overlay.go`
- Create: `internal/chatexec/overlay_test.go`

- [ ] **Step 1: Make the package directory**

  ```bash
  mkdir -p ~/projects/felix/internal/chatexec
  ```

- [ ] **Step 2: Copy `overlay.go`**

  ```bash
  cp ~/projects/cloudcat/internal/chatexec/overlay.go ~/projects/felix/internal/chatexec/overlay.go
  ```

- [ ] **Step 3: Rename imports**

  ```bash
  sed -i '' 's|github.com/sausheong/cloudcat|github.com/sausheong/felix|g' ~/projects/felix/internal/chatexec/overlay.go
  ```

- [ ] **Step 4: Read the file and identify the `SendToAgent` field and its usage**

  ```bash
  grep -n "SendToAgent" ~/projects/felix/internal/chatexec/overlay.go
  ```
  Expected: one field declaration on the struct, one or two references in methods (probably in a tool-list helper and in `Execute`).

- [ ] **Step 5: Remove the SendToAgent field and all references**

  Edit `internal/chatexec/overlay.go`:
  1. Remove the `SendToAgent *tools.SendToAgentTool` field from the struct.
  2. Remove any case in `Execute`/`List`/`Get` (whatever methods exist) that dispatches to `o.SendToAgent`.
  3. Remove any nil-check that gates on `o.SendToAgent != nil`.

  After editing, verify:
  ```bash
  grep -n "SendToAgent" ~/projects/felix/internal/chatexec/overlay.go
  ```
  Expected: empty.

- [ ] **Step 6: Copy `overlay_test.go`**

  ```bash
  cp ~/projects/cloudcat/internal/chatexec/overlay_test.go ~/projects/felix/internal/chatexec/overlay_test.go
  ```

- [ ] **Step 7: Rename imports in test file**

  ```bash
  sed -i '' 's|github.com/sausheong/cloudcat|github.com/sausheong/felix|g' ~/projects/felix/internal/chatexec/overlay_test.go
  ```

- [ ] **Step 8: Remove any `SendToAgent`-specific test cases**

  ```bash
  grep -n "SendToAgent" ~/projects/felix/internal/chatexec/overlay_test.go
  ```
  Delete the test functions or subtests that reference `SendToAgent`. Other tests (Task, Cron, fallthrough) stay as-is. After editing:
  ```bash
  grep -n "SendToAgent" ~/projects/felix/internal/chatexec/overlay_test.go
  ```
  Expected: empty.

- [ ] **Step 9: Build and test**

  ```bash
  cd ~/projects/felix && go test ./internal/chatexec/...
  ```
  Expected: PASS for the overlay test cases. If a compile error mentions an interface mismatch with `tools` package, refer back to Task 2.0's findings.

- [ ] **Step 10: Commit**

  ```bash
  cd ~/projects/felix && git add internal/chatexec/overlay.go internal/chatexec/overlay_test.go
  git commit -m "feat(chatexec): add ChatToolOverlay (per-call Task + Cron tools)

  Ported from cloudcat. Strips the SendToAgent field that cloudcat
  uses for fleet Self attribution — Felix's in-process multi-agent
  works through the shared tools registry without per-call overrides."
  ```

---

### Task 2.2: Port `chatexec.go` (strip fleet fields)

The main file. ~581 lines in cloudcat. The Felix port:
- removes 7 fields from `TurnDeps`: `FleetBaseDomain`, `InboxSender`, `AgentExists`, `ReplyTo`, `PeerGrant`, `TraceContext`, `OverlayMetrics`
- removes the per-turn `peerGrantChecker` permission wrapper
- removes the `extractTraceparentCtx` call
- removes the SendToAgent overlay branch (handled in Task 2.1)
- drops the `ChatToolOverlay.SendToAgent` build block

**Files:**
- Create: `internal/chatexec/chatexec.go`

- [ ] **Step 1: Copy the file**

  ```bash
  cp ~/projects/cloudcat/internal/chatexec/chatexec.go ~/projects/felix/internal/chatexec/chatexec.go
  ```

- [ ] **Step 2: Rename imports**

  ```bash
  sed -i '' 's|github.com/sausheong/cloudcat|github.com/sausheong/felix|g' ~/projects/felix/internal/chatexec/chatexec.go
  ```

- [ ] **Step 3: Remove the 7 fleet fields from `TurnDeps`**

  Open `internal/chatexec/chatexec.go`. Find `type TurnDeps struct {` and delete these field declarations (and the comments above each):
  - `FleetBaseDomain string`
  - `InboxSender tools.InboxSender`
  - `AgentExists tools.AgentExists`
  - `ReplyTo map[string]string`
  - `PeerGrant *config.PeerGrant`
  - `TraceContext string`
  - `OverlayMetrics OverlayMetrics`

  Verify:
  ```bash
  grep -nE "FleetBaseDomain|InboxSender|AgentExists|ReplyTo|PeerGrant|TraceContext|OverlayMetrics" ~/projects/felix/internal/chatexec/chatexec.go
  ```
  Expected: empty.

- [ ] **Step 4: Remove the `peerGrantChecker` permission overlay block**

  Find and delete this block (around line 200-209 in cloudcat):
  ```go
  permission := deps.Permission
  if deps.PeerGrant != nil {
      permission = &peerGrantChecker{base: deps.Permission, grant: *deps.PeerGrant}
  }
  ```
  Replace with the single line:
  ```go
  permission := deps.Permission
  ```

  Verify:
  ```bash
  grep -n "peerGrantChecker" ~/projects/felix/internal/chatexec/chatexec.go
  ```
  Expected: empty.

- [ ] **Step 5: Remove the `extractTraceparentCtx` call**

  Find and delete the line (around line 284 in cloudcat):
  ```go
  parentCtx = extractTraceparentCtx(parentCtx, deps.TraceContext)
  ```
  Plus the multi-line comment block immediately above it about W3C traceparent propagation.

  Verify:
  ```bash
  grep -n "extractTraceparent\|TraceContext" ~/projects/felix/internal/chatexec/chatexec.go
  ```
  Expected: empty.

- [ ] **Step 6: Remove the SendToAgent overlay branch**

  Find and delete the block (around line 243-264 in cloudcat) that builds `overlay.SendToAgent`:
  ```go
  if deps.InboxSender != nil && deps.AgentExists != nil {
      // ... entire block including SendToAgentTool construction ...
      overlay.SendToAgent = &tools.SendToAgentTool{
          ...
      }
  }
  ```
  Also adjust the condition `if overlay.Task != nil || overlay.Cron != nil || overlay.SendToAgent != nil {` to drop the `|| overlay.SendToAgent != nil` clause.

  Verify:
  ```bash
  grep -n "SendToAgent\|InboxSender\|AgentExists" ~/projects/felix/internal/chatexec/chatexec.go
  ```
  Expected: empty.

- [ ] **Step 7: Remove the `OverlayMetrics: deps.OverlayMetrics` assignment in the overlay constructor**

  The overlay construction line:
  ```go
  overlay := &ChatToolOverlay{Base: deps.Tools, Metrics: deps.OverlayMetrics}
  ```
  becomes:
  ```go
  overlay := &ChatToolOverlay{Base: deps.Tools}
  ```
  (Since Task 2.1 may have dropped the `Metrics` field too if it was named `OverlayMetrics`. Check that file and adjust accordingly — match the overlay struct's actual field set.)

- [ ] **Step 8: Build the package**

  ```bash
  cd ~/projects/felix && go build ./internal/chatexec/...
  ```
  Expected: clean build. If there's a compile error referencing a missing felix-side method (drift not caught in Task 2.0), bridge it now: either add the missing thing to felix's interface or wrap with a local helper in chatexec.

- [ ] **Step 9: Commit**

  ```bash
  cd ~/projects/felix && git add internal/chatexec/chatexec.go
  git commit -m "feat(chatexec): add RunTurn driver, stripped of fleet fields

  Ported from cloudcat. Drops 7 fleet/inbox-only fields from TurnDeps
  (FleetBaseDomain, InboxSender, AgentExists, ReplyTo, PeerGrant,
  TraceContext, OverlayMetrics). The package still has no consumer —
  startup + gateway wiring lands in Phase 3."
  ```

---

### Task 2.3: Port the chatexec test file

Cloudcat has `chatexec_test.go` (203 lines). It tests `RunTurn` end-to-end with fakes. Some tests will reference fleet fields and must be deleted; others (the happy path, ErrAgentNotConfigured, ErrProviderNotConfigured, ErrRunsRegistryMissing) port directly.

**Files:**
- Create: `internal/chatexec/chatexec_test.go`

- [ ] **Step 1: Copy the test file**

  ```bash
  cp ~/projects/cloudcat/internal/chatexec/chatexec_test.go ~/projects/felix/internal/chatexec/chatexec_test.go
  ```

- [ ] **Step 2: Rename imports**

  ```bash
  sed -i '' 's|github.com/sausheong/cloudcat|github.com/sausheong/felix|g' ~/projects/felix/internal/chatexec/chatexec_test.go
  ```

- [ ] **Step 3: Identify fleet-only test cases**

  ```bash
  grep -nE "PeerGrant|InboxSender|AgentExists|FleetBaseDomain|ReplyTo|TraceContext|OverlayMetrics" ~/projects/felix/internal/chatexec/chatexec_test.go
  ```
  Note the test function names containing these references.

- [ ] **Step 4: Delete fleet-only test functions**

  Open the file. For each test function that references any of the fields listed in Step 3, delete the entire function. Other tests remain.

  Verify:
  ```bash
  grep -nE "PeerGrant|InboxSender|AgentExists|FleetBaseDomain|ReplyTo|TraceContext|OverlayMetrics" ~/projects/felix/internal/chatexec/chatexec_test.go
  ```
  Expected: empty.

- [ ] **Step 5: Build and run tests**

  ```bash
  cd ~/projects/felix && go test ./internal/chatexec/...
  ```
  Expected: PASS for the remaining test cases. If a test fails because of an interface mismatch, refer to Task 2.0 findings or add a local test helper.

- [ ] **Step 6: Commit**

  ```bash
  cd ~/projects/felix && git add internal/chatexec/chatexec_test.go
  git commit -m "test(chatexec): port unit tests, drop fleet-only cases

  Removes test functions covering PeerGrant, InboxSender/AgentExists,
  ReplyTo, FleetBaseDomain, and TraceContext — all of which are
  fields that the Felix port doesn't carry. Happy path, agent-not-
  configured, provider-not-configured, and registry-missing cases
  remain."
  ```

---

### Task 2.4: Phase 2 verification

- [ ] **Step 1: Run the chatexec suite with race detector**

  ```bash
  cd ~/projects/felix && go test -race -count=2 ./internal/chatexec/...
  ```
  Expected: `ok`.

- [ ] **Step 2: Run go vet**

  ```bash
  cd ~/projects/felix && go vet ./internal/chatexec/...
  ```
  Expected: empty.

- [ ] **Step 3: Run the full felix test suite**

  ```bash
  cd ~/projects/felix && go test ./...
  ```
  Expected: `ok` for every package. The chatexec package is unwired, so this is purely a regression check.

Phase 2 is done. `chatexec.RunTurn` exists but has no caller — Phase 3 makes it the workhorse.

---

# Phase 3 — Gateway integration + startup wiring

This phase replaces Felix's current `handleChatSend` implementation with one that drives everything through `chatexec.RunTurn` plus the new RPC methods. It edits existing files heavily.

### Task 3.1: Locate Felix's current chat handler and snapshot its behaviour

Before editing, capture the current implementation so the rewrite can preserve semantics where they matter (auth, message-size limits, JSON-RPC envelope).

**Files:**
- (Read-only investigation.)

- [ ] **Step 1: Find the WebSocket handler file**

  ```bash
  cd ~/projects/felix && grep -ln "handleChat\|chat\\.send" internal/gateway/*.go
  ```
  Note the file path(s). The likely file is `internal/gateway/websocket.go`.

- [ ] **Step 2: Locate the current `handleChatSend` (or equivalent) and read it end-to-end**

  ```bash
  cd ~/projects/felix && grep -n "handleChatSend\|func .* WebSocketHandler" internal/gateway/websocket.go
  ```
  Read the file from the `handleChatSend` declaration to its end. Note:
  - parameter parsing (`agentId`, `sessionKey`, `text`)
  - any auth/origin checks done before processing
  - how it currently obtains the agent's runtime
  - how it currently streams events back over the WebSocket
  - the JSON-RPC response shape it returns

- [ ] **Step 3: Locate the dispatcher that routes `chat.send`**

  ```bash
  cd ~/projects/felix && grep -n "dispatch\|chat\\.send\|chat\\.abort" internal/gateway/websocket.go
  ```
  Note the dispatch function. Phase 3 adds `chat.subscribe`, `chat.replay`, and `chat.abort` cases there.

- [ ] **Step 4: Locate the WebSocketHandler struct definition and its existing Set* methods**

  ```bash
  cd ~/projects/felix && grep -n "type WebSocketHandler\|func (h \\*WebSocketHandler) Set" internal/gateway/websocket.go
  ```
  Phase 3 adds `SetRunsRegistry` and `BroadcastNewRun` methods alongside these.

- [ ] **Step 5: Locate the startup wiring that constructs the WebSocketHandler**

  ```bash
  cd ~/projects/felix && grep -n "NewWebSocketHandler\|WebSocketHandler{" internal/startup/*.go
  ```
  Note the file and the area to edit in Task 3.6.

No file changes. End this task with a note in the next task's commit message about what's being preserved vs. replaced.

---

### Task 3.2: Add `SetRunsRegistry` and `BroadcastNewRun` methods

These are additive — no existing behaviour changes. The methods are called by startup in Task 3.6, and `BroadcastNewRun` is the `OnNewRun` callback.

**Files:**
- Modify: `internal/gateway/websocket.go` (additions at the end of the file or in a logical Set*-methods section)

- [ ] **Step 1: Add the `runs` import**

  At the top of `internal/gateway/websocket.go`, add to the import block:
  ```go
  "github.com/sausheong/felix/internal/gateway/runs"
  ```

- [ ] **Step 2: Add a `runs *runs.Registry` field to the `WebSocketHandler` struct**

  Locate `type WebSocketHandler struct {` and add the field (with the others):
  ```go
  // runs is the durable in-flight run registry. Set by SetRunsRegistry.
  // nil until startup wires it; chat.send fails fast with an RPC error
  // if it's still nil at request time.
  runs *runs.Registry
  ```

- [ ] **Step 3: Add the `SetRunsRegistry` method**

  Append to the file (or place near other Set* methods):
  ```go
  // SetRunsRegistry installs the durable-run registry. Called once by
  // startup after the registry is constructed.
  func (h *WebSocketHandler) SetRunsRegistry(reg *runs.Registry) {
      h.mu.Lock()
      defer h.mu.Unlock()
      h.runs = reg
  }
  ```
  (If `WebSocketHandler` uses an `RWMutex` or a different name, match it.)

- [ ] **Step 4: Add the `BroadcastNewRun` method**

  Append to the file:
  ```go
  // BroadcastNewRun is the runs.Registry.OnNewRun callback. For every
  // open conn currently viewing the same (agent, session) scope as the
  // new run, push a JSON-RPC notification "run_started" so the
  // frontend can call chat.replay to attach. Background runs (none
  // yet in Felix, but future cron / scheduled tasks) become visible
  // without polling.
  func (h *WebSocketHandler) BroadcastNewRun(scope runs.SessionScope, run *runs.Run) {
      h.mu.RLock()
      conns := make([]*websocket.Conn, 0)
      for conn, view := range h.activeViews {
          if view.AgentID == scope.AgentID && view.SessionKey == scope.SessionKey {
              conns = append(conns, conn)
          }
      }
      h.mu.RUnlock()

      notif := map[string]any{
          "jsonrpc": "2.0",
          "method":  "run_started",
          "params": map[string]any{
              "runId":      run.ID,
              "agentId":    scope.AgentID,
              "sessionKey": scope.SessionKey,
          },
      }
      for _, c := range conns {
          _ = c.WriteJSON(notif)
      }
  }
  ```

  Note: `h.activeViews` is a placeholder for "the per-conn view tracking that Felix uses." If Felix tracks active session per conn differently, adapt the loop. If Felix doesn't track it at all yet, broadcast to all open conns and let the frontend filter — leave a TODO comment in the code marking this as a follow-up only if the broadcast volume turns out to be a problem. Worst case: every open conn gets a `run_started` it ignores.

- [ ] **Step 5: Build**

  ```bash
  cd ~/projects/felix && go build ./internal/gateway/...
  ```
  Expected: clean build.

- [ ] **Step 6: Commit**

  ```bash
  cd ~/projects/felix && git add internal/gateway/websocket.go
  git commit -m "feat(gateway): add SetRunsRegistry + BroadcastNewRun on WebSocketHandler

  Additive plumbing for the runs package. The registry pointer is nil
  until startup wires it (next phase). BroadcastNewRun is the OnNewRun
  callback that pushes run_started notifications to conns viewing the
  same scope so frontends can attach via chat.replay."
  ```

---

### Task 3.3: Refactor `handleChatSend` to use `chatexec.RunTurn`

Replace the body of `handleChatSend` with a call to `chatexec.RunTurn` plus a `wsSubscriber` adapter that pushes events back to the conn as JSON-RPC notifications.

**Files:**
- Modify: `internal/gateway/websocket.go`

- [ ] **Step 1: Add the `chatexec` import**

  ```go
  "github.com/sausheong/felix/internal/chatexec"
  ```

- [ ] **Step 2: Define the `wsSubscriber` adapter type at the bottom of the file**

  ```go
  // wsSubscriber adapts chatexec.Subscriber → JSON-RPC notifications on
  // the originating WebSocket conn. Used by handleChatSend so the
  // sender sees its own run's events streamed live.
  type wsSubscriber struct {
      conn  *websocket.Conn
      rpcID any // the chat.send request ID, echoed in notifications
  }

  func (s *wsSubscriber) OnAttached(runID string) {
      _ = s.conn.WriteJSON(map[string]any{
          "jsonrpc": "2.0",
          "id":      s.rpcID,
          "result": map[string]any{
              "runId": runID,
          },
      })
  }

  func (s *wsSubscriber) OnEvent(e runs.Event) {
      _ = s.conn.WriteJSON(map[string]any{
          "jsonrpc": "2.0",
          "method":  "chat.event",
          "params":  eventToResult(e),
      })
  }

  // eventToResult turns a runs.Event into the JSON shape the chat
  // client expects. Mirrors cloudcat's gateway/websocket.go::eventToResult.
  func eventToResult(e runs.Event) map[string]any {
      m := map[string]any{
          "seq":  e.Seq,
          "ts":   e.Ts,
          "type": string(e.Type),
      }
      if len(e.Payload) > 0 {
          m["payload"] = e.Payload
      }
      if e.Type == runs.EventTypeDone {
          m["status"] = string(e.Status)
          if e.Reason != "" {
              m["reason"] = string(e.Reason)
          }
          if e.SupersededBy != "" {
              m["supersededBy"] = e.SupersededBy
          }
          if e.Error != "" {
              m["error"] = e.Error
          }
      }
      return m
  }
  ```

- [ ] **Step 3: Replace the body of `handleChatSend`**

  Replace the existing body with:
  ```go
  func (h *WebSocketHandler) handleChatSend(conn *websocket.Conn, req JSONRPCRequest) {
      var params struct {
          AgentID    string `json:"agentId"`
          SessionKey string `json:"sessionKey"`
          Text       string `json:"text"`
      }
      if err := json.Unmarshal(req.Params, &params); err != nil {
          writeRPCError(conn, req.ID, -32602, "invalid params: "+err.Error())
          return
      }
      if params.SessionKey == "" {
          params.SessionKey = "default"
      }

      h.mu.RLock()
      reg := h.runs
      cfg := h.config
      providers := h.providers
      h.mu.RUnlock()

      if reg == nil {
          writeRPCError(conn, req.ID, -32000, "runs registry not configured")
          return
      }

      sub := &wsSubscriber{conn: conn, rpcID: req.ID}
      scope := runs.SessionScope{AgentID: params.AgentID, SessionKey: params.SessionKey}

      deps := chatexec.TurnDeps{
          Runs:           reg,
          Sessions:       h.sessions,
          SessionsBase:   h.sessionsBase,
          Providers:      providers,
          Tools:          h.tools,
          Permission:     h.permission,
          Skills:         h.skills,
          Memory:         h.memory,
          CompactionProv: h.compactionProv,
          Config:         cfg,
          SubagentBuild:  h.subagentBuild,
          JobScheduler:   h.jobScheduler,
          Metrics:        h.metrics,
          ServerCtx:      h.serverCtx,
          CortexProvider: h.cortexProvider,
          OnTraceMark:    h.makeTraceMarkForwarder(conn, req.ID),
      }

      // Launch in a goroutine — chatexec.RunTurn blocks until the run
      // finishes, but chat.send returns as soon as the run is registered.
      // OnAttached writes the JSON-RPC response with the runId.
      go func() {
          _, err := chatexec.RunTurn(context.Background(), deps, scope, params.Text, sub)
          if err != nil {
              slog.Error("chatexec.RunTurn", "scope", scope, "error", err)
          }
      }()
  }
  ```

  Notes:
  - Field names on `WebSocketHandler` (`h.sessions`, `h.providers`, etc.) must match what Felix actually has. If a field is missing, add it via a new `Set*` method in this task (and wire it in Task 3.6).
  - `h.makeTraceMarkForwarder` is whatever Felix uses to forward trace marks today. If Felix doesn't have one, omit `OnTraceMark` from `deps` — chatexec accepts nil.

- [ ] **Step 4: Build**

  ```bash
  cd ~/projects/felix && go build ./internal/gateway/...
  ```
  Resolve any field-name mismatches inline. Each missing field is either:
  - present under a different name → use the existing name, or
  - genuinely missing → add a `Set*` method + struct field now (the wiring in Task 3.6 will populate it).

- [ ] **Step 5: Confirm the dispatcher still routes `chat.send` to this method**

  ```bash
  cd ~/projects/felix && grep -n "chat\\.send" internal/gateway/websocket.go
  ```
  Expected: at least one match in the dispatch switch. If the dispatcher had a different signature for the handler, adapt either it or the handler.

- [ ] **Step 6: Run gateway tests to see what broke**

  ```bash
  cd ~/projects/felix && go test ./internal/gateway/...
  ```
  Expected: some failures — tests likely assume the old synchronous behaviour. Note each failure. Fix any that are real semantic regressions; for tests that are simply checking "blocked until done", update them to await `chat.done` notifications instead. Tests for the JSON-RPC response shape change since `chat.send` now returns `{runId}` instead of the final reply.

- [ ] **Step 7: Commit**

  ```bash
  cd ~/projects/felix && git add internal/gateway/websocket.go
  git commit -m "refactor(gateway): drive chat.send through chatexec.RunTurn

  Replaces the old in-method chat loop with a delegated call to
  chatexec.RunTurn. chat.send now returns {runId} as soon as the run
  is registered; events stream as chat.event notifications via the
  wsSubscriber adapter. Disconnect no longer cancels the run — it
  survives on the runs.Registry and can be re-attached via the
  chat.subscribe / chat.replay methods added in the next commits.

  Existing gateway tests that assumed synchronous response are updated
  to await chat.done notifications."
  ```

---

### Task 3.4: Add `handleChatAbort`

The simplest of the new methods. Aborts the in-flight run for `(agentId, sessionKey)`.

**Files:**
- Modify: `internal/gateway/websocket.go`

- [ ] **Step 1: Add the method**

  ```go
  // handleChatAbort cancels the in-flight run for the given scope, if
  // any. Aborting a finished/missing run is a no-op (success).
  func (h *WebSocketHandler) handleChatAbort(conn *websocket.Conn, req JSONRPCRequest) {
      var params struct {
          AgentID    string `json:"agentId"`
          SessionKey string `json:"sessionKey"`
      }
      if err := json.Unmarshal(req.Params, &params); err != nil {
          writeRPCError(conn, req.ID, -32602, "invalid params: "+err.Error())
          return
      }
      if params.SessionKey == "" {
          params.SessionKey = "default"
      }

      h.mu.RLock()
      reg := h.runs
      h.mu.RUnlock()
      if reg == nil {
          writeRPCError(conn, req.ID, -32000, "runs registry not configured")
          return
      }

      run := reg.GetBySession(runs.SessionScope{AgentID: params.AgentID, SessionKey: params.SessionKey})
      if run == nil {
          _ = conn.WriteJSON(map[string]any{
              "jsonrpc": "2.0",
              "id":      req.ID,
              "result":  map[string]any{"aborted": false},
          })
          return
      }
      if run.CancelFn != nil {
          run.CancelFn()
      }
      _ = run.Finish(runs.StatusCancelled, runs.ReasonUserAbort, "")
      _ = conn.WriteJSON(map[string]any{
          "jsonrpc": "2.0",
          "id":      req.ID,
          "result":  map[string]any{"aborted": true, "runId": run.ID},
      })
  }
  ```

- [ ] **Step 2: Register `chat.abort` in the dispatcher**

  Find the dispatch switch and add (or replace any existing `chat.abort` case):
  ```go
  case "chat.abort":
      h.handleChatAbort(conn, req)
  ```

- [ ] **Step 3: Build**

  ```bash
  cd ~/projects/felix && go build ./internal/gateway/...
  ```
  Expected: clean build.

- [ ] **Step 4: Commit**

  ```bash
  cd ~/projects/felix && git add internal/gateway/websocket.go
  git commit -m "feat(gateway): handleChatAbort cancels in-flight run via Registry

  Looks up the active run by (agent, session), invokes its CancelFn,
  and calls Run.Finish(cancelled, user_abort, \"\"). Subscribers see
  the terminal Done event. No-op (returns {aborted:false}) when no
  run is active."
  ```

---

### Task 3.5: Add `handleChatSubscribe`

Attaches the conn to an in-flight run's live event stream. Past events with `seq > fromSeq` are returned synchronously; live events arrive via subsequent `chat.event` notifications.

**Files:**
- Modify: `internal/gateway/websocket.go`

- [ ] **Step 1: Add the method**

  ```go
  // handleChatSubscribe attaches conn to the in-flight run for scope.
  // Returns past events (seq > fromSeq) in the RPC response; live events
  // arrive as chat.event notifications until Finish closes the channel.
  func (h *WebSocketHandler) handleChatSubscribe(conn *websocket.Conn, req JSONRPCRequest) {
      var params struct {
          AgentID    string `json:"agentId"`
          SessionKey string `json:"sessionKey"`
          FromSeq    int64  `json:"fromSeq"`
      }
      if err := json.Unmarshal(req.Params, &params); err != nil {
          writeRPCError(conn, req.ID, -32602, "invalid params: "+err.Error())
          return
      }
      if params.SessionKey == "" {
          params.SessionKey = "default"
      }

      h.mu.RLock()
      reg := h.runs
      h.mu.RUnlock()
      if reg == nil {
          writeRPCError(conn, req.ID, -32000, "runs registry not configured")
          return
      }

      run := reg.GetBySession(runs.SessionScope{AgentID: params.AgentID, SessionKey: params.SessionKey})
      if run == nil {
          _ = conn.WriteJSON(map[string]any{
              "jsonrpc": "2.0",
              "id":      req.ID,
              "result":  map[string]any{"active": false},
          })
          return
      }

      past, live, lastSeq, err := run.Subscribe(conn, params.FromSeq)
      if err != nil {
          writeRPCError(conn, req.ID, -32000, "subscribe: "+err.Error())
          return
      }

      pastJSON := make([]map[string]any, 0, len(past))
      for _, e := range past {
          pastJSON = append(pastJSON, eventToResult(e))
      }
      _ = conn.WriteJSON(map[string]any{
          "jsonrpc": "2.0",
          "id":      req.ID,
          "result": map[string]any{
              "active":  true,
              "runId":   run.ID,
              "lastSeq": lastSeq,
              "past":    pastJSON,
          },
      })

      go forwardEvents(conn, req.ID, live)
  }

  // forwardEvents drains live events to conn until the channel closes
  // (Unsubscribe, Finish, or fan-out drop).
  func forwardEvents(conn *websocket.Conn, _ any, ch <-chan runs.Event) {
      for e := range ch {
          _ = conn.WriteJSON(map[string]any{
              "jsonrpc": "2.0",
              "method":  "chat.event",
              "params":  eventToResult(e),
          })
      }
  }
  ```

- [ ] **Step 2: Register `chat.subscribe` in the dispatcher**

  ```go
  case "chat.subscribe":
      h.handleChatSubscribe(conn, req)
  ```

- [ ] **Step 3: Build**

  ```bash
  cd ~/projects/felix && go build ./internal/gateway/...
  ```
  Expected: clean build.

- [ ] **Step 4: Commit**

  ```bash
  cd ~/projects/felix && git add internal/gateway/websocket.go
  git commit -m "feat(gateway): handleChatSubscribe with gap-fill and live channel

  Subscribers get past events (seq > fromSeq) synchronously in the RPC
  response, then live events as chat.event notifications. Gap-fill is
  held under Run.mu so the boundary event (seq == lastSeqAtAttach)
  isn't delivered twice."
  ```

---

### Task 3.6: Add `handleChatReplay`

Replays events for a finished or live run from disk (not the registry). Used when the client knows the runID but doesn't need a live subscription (or wants events from before fromSeq).

**Files:**
- Modify: `internal/gateway/websocket.go`

- [ ] **Step 1: Add the method**

  ```go
  // handleChatReplay reads events with seq > fromSeq from the on-disk
  // log file for the given run. Works for both in-flight and finished
  // runs. Does not attach a live subscription — use chat.subscribe for
  // that.
  func (h *WebSocketHandler) handleChatReplay(conn *websocket.Conn, req JSONRPCRequest) {
      var params struct {
          AgentID    string `json:"agentId"`
          SessionKey string `json:"sessionKey"`
          RunID      string `json:"runId"`
          FromSeq    int64  `json:"fromSeq"`
      }
      if err := json.Unmarshal(req.Params, &params); err != nil {
          writeRPCError(conn, req.ID, -32602, "invalid params: "+err.Error())
          return
      }
      if params.SessionKey == "" {
          params.SessionKey = "default"
      }
      if params.RunID == "" {
          writeRPCError(conn, req.ID, -32602, "runId is required")
          return
      }

      h.mu.RLock()
      sessionsBase := h.sessionsBase
      h.mu.RUnlock()

      logPath := filepath.Join(sessionsBase, params.AgentID, params.SessionKey+".runs", params.RunID+".jsonl")
      past, err := runs.ReadLog(logPath, params.FromSeq)
      if err != nil {
          writeRPCError(conn, req.ID, -32000, "replay: "+err.Error())
          return
      }

      pastJSON := make([]map[string]any, 0, len(past))
      for _, e := range past {
          pastJSON = append(pastJSON, eventToResult(e))
      }
      _ = conn.WriteJSON(map[string]any{
          "jsonrpc": "2.0",
          "id":      req.ID,
          "result": map[string]any{
              "runId": params.RunID,
              "past":  pastJSON,
          },
      })
  }
  ```

- [ ] **Step 2: Register `chat.replay` in the dispatcher**

  ```go
  case "chat.replay":
      h.handleChatReplay(conn, req)
  ```

- [ ] **Step 3: Make sure `path/filepath` is imported**

  ```bash
  grep -q '"path/filepath"' ~/projects/felix/internal/gateway/websocket.go || echo "MISSING IMPORT"
  ```
  If missing, add `"path/filepath"` to the import block.

- [ ] **Step 4: Build**

  ```bash
  cd ~/projects/felix && go build ./internal/gateway/...
  ```
  Expected: clean build.

- [ ] **Step 5: Commit**

  ```bash
  cd ~/projects/felix && git add internal/gateway/websocket.go
  git commit -m "feat(gateway): handleChatReplay reads runID log from disk

  Works for both in-flight and finished runs. Caller passes runId +
  fromSeq; the response carries every event with seq > fromSeq in
  order. No live subscription is attached — use chat.subscribe for
  that flow."
  ```

---

### Task 3.7: Wire `runs.Registry` and recovery in startup

Connect the new pieces in `internal/startup/startup.go`.

**Files:**
- Modify: `internal/startup/startup.go`

- [ ] **Step 1: Add the `runs` import**

  ```go
  "github.com/sausheong/felix/internal/gateway/runs"
  ```

- [ ] **Step 2: Locate the spot where `sessionsDir` is known**

  ```bash
  cd ~/projects/felix && grep -n "sessionsDir\|sessions.*Dir\|sessions/" internal/startup/startup.go | head -10
  ```
  Note the variable name and the line where it's first assigned.

- [ ] **Step 3: Locate where `wsHandler` (or whatever Felix names it) is constructed**

  ```bash
  cd ~/projects/felix && grep -n "NewWebSocketHandler\|WebSocketHandler{" internal/startup/startup.go
  ```
  Note the line.

- [ ] **Step 4: Insert the registry wiring after `wsHandler` exists**

  After the WS handler is constructed (so its `Set*` methods exist) and `sessionsDir` is known, insert:
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

  (Adapt the variable names — `wsHandler` should match what Felix actually calls it; if the field is exposed via the result struct, set it on the right object.)

- [ ] **Step 5: Build the binary**

  ```bash
  cd ~/projects/felix && go build ./...
  ```
  Expected: clean build.

- [ ] **Step 6: Run the full test suite**

  ```bash
  cd ~/projects/felix && go test ./...
  ```
  Expected: `ok` for every package. Any failures here are real regressions from the gateway refactor — investigate and fix before committing.

- [ ] **Step 7: Commit**

  ```bash
  cd ~/projects/felix && git add internal/startup/startup.go
  git commit -m "feat(startup): construct runs.Registry, recover at boot, wire OnNewRun

  At gateway boot: build the registry rooted at sessionsDir, walk for
  interrupted runs (writes synthetic terminal events for any 'running'
  rows left over from a crashed process), install the registry on the
  WS handler, and register BroadcastNewRun as the OnNewRun callback so
  background-run notifications reach open conns viewing the same scope."
  ```

---

### Task 3.8: Smoke test the HTML chat client

This is the only manual test in the plan. It exercises the full stack end-to-end and confirms the wire-format change to `chat.send` doesn't break the existing UI.

**Files:**
- (No file changes. Manual UI exercise.)

- [ ] **Step 1: Start felix locally**

  ```bash
  cd ~/projects/felix && go run ./cmd/felix start
  ```
  Expected: gateway listens on `127.0.0.1:18789`. Watch for `runs recovery complete` log line on the first boot after this change (it'll say `recovered=0` on a fresh install, which is fine).

- [ ] **Step 2: Open the chat UI**

  Visit `http://127.0.0.1:18789/chat` (or whatever path Felix exposes) in a browser. Open the browser devtools Network → WS tab.

- [ ] **Step 3: Send a message**

  Send "hello, are you there?" through the chat UI.

  Expected in the WS frame log:
  - Outbound: `{"jsonrpc":"2.0","id":N,"method":"chat.send","params":{"agentId":"default","sessionKey":"default","text":"hello, are you there?"}}`
  - Inbound (immediately): `{"jsonrpc":"2.0","id":N,"result":{"runId":"<ULID>"}}`
  - Inbound (streaming): one or more `{"method":"chat.event","params":{...}}` notifications
  - Inbound (terminal): a `chat.event` with `payload.type` `"done"` (or whatever the wire format settles on)

  Expected in the UI: the assistant's reply appears character-by-character or chunk-by-chunk; the input clears for the next turn.

- [ ] **Step 4: Disconnect mid-stream and reconnect**

  Send a longer prompt that takes >5s to respond ("write a 500-word essay on..."). While the response is streaming, close the browser tab. Reopen the chat UI.

  Expected: the UI reconnects and silently catches up — either the chat shell already issues `chat.replay` or `chat.subscribe` on reconnect, or the reply is missing from the visible thread but the new send still works (acceptable for this wave).

- [ ] **Step 5: Force-restart the gateway mid-run**

  Send another long prompt. While it's streaming, Ctrl-C the `go run` process. Restart with `go run ./cmd/felix start`.

  Expected in the log on restart: `runs recovery complete recovered=1` (or higher). On disk:
  ```bash
  ls ~/.felix/sessions/default/default.runs/
  cat ~/.felix/sessions/default/default.runs/index.json | jq '.runs[-1] | {id, status, ended_at}'
  ```
  The last run should have `status: "interrupted"` and a non-empty `ended_at`.

- [ ] **Step 6: Send `chat.abort` while a run is in flight**

  Send a long prompt. Within 1-2 seconds, click the UI's stop button (or, if the UI has none, send a raw `chat.abort` from the browser console:
  ```js
  ws.send(JSON.stringify({jsonrpc:"2.0", id:99, method:"chat.abort", params:{agentId:"default", sessionKey:"default"}}))
  ```
  )

  Expected:
  - Response: `{"id":99,"result":{"aborted":true,"runId":"<ULID>"}}`
  - The streaming reply stops immediately.
  - The terminal `chat.event` shows `status: "cancelled"`, `reason: "user_abort"`.

- [ ] **Step 7: Commit any UI tweaks made during smoke testing**

  If Step 4 required updating the UI to issue `chat.replay` on reconnect, that's an extra commit:
  ```bash
  cd ~/projects/felix && git add <whatever-changed>
  git commit -m "fix(chat-ui): issue chat.replay on reconnect to resume in-flight run"
  ```

---

### Task 3.9: Phase 3 verification

- [ ] **Step 1: Run the full suite with race detector**

  ```bash
  cd ~/projects/felix && go test -race -count=2 ./...
  ```
  Expected: `ok` for every package.

- [ ] **Step 2: Run go vet on the whole module**

  ```bash
  cd ~/projects/felix && go vet ./...
  ```
  Expected: empty.

- [ ] **Step 3: Confirm no broken imports or unused vars in any edited file**

  ```bash
  cd ~/projects/felix && go build ./...
  ```
  Expected: clean.

- [ ] **Step 4: Confirm the new RPC methods are reachable**

  ```bash
  cd ~/projects/felix && grep -E 'case "chat\\.(send|abort|subscribe|replay)"' internal/gateway/websocket.go
  ```
  Expected: 4 lines (one per case).

Phase 3 is done. The wave is complete.

---

## Wave-complete check

- [ ] **All three phases merged into `main`**
- [ ] **Manual smoke test (Task 3.8) passes for: send, disconnect-recover, crash-recover, abort**
- [ ] **`go test -race ./...` is green**
- [ ] **`docs/superpowers/specs/2026-05-27-runs-chatexec-port-design.md` accurately describes what shipped — update if it doesn't**

---

## Rollback

If anything goes wrong post-merge:

- **Phase 3 only:** `git revert <P3-commits>` rolls back the gateway integration but keeps the `runs` and `chatexec` packages in tree as dead code. Safe — nothing else depends on them.
- **Phase 2 only:** `git revert <P2-commits>` rolls back chatexec. Phase 3 won't compile without it, so this implies reverting P3 first.
- **Phase 1 only:** `git revert <P1-commits>` rolls back the runs package. Same dependency: revert P2 and P3 first.

Disk artifacts (`<sessionsDir>/*/*.runs/`) created during Phase 3 are harmless after a revert — Felix without the runs package simply ignores those directories.
