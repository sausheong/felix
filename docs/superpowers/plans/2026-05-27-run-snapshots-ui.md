# Run Snapshots UI + Wave 1 Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface Wave 1's on-disk run history in the felix sidebar (view + delete) and discharge the four carry-forward items from Wave 1's final review.

**Architecture:** Three sequential phases. P1 adds two new JSON-RPC methods (`chat.runs`, `chat.deleteRun`) backed by a new `runs.Registry.DeleteRun` method, plus the gateway handler unit tests Wave 1 lacked. P2 wires `chatexec.OverlayMetrics` (currently dead plumbing) and tightens two documentation seams. P3 builds the sidebar chevron + sub-list UI for run history, with read-only "view a past run" mode and a confirm-then-delete affordance.

**Tech Stack:** Go 1.25.1, gorilla/websocket v1.5.3, existing felix packages (`internal/gateway`, `internal/gateway/runs`, `internal/chatexec`, `internal/llm/llmtest`). Frontend is embedded HTML+JS in `internal/gateway/chat.go`.

**Spec:** `docs/superpowers/specs/2026-05-27-run-snapshots-ui-design.md`

**Predecessor:** Wave 1 (`f7d97e0` on `main`).

---

## Pre-flight checks

- [ ] **Felix on main, clean tree**
  ```bash
  cd ~/projects/felix && git status -s && git branch --show-current
  ```
  Expected: empty status (or only untracked `.md` files from before the gitignore whitelist), branch `main`.

- [ ] **Wave 1 merged**
  ```bash
  cd ~/projects/felix && git log --oneline -1 --grep="Merge feat/runs-chatexec-port"
  ```
  Expected: shows `f7d97e0` (or whatever the merge commit SHA is) describing Wave 1.

- [ ] **All tests pass before starting**
  ```bash
  cd ~/projects/felix && go test ./...
  ```
  Expected: every package `ok`.

- [ ] **Branch for Wave 2**
  ```bash
  cd ~/projects/felix && git checkout -b feat/run-snapshots-ui
  ```

---

# Phase 1 — Backend (handlers + registry method + handler unit tests)

### Task 1.1: Add `Registry.DeleteRun`

Encapsulates the file-then-index delete order so a future change to disk layout ripples through one place.

**Files:**
- Modify: `internal/gateway/runs/registry.go` (add method at end of file)
- Modify: `internal/gateway/runs/registry_test.go` (add test cases)

- [ ] **Step 1: Read the current end of registry.go**
  ```bash
  cd ~/projects/felix && tail -30 internal/gateway/runs/registry.go
  ```
  Confirm location of `Remove` (which is similar in spirit — also deletes from maps).

- [ ] **Step 2: Add `DeleteRun` method**
  Append to `internal/gateway/runs/registry.go`:
  ```go
  // DeleteRun removes a completed run from disk: deletes the per-run
  // <runID>.jsonl log file (best-effort; failures are logged) and rewrites
  // index.json without the row (atomic via WriteFileAtomic). Returns an
  // error if the run is currently in-flight — callers must wait for or
  // cancel an active run before deleting.
  //
  // File-delete failures are non-fatal: a missing log after this returns
  // nil leaves the index entry gone, so ReadLog on the path returns
  // (nil, nil) on the next access. This is the safer order than
  // "rewrite index first, then delete file" — a crash between the two
  // would leave the index referencing a present log.
  func (reg *Registry) DeleteRun(scope SessionScope, runID string) error {
      reg.mu.Lock()
      if run, ok := reg.runs[runID]; ok && !run.Completed.Load() {
          reg.mu.Unlock()
          return fmt.Errorf("cannot delete in-flight run %s", runID)
      }
      reg.mu.Unlock()

      // File first (so a partial failure leaves the index claiming a
      // present log, which is harmless — recovery / replay tolerate the
      // mismatch).
      dir := reg.runsDir(scope)
      logPath := filepath.Join(dir, runID+".jsonl")
      if err := os.Remove(logPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
          slog.Warn("DeleteRun: log file remove failed", "logPath", logPath, "error", err)
      }

      // Then index.
      indexPath := filepath.Join(dir, "index.json")
      idx, err := loadIndex(indexPath)
      if err != nil {
          // Treat unreadable index as already-empty; nothing to rewrite.
          return nil
      }
      out := idx.Runs[:0]
      for _, r := range idx.Runs {
          if r.ID == runID {
              continue
          }
          out = append(out, r)
      }
      if len(out) == len(idx.Runs) {
          // Row wasn't in the index — nothing to do.
          return nil
      }
      idx.Runs = out
      return saveIndex(indexPath, idx)
  }
  ```

  Verify required imports are present at top of file: `errors`, `fmt`, `io/fs`, `log/slog`, `os`, `path/filepath`. If any are missing, add them.

  ```bash
  cd ~/projects/felix && grep -E '^\s*"(errors|fmt|io/fs|log/slog|os|path/filepath)"' internal/gateway/runs/registry.go
  ```

- [ ] **Step 3: Build to confirm the method compiles**
  ```bash
  cd ~/projects/felix && go build ./internal/gateway/runs/...
  ```
  Expected: clean.

- [ ] **Step 4: Add test cases to registry_test.go**

  Append (or add to an existing `TestRun_DeleteRun` test block — use whichever fits the file's existing test naming convention):
  ```go
  func TestRegistry_DeleteRun_HappyPath(t *testing.T) {
      dir := t.TempDir()
      reg := NewRegistry(dir)
      scope := SessionScope{AgentID: "a1", SessionKey: "s1"}

      // Create and Finish a run so it lands in the index as completed.
      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()
      run, err := reg.Create(scope, "run-1", cancel)
      if err != nil {
          t.Fatalf("Create: %v", err)
      }
      if _, err := run.Append(EventTypeTextDelta, []byte(`{"text":"hi"}`)); err != nil {
          t.Fatalf("Append: %v", err)
      }
      if err := run.Finish(StatusCompleted, "", ""); err != nil {
          t.Fatalf("Finish: %v", err)
      }

      // File + index entry exist.
      logPath := filepath.Join(dir, "a1", "s1.runs", "run-1.jsonl")
      if _, err := os.Stat(logPath); err != nil {
          t.Fatalf("expected log file before delete: %v", err)
      }
      snap, _ := reg.Snapshot(scope)
      if len(snap) != 1 {
          t.Fatalf("expected 1 run in index before delete, got %d", len(snap))
      }

      // Delete and verify both go away.
      if err := reg.DeleteRun(scope, "run-1"); err != nil {
          t.Fatalf("DeleteRun: %v", err)
      }
      if _, err := os.Stat(logPath); !errors.Is(err, fs.ErrNotExist) {
          t.Fatalf("expected log file deleted, stat err=%v", err)
      }
      snap2, _ := reg.Snapshot(scope)
      if len(snap2) != 0 {
          t.Fatalf("expected 0 runs in index after delete, got %d", len(snap2))
      }
  }

  func TestRegistry_DeleteRun_RefusesInFlight(t *testing.T) {
      dir := t.TempDir()
      reg := NewRegistry(dir)
      scope := SessionScope{AgentID: "a1", SessionKey: "s1"}

      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()
      _, err := reg.Create(scope, "run-1", cancel)
      if err != nil {
          t.Fatalf("Create: %v", err)
      }

      // Did NOT call Finish — run is still in-flight.
      err = reg.DeleteRun(scope, "run-1")
      if err == nil {
          t.Fatal("expected error deleting in-flight run, got nil")
      }
      if !strings.Contains(err.Error(), "in-flight") {
          t.Errorf("error should mention 'in-flight', got: %v", err)
      }
      _ = ctx // silence unused if Create's API changes
  }

  func TestRegistry_DeleteRun_UnknownIDNoError(t *testing.T) {
      dir := t.TempDir()
      reg := NewRegistry(dir)
      scope := SessionScope{AgentID: "a1", SessionKey: "s1"}

      // No runs ever created. Deleting an unknown ID is a no-op.
      if err := reg.DeleteRun(scope, "ghost"); err != nil {
          t.Fatalf("DeleteRun on empty registry: %v", err)
      }
  }

  func TestRegistry_DeleteRun_TolerantOfMissingLog(t *testing.T) {
      dir := t.TempDir()
      reg := NewRegistry(dir)
      scope := SessionScope{AgentID: "a1", SessionKey: "s1"}

      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()
      run, _ := reg.Create(scope, "run-1", cancel)
      _ = run.Finish(StatusCompleted, "", "")

      // Pre-delete the log file manually so DeleteRun has nothing to rm.
      logPath := filepath.Join(dir, "a1", "s1.runs", "run-1.jsonl")
      _ = os.Remove(logPath)

      // Should still succeed and clear the index row.
      if err := reg.DeleteRun(scope, "run-1"); err != nil {
          t.Fatalf("DeleteRun with missing log: %v", err)
      }
      snap, _ := reg.Snapshot(scope)
      if len(snap) != 0 {
          t.Fatalf("expected 0 runs after delete-with-missing-log, got %d", len(snap))
      }
  }
  ```

  Confirm required test imports: `errors`, `io/fs`, `os`, `path/filepath`, `strings`, `testing`, plus `context`. Add any that aren't there.

- [ ] **Step 5: Run tests**
  ```bash
  cd ~/projects/felix && go test ./internal/gateway/runs -run TestRegistry_DeleteRun -v
  ```
  Expected: 4 PASS.

- [ ] **Step 6: Full runs package suite + race**
  ```bash
  cd ~/projects/felix && go test -race -count=2 ./internal/gateway/runs/...
  ```
  Expected: ok.

- [ ] **Step 7: Commit**
  ```bash
  cd ~/projects/felix && git add internal/gateway/runs/registry.go internal/gateway/runs/registry_test.go
  git commit -m "feat(runs): add Registry.DeleteRun (refuses in-flight, file-then-index order)

  Removes a completed run from disk: deletes <runID>.jsonl best-effort,
  then atomically rewrites index.json without the row. File-first order
  means a crash mid-delete leaves the index claiming a present log,
  which is harmless — ReadLog tolerates missing files.

  Refuses to delete a run that's still in-flight (Run.Completed == false).
  4 test cases cover happy path, in-flight refusal, unknown ID no-op,
  and pre-deleted-log tolerance."
  ```

---

### Task 1.2: Add `handleChatRuns` + dispatcher registration

**Files:**
- Modify: `internal/gateway/websocket.go`

- [ ] **Step 1: Locate the dispatch switch and other chat handlers**
  ```bash
  cd ~/projects/felix && grep -n 'case "chat\.\|func (h \*WebSocketHandler) handleChat' internal/gateway/websocket.go
  ```
  Note the line of the dispatch switch (look for the block with `case "chat.send":`) and where the existing chat.* handlers live.

- [ ] **Step 2: Add `handleChatRuns` method**

  Place it near `handleChatReplay` (added in Wave 1). Use the same RLock-snapshot pattern as the other chat.* handlers.

  ```go
  // handleChatRuns returns the past run summaries for a session, sorted
  // newest-first. Reads from the on-disk index.json via Registry.Snapshot.
  // No live subscription is attached — frontends typically follow this
  // with chat.replay to view a specific run.
  func (h *WebSocketHandler) handleChatRuns(conn *websocket.Conn, req JSONRPCRequest) {
      var params struct {
          AgentID    string `json:"agentId"`
          SessionKey string `json:"sessionKey"`
      }
      if err := json.Unmarshal(req.Params, &params); err != nil {
          writeRPCError(conn, h.metrics, req.ID, -32602, "invalid params: "+err.Error())
          return
      }
      if params.AgentID == "" {
          params.AgentID = "default"
      }
      if params.SessionKey == "" {
          h.mu.RLock()
          if m, ok := h.activeSessionKeys[conn]; ok {
              params.SessionKey = m[params.AgentID]
          }
          h.mu.RUnlock()
          if params.SessionKey == "" {
              params.SessionKey = "ws_default"
          }
      }

      h.mu.RLock()
      reg := h.runs
      metrics := h.metrics
      h.mu.RUnlock()
      if reg == nil {
          writeRPCError(conn, metrics, req.ID, -32000, "runs registry not configured")
          return
      }

      summaries, err := reg.Snapshot(runs.SessionScope{AgentID: params.AgentID, SessionKey: params.SessionKey})
      if err != nil {
          writeRPCError(conn, metrics, req.ID, -32000, "runs snapshot: "+err.Error())
          return
      }

      // Newest first — index.json is append-order, oldest first.
      sort.Slice(summaries, func(i, j int) bool {
          return summaries[i].StartedAt > summaries[j].StartedAt
      })

      writeJSON(conn, JSONRPCResponse{
          JSONRPC: "2.0",
          Result:  map[string]any{"runs": summaries},
          ID:      req.ID,
      })
  }
  ```

  Confirm `sort` is imported at the top of the file. If not, add it.

  ```bash
  cd ~/projects/felix && grep '"sort"' internal/gateway/websocket.go
  ```
  If empty, add `"sort"` to the import block.

- [ ] **Step 3: Register `chat.runs` in the dispatcher**

  Find the `case "chat.replay":` line and add right after it:
  ```go
  case "chat.runs":
      h.handleChatRuns(conn, req)
  ```

- [ ] **Step 4: Build**
  ```bash
  cd ~/projects/felix && go build ./internal/gateway/...
  ```
  Expected: clean.

- [ ] **Step 5: Commit**
  ```bash
  cd ~/projects/felix && git add internal/gateway/websocket.go
  git commit -m "feat(gateway): handleChatRuns lists past run summaries for a session

  New JSON-RPC method chat.runs. Returns Registry.Snapshot for the
  given (agentId, sessionKey) sorted newest-first. Same session-key
  fallback chain as the other chat.* handlers (explicit →
  activeSessionKeys[conn][agentId] → 'ws_default').

  Unit tests land in the dedicated handler test file in the next
  commit."
  ```

---

### Task 1.3: Add `handleChatDeleteRun` + dispatcher registration

**Files:**
- Modify: `internal/gateway/websocket.go`

- [ ] **Step 1: Add the handler**

  Right after `handleChatRuns`:
  ```go
  // handleChatDeleteRun removes a completed run from disk. Refuses to
  // delete an in-flight run (the registry enforces this).
  func (h *WebSocketHandler) handleChatDeleteRun(conn *websocket.Conn, req JSONRPCRequest) {
      var params struct {
          AgentID    string `json:"agentId"`
          SessionKey string `json:"sessionKey"`
          RunID      string `json:"runId"`
      }
      if err := json.Unmarshal(req.Params, &params); err != nil {
          writeRPCError(conn, h.metrics, req.ID, -32602, "invalid params: "+err.Error())
          return
      }
      if params.RunID == "" {
          writeRPCError(conn, h.metrics, req.ID, -32602, "runId is required")
          return
      }
      if params.AgentID == "" {
          params.AgentID = "default"
      }
      if params.SessionKey == "" {
          h.mu.RLock()
          if m, ok := h.activeSessionKeys[conn]; ok {
              params.SessionKey = m[params.AgentID]
          }
          h.mu.RUnlock()
          if params.SessionKey == "" {
              params.SessionKey = "ws_default"
          }
      }

      h.mu.RLock()
      reg := h.runs
      metrics := h.metrics
      h.mu.RUnlock()
      if reg == nil {
          writeRPCError(conn, metrics, req.ID, -32000, "runs registry not configured")
          return
      }

      if err := reg.DeleteRun(runs.SessionScope{AgentID: params.AgentID, SessionKey: params.SessionKey}, params.RunID); err != nil {
          writeRPCError(conn, metrics, req.ID, -32000, err.Error())
          return
      }
      writeJSON(conn, JSONRPCResponse{
          JSONRPC: "2.0",
          Result:  map[string]any{"deleted": true},
          ID:      req.ID,
      })
  }
  ```

- [ ] **Step 2: Register `chat.deleteRun` in the dispatcher**

  Right after the `case "chat.runs":` block:
  ```go
  case "chat.deleteRun":
      h.handleChatDeleteRun(conn, req)
  ```

- [ ] **Step 3: Build**
  ```bash
  cd ~/projects/felix && go build ./internal/gateway/...
  ```
  Expected: clean.

- [ ] **Step 4: Commit**
  ```bash
  cd ~/projects/felix && git add internal/gateway/websocket.go
  git commit -m "feat(gateway): handleChatDeleteRun removes a completed run via Registry

  New JSON-RPC method chat.deleteRun. Validates runId is non-empty,
  resolves scope through the standard fallback chain, delegates to
  Registry.DeleteRun which refuses in-flight runs and orders
  file-delete before index rewrite.

  Reply: {deleted: true} on success, RPC error -32000 on registry
  rejection (including 'in-flight' refusal), -32602 on missing runId."
  ```

---

### Task 1.4: Handler unit tests (backfill Wave 1 + cover new methods)

**Files:**
- Create: `internal/gateway/websocket_chat_test.go`

This is the largest task in P1. Six handler tests for chat.send/abort/subscribe/replay (backfill from Wave 1) plus four for the new chat.runs/chat.deleteRun. Goal: every chat.* handler has at least one test that exercises its happy path AND the smoke-test-found abort fallback regression.

- [ ] **Step 1: Look for existing handler test fixtures to reuse**

  ```bash
  cd ~/projects/felix && ls internal/gateway/*_test.go && grep -l "WebSocketHandler\|NewWebSocketHandler\|fake.*Conn\|recordSubscriber" internal/gateway/*_test.go
  ```
  Read `internal/gateway/server_test.go` for the existing pattern that constructs `*WebSocketHandler` with a tempdir for sessions and a faked `*websocket.Conn`. Reuse that style.

  Also read `internal/llm/llmtest/scripted.go` for the test LLM provider added in Wave 1's Task 2.3.

- [ ] **Step 2: Create the test file with a small test helper for building a fully-wired handler**

  Header + helper:
  ```go
  package gateway

  import (
      "context"
      "encoding/json"
      "net/http"
      "net/http/httptest"
      "strings"
      "sync"
      "testing"
      "time"

      "github.com/gorilla/websocket"

      "github.com/sausheong/felix/internal/config"
      "github.com/sausheong/felix/internal/gateway/runs"
      "github.com/sausheong/felix/internal/llm"
      "github.com/sausheong/felix/internal/llm/llmtest"
      "github.com/sausheong/felix/internal/session"
      "github.com/sausheong/felix/internal/tools"
  )

  // testHandler builds a WebSocketHandler with the minimum wiring needed
  // for chat.* handler tests. Backed by a tempdir for sessions, a scripted
  // LLM provider, the real runs.Registry, and an empty tool registry.
  // Returns the handler, the registry, and the sessionsDir.
  func testHandler(t *testing.T, scripted ...string) (*WebSocketHandler, *runs.Registry, string) {
      t.Helper()
      sessionsDir := t.TempDir()
      sessionStore := session.NewStore(sessionsDir)

      provider := llmtest.NewScriptedProvider(scripted...)
      providers := map[string]llm.LLMProvider{"local": provider}

      cfg := &config.Config{
          Agents: config.AgentsConfig{
              List: []config.AgentConfig{
                  {ID: "default", Name: "Test", Model: "local/test-model"},
              },
          },
      }

      toolReg := tools.NewRegistry()
      h := NewWebSocketHandler(providers, toolReg, sessionStore, cfg, sessionsDir)

      reg := runs.NewRegistry(sessionsDir)
      h.SetRunsRegistry(reg)
      h.SetServerCtx(context.Background())
      reg.OnNewRun = h.BroadcastNewRun

      return h, reg, sessionsDir
  }

  // wsPair returns a connected client/server *websocket.Conn pair backed
  // by a real httptest server. The server-side handler accepts the upgrade
  // and registers conn into activeSessionKeys for the test agent so the
  // session-key fallback chain works.
  //
  // Caller closes both conns via t.Cleanup.
  func wsPair(t *testing.T, h *WebSocketHandler) (*websocket.Conn, *websocket.Conn) {
      t.Helper()
      var serverConn *websocket.Conn
      var mu sync.Mutex
      ready := make(chan struct{})

      upgrader := websocket.Upgrader{
          CheckOrigin: func(r *http.Request) bool { return true },
      }
      srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          c, err := upgrader.Upgrade(w, r, nil)
          if err != nil {
              t.Errorf("upgrade: %v", err)
              return
          }
          mu.Lock()
          serverConn = c
          mu.Unlock()
          close(ready)
          // Keep the conn open until the test is done.
          select {
          case <-r.Context().Done():
          }
      }))
      t.Cleanup(srv.Close)

      url := "ws" + strings.TrimPrefix(srv.URL, "http")
      clientConn, _, err := websocket.DefaultDialer.Dial(url, nil)
      if err != nil {
          t.Fatalf("dial: %v", err)
      }
      t.Cleanup(func() { _ = clientConn.Close() })

      <-ready
      mu.Lock()
      sc := serverConn
      mu.Unlock()
      t.Cleanup(func() { _ = sc.Close() })

      return clientConn, sc
  }

  // readJSON reads one JSON-RPC envelope from conn with a short timeout.
  // Returns parsed map for flexibility; tests assert on individual keys.
  func readJSON(t *testing.T, conn *websocket.Conn) map[string]any {
      t.Helper()
      _ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
      _, data, err := conn.ReadMessage()
      if err != nil {
          t.Fatalf("ReadMessage: %v", err)
      }
      var m map[string]any
      if err := json.Unmarshal(data, &m); err != nil {
          t.Fatalf("Unmarshal %q: %v", data, err)
      }
      return m
  }

  // makeReq builds a JSONRPCRequest with the given method, params, and id.
  func makeReq(t *testing.T, method string, params any, id any) JSONRPCRequest {
      t.Helper()
      raw, err := json.Marshal(params)
      if err != nil {
          t.Fatalf("marshal params: %v", err)
      }
      return JSONRPCRequest{JSONRPC: "2.0", Method: method, Params: raw, ID: id}
  }
  ```

  Note: `llmtest.NewScriptedProvider` accepts variadic strings as the scripted responses (one per turn). For tests that don't exercise the LLM, pass an empty slice.

- [ ] **Step 3: Test `handleChatAbort` no-run-active path**

  Append:
  ```go
  func TestHandleChatAbort_NoActiveRun(t *testing.T) {
      h, _, _ := testHandler(t)
      _, serverConn := wsPair(t, h)

      h.handleChatAbort(serverConn, makeReq(t, "chat.abort",
          map[string]string{"agentId": "default", "sessionKey": "ws_default"}, "1"))

      resp := readJSON(t, serverConn)
      result, _ := resp["result"].(map[string]any)
      if got, _ := result["aborted"].(bool); got {
          t.Errorf("aborted=true, want false (no active run)")
      }
  }
  ```

- [ ] **Step 4: Test `handleChatAbort` fallback via activeSessionKeys**

  This is the regression guard for the bug smoke test caught in Wave 1.
  ```go
  func TestHandleChatAbort_FallbackResolvesViaActiveSessionKeys(t *testing.T) {
      h, reg, _ := testHandler(t)
      _, serverConn := wsPair(t, h)

      // Register a run for an agent that is NOT 'default'.
      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()
      run, err := reg.Create(runs.SessionScope{AgentID: "Other", SessionKey: "ws_default"}, "run-1", cancel)
      if err != nil {
          t.Fatalf("Create: %v", err)
      }
      defer run.Finish(runs.StatusCompleted, "", "")

      // Populate activeSessionKeys[conn] so the fallback can find the scope
      // even though chat.abort comes with empty params.
      h.mu.Lock()
      if h.activeSessionKeys[serverConn] == nil {
          h.activeSessionKeys[serverConn] = map[string]string{}
      }
      h.activeSessionKeys[serverConn]["Other"] = "ws_default"
      h.mu.Unlock()

      // Send chat.abort with empty params (mimics the buggy old client).
      h.handleChatAbort(serverConn, makeReq(t, "chat.abort", map[string]string{}, "abort"))

      resp := readJSON(t, serverConn)
      result, _ := resp["result"].(map[string]any)
      if got, _ := result["aborted"].(bool); !got {
          t.Errorf("aborted=false, want true (fallback should find the Other-agent run); resp=%v", resp)
      }
  }
  ```

- [ ] **Step 5: Test `handleChatRuns` happy path + sorting**
  ```go
  func TestHandleChatRuns_ListsNewestFirst(t *testing.T) {
      h, reg, _ := testHandler(t)
      _, serverConn := wsPair(t, h)
      scope := runs.SessionScope{AgentID: "default", SessionKey: "ws_default"}

      // Create three completed runs with deliberate timestamps spread.
      _, cancel := context.WithCancel(context.Background())
      defer cancel()
      for _, id := range []string{"r-old", "r-mid", "r-new"} {
          run, err := reg.Create(scope, id, cancel)
          if err != nil {
              t.Fatalf("Create %s: %v", id, err)
          }
          _ = run.Finish(runs.StatusCompleted, "", "")
          time.Sleep(5 * time.Millisecond) // distinct StartedAt
      }

      h.handleChatRuns(serverConn, makeReq(t, "chat.runs",
          map[string]string{"agentId": "default", "sessionKey": "ws_default"}, "lr"))

      resp := readJSON(t, serverConn)
      result, _ := resp["result"].(map[string]any)
      arr, ok := result["runs"].([]any)
      if !ok {
          t.Fatalf("runs not an array: %v", resp)
      }
      if len(arr) != 3 {
          t.Fatalf("want 3 runs, got %d", len(arr))
      }
      // First should be r-new (newest).
      firstID, _ := arr[0].(map[string]any)["id"].(string)
      if firstID != "r-new" {
          t.Errorf("first run id=%q, want r-new (newest first sort)", firstID)
      }
  }
  ```

- [ ] **Step 6: Test `handleChatDeleteRun` happy path**
  ```go
  func TestHandleChatDeleteRun_HappyPath(t *testing.T) {
      h, reg, _ := testHandler(t)
      _, serverConn := wsPair(t, h)
      scope := runs.SessionScope{AgentID: "default", SessionKey: "ws_default"}

      _, cancel := context.WithCancel(context.Background())
      defer cancel()
      run, _ := reg.Create(scope, "delme", cancel)
      _ = run.Finish(runs.StatusCompleted, "", "")

      h.handleChatDeleteRun(serverConn, makeReq(t, "chat.deleteRun",
          map[string]string{"agentId": "default", "sessionKey": "ws_default", "runId": "delme"}, "dr"))

      resp := readJSON(t, serverConn)
      result, _ := resp["result"].(map[string]any)
      if del, _ := result["deleted"].(bool); !del {
          t.Errorf("deleted=false, want true; resp=%v", resp)
      }

      // Confirm it's gone from the snapshot.
      snap, _ := reg.Snapshot(scope)
      if len(snap) != 0 {
          t.Errorf("snapshot still has %d run(s) after delete", len(snap))
      }
  }
  ```

- [ ] **Step 7: Test `handleChatDeleteRun` rejects missing runId**
  ```go
  func TestHandleChatDeleteRun_RequiresRunID(t *testing.T) {
      h, _, _ := testHandler(t)
      _, serverConn := wsPair(t, h)

      h.handleChatDeleteRun(serverConn, makeReq(t, "chat.deleteRun",
          map[string]string{"agentId": "default", "sessionKey": "ws_default"}, "dr"))

      resp := readJSON(t, serverConn)
      e, _ := resp["error"].(map[string]any)
      if e == nil {
          t.Fatalf("want error response, got %v", resp)
      }
      msg, _ := e["message"].(string)
      if !strings.Contains(msg, "runId") {
          t.Errorf("error message should mention runId, got: %s", msg)
      }
  }
  ```

- [ ] **Step 8: Test `handleChatReplay` returns past array for a known run**
  ```go
  func TestHandleChatReplay_ReturnsPastEvents(t *testing.T) {
      h, reg, _ := testHandler(t)
      _, serverConn := wsPair(t, h)
      scope := runs.SessionScope{AgentID: "default", SessionKey: "ws_default"}

      _, cancel := context.WithCancel(context.Background())
      defer cancel()
      run, _ := reg.Create(scope, "replay-me", cancel)
      _, _ = run.Append(runs.EventTypeTextDelta, []byte(`{"text":"hello"}`))
      _ = run.Finish(runs.StatusCompleted, "", "")

      h.handleChatReplay(serverConn, makeReq(t, "chat.replay",
          map[string]any{"agentId": "default", "sessionKey": "ws_default", "runId": "replay-me", "fromSeq": 0}, "rp"))

      resp := readJSON(t, serverConn)
      result, _ := resp["result"].(map[string]any)
      past, ok := result["past"].([]any)
      if !ok {
          t.Fatalf("past not an array: %v", resp)
      }
      if len(past) < 1 {
          t.Errorf("want at least 1 past event, got %d", len(past))
      }
  }
  ```

- [ ] **Step 9: Test `handleChatReplay` on unknown run returns empty past**
  ```go
  func TestHandleChatReplay_UnknownRunReturnsEmpty(t *testing.T) {
      h, _, _ := testHandler(t)
      _, serverConn := wsPair(t, h)

      h.handleChatReplay(serverConn, makeReq(t, "chat.replay",
          map[string]any{"agentId": "default", "sessionKey": "ws_default", "runId": "no-such-run", "fromSeq": 0}, "rp"))

      resp := readJSON(t, serverConn)
      if _, isErr := resp["error"]; isErr {
          t.Errorf("expected no error for unknown runId, got: %v", resp)
      }
      result, _ := resp["result"].(map[string]any)
      past, _ := result["past"].([]any)
      if len(past) != 0 {
          t.Errorf("want empty past for unknown run, got %d events", len(past))
      }
  }
  ```

- [ ] **Step 10: Test `handleChatSubscribe` no-active-run path**
  ```go
  func TestHandleChatSubscribe_NoActiveRun(t *testing.T) {
      h, _, _ := testHandler(t)
      _, serverConn := wsPair(t, h)

      h.handleChatSubscribe(serverConn, makeReq(t, "chat.subscribe",
          map[string]any{"agentId": "default", "sessionKey": "ws_default", "fromSeq": 0}, "sub"))

      resp := readJSON(t, serverConn)
      result, _ := resp["result"].(map[string]any)
      if active, _ := result["active"].(bool); active {
          t.Errorf("active=true, want false (no in-flight run); resp=%v", resp)
      }
  }
  ```

- [ ] **Step 10b: Test `handleChatSend` happy path**

  Uses the scripted LLM provider to confirm chat.send wiring round-trips through chatexec.RunTurn and produces a `run_attached` response.

  ```go
  func TestHandleChatSend_RunAttachedThenDone(t *testing.T) {
      // Scripted response: one assistant turn that says "ok".
      h, _, _ := testHandler(t, "ok")
      _, serverConn := wsPair(t, h)

      h.handleChatSend(serverConn, makeReq(t, "chat.send",
          map[string]string{"agentId": "default", "sessionKey": "ws_default", "text": "hello"},
          1))

      // First inbound: the run_attached Result on rpcID=1.
      resp := readJSON(t, serverConn)
      if got, _ := resp["id"].(float64); int(got) != 1 {
          t.Errorf("first response id=%v, want 1", resp["id"])
      }
      result, _ := resp["result"].(map[string]any)
      if typ, _ := result["type"].(string); typ != "run_attached" {
          t.Errorf("first response type=%q, want 'run_attached'; resp=%v", typ, resp)
      }
      if runID, _ := result["runID"].(string); runID == "" {
          t.Errorf("first response missing runID; resp=%v", resp)
      }

      // Drain a few more frames until we see the terminal done event
      // (skip the trace events in between).
      for i := 0; i < 30; i++ {
          msg := readJSON(t, serverConn)
          r, _ := msg["result"].(map[string]any)
          if typ, _ := r["type"].(string); typ == "done" {
              return // happy path
          }
      }
      t.Fatal("did not see done event within 30 frames")
  }
  ```

  Note: if `llmtest.NewScriptedProvider` accepts the scripted assistant turns differently (e.g. as a `[]string` slice or as `Response` structs), adapt the `testHandler` call. Read the constructor signature first if the test fails to compile.

- [ ] **Step 11: All chat.* handlers reject when registry is nil**
  ```go
  func TestChatHandlers_RejectNilRegistry(t *testing.T) {
      h, _, _ := testHandler(t)
      _, serverConn := wsPair(t, h)

      // Clear the registry.
      h.mu.Lock()
      h.runs = nil
      h.mu.Unlock()

      handlers := []struct {
          name string
          fn   func(*websocket.Conn, JSONRPCRequest)
          req  JSONRPCRequest
      }{
          {"chat.abort", h.handleChatAbort, makeReq(t, "chat.abort", map[string]string{}, "a")},
          {"chat.subscribe", h.handleChatSubscribe, makeReq(t, "chat.subscribe", map[string]any{"fromSeq": 0}, "b")},
          {"chat.runs", h.handleChatRuns, makeReq(t, "chat.runs", map[string]string{}, "c")},
          {"chat.deleteRun", h.handleChatDeleteRun, makeReq(t, "chat.deleteRun", map[string]string{"runId": "x"}, "d")},
      }
      for _, tc := range handlers {
          t.Run(tc.name, func(t *testing.T) {
              tc.fn(serverConn, tc.req)
              resp := readJSON(t, serverConn)
              if _, isErr := resp["error"]; !isErr {
                  t.Errorf("%s with nil registry should error, got: %v", tc.name, resp)
              }
          })
      }
  }
  ```

- [ ] **Step 12: Run the full new test file**
  ```bash
  cd ~/projects/felix && go test ./internal/gateway -run "TestHandleChat|TestChatHandlers" -v
  ```
  Expected: all PASS. If `llmtest.NewScriptedProvider`'s arg shape differs from variadic strings, adjust the helper (read `~/projects/felix/internal/llm/llmtest/scripted.go`).

- [ ] **Step 13: Full gateway suite + race**
  ```bash
  cd ~/projects/felix && go test -race -count=2 ./internal/gateway/...
  ```
  Expected: ok.

- [ ] **Step 14: Commit**
  ```bash
  cd ~/projects/felix && git add internal/gateway/websocket_chat_test.go
  git commit -m "test(gateway): unit tests for chat.* handlers

  Backfills Wave 1's missing handler coverage AND covers the new
  chat.runs / chat.deleteRun methods. 9 tests across 8 functions:

  - handleChatAbort: no-active-run, fallback-via-activeSessionKeys
    (regression guard for the smoke-test-found bug)
  - handleChatSubscribe: no-active-run
  - handleChatReplay: returns-past, unknown-run-empty
  - handleChatRuns: newest-first sort
  - handleChatDeleteRun: happy path, missing-runId rejection
  - all chat.* handlers reject when h.runs is nil

  testHandler helper builds a fully-wired WebSocketHandler with a
  tempdir + scripted provider. wsPair gives a real httptest WS conn
  pair so writeJSON/conn writes round-trip through real serialisation."
  ```

---

### Task 1.5: Phase 1 verification

- [ ] **Step 1: vet + build + full felix suite + race**
  ```bash
  cd ~/projects/felix && go vet ./... && go build ./... && go test -race -count=2 ./...
  ```
  Expected: all clean.

- [ ] **Step 2: Confirm dispatcher has 7 chat.* methods (5 from Wave 1 + 2 new)**
  ```bash
  cd ~/projects/felix && grep -E 'case "chat\.(send|abort|subscribe|replay|compact|runs|deleteRun)"' internal/gateway/websocket.go
  ```
  Expected: 7 lines (one per case).

Phase 1 is done. Backend is complete and tested.

---

# Phase 2 — Wave 1 polish (OverlayMetrics wiring + docs)

### Task 2.1: Unify `MetricsLike` and `OverlayMetrics`, wire into chatexec.RunTurn

The audit in spec §Components found that `chatexec.MetricsLike` (`IncChatTurns()`) and `chatexec.OverlayMetrics` (`IncToolCalls(name)`) are distinct interfaces. `*gateway.Metrics` implements both, but `chatexec.RunTurn` constructs the overlay without populating its `Metrics` field. Result: chat-path tool calls do NOT increment `felix_tool_calls_total`.

Fix: unify the two interfaces and wire `deps.Metrics` through to the overlay.

**Files:**
- Modify: `internal/chatexec/overlay.go`
- Modify: `internal/chatexec/chatexec.go`

- [ ] **Step 1: Read both interface definitions to confirm the shape**
  ```bash
  cd ~/projects/felix && grep -B 1 -A 4 "type MetricsLike\|type OverlayMetrics" internal/chatexec/*.go
  ```
  Expected: two interface blocks, one method each. Both implemented by `*gateway.Metrics`.

- [ ] **Step 2: Edit `internal/chatexec/overlay.go` — drop `OverlayMetrics` type alias**

  Change the field on `ChatToolOverlay` from `Metrics OverlayMetrics` to `Metrics MetricsLike`, and DELETE the `OverlayMetrics` interface declaration entirely. The `Execute` method's nil check `if e.Metrics != nil { e.Metrics.IncToolCalls(name) }` stays — the new `MetricsLike` interface has `IncToolCalls(name string)` so the call still compiles.

  Specifically:
  - Delete the type declaration and its docstring:
    ```go
    // OverlayMetrics is the minimal metrics surface ChatToolOverlay uses.
    // Backed by gateway.Metrics in production; tests / inbox worker pass
    // nil and the overlay skips the counter bump.
    type OverlayMetrics interface {
        IncToolCalls(toolName string)
    }
    ```
  - Change the struct field type from `OverlayMetrics` to `MetricsLike`.

- [ ] **Step 3: Edit `internal/chatexec/chatexec.go` — extend `MetricsLike` with `IncToolCalls`**

  Find `type MetricsLike interface` and change the body from:
  ```go
  type MetricsLike interface {
      IncChatTurns()
  }
  ```
  to:
  ```go
  type MetricsLike interface {
      IncChatTurns()
      IncToolCalls(toolName string)
  }
  ```

  Update the docstring to reflect both methods. Something like:
  > // MetricsLike is the minimal metrics surface chatexec uses for both
  > // per-turn counters (IncChatTurns) and per-tool-call counters
  > // (IncToolCalls, called from ChatToolOverlay.Execute). Backed by
  > // gateway.Metrics in production; tests pass nil and chatexec/overlay
  > // skip the counter bumps.

- [ ] **Step 4: Wire `deps.Metrics` into the overlay construction**

  Find this line in `internal/chatexec/chatexec.go`:
  ```go
  overlay := &ChatToolOverlay{Base: deps.Tools}
  ```
  Change to:
  ```go
  overlay := &ChatToolOverlay{Base: deps.Tools, Metrics: deps.Metrics}
  ```

- [ ] **Step 5: Build the chatexec package**
  ```bash
  cd ~/projects/felix && go build ./internal/chatexec/...
  ```
  Expected: clean. If the build complains that `*gateway.Metrics` doesn't satisfy `MetricsLike`, double-check the metrics file in step 1 of Task 1.4 — `IncToolCalls` should already exist there from Wave 1 (verified earlier; signature is `func (m *Metrics) IncToolCalls(toolName string)`).

- [ ] **Step 6: Build the whole module**
  ```bash
  cd ~/projects/felix && go build ./...
  ```
  Expected: clean. The handler test helper from Task 1.4 imports things that may now have changed signatures — confirm the file still compiles.

- [ ] **Step 7: Run chatexec tests including the existing overlay tests**
  ```bash
  cd ~/projects/felix && go test ./internal/chatexec/... -v
  ```
  Expected: all PASS. The `OverlayMetrics` interface rename should not break any tests because the test file uses a `countingMetrics`-style fake that satisfies the new shape (it just needs both methods; if it only has `IncToolCalls`, add `IncChatTurns()` as a noop method on the test fake).

  If a test fails because the fake metric struct only implements `IncToolCalls`, add a no-op `IncChatTurns()` method to the fake.

- [ ] **Step 8: Commit**
  ```bash
  cd ~/projects/felix && git add internal/chatexec/overlay.go internal/chatexec/chatexec.go
  git commit -m "fix(chatexec): wire OverlayMetrics so chat-path tool calls count

  Wave 1 audit found ChatToolOverlay.Metrics was typed as a separate
  OverlayMetrics interface, and chatexec.RunTurn constructed the
  overlay without populating the field. Result: tool calls dispatched
  via the chat path silently skipped felix_tool_calls_total.

  Fix: drop the separate OverlayMetrics interface, extend
  MetricsLike with IncToolCalls(toolName string), and pass
  deps.Metrics into the overlay constructor. *gateway.Metrics
  already implements both methods (Wave 1 added IncChatTurns; tool
  calls have always been there)."
  ```

---

### Task 2.2: Wire-format comment block on wsSubscriber / forwardEvents

**Files:**
- Modify: `internal/gateway/websocket.go`

- [ ] **Step 1: Locate `wsSubscriber` and `forwardEvents`**
  ```bash
  cd ~/projects/felix && grep -n "^type wsSubscriber\|^func forwardEvents\|^func (s \*wsSubscriber)" internal/gateway/websocket.go
  ```

- [ ] **Step 2: Insert comment block immediately above `type wsSubscriber struct`**

  Add this comment block:
  ```go
  // Wire-format note: this codebase has two paths for sending event
  // payloads to a WebSocket conn, intentionally asymmetric.
  //
  //   1. chat.send → wsSubscriber.OnEvent — writes each event as a
  //      JSONRPCResponse with Result set, ID = the original chat.send
  //      request ID. The existing felix HTML chat client treats multiple
  //      Results sharing one rpcID as a stream; do NOT change this without
  //      updating the client.
  //
  //   2. chat.subscribe → forwardEvents — writes each event as a JSON-RPC
  //      notification (method = "chat.event", no ID). Newer clients that
  //      attach to existing runs (post-disconnect, multi-tab) consume this
  //      shape.
  //
  // Same underlying runs.Event; two different envelopes. The asymmetry
  // exists for backward-compatibility with the chat.send-as-stream pattern
  // the felix HTML client was designed around before durable-runs landed.
  ```

- [ ] **Step 3: Build (sanity)**
  ```bash
  cd ~/projects/felix && go build ./internal/gateway/...
  ```

- [ ] **Step 4: Commit**
  ```bash
  cd ~/projects/felix && git add internal/gateway/websocket.go
  git commit -m "docs(gateway): explain chat.send vs chat.subscribe wire-format asymmetry

  Wave 1 final review flagged that the two event-delivery paths use
  different envelopes (Results-on-shared-rpcID vs chat.event
  notifications) without explanation. Future authors are likely to
  'unify' them and break the existing chat client.

  Block comment above wsSubscriber documents the asymmetry, why it
  exists, and the constraint."
  ```

---

### Task 2.3: Trim stale inbox-worker reference in chatexec docstring

**Files:**
- Modify: `internal/chatexec/chatexec.go`

- [ ] **Step 1: Locate the package docstring**
  ```bash
  cd ~/projects/felix && sed -n '1,15p' internal/chatexec/chatexec.go
  ```
  Expected: top of file shows the package doc mentioning "the inbox worker".

- [ ] **Step 2: Edit the docstring**

  Replace the existing package doc (lines 1-8 or so) with:
  ```go
  // Package chatexec runs a single chat turn end-to-end: derives the run
  // context, opens the session, drives the harness runtime, fans events
  // out to the runs.Registry (for durable replay) and to a live Subscriber
  // (for WebSocket clients).
  //
  // The package is consumed by the WebSocket chat handler. It is written
  // to be transport-agnostic; additional consumers (e.g. a future inbox
  // or cron-driven turn dispatcher) can plug in by implementing the
  // Subscriber interface.
  package chatexec
  ```

  (Note `package chatexec` at the end — exact placement is whatever the existing file has.)

- [ ] **Step 3: Build**
  ```bash
  cd ~/projects/felix && go build ./internal/chatexec/...
  ```

- [ ] **Step 4: Commit**
  ```bash
  cd ~/projects/felix && git add internal/chatexec/chatexec.go
  git commit -m "docs(chatexec): drop stale 'inbox worker' reference from package docstring

  Wave 1 final review noted: the docstring says the package is consumed
  by both the gateway chat handler AND the inbox worker. Felix has no
  inbox worker — that's cloudcat. Trim to reflect actual current
  consumers; leave the door open for future Subscriber-implementing
  callers."
  ```

---

### Task 2.4: Phase 2 verification

- [ ] **Step 1: vet + build + full felix suite + race**
  ```bash
  cd ~/projects/felix && go vet ./... && go build ./... && go test -race -count=2 ./...
  ```
  Expected: all clean.

Phase 2 done.

---

# Phase 3 — Frontend (sidebar chevron + sub-list + read-only mode + delete)

Felix's chat client is embedded HTML+JS at `~/projects/felix/internal/gateway/chat.go` (4264 lines). The implementer should grep targets rather than reading the whole file.

### Task 3.1: Add session-row chevron + per-session expand state + chat.runs fetch

**Files:**
- Modify: `internal/gateway/chat.go`

- [ ] **Step 1: Locate the session-row JS rendering**
  ```bash
  cd ~/projects/felix && grep -n "session-row\|sessions-list\|renderSessions\|innerHTML.*ses-name\|sessions-ul" internal/gateway/chat.go | head -20
  ```
  Read the surrounding 50-80 lines around the matches that build `.session-row` DOM elements. Note variable names: the session list array, the active session indicator, and the function that re-renders the sidebar list after a session change.

- [ ] **Step 2: Add CSS for the chevron and run sub-list**

  Find the `.session-row` CSS rules (around line 442 per the pre-flight grep) and append:
  ```css
  .session-row .ses-chevron {
      width: 14px; height: 14px; flex-shrink: 0;
      cursor: pointer; opacity: 0.5;
      transition: transform 0.15s, opacity 0.15s;
  }
  .session-row .ses-chevron:hover { opacity: 1; }
  .session-row .ses-chevron.expanded { transform: rotate(90deg); }

  .runs-sublist {
      margin: 0.25rem 0 0.5rem 1.75rem;
      padding-left: 0.5rem;
      border-left: 1px solid var(--border, #2a2a2a);
      display: none;
  }
  .runs-sublist.expanded { display: block; }
  .run-row {
      display: flex; align-items: center; gap: 0.5rem;
      padding: 0.3rem 0.4rem;
      font-size: var(--fs-xs);
      color: var(--text-muted);
      border-radius: 4px;
      cursor: pointer;
  }
  .run-row:hover { background: var(--bg-msg-asst); color: var(--text); }
  .run-row .run-time { font-variant-numeric: tabular-nums; }
  .run-row .run-status {
      padding: 0 0.4em; border-radius: 3px;
      font-size: 0.85em;
      background: var(--bg-msg-user, #333);
  }
  .run-row .run-status.completed   { background: color-mix(in oklch, var(--ok, green) 18%, transparent); color: var(--ok, green); }
  .run-row .run-status.cancelled   { background: color-mix(in oklch, var(--text-muted) 18%, transparent); color: var(--text-muted); }
  .run-row .run-status.failed      { background: color-mix(in oklch, var(--error) 18%, transparent); color: var(--error); }
  .run-row .run-status.interrupted { background: color-mix(in oklch, orange 18%, transparent); color: orange; }
  .run-row .run-status.running     { background: color-mix(in oklch, blue 18%, transparent); color: blue; }
  .run-row .run-count { margin-left: auto; }
  .run-row .run-delete {
      background: none; border: none; cursor: pointer;
      opacity: 0; padding: 0.1rem;
      color: var(--text-muted);
  }
  .run-row:hover .run-delete { opacity: 0.7; }
  .run-row .run-delete:hover { opacity: 1; color: var(--error); }
  .run-row .run-delete .icon { width: 12px; height: 12px; }
  ```

  (The `--ok`, `--error`, `--text-muted` etc. CSS variables exist in the file's theme block — confirm with `grep "--ok:\\|--error:\\|--text-muted:" internal/gateway/chat.go`. Adjust the color references if the actual variable names differ.)

- [ ] **Step 3: Add JS state and chat.runs fetcher**

  Find a sensible place in the existing JS section to add module-level state (near where `sessions` or `currentSession` is declared). Add:
  ```javascript
  // Per-session run history cache. Key = `${agentId}::${sessionKey}`.
  // Each value: { runs: [RunSummary], expanded: bool, loading: bool }.
  const runsBySession = new Map();
  function runsKey(agentId, sessionKey) { return agentId + "::" + sessionKey; }

  function fetchRuns(agentId, sessionKey) {
      const key = runsKey(agentId, sessionKey);
      const entry = runsBySession.get(key) || { runs: [], expanded: false, loading: false };
      entry.loading = true;
      runsBySession.set(key, entry);
      const id = "runs-" + key;
      ws.send(JSON.stringify({
          jsonrpc: "2.0",
          method: "chat.runs",
          params: { agentId, sessionKey },
          id
      }));
  }
  ```

- [ ] **Step 4: Wire the chat.runs response handler**

  Find the WebSocket onmessage handler (search for `ws.onmessage`, `onmessage = `, or `case "chat.send"` in the message dispatcher). Add handling for responses whose `id` starts with `runs-`:
  ```javascript
  // Inside the existing onmessage dispatch, after parsing `msg`:
  if (typeof msg.id === "string" && msg.id.startsWith("runs-")) {
      const key = msg.id.slice("runs-".length);
      const entry = runsBySession.get(key) || { runs: [], expanded: false, loading: false };
      entry.loading = false;
      if (msg.result && Array.isArray(msg.result.runs)) {
          entry.runs = msg.result.runs;
      } else if (msg.error) {
          console.error("chat.runs error:", msg.error);
      }
      runsBySession.set(key, entry);
      renderRunsSublistFor(key);
      return;
  }
  ```

  `renderRunsSublistFor` is defined in Task 3.2.

- [ ] **Step 5: Modify the session-row rendering to inject the chevron**

  Find the JS code that builds each `.session-row` DOM element (likely around the `renderSessions` or similar function — the grep above points to it). At the start of each row's innerHTML, prepend the chevron icon:
  ```javascript
  // Inside the per-session row builder, BEFORE the existing .ses-icon / .ses-name / .ses-count / .ses-clear:
  const chevHTML = '<svg class="ses-chevron" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 6l6 6-6 6"/></svg>';
  // ... rendering code ...
  // After the session row is appended, also append a placeholder sub-list ul:
  const sublistKey = runsKey(agentId, session.key);
  const sublist = document.createElement('div');
  sublist.className = 'runs-sublist';
  sublist.dataset.key = sublistKey;
  // Insert sublist immediately after the .session-row in the DOM.
  ```

  Note: the implementer must adapt the exact rendering pattern to match what felix's existing session-row builder looks like. If it uses string concatenation + innerHTML, prepend the chev HTML. If it uses `createElement`, create an SVG child node.

- [ ] **Step 6: Wire the chevron click**

  In the same session-row builder, attach a click handler:
  ```javascript
  // Click chevron → toggle expansion + fetch if needed.
  rowEl.querySelector('.ses-chevron').addEventListener('click', (e) => {
      e.stopPropagation(); // don't trigger session-switch
      const key = runsKey(agentId, session.key);
      const entry = runsBySession.get(key) || { runs: [], expanded: false, loading: false };
      entry.expanded = !entry.expanded;
      runsBySession.set(key, entry);
      // Toggle CSS class on chevron and sublist.
      rowEl.querySelector('.ses-chevron').classList.toggle('expanded', entry.expanded);
      const sublist = rowEl.parentNode.querySelector(`.runs-sublist[data-key="${CSS.escape(key)}"]`);
      if (sublist) sublist.classList.toggle('expanded', entry.expanded);
      // Lazy-load on first expand.
      if (entry.expanded && entry.runs.length === 0 && !entry.loading) {
          fetchRuns(agentId, session.key);
      } else if (entry.expanded) {
          renderRunsSublistFor(key);
      }
  });
  ```

- [ ] **Step 7: Build + run the gateway suite to confirm Go-side compiles (no Go changes; HTML is embedded as a string)**
  ```bash
  cd ~/projects/felix && go build ./internal/gateway/...
  ```

- [ ] **Step 8: Commit**
  ```bash
  cd ~/projects/felix && git add internal/gateway/chat.go
  git commit -m "feat(chat-ui): per-session chevron expands a runs sublist (chat.runs)

  Each session row in the sidebar grows a chevron; click toggles a
  sub-list of past runs for that session. First expand triggers a
  chat.runs RPC; results are cached per (agentId, sessionKey). The
  sub-list renderer lands in the next commit."
  ```

---

### Task 3.2: Render runs sub-list rows + delete affordance

**Files:**
- Modify: `internal/gateway/chat.go`

- [ ] **Step 1: Add `renderRunsSublistFor`, `formatRunRow`, and the delete confirm flow**

  ```javascript
  function renderRunsSublistFor(key) {
      const sublist = document.querySelector(`.runs-sublist[data-key="${CSS.escape(key)}"]`);
      if (!sublist) return;
      const entry = runsBySession.get(key);
      if (!entry) { sublist.innerHTML = ''; return; }
      if (entry.loading) {
          sublist.innerHTML = '<div class="run-row" style="opacity:0.5">Loading…</div>';
          return;
      }
      if (entry.runs.length === 0) {
          sublist.innerHTML = '<div class="run-row" style="opacity:0.5">No past runs</div>';
          return;
      }
      sublist.innerHTML = entry.runs.map(r => formatRunRow(key, r)).join('');
      // Attach handlers per row.
      sublist.querySelectorAll('.run-row[data-run-id]').forEach(rowEl => {
          const runId = rowEl.dataset.runId;
          rowEl.addEventListener('click', (e) => {
              if (e.target.closest('.run-delete')) return; // delete handled below
              const [agentId, sessionKey] = key.split('::');
              loadRunReadOnly(agentId, sessionKey, runId);
          });
          const delBtn = rowEl.querySelector('.run-delete');
          if (delBtn) {
              delBtn.addEventListener('click', (e) => {
                  e.stopPropagation();
                  if (!confirm("Delete this run? The conversation history stays; only the per-turn event log is removed.")) return;
                  const [agentId, sessionKey] = key.split('::');
                  deleteRun(agentId, sessionKey, runId);
              });
          }
      });
  }

  function formatRunRow(key, r) {
      const t = (r.started_at || '').slice(11, 19); // HH:MM:SS from RFC3339
      const status = (r.status || 'unknown');
      const count = (r.last_seq || 0) + ' events';
      // Hide delete for runs that match the live runId (set when chat.send fires).
      const isLive = (liveRunIdBySession.get(key) === r.id);
      const delHTML = isLive ? '' :
          '<button class="run-delete" title="Delete run"><svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/></svg></button>';
      return `<div class="run-row" data-run-id="${r.id}">`
          + `<span class="run-time">${t}</span>`
          + `<span class="run-status ${status}">${status}</span>`
          + `<span class="run-count">${count}</span>`
          + delHTML
          + `</div>`;
  }

  // Tracks the in-flight runId per (agentId, sessionKey) so the delete
  // button hides for the active run. Populated in the chat.send/run_attached
  // response handler.
  const liveRunIdBySession = new Map();
  ```

- [ ] **Step 2: Hook live-run tracking into the existing chat.send/run_attached handler**

  Find where the chat client handles the `{type: "run_attached", runID}` response from chat.send (search for `run_attached` in the file). Inside that handler, add:
  ```javascript
  // Track the live runId so we can hide the delete button next to it
  // in the runs sublist. Cleared in the done-event handler.
  const liveKey = runsKey(currentAgentId(), currentSessionKey());
  liveRunIdBySession.set(liveKey, msg.result.runID);
  ```

  And find where the `done` event is handled (search for `"type":"done"` or similar). Add:
  ```javascript
  liveRunIdBySession.delete(liveKey); // clear so the row's delete button reappears
  ```

  Use whatever `currentAgentId()` / `currentSessionKey()` helpers the file already has — they're likely named something like `agentSelect.value` and `sessionSelect.value`. If no helper exists, inline the lookups.

- [ ] **Step 3: Implement `deleteRun`**

  ```javascript
  function deleteRun(agentId, sessionKey, runId) {
      const id = "del-" + runId;
      ws.send(JSON.stringify({
          jsonrpc: "2.0",
          method: "chat.deleteRun",
          params: { agentId, sessionKey, runId },
          id
      }));
  }
  ```

  And in the onmessage dispatch (same place as chat.runs handling in Task 3.1):
  ```javascript
  if (typeof msg.id === "string" && msg.id.startsWith("del-")) {
      const runId = msg.id.slice("del-".length);
      if (msg.error) {
          alert("Delete failed: " + (msg.error.message || "unknown"));
          return;
      }
      // Remove the row from all cached sublists that contain this runId.
      for (const [key, entry] of runsBySession.entries()) {
          const idx = entry.runs.findIndex(r => r.id === runId);
          if (idx >= 0) {
              entry.runs.splice(idx, 1);
              renderRunsSublistFor(key);
          }
      }
      return;
  }
  ```

- [ ] **Step 4: Build (confirms HTML string still compiles in Go)**
  ```bash
  cd ~/projects/felix && go build ./internal/gateway/...
  ```

- [ ] **Step 5: Commit**
  ```bash
  cd ~/projects/felix && git add internal/gateway/chat.go
  git commit -m "feat(chat-ui): render runs sublist with timestamp/status/count + delete

  Each run row shows HH:MM:SS, color-coded status badge, event count,
  and a delete button (hidden for the currently-in-flight run).
  Delete fires chat.deleteRun, optimistically removes the row on
  success, alerts on error.

  Click-on-row → loadRunReadOnly lands in the next commit."
  ```

---

### Task 3.3: Read-only view mode (chat.replay) + "Back to live" banner

**Files:**
- Modify: `internal/gateway/chat.go`

- [ ] **Step 1: Add CSS for the banner**

  Append to the file's CSS section:
  ```css
  .replay-banner {
      background: color-mix(in oklch, orange 18%, var(--bg, transparent));
      border-bottom: 1px solid color-mix(in oklch, orange 40%, transparent);
      color: var(--text);
      padding: 0.5rem 1rem;
      font-size: var(--fs-sm);
      display: flex; align-items: center; gap: 0.5rem;
      cursor: pointer;
  }
  .replay-banner:hover { background: color-mix(in oklch, orange 28%, var(--bg, transparent)); }
  .replay-banner .label { flex: 1; }
  body.replay-mode #composer,
  body.replay-mode #stop-btn,
  body.replay-mode #send-btn { display: none !important; }
  body.replay-mode .composer-area { display: none !important; }
  ```

  (Confirm the actual ID names of the composer/input/stop button by grepping. Adjust selectors to match.)

- [ ] **Step 2: Add `loadRunReadOnly`, `renderReplayMode`, `exitReplayMode`**

  ```javascript
  let replayState = null; // { agentId, sessionKey, runId, events: [] }

  function loadRunReadOnly(agentId, sessionKey, runId) {
      const id = "replay-" + runId;
      replayState = { agentId, sessionKey, runId, events: [] };
      ws.send(JSON.stringify({
          jsonrpc: "2.0",
          method: "chat.replay",
          params: { agentId, sessionKey, runId, fromSeq: 0 },
          id
      }));
  }

  function renderReplayMode() {
      document.body.classList.add('replay-mode');
      // Show banner.
      let banner = document.getElementById('replay-banner');
      if (!banner) {
          banner = document.createElement('div');
          banner.id = 'replay-banner';
          banner.className = 'replay-banner';
          banner.innerHTML = '<span>← <strong>Back to live</strong> · Viewing past run (read-only)</span>';
          banner.addEventListener('click', exitReplayMode);
          // Insert at top of chat area.
          const chatArea = document.getElementById('chat-area') || document.querySelector('.chat-area');
          chatArea.insertBefore(banner, chatArea.firstChild);
      }
      // Render events into the message pane.
      // Use the existing per-event renderer (same one chat.send events flow through).
      const messages = document.getElementById('messages') || document.querySelector('.messages');
      messages.innerHTML = ''; // clear
      if (!replayState || replayState.events.length === 0) {
          messages.innerHTML = '<div style="opacity:0.5;padding:1rem">Run is empty or no longer available.</div>';
          return;
      }
      // Reuse the existing renderEvent function from the live path.
      // If your file calls it differently (e.g. renderAgentEvent, appendEvent),
      // use the actual name.
      replayState.events.forEach(e => renderEvent(e));
  }

  function exitReplayMode() {
      document.body.classList.remove('replay-mode');
      const banner = document.getElementById('replay-banner');
      if (banner) banner.remove();
      replayState = null;
      // Re-render the live chat view from the session's persisted message
      // history. The existing session-switch code path does this; call
      // whichever helper it uses (e.g. loadSession(currentSessionKey)).
      const sk = (typeof currentSessionKey === 'function') ? currentSessionKey() : null;
      const ai = (typeof currentAgentId === 'function') ? currentAgentId() : null;
      if (ai && sk && typeof loadSession === 'function') {
          loadSession(ai, sk);
      } else {
          // Fallback: hard reload as last resort.
          location.reload();
      }
  }
  ```

- [ ] **Step 3: Wire the chat.replay response handler**

  In onmessage dispatch:
  ```javascript
  if (typeof msg.id === "string" && msg.id.startsWith("replay-")) {
      if (msg.error) {
          alert("Replay failed: " + (msg.error.message || "unknown"));
          replayState = null;
          return;
      }
      const past = (msg.result && Array.isArray(msg.result.past)) ? msg.result.past : [];
      if (replayState) {
          replayState.events = past;
          renderReplayMode();
      }
      return;
  }
  ```

- [ ] **Step 4: Build**
  ```bash
  cd ~/projects/felix && go build ./internal/gateway/...
  ```

- [ ] **Step 5: Commit**
  ```bash
  cd ~/projects/felix && git add internal/gateway/chat.go
  git commit -m "feat(chat-ui): read-only mode loads a past run via chat.replay

  Click a run row → fires chat.replay → events render into the chat
  pane in read-only mode. Yellow 'Back to live' banner appears above
  the messages and the composer/stop button are hidden via a
  body.replay-mode class. Banner click restores the live view by
  re-calling loadSession (or hard-reload as fallback)."
  ```

---

### Task 3.4: Wave 2 smoke test

**Files:**
- (No file changes. Manual UI exercise.)

- [ ] **Step 1: Start felix locally**
  ```bash
  cd ~/projects/felix && go build -o /tmp/felix-w2 ./cmd/felix && /tmp/felix-w2 start > /tmp/felix-w2.log 2>&1 &
  PID=$!
  sleep 4
  curl -sS http://127.0.0.1:18789/health
  ```

- [ ] **Step 2: Open the chat UI in a browser**
  Visit `http://127.0.0.1:18789/chat`.

- [ ] **Step 3: Send 2-3 chats to populate runs**
  Pick any agent. Send "hello" three times.

  Expected: each turn finishes with the assistant's reply.

- [ ] **Step 4: Click the chevron next to the active session**

  Expected:
  - Chevron rotates 90°
  - Sub-list appears showing 3 run rows (newest at top)
  - Each row shows HH:MM:SS, status="completed" (green), event count, and a delete icon on hover

- [ ] **Step 5: Click a past run row body (not the delete button)**

  Expected:
  - Yellow "Back to live" banner appears at top of chat pane
  - Composer + Stop button disappear
  - Chat pane re-renders showing the events from that run
  - Banner click returns to live view; composer reappears

- [ ] **Step 6: Click the delete icon on a past run**

  Expected:
  - Confirm dialog: "Delete this run? The conversation history stays..."
  - Accept → row disappears from sub-list
  - Verify on disk:
    ```bash
    ls ~/.felix/sessions/*/*.runs/ | head -10
    ```
    The deleted runID's `.jsonl` file should be gone; `index.json` should not list it.

- [ ] **Step 7: Stop felix**
  ```bash
  kill -TERM $PID; sleep 1; kill -9 $PID 2>/dev/null
  ```

- [ ] **Step 8: Commit any UI tweaks discovered during smoke**
  ```bash
  cd ~/projects/felix && git status -s
  ```
  If any tweaks landed, commit them with a clear message; otherwise skip.

---

### Task 3.5: Phase 3 verification

- [ ] **Step 1: vet + build + race + full felix suite**
  ```bash
  cd ~/projects/felix && go vet ./... && go build ./... && go test -race -count=2 ./...
  ```
  Expected: all clean.

- [ ] **Step 2: Confirm dispatcher still has 7 chat.* cases**
  ```bash
  cd ~/projects/felix && grep -E 'case "chat\.' internal/gateway/websocket.go
  ```
  Expected: 7 lines.

Phase 3 done.

---

## Wave-complete check

- [ ] **All three phases merged into `feat/run-snapshots-ui`**
- [ ] **Smoke test (Task 3.4) passes for: expand, view, back-to-live, delete**
- [ ] **`go test -race ./...` is green**
- [ ] **Spec at `docs/superpowers/specs/2026-05-27-run-snapshots-ui-design.md` accurately describes what shipped — update if it doesn't**
- [ ] **Merge to main**
  ```bash
  cd ~/projects/felix && git checkout main && git merge --no-ff feat/run-snapshots-ui
  ```

---

## Rollback

If anything goes wrong post-merge:

- **Phase 3 only:** `git revert <P3-commits>` rolls back the UI but keeps the backend chat.runs/chat.deleteRun and Wave 1 polish intact. New RPCs are then dead code from a UI perspective but harmless.
- **Phase 2 only:** Revert just the chatexec/overlay changes. The wire-format comment can stay independently.
- **Phase 1 only:** `git revert <P1-commits>` rolls back the new RPCs + tests + Registry.DeleteRun. P2/P3 would then fail to build (P3's frontend calls chat.runs/chat.deleteRun). Revert all three together if reverting P1.

Disk artifacts: the `.runs/` directories created during Wave 1 are unaffected; this wave only adds RPCs that read/delete them.
