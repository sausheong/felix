# Cloudcat UI Merge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce Felix's menubar to Chat + Quit, and replace the flat chat UI with cloudcat's sidebar-based shell including Files panel, embed view for Settings/Jobs/Logs, and file attachment.

**Architecture:** Two commits. First: strip the menubar. Second: replace `chat.go` wholesale with a Felix-adapted version of cloudcat's, add the `paths.go`/`files.go`/`files_page.go` files backend, and wire everything into `server.go` + `startup.go`.

**Tech Stack:** Go 1.25, chi router, gorilla/websocket, systray, stdlib HTML/CSS/JS (no new dependencies)

**Source repo:** All new Go files ported from `~/projects/cloudcat` with only import-path changes (`cloudcat` → `felix`) unless noted otherwise.

---

## File Map

| File | Action | Notes |
|------|--------|-------|
| `cmd/felix-app/main.go` | Modify | Remove 4 menu items + handlers |
| `internal/gateway/paths.go` | Create | `resolveAgentPath`, `agentWorkspace`, `isInside` helpers |
| `internal/gateway/paths_disk_unix.go` | Create | `diskUsageOK` — Unix/macOS build (`!windows`) |
| `internal/gateway/paths_disk_windows.go` | Create | `diskUsageOK` stub — Windows build |
| `internal/gateway/paths_test.go` | Create | Unit tests for path helpers |
| `internal/gateway/files.go` | Create | HTTP handlers for /files/* endpoints |
| `internal/gateway/files_test.go` | Create | Unit tests for FilesHandlers |
| `internal/gateway/files_page.go` | Create | Serves /files page HTML |
| `internal/gateway/server.go` | Modify | Add `Files *FilesHandlers` to ServerOptions + 8 routes |
| `internal/startup/startup.go` | Modify | Instantiate FilesHandlers, pass into ServerOptions |
| `internal/gateway/chat.go` | Replace | Cloudcat's chat.go with Felix-specific adaptations |

---

## Task 1: Simplify the Menubar

**Files:**
- Modify: `cmd/felix-app/main.go:154-215`

- [ ] **Step 1: Edit `onReady()` to remove 4 menu items**

  In `cmd/felix-app/main.go`, replace the block from `mChat :=` through the end of the `go func()` select statement with the simplified version:

  ```go
  mChat := systray.AddMenuItem("Open Chat", "Open chat in browser")
  systray.AddSeparator()
  mQuit := systray.AddMenuItem("Quit", "Shut down and exit")

  quitCh := make(chan os.Signal, 1)
  signal.Notify(quitCh, syscall.SIGTERM, syscall.SIGINT)

  go func() {
      for {
          select {
          case <-mChat.ClickedCh:
              openURL(fmt.Sprintf("http://localhost:%d/chat", port))
          case <-mQuit.ClickedCh:
              shutdownAndExit(gw, "menu Quit clicked")
              return
          case sig := <-quitCh:
              slog.Warn("received termination signal",
                  "signal", sig.String(),
                  "ppid", os.Getppid())
              shutdownAndExit(gw, fmt.Sprintf("signal %s", sig))
              return
          case err := <-gw.exitCh:
              slog.Error("gateway subprocess exited unexpectedly", "error", err)
              showError("Felix's gateway process stopped unexpectedly. Use Quit and relaunch.")
              gw = &gateway{port: port, owned: false, exitCh: noExitCh()}
          }
      }
  }()
  ```

  Also remove the now-unused `openFile` function at the bottom of the file (lines 258–274) — it was only called from the removed Logs handler. If it is used elsewhere, keep it.

- [ ] **Step 2: Build**

  ```bash
  go build ./cmd/felix-app/
  ```
  Expected: no errors.

- [ ] **Step 3: Commit**

  ```bash
  git add cmd/felix-app/main.go
  git commit -m "feat(app): simplify menubar to Open Chat + Quit"
  ```

---

## Task 2: Port Path Helpers

**Files:**
- Create: `internal/gateway/paths.go`
- Create: `internal/gateway/paths_disk_unix.go`
- Create: `internal/gateway/paths_disk_windows.go`
- Create: `internal/gateway/paths_test.go`

- [ ] **Step 1: Write the failing test**

  Create `internal/gateway/paths_test.go`:

  ```go
  package gateway

  import (
      "os"
      "path/filepath"
      "testing"

      "github.com/sausheong/felix/internal/config"
  )

  func newTestConfig(t *testing.T, agentID, workspace string) *config.Config {
      t.Helper()
      return &config.Config{
          Agents: config.AgentsConfig{
              List: []config.AgentConfig{
                  {ID: agentID, Workspace: workspace},
              },
          },
      }
  }

  func TestResolveAgentPath_HappyPaths(t *testing.T) {
      ws := t.TempDir()
      ws, err := filepath.EvalSymlinks(ws)
      if err != nil {
          t.Fatal(err)
      }
      if err := os.MkdirAll(filepath.Join(ws, "src", "utils"), 0o755); err != nil {
          t.Fatal(err)
      }
      if err := os.WriteFile(filepath.Join(ws, "src", "main.go"), []byte("package main"), 0o644); err != nil {
          t.Fatal(err)
      }
      cfg := newTestConfig(t, "default", ws)

      cases := []struct {
          name, rel, want string
      }{
          {"root", "", ws},
          {"dot", ".", ws},
          {"subdir", "src", filepath.Join(ws, "src")},
          {"nested subdir", "src/utils", filepath.Join(ws, "src", "utils")},
          {"file", "src/main.go", filepath.Join(ws, "src", "main.go")},
      }
      for _, tc := range cases {
          t.Run(tc.name, func(t *testing.T) {
              got, err := resolveAgentPath(cfg, "default", tc.rel)
              if err != nil {
                  t.Fatalf("unexpected error: %v", err)
              }
              if got != tc.want {
                  t.Errorf("got %q, want %q", got, tc.want)
              }
          })
      }
  }

  func TestResolveAgentPath_Rejections(t *testing.T) {
      ws := t.TempDir()
      ws, _ = filepath.EvalSymlinks(ws)
      cfg := newTestConfig(t, "default", ws)

      cases := []string{
          "../escape",
          ".dotfile",
          "sub/.hidden",
      }
      for _, rel := range cases {
          t.Run(rel, func(t *testing.T) {
              _, err := resolveAgentPath(cfg, "default", rel)
              if err == nil {
                  t.Errorf("expected error for %q, got nil", rel)
              }
          })
      }
  }

  func TestResolveAgentPath_UnknownAgent(t *testing.T) {
      ws := t.TempDir()
      cfg := newTestConfig(t, "default", ws)
      _, err := resolveAgentPath(cfg, "nonexistent", "foo")
      if err == nil {
          t.Error("expected error for unknown agent, got nil")
      }
  }

  func TestAgentWorkspace(t *testing.T) {
      ws := t.TempDir()
      cfg := newTestConfig(t, "myagent", ws)
      if got := agentWorkspace(cfg, "myagent"); got != ws {
          t.Errorf("got %q, want %q", got, ws)
      }
      if got := agentWorkspace(cfg, "unknown"); got != "" {
          t.Errorf("expected empty string for unknown agent, got %q", got)
      }
  }

  func TestIsInside(t *testing.T) {
      ws := t.TempDir()
      ws, _ = filepath.EvalSymlinks(ws)
      sub := filepath.Join(ws, "sub")
      outside := t.TempDir()
      outside, _ = filepath.EvalSymlinks(outside)

      if !isInside(ws, ws) {
          t.Error("workspace should be inside itself")
      }
      if !isInside(sub, ws) {
          t.Error("subdir should be inside workspace")
      }
      if isInside(outside, ws) {
          t.Error("sibling dir should not be inside workspace")
      }
  }
  ```

- [ ] **Step 2: Run test — expect compile failure**

  ```bash
  go test ./internal/gateway/ -run TestResolveAgentPath -v 2>&1 | head -20
  ```
  Expected: compilation error — `resolveAgentPath` undefined.

- [ ] **Step 3: Create `internal/gateway/paths.go`**

  ```go
  package gateway

  import (
      "errors"
      "fmt"
      "os"
      "path/filepath"
      "strings"

      "github.com/sausheong/felix/internal/config"
  )

  // resolveAgentPath validates rel against agentID's workspace and returns the
  // absolute filesystem path. Rejects path-escape attempts, symlinks that
  // resolve outside the workspace, and any path component starting with '.'.
  // When the target doesn't yet exist (e.g. an upload destination), the parent
  // directory must exist and resolve inside the workspace.
  func resolveAgentPath(cfg *config.Config, agentID, rel string) (string, error) {
      if cfg == nil {
          return "", errors.New("nil config")
      }
      var workspace string
      for _, a := range cfg.Agents.List {
          if a.ID == agentID {
              workspace = a.Workspace
              break
          }
      }
      if workspace == "" {
          return "", fmt.Errorf("unknown agent %q", agentID)
      }

      for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
          if strings.HasPrefix(part, ".") && part != "." && part != "" {
              return "", fmt.Errorf("dotfile path not allowed: %q", rel)
          }
      }

      clean := filepath.Clean(filepath.Join(workspace, rel))

      resolved, err := filepath.EvalSymlinks(clean)
      if err != nil {
          if !os.IsNotExist(err) {
              return "", err
          }
          parent := filepath.Dir(clean)
          parentResolved, perr := filepath.EvalSymlinks(parent)
          if perr != nil {
              return "", perr
          }
          if !isInside(parentResolved, workspace) {
              return "", fmt.Errorf("path escapes workspace: %q", rel)
          }
          return filepath.Join(parentResolved, filepath.Base(clean)), nil
      }
      if !isInside(resolved, workspace) {
          return "", fmt.Errorf("path escapes workspace: %q", rel)
      }
      return resolved, nil
  }

  // isInside reports whether path is workspace itself or a descendant of it.
  func isInside(path, workspace string) bool {
      wsAbs, err := filepath.EvalSymlinks(workspace)
      if err != nil {
          wsAbs = workspace
      }
      if path == wsAbs {
          return true
      }
      return strings.HasPrefix(path+string(os.PathSeparator), wsAbs+string(os.PathSeparator))
  }

  // agentWorkspace returns the configured workspace dir for the agent, or "" if unknown.
  func agentWorkspace(cfg *config.Config, agentID string) string {
      if cfg == nil {
          return ""
      }
      for _, a := range cfg.Agents.List {
          if a.ID == agentID {
              return a.Workspace
          }
      }
      return ""
  }

  // ensureWorkspace creates the workspace directory if it does not yet exist.
  // resolveAgentPath's EvalSymlinks call fails on a non-existent workspace,
  // producing a misleading error; calling this before resolveAgentPath fixes that.
  func ensureWorkspace(ws string) error {
      if ws == "" {
          return nil
      }
      return os.MkdirAll(ws, 0o755)
  }
  ```

- [ ] **Step 4: Create `internal/gateway/paths_disk_unix.go`**

  ```go
  //go:build !windows

  package gateway

  import "syscall"

  // diskUsageOK reports whether writing addBytes more bytes to the filesystem
  // containing path would keep total usage under 80% capacity.
  func diskUsageOK(path string, addBytes int64) (bool, error) {
      var st syscall.Statfs_t
      if err := syscall.Statfs(path, &st); err != nil {
          return false, err
      }
      total := int64(st.Blocks) * int64(st.Bsize)
      free := int64(st.Bavail) * int64(st.Bsize)
      if total == 0 {
          return true, nil
      }
      used := total - free
      projected := used + addBytes
      return float64(projected)/float64(total) < 0.80, nil
  }
  ```

- [ ] **Step 5: Create `internal/gateway/paths_disk_windows.go`**

  ```go
  //go:build windows

  package gateway

  // diskUsageOK on Windows always permits the write. A proper implementation
  // would use syscall.GetDiskFreeSpaceEx — left for a follow-up.
  func diskUsageOK(_ string, _ int64) (bool, error) {
      return true, nil
  }
  ```

- [ ] **Step 6: Run tests — expect pass**

  ```bash
  go test ./internal/gateway/ -run "TestResolveAgentPath|TestAgentWorkspace|TestIsInside" -v
  ```
  Expected: all PASS.

- [ ] **Step 7: Commit**

  ```bash
  git add internal/gateway/paths.go internal/gateway/paths_disk_unix.go \
          internal/gateway/paths_disk_windows.go internal/gateway/paths_test.go
  git commit -m "feat(gateway): add path resolution helpers for workspace file access"
  ```

---

## Task 3: Port Files Backend

**Files:**
- Create: `internal/gateway/files.go`
- Create: `internal/gateway/files_test.go`

- [ ] **Step 1: Write the failing tests**

  Create `internal/gateway/files_test.go`:

  ```go
  package gateway

  import (
      "bytes"
      "encoding/json"
      "mime/multipart"
      "net/http"
      "net/http/httptest"
      "os"
      "path/filepath"
      "strings"
      "testing"

      "github.com/sausheong/felix/internal/config"
  )

  func newTestFilesHandlers(t *testing.T, agentID, workspace string) *FilesHandlers {
      t.Helper()
      cfg := newTestConfig(t, agentID, workspace)
      return NewFilesHandlers(func() *config.Config { return cfg })
  }

  func TestList_EmptyWorkspace(t *testing.T) {
      ws := t.TempDir()
      ws, _ = filepath.EvalSymlinks(ws)
      h := newTestFilesHandlers(t, "default", ws)

      req := httptest.NewRequest("GET", "/files/list?agent=default&dir=", nil)
      rr := httptest.NewRecorder()
      h.List(rr, req)

      if rr.Code != http.StatusOK {
          t.Fatalf("got %d, want 200", rr.Code)
      }
      var resp struct {
          Entries []struct{ Name string } `json:"entries"`
      }
      if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
          t.Fatal(err)
      }
      if len(resp.Entries) != 0 {
          t.Errorf("expected 0 entries, got %d", len(resp.Entries))
      }
  }

  func TestList_DirsBeforeFiles(t *testing.T) {
      ws := t.TempDir()
      ws, _ = filepath.EvalSymlinks(ws)
      os.MkdirAll(filepath.Join(ws, "zdir"), 0o755)
      os.WriteFile(filepath.Join(ws, "afile.txt"), []byte("hi"), 0o644)
      h := newTestFilesHandlers(t, "default", ws)

      req := httptest.NewRequest("GET", "/files/list?agent=default&dir=", nil)
      rr := httptest.NewRecorder()
      h.List(rr, req)

      var resp struct {
          Entries []struct {
              Name string `json:"name"`
              Type string `json:"type"`
          } `json:"entries"`
      }
      json.NewDecoder(rr.Body).Decode(&resp)
      if len(resp.Entries) != 2 {
          t.Fatalf("want 2 entries, got %d", len(resp.Entries))
      }
      if resp.Entries[0].Type != "dir" {
          t.Errorf("first entry should be dir, got %q", resp.Entries[0].Type)
      }
      if resp.Entries[1].Type != "file" {
          t.Errorf("second entry should be file, got %q", resp.Entries[1].Type)
      }
  }

  func TestList_DotfilesHidden(t *testing.T) {
      ws := t.TempDir()
      ws, _ = filepath.EvalSymlinks(ws)
      os.WriteFile(filepath.Join(ws, ".hidden"), []byte("secret"), 0o644)
      os.WriteFile(filepath.Join(ws, "visible.txt"), []byte("hi"), 0o644)
      h := newTestFilesHandlers(t, "default", ws)

      req := httptest.NewRequest("GET", "/files/list?agent=default&dir=", nil)
      rr := httptest.NewRecorder()
      h.List(rr, req)

      var resp struct {
          Entries []struct{ Name string } `json:"entries"`
      }
      json.NewDecoder(rr.Body).Decode(&resp)
      for _, e := range resp.Entries {
          if strings.HasPrefix(e.Name, ".") {
              t.Errorf("dotfile %q should be hidden", e.Name)
          }
      }
      if len(resp.Entries) != 1 {
          t.Errorf("want 1 visible entry, got %d", len(resp.Entries))
      }
  }

  func TestUpload_ThenList(t *testing.T) {
      ws := t.TempDir()
      ws, _ = filepath.EvalSymlinks(ws)
      h := newTestFilesHandlers(t, "default", ws)

      var body bytes.Buffer
      w := multipart.NewWriter(&body)
      fw, _ := w.CreateFormFile("file", "hello.txt")
      fw.Write([]byte("hello world"))
      w.Close()

      req := httptest.NewRequest("POST", "/files/upload?agent=default&dir=", &body)
      req.Header.Set("Content-Type", w.FormDataContentType())
      rr := httptest.NewRecorder()
      h.Upload(rr, req)

      if rr.Code != http.StatusOK {
          t.Fatalf("upload got %d: %s", rr.Code, rr.Body.String())
      }

      if _, err := os.Stat(filepath.Join(ws, "hello.txt")); err != nil {
          t.Errorf("uploaded file not found: %v", err)
      }
  }

  func TestDelete_File(t *testing.T) {
      ws := t.TempDir()
      ws, _ = filepath.EvalSymlinks(ws)
      path := filepath.Join(ws, "todelete.txt")
      os.WriteFile(path, []byte("bye"), 0o644)
      h := newTestFilesHandlers(t, "default", ws)

      req := httptest.NewRequest("DELETE", "/files?agent=default&path=todelete.txt", nil)
      rr := httptest.NewRecorder()
      h.Delete(rr, req)

      if rr.Code != http.StatusNoContent {
          t.Fatalf("delete got %d: %s", rr.Code, rr.Body.String())
      }
      if _, err := os.Stat(path); !os.IsNotExist(err) {
          t.Error("file should be gone after delete")
      }
  }

  func TestMkDir(t *testing.T) {
      ws := t.TempDir()
      ws, _ = filepath.EvalSymlinks(ws)
      h := newTestFilesHandlers(t, "default", ws)

      body := `{"agent":"default","path":"newdir"}`
      req := httptest.NewRequest("POST", "/files/mkdir", strings.NewReader(body))
      req.Header.Set("Content-Type", "application/json")
      rr := httptest.NewRecorder()
      h.MkDir(rr, req)

      if rr.Code != http.StatusNoContent {
          t.Fatalf("mkdir got %d: %s", rr.Code, rr.Body.String())
      }
      info, err := os.Stat(filepath.Join(ws, "newdir"))
      if err != nil || !info.IsDir() {
          t.Error("directory should exist after mkdir")
      }
  }

  func TestRename(t *testing.T) {
      ws := t.TempDir()
      ws, _ = filepath.EvalSymlinks(ws)
      os.WriteFile(filepath.Join(ws, "old.txt"), []byte("data"), 0o644)
      h := newTestFilesHandlers(t, "default", ws)

      body := `{"agent":"default","path":"old.txt","newName":"new.txt"}`
      req := httptest.NewRequest("POST", "/files/rename", strings.NewReader(body))
      req.Header.Set("Content-Type", "application/json")
      rr := httptest.NewRecorder()
      h.Rename(rr, req)

      if rr.Code != http.StatusNoContent {
          t.Fatalf("rename got %d: %s", rr.Code, rr.Body.String())
      }
      if _, err := os.Stat(filepath.Join(ws, "new.txt")); err != nil {
          t.Error("renamed file should exist")
      }
  }
  ```

- [ ] **Step 2: Run tests — expect compile failure**

  ```bash
  go test ./internal/gateway/ -run "TestList|TestUpload|TestDelete|TestMkDir|TestRename" -v 2>&1 | head -10
  ```
  Expected: compile error — `FilesHandlers` undefined.

- [ ] **Step 3: Create `internal/gateway/files.go`**

  Copy `~/projects/cloudcat/internal/gateway/files.go` to `internal/gateway/files.go`, then change the single import path:

  ```bash
  cp ~/projects/cloudcat/internal/gateway/files.go internal/gateway/files.go
  sed -i '' 's|github.com/sausheong/cloudcat/internal/config|github.com/sausheong/felix/internal/config|g' internal/gateway/files.go
  ```

  Verify the substitution:
  ```bash
  grep "sausheong" internal/gateway/files.go
  ```
  Expected: only `github.com/sausheong/felix/internal/config` appears.

- [ ] **Step 4: Run tests — expect pass**

  ```bash
  go test ./internal/gateway/ -run "TestList|TestUpload|TestDelete|TestMkDir|TestRename" -v
  ```
  Expected: all PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/gateway/files.go internal/gateway/files_test.go
  git commit -m "feat(gateway): add files backend for workspace file explorer"
  ```

---

## Task 4: Port Files Page

**Files:**
- Create: `internal/gateway/files_page.go`

- [ ] **Step 1: Copy and adapt `files_page.go`**

  ```bash
  cp ~/projects/cloudcat/internal/gateway/files_page.go internal/gateway/files_page.go
  sed -i '' 's|github.com/sausheong/cloudcat/internal/config|github.com/sausheong/felix/internal/config|g' internal/gateway/files_page.go
  sed -i '' "s|localStorage.getItem('cloudcat-theme')|localStorage.getItem('felix-theme')|g" internal/gateway/files_page.go
  ```

  The file has no package-level import references to cloudcat outside of config. Verify:
  ```bash
  grep "cloudcat" internal/gateway/files_page.go
  ```
  Expected: no output (all cloudcat references removed).

- [ ] **Step 2: Build**

  ```bash
  go build ./internal/gateway/
  ```
  Expected: no errors.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/gateway/files_page.go
  git commit -m "feat(gateway): add files page HTML for workspace file explorer"
  ```

---

## Task 5: Wire Files into Server and Startup

**Files:**
- Modify: `internal/gateway/server.go`
- Modify: `internal/startup/startup.go`

- [ ] **Step 1: Add `Files *FilesHandlers` to `ServerOptions` in `server.go`**

  In `internal/gateway/server.go`, add one field to the `ServerOptions` struct after `LogBuffer`:

  ```go
  LogBuffer      *LogBuffer        // optional log buffer for /logs
  Files          *FilesHandlers    // optional /files/* handlers
  ```

- [ ] **Step 2: Add the 8 file routes in `server.go`'s `routes()` method**

  After the `if s.opts.LogBuffer != nil { ... }` block, append:

  ```go
  if s.opts.Files != nil {
      s.router.Get("/files", NewFilesPageHandler())
      s.router.Get("/files/list", s.opts.Files.List)
      s.router.Get("/files/raw", s.opts.Files.Raw)
      s.router.Post("/files/upload", s.opts.Files.Upload)
      s.router.Delete("/files", s.opts.Files.Delete)
      s.router.Post("/files/move", s.opts.Files.Move)
      s.router.Post("/files/rename", s.opts.Files.Rename)
      s.router.Post("/files/mkdir", s.opts.Files.MkDir)
  }
  ```

- [ ] **Step 3: Instantiate `FilesHandlers` in `startup.go`**

  In `internal/startup/startup.go`, in `StartGateway()`, locate the `gateway.NewServer(...)` call (around line 843). Add the `FilesHandlers` instantiation immediately above it:

  ```go
  filesHandlers := gateway.NewFilesHandlers(func() *config.Config { return cfg })
  ```

  Then add `Files: filesHandlers` to the `ServerOptions` literal:

  ```go
  srv := gateway.NewServer(cfg.Gateway.Host, port, wsHandler, gateway.ServerOptions{
      AuthToken:      cfg.Gateway.Auth.Token,
      MetricsHandler: metrics.Handler(),
      UIHandler:      gateway.NewUIHandler(cfg, version),
      ChatHandler:    gateway.NewChatHandler(port),
      JobsHandler:    gateway.NewJobsHandler(port),
      Settings: gateway.NewSettingsHandlers(cfg, toolReg, settingsBootstrap(bootstrapTracker), func(newCfg *config.Config) {
          wsHandler.UpdateConfig(newCfg)
          slog.Info("config updated via settings page")
      }),
      Skills:    skillHandlers,
      Memory:    gateway.NewMemoryHandlers(memMgr),
      MCP: gateway.NewMCPHandlers(mcpMgr, func() *config.Config {
          return cfg
      }),
      LogBuffer: logBuf,
      Files:     filesHandlers,  // ← add this line
  })
  ```

- [ ] **Step 4: Build everything**

  ```bash
  go build ./...
  ```
  Expected: no errors.

- [ ] **Step 5: Verify `/files` route exists**

  Start the server in the background, curl the endpoint, then stop:

  ```bash
  ./felix start &
  sleep 2
  curl -s -o /dev/null -w "%{http_code}" http://localhost:18789/files
  kill %1
  ```
  Expected: `200`.

- [ ] **Step 6: Commit**

  ```bash
  git add internal/gateway/server.go internal/startup/startup.go
  git commit -m "feat(gateway): wire FilesHandlers into server routes and startup"
  ```

---

## Task 6: Replace Chat UI

**Files:**
- Replace: `internal/gateway/chat.go`

This is the largest task. The strategy is: copy cloudcat's `chat.go`, then apply eight targeted adaptations.

- [ ] **Step 1: Copy cloudcat's chat.go**

  ```bash
  cp ~/projects/cloudcat/internal/gateway/chat.go internal/gateway/chat.go
  ```

- [ ] **Step 2: Fix the package import path**

  ```bash
  sed -i '' 's|github.com/sausheong/cloudcat/internal/config|github.com/sausheong/felix/internal/config|g' internal/gateway/chat.go
  ```

- [ ] **Step 3: Rename localStorage theme key**

  ```bash
  sed -i '' "s|cloudcat-theme|felix-theme|g" internal/gateway/chat.go
  ```

  Verify:
  ```bash
  grep "cloudcat-theme" internal/gateway/chat.go
  ```
  Expected: no output.

- [ ] **Step 4: Replace page title and brand text**

  ```bash
  sed -i '' 's|<title>CloudCat</title>|<title>Felix</title>|g' internal/gateway/chat.go
  sed -i '' 's|title="CloudCat"|title="Felix"|g' internal/gateway/chat.go
  sed -i '' 's|>CloudCat<|>Felix<|g' internal/gateway/chat.go
  sed -i '' "s|Reply to Cloudcat...|Message Felix...|g" internal/gateway/chat.go
  ```

- [ ] **Step 5: Remove the cat logo `<img>` element**

  Find and delete the line containing `<img` inside the `#brand` anchor. It looks like:

  ```html
  <img class="logo" src="/auth/logo.png" ...>
  ```

  Also remove the CSS rules that reference `#brand .logo` and `html.dark #brand .logo` — they are dead code once the image is gone. Use your editor or:

  ```bash
  grep -n "logo\|auth/logo\|mix-blend-mode\|brand .logo" internal/gateway/chat.go | head -20
  ```

  Then manually delete those lines. The brand `<a>` tag should contain only the `<span class="brand-text">Felix</span>` after this step.

- [ ] **Step 6: Remove the OAuth user menu**

  Locate the `#user-row` div. It contains:
  - `<button id="theme-toggle">` ← keep this
  - `<div class="user-avatar" id="user-avatar">` ← remove
  - `<span id="user-name" class="sb-label">` ← remove
  - `<button id="user-menu-btn" ...>` ← remove
  - The following `<div id="user-menu" hidden>` block ← remove entirely

  Find these with:
  ```bash
  grep -n "user-avatar\|user-name\|user-menu-btn\|user-menu\|signout\|data-action" internal/gateway/chat.go | head -20
  ```

  Delete those lines so `#user-row` contains only the theme-toggle button.

- [ ] **Step 7: Remove OAuth JS calls**

  Find and delete the following JS patterns in the `<script>` block:

  ```bash
  grep -n "auth/me\|auth/logout\|user-avatar\|user-name\|signout\|data-action.*restart\|admin/restart\|admin/recreate" internal/gateway/chat.go | head -30
  ```

  Delete:
  - The `fetch('/auth/me', ...)` call and its `.then(...)` handler (typically populates `#user-avatar` and `#user-name`)
  - The `signout` action handler in the user-menu event listener
  - The `restart` action handler (calls `POST /admin/restart`)
  - Any `POST /admin/recreate` call

- [ ] **Step 8: Remove `/favicon.png` link tag**

  ```bash
  sed -i '' 's|<link rel="icon" type="image/png" href="/favicon.png">||g' internal/gateway/chat.go
  ```

- [ ] **Step 9: Build**

  ```bash
  go build ./...
  ```
  Expected: no errors. If there are template-format errors (the `%%` escaping used inside `fmt.Fprintf` calls), they will show at build time as Go compilation errors — not runtime errors.

- [ ] **Step 10: Run the server and do a manual smoke test**

  ```bash
  ./felix start
  ```

  Open `http://localhost:18789/chat` in a browser and verify:

  - [ ] Sidebar renders with brand text "Felix" (not "CloudCat")
  - [ ] Sidebar collapses to icon rail on click
  - [ ] Agent selector in sidebar shows configured agents
  - [ ] Clicking "Settings" in sidebar loads the settings page inside the main pane via iframe
  - [ ] Clicking "Jobs" in sidebar loads the jobs page via iframe
  - [ ] Clicking "Logs" in sidebar loads the logs page via iframe
  - [ ] Clicking "Files" in sidebar loads the file explorer via aside panel
  - [ ] Theme toggle (moon/sun) switches light/dark and persists across refresh
  - [ ] Sending a chat message works (round-trip to agent)
  - [ ] Paperclip button and drag-and-drop area appear in the input row
  - [ ] No "CloudCat" text visible anywhere
  - [ ] No OAuth-related UI elements (no avatar, no sign-out button)

- [ ] **Step 11: Commit**

  ```bash
  git add internal/gateway/chat.go
  git commit -m "feat(gateway): replace chat UI with cloudcat sidebar shell adapted for Felix"
  ```

---

## Self-Review

**Spec coverage check:**

| Spec requirement | Task |
|---|---|
| Menubar → Open Chat + Quit only | Task 1 |
| Collapsible sidebar | Task 6 (chat.go) |
| Session list + filter in sidebar | Task 6 (chat.go) |
| Agent selector in sidebar | Task 6 (chat.go) |
| Settings/Jobs/Logs via iframe embed | Task 6 (chat.go) |
| Files panel (aside overlay) | Task 6 (chat.go) + Task 3/4/5 |
| File attachment in input | Task 6 (chat.go — JS already in cloudcat's file) |
| Light/dark theme (warm cream + forest green) | Task 6 (chat.go CSS) |
| Mobile-responsive sidebar | Task 6 (chat.go CSS) |
| Felix branding (not CloudCat) | Task 6 steps 4–5 |
| OAuth UI removed | Task 6 steps 6–7 |
| resolveAgentPath + workspace clamping | Task 2 |
| All 7 file API endpoints | Task 3 |
| Routes registered in server.go | Task 5 |
| FilesHandlers wired in startup.go | Task 5 |

All spec requirements are covered.

**Placeholder scan:** No TBDs, no "implement later", no vague steps — all code is shown or has exact `cp`/`sed` commands.

**Type consistency:** `FilesHandlers`, `NewFilesHandlers`, `resolveAgentPath`, `agentWorkspace`, `isInside`, `ensureWorkspace`, `diskUsageOK` — all defined in Task 2/3 and referenced consistently in Task 5/6.
