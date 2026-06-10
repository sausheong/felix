# Bundled Ollama Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship Felix with a bundled Ollama binary, supervised by a minimal child-process manager, so users with no API key get a working offline-LLM agent on first run via a curated 3-model wizard pull.

**Architecture:** New `internal/local` package owns binary discovery, a minimal supervisor (spawn → readiness poll → signal-on-exit; no restart, no health-loop), and an HTTP installer wrapping Ollama's `/api/pull`, `/api/tags`, `/api/delete`. The existing OpenAI-compatible LLM client is reused via a one-line switch case for a new `local` provider. The bundled `ollama` binary ships in `bin/ollama` per platform; the model is pulled on first run.

**Tech Stack:** Go 1.22+, stdlib `net/http` + `os/exec`, `github.com/stretchr/testify`, `github.com/spf13/cobra` (existing CLI), `github.com/sashabaranov/go-openai` (existing client).

**Spec:** `docs/superpowers/specs/2026-04-20-bundled-ollama-design.md`

---

## File structure

**New files:**

| Path | Responsibility |
|---|---|
| `internal/local/discover.go` | Resolve `ollama` binary path via search order |
| `internal/local/discover_test.go` | Unit tests for discovery |
| `internal/local/supervisor.go` | Spawn, ready-poll, signal-on-exit; ~150 LOC |
| `internal/local/supervisor_test.go` | Unit tests using fake-binary shell scripts |
| `internal/local/installer.go` | HTTP wrapper for Ollama API (Pull/List/Delete/Status) |
| `internal/local/installer_test.go` | Unit tests against `httptest.NewServer` |
| `internal/local/config.go` | Provider-injection helper that writes `local` block into `felix.json5` |
| `internal/local/config_test.go` | Unit tests using temp dirs |
| `internal/local/integration_test.go` | Build-tag `local` integration test against real Ollama + tiny model |
| `cmd/felix/model_cmd.go` | `felix model {pull,list,rm,status}` cobra subcommand |
| `cmd/felix/model_cmd_test.go` | CLI tests against mock Ollama HTTP |

**Modified files:**

| Path | Change |
|---|---|
| `internal/config/config.go` | Add `LocalConfig` struct, `Local` field on `Config`, defaults |
| `internal/config/config_test.go` | Test defaults for the new section |
| `internal/llm/provider.go` | Add `case "local":` to `NewProvider` switch |
| `internal/llm/provider_test.go` | Test the new case |
| `internal/startup/startup.go` | Start `local.Supervisor`, register cleanup, register `local` provider with bound port |
| `cmd/felix/main.go` | Register `modelCmd()` subcommand; rewrite onboard wizard with local-first curated list |
| `Makefile` | Add `ollama-fetch` target with `OLLAMA_VERSION` pin; modify `build-release`, `installer`, `sign` to bundle `bin/ollama` |
| `installer/scripts/postinstall` | When bundled `bin/ollama` is present, skip the API-key wizard and inject the `local` provider |

---

## Task 1: Add LocalConfig schema and defaults

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestDefaultConfigLocalSection(t *testing.T) {
	cfg := DefaultConfig()
	require.NotNil(t, cfg)

	assert.True(t, cfg.Local.Enabled, "local should default to enabled")
	assert.Equal(t, "5m", cfg.Local.KeepAlive)
	assert.Equal(t, "", cfg.Local.ModelsDir, "models_dir should default to empty (resolved at runtime)")
}

func TestLocalConfigParsing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "felix.json5")
	contents := `{
		"agents": { "list": [{"id": "a1", "model": "local/qwen2.5:0.5b"}] },
		"local": { "enabled": false, "keep_alive": "30m", "models_dir": "/tmp/m" }
	}`
	require.NoError(t, os.WriteFile(cfgPath, []byte(contents), 0o600))

	cfg, err := Load(cfgPath)
	require.NoError(t, err)
	assert.False(t, cfg.Local.Enabled)
	assert.Equal(t, "30m", cfg.Local.KeepAlive)
	assert.Equal(t, "/tmp/m", cfg.Local.ModelsDir)
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/config/ -run 'TestDefaultConfigLocalSection|TestLocalConfigParsing' -v
```

Expected: FAIL with `cfg.Local undefined` (or similar).

- [ ] **Step 3: Add LocalConfig struct and field**

In `internal/config/config.go`, add to the `Config` struct (after the existing `Security` field):

```go
	Local     LocalConfig              `json:"local"`
```

Add the type definition (next to the other `*Config` types, e.g. after `MemoryConfig`):

```go
// LocalConfig configures the bundled Ollama supervisor.
type LocalConfig struct {
	Enabled   bool   `json:"enabled"`    // master switch
	ModelsDir string `json:"models_dir"` // override; empty → ~/.felix/ollama/models
	KeepAlive string `json:"keep_alive"` // OLLAMA_KEEP_ALIVE
}
```

Add defaults in `DefaultConfig()` (next to the existing sections):

```go
		Local: LocalConfig{
			Enabled:   true,
			KeepAlive: "5m",
		},
```

Update `UpdateFrom` to copy the new field:

```go
	c.Local = src.Local
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/config/ -v
```

Expected: PASS for the new tests and all existing tests.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add LocalConfig schema for bundled Ollama supervisor"
```

---

## Task 2: Binary path discovery

**Files:**
- Create: `internal/local/discover.go`
- Create: `internal/local/discover_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/local/discover_test.go`:

```go
package local

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func touch(t *testing.T, path string, exec bool) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o644))
	if exec {
		require.NoError(t, os.Chmod(path, 0o755))
	}
}

func TestDiscoverEnvOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("env-override test uses POSIX exec bit")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ollama")
	touch(t, bin, true)
	t.Setenv("FELIX_OLLAMA_BIN", bin)

	got, err := Discover("/some/other/dir")
	require.NoError(t, err)
	assert.Equal(t, bin, got)
}

func TestDiscoverNextToFelixBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX exec bit")
	}
	t.Setenv("FELIX_OLLAMA_BIN", "")
	t.Setenv("PATH", "")

	felixDir := t.TempDir()
	bin := filepath.Join(felixDir, "bin", "ollama")
	touch(t, bin, true)

	got, err := Discover(felixDir)
	require.NoError(t, err)
	assert.Equal(t, bin, got)
}

func TestDiscoverNotFound(t *testing.T) {
	t.Setenv("FELIX_OLLAMA_BIN", "")
	t.Setenv("PATH", "")
	dir := t.TempDir()

	_, err := Discover(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBinaryNotFound)
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/local/ -run TestDiscover -v
```

Expected: FAIL — package doesn't exist yet.

- [ ] **Step 3: Implement discover.go**

Create `internal/local/discover.go`:

```go
// Package local manages the bundled Ollama child process and models.
package local

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ErrBinaryNotFound is returned when the ollama binary cannot be located.
var ErrBinaryNotFound = errors.New("ollama binary not found")

// Discover returns the absolute path to the ollama binary.
//
// Search order:
//  1. $FELIX_OLLAMA_BIN (env override, for dev/testing)
//  2. <felixBinDir>/bin/ollama(.exe) — sibling to felix in unpacked zips
//  3. macOS app bundle: <felixBinDir>/../Resources/bin/ollama
//  4. PATH lookup (last resort, dev convenience)
//
// felixBinDir should be the directory containing the running felix binary.
func Discover(felixBinDir string) (string, error) {
	exe := "ollama"
	if runtime.GOOS == "windows" {
		exe = "ollama.exe"
	}

	if env := os.Getenv("FELIX_OLLAMA_BIN"); env != "" {
		if isExec(env) {
			return env, nil
		}
		return "", fmt.Errorf("%w: FELIX_OLLAMA_BIN=%s is not executable", ErrBinaryNotFound, env)
	}

	candidates := []string{
		filepath.Join(felixBinDir, "bin", exe),
	}
	if runtime.GOOS == "darwin" {
		// felix CLI inside Felix.app/Contents/MacOS — bundle is at ../Resources/bin
		candidates = append(candidates, filepath.Join(felixBinDir, "..", "Resources", "bin", exe))
	}

	for _, c := range candidates {
		if isExec(c) {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs, nil
			}
			return c, nil
		}
	}

	if path, err := exec.LookPath(exe); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("%w: searched env, %v, PATH", ErrBinaryNotFound, candidates)
}

func isExec(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // Windows has no exec bit; trust the .exe
	}
	return fi.Mode().Perm()&0o111 != 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/local/ -run TestDiscover -v
```

Expected: PASS for all three discovery tests.

- [ ] **Step 5: Commit**

```bash
git add internal/local/discover.go internal/local/discover_test.go
git commit -m "feat: add ollama binary path discovery for bundled supervisor"
```

---

## Task 3: Installer — List (`/api/tags`)

**Files:**
- Create: `internal/local/installer.go`
- Create: `internal/local/installer_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/local/installer_test.go`:

```go
package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMockOllama(t *testing.T, handler http.HandlerFunc) (*Installer, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	inst := NewInstaller(srv.URL)
	return inst, srv.Close
}

func TestInstallerList(t *testing.T) {
	inst, closeFn := newMockOllama(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/tags", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "qwen2.5:0.5b", "size": 394 << 20},
				{"name": "llama4.1:8b", "size": int64(4700) << 20},
			},
		})
	})
	defer closeFn()

	models, err := inst.List(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 2)
	assert.Equal(t, "qwen2.5:0.5b", models[0].Name)
	assert.Equal(t, int64(394<<20), models[0].SizeBytes)
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/local/ -run TestInstallerList -v
```

Expected: FAIL — `Installer` type doesn't exist.

- [ ] **Step 3: Implement minimal Installer + List**

Create `internal/local/installer.go`:

```go
package local

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Installer wraps the Ollama HTTP API for model management.
type Installer struct {
	baseURL string // e.g. http://127.0.0.1:18790
	client  *http.Client
}

// NewInstaller creates an Installer pointing at an Ollama instance.
func NewInstaller(baseURL string) *Installer {
	return &Installer{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 0}, // streaming-friendly; per-request contexts handle cancellation
	}
}

// Model describes a locally-available model.
type Model struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size"`
}

// List returns the locally-pulled models via /api/tags.
func (i *Installer) List(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := i.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list: ollama returned %s", resp.Status)
	}
	var body struct {
		Models []Model `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("list: decode: %w", err)
	}
	return body.Models, nil
}

// shortDeadline returns a context cancelled after d for one-shot calls.
func shortDeadline(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/local/ -run TestInstallerList -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/local/installer.go internal/local/installer_test.go
git commit -m "feat: add Installer.List wrapping Ollama /api/tags"
```

---

## Task 4: Installer — Delete (`/api/delete`)

**Files:**
- Modify: `internal/local/installer.go`
- Modify: `internal/local/installer_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/local/installer_test.go`:

```go
func TestInstallerDelete(t *testing.T) {
	var gotBody map[string]string
	inst, closeFn := newMockOllama(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/delete", r.URL.Path)
		assert.Equal(t, http.MethodDelete, r.Method)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	})
	defer closeFn()

	err := inst.Delete(context.Background(), "qwen2.5:0.5b")
	require.NoError(t, err)
	assert.Equal(t, "qwen2.5:0.5b", gotBody["name"])
}

func TestInstallerDeleteNotFound(t *testing.T) {
	inst, closeFn := newMockOllama(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer closeFn()

	err := inst.Delete(context.Background(), "nope")
	require.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/local/ -run TestInstallerDelete -v
```

Expected: FAIL — `Delete` undefined.

- [ ] **Step 3: Implement Delete**

Add to `internal/local/installer.go`:

```go
// Delete removes a model via /api/delete.
func (i *Installer) Delete(ctx context.Context, name string) error {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, i.baseURL+"/api/delete", bytesReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("delete %q: ollama returned %s", name, resp.Status)
	}
	return nil
}
```

Add the helper at the bottom of the file:

```go
// bytesReader is a small wrapper to avoid importing bytes alongside io for one call site.
func bytesReader(b []byte) *bytesReadCloser { return &bytesReadCloser{b: b} }

type bytesReadCloser struct {
	b   []byte
	pos int
}

func (r *bytesReadCloser) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

func (r *bytesReadCloser) Close() error { return nil }
```

Add `"io"` to the imports.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/local/ -v
```

Expected: PASS for `TestInstallerDelete*` plus all earlier tests.

- [ ] **Step 5: Commit**

```bash
git add internal/local/installer.go internal/local/installer_test.go
git commit -m "feat: add Installer.Delete wrapping Ollama /api/delete"
```

---

## Task 5: Installer — Pull with streaming progress

**Files:**
- Modify: `internal/local/installer.go`
- Modify: `internal/local/installer_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/local/installer_test.go`:

```go
func TestInstallerPullStreamsProgress(t *testing.T) {
	inst, closeFn := newMockOllama(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/pull", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		assert.Equal(t, "qwen2.5:0.5b", req["name"])

		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)
		for _, line := range []string{
			`{"status":"pulling manifest"}`,
			`{"status":"downloading","digest":"sha256:abc","total":1000,"completed":250}`,
			`{"status":"downloading","digest":"sha256:abc","total":1000,"completed":1000}`,
			`{"status":"success"}`,
		} {
			_, _ = w.Write([]byte(line + "\n"))
			flusher.Flush()
		}
	})
	defer closeFn()

	var events []ProgressEvent
	err := inst.Pull(context.Background(), "qwen2.5:0.5b", func(ev ProgressEvent) {
		events = append(events, ev)
	})
	require.NoError(t, err)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, "success", last.Status)
}

func TestInstallerPullSurfacesError(t *testing.T) {
	inst, closeFn := newMockOllama(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(`{"error":"manifest not found"}` + "\n"))
		flusher.Flush()
	})
	defer closeFn()

	err := inst.Pull(context.Background(), "ghost", func(ProgressEvent) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest not found")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/local/ -run TestInstallerPull -v
```

Expected: FAIL — `Pull`/`ProgressEvent` undefined.

- [ ] **Step 3: Implement Pull with streaming NDJSON**

Add to `internal/local/installer.go`:

```go
// ProgressEvent is one line of pull progress emitted by Ollama.
type ProgressEvent struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Pull streams model bytes via /api/pull, calling onEvent for each NDJSON line.
// onEvent may be nil. The function returns when the stream ends or an error occurs.
func (i *Installer) Pull(ctx context.Context, name string, onEvent func(ProgressEvent)) error {
	body, err := json.Marshal(map[string]any{"name": name, "stream": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.baseURL+"/api/pull", bytesReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.client.Do(req)
	if err != nil {
		return fmt.Errorf("pull: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pull %q: ollama returned %s", name, resp.Status)
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var ev ProgressEvent
		if err := dec.Decode(&ev); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("pull %q: decode: %w", name, err)
		}
		if ev.Error != "" {
			return fmt.Errorf("pull %q: %s", name, ev.Error)
		}
		if onEvent != nil {
			onEvent(ev)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/local/ -v
```

Expected: PASS for `TestInstallerPull*` plus all earlier tests.

- [ ] **Step 5: Commit**

```bash
git add internal/local/installer.go internal/local/installer_test.go
git commit -m "feat: add Installer.Pull with streaming NDJSON progress"
```

---

## Task 5b: Installer — free-disk pre-check + model-size lookup

**Files:**
- Modify: `internal/local/installer.go`
- Modify: `internal/local/installer_test.go`

The spec's error-handling table requires "Insufficient disk for model pull → free-space check before pull." This task adds a `Show` call (Ollama `/api/show`) to discover model size, plus a `EnsureFreeSpace` helper.

- [ ] **Step 1: Write the failing test**

Append to `internal/local/installer_test.go`:

```go
func TestInstallerShowReturnsSize(t *testing.T) {
	inst, closeFn := newMockOllama(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/show", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"size": int64(4_700_000_000),
		})
	})
	defer closeFn()
	sz, err := inst.Show(context.Background(), "llama4.1:8b")
	require.NoError(t, err)
	assert.Equal(t, int64(4_700_000_000), sz)
}

func TestEnsureFreeSpaceErrorsWhenInsufficient(t *testing.T) {
	dir := t.TempDir()
	// Ask for an absurdly large amount; should fail unless your tmp has petabytes free.
	err := EnsureFreeSpace(dir, 1<<60)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient")
}

func TestEnsureFreeSpacePassesWhenTrivial(t *testing.T) {
	require.NoError(t, EnsureFreeSpace(t.TempDir(), 1024))
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/local/ -run 'TestInstallerShow|TestEnsureFreeSpace' -v
```

Expected: FAIL — `Show` and `EnsureFreeSpace` undefined.

- [ ] **Step 3: Implement Show and EnsureFreeSpace**

Add to `internal/local/installer.go`:

```go
// Show returns the size in bytes of a remote (or local) model via /api/show.
func (i *Installer) Show(ctx context.Context, name string) (int64, error) {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.baseURL+"/api/show", bytesReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("show: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("show %q: ollama returned %s", name, resp.Status)
	}
	var out struct {
		Size int64 `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("show %q: decode: %w", name, err)
	}
	return out.Size, nil
}
```

Create a new file `internal/local/diskspace.go` to keep the OS-specific syscall isolated:

```go
package local

import (
	"fmt"
	"syscall"
)

// EnsureFreeSpace returns an error if the filesystem containing path has
// less than wantBytes available.
func EnsureFreeSpace(path string, wantBytes int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return fmt.Errorf("statfs %q: %w", path, err)
	}
	avail := int64(stat.Bavail) * int64(stat.Bsize)
	if avail < wantBytes {
		return fmt.Errorf("insufficient disk: need %d bytes, have %d at %s", wantBytes, avail, path)
	}
	return nil
}
```

(For Windows, build-tag a stub that returns nil — out of scope for this task; add a follow-up if Windows release is needed.)

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/local/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/local/installer.go internal/local/diskspace.go internal/local/installer_test.go
git commit -m "feat: add Installer.Show and EnsureFreeSpace for pre-pull disk check"
```

---

## Task 6: Supervisor — free-port probe

**Files:**
- Create: `internal/local/supervisor.go`
- Create: `internal/local/supervisor_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/local/supervisor_test.go`:

```go
package local

import (
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeFreePortReturnsFirstFree(t *testing.T) {
	port, err := probeFreePort(18790, 18799)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, port, 18790)
	assert.LessOrEqual(t, port, 18799)
}

func TestProbeFreePortSkipsBound(t *testing.T) {
	// Bind 18790 ourselves; probe should return 18791 or higher.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	bound := ln.Addr().(*net.TCPAddr).Port

	got, err := probeFreePort(bound, bound+5)
	require.NoError(t, err)
	assert.NotEqual(t, bound, got)
}

func TestProbeFreePortAllTaken(t *testing.T) {
	// Bind a single port and ask probe for only that port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	bound := ln.Addr().(*net.TCPAddr).Port

	_, err = probeFreePort(bound, bound)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoFreePort)
}

func TestNew_DefaultsApplied(t *testing.T) {
	s := New(Options{BinPath: "/x/ollama", ModelsDir: "/m"})
	assert.Equal(t, "/x/ollama", s.binPath)
	assert.Equal(t, "/m", s.modelsDir)
}

// helper to parse an address printed by ln.Addr.
func portFromAddr(t *testing.T, addr net.Addr) int {
	t.Helper()
	_, p, err := net.SplitHostPort(addr.String())
	require.NoError(t, err)
	n, err := strconv.Atoi(p)
	require.NoError(t, err)
	return n
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/local/ -run 'TestProbeFreePort|TestNew_' -v
```

Expected: FAIL — `Supervisor`, `probeFreePort`, `New`, `Options`, `ErrNoFreePort` undefined.

- [ ] **Step 3: Create supervisor scaffold + port probe**

Create `internal/local/supervisor.go`:

```go
package local

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
)

// ErrNoFreePort is returned when no port in the configured range is free.
var ErrNoFreePort = errors.New("no free port in range")

// Options configures a Supervisor.
type Options struct {
	BinPath   string // absolute path to the ollama binary
	ModelsDir string // OLLAMA_MODELS
	KeepAlive string // OLLAMA_KEEP_ALIVE; empty → "5m"
	PortLow   int    // first port to try; 0 → 18790
	PortHigh  int    // last port to try inclusive; 0 → 18799
}

// Supervisor manages a single ollama serve child process.
type Supervisor struct {
	binPath   string
	modelsDir string
	keepAlive string
	portLow   int
	portHigh  int

	mu        sync.Mutex
	cmd       *exec.Cmd
	cancelCtx context.CancelFunc
	boundPort int
	alive     atomic.Bool
}

// New constructs a Supervisor with defaults applied.
func New(opt Options) *Supervisor {
	if opt.KeepAlive == "" {
		opt.KeepAlive = "5m"
	}
	if opt.PortLow == 0 {
		opt.PortLow = 18790
	}
	if opt.PortHigh == 0 {
		opt.PortHigh = 18799
	}
	return &Supervisor{
		binPath:   opt.BinPath,
		modelsDir: opt.ModelsDir,
		keepAlive: opt.KeepAlive,
		portLow:   opt.PortLow,
		portHigh:  opt.PortHigh,
	}
}

// BoundPort returns the port the child is listening on (0 if not started).
func (s *Supervisor) BoundPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.boundPort
}

// Healthy returns true while the child process is alive.
func (s *Supervisor) Healthy() bool {
	return s.alive.Load()
}

// probeFreePort tries each port in [low, high] and returns the first free one.
// It does not hold the listener — the caller races to bind it. This is fine
// for our use case because the next thing that happens is exec(ollama serve)
// which binds within milliseconds.
func probeFreePort(low, high int) (int, error) {
	for p := low; p <= high; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return p, nil
	}
	return 0, fmt.Errorf("%w: tried %d..%d", ErrNoFreePort, low, high)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/local/ -v
```

Expected: PASS for the new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/local/supervisor.go internal/local/supervisor_test.go
git commit -m "feat: add supervisor scaffold and free-port probe"
```

---

## Task 7: Supervisor.Start with readiness poll

**Files:**
- Modify: `internal/local/supervisor.go`
- Modify: `internal/local/supervisor_test.go`

- [ ] **Step 1: Write the failing test using a fake "ollama" shell script**

Append to `internal/local/supervisor_test.go`:

```go
import (
	// extend imports as needed at top
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// writeFakeOllama writes a shell script that:
//   - starts an HTTP server on the requested port (passed via OLLAMA_HOST)
//     responding 200 to /api/version
//   - blocks until killed
// Returns the absolute path to the script.
func writeFakeOllama(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary tests are POSIX-only")
	}
	path := filepath.Join(dir, "ollama")
	body := `#!/bin/sh
HOST="${OLLAMA_HOST:-127.0.0.1:0}"
PORT="${HOST##*:}"
exec /usr/bin/env python3 - <<PY
import http.server, socketserver
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/api/version":
            self.send_response(200); self.end_headers(); self.wfile.write(b'{"version":"fake"}')
        else:
            self.send_response(404); self.end_headers()
    def log_message(self, *a, **k): pass
with socketserver.TCPServer(("127.0.0.1", ${PORT}), H) as srv:
    srv.serve_forever()
PY
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
	return path
}

func TestSupervisorStartReady(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeOllama(t, dir)
	s := New(Options{BinPath: bin, ModelsDir: dir})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, s.Start(ctx))
	t.Cleanup(func() { _ = s.Stop() })

	port := s.BoundPort()
	assert.GreaterOrEqual(t, port, 18790)
	assert.True(t, s.Healthy())

	// Sanity-check the ready endpoint.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/version", port))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
}

func TestSupervisorStartNoFreePort(t *testing.T) {
	// Open all 10 ports in the range so probing fails.
	listeners := make([]net.Listener, 0, 10)
	t.Cleanup(func() {
		for _, ln := range listeners {
			ln.Close()
		}
	})
	first, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listeners = append(listeners, first)
	low := portFromAddr(t, first.Addr())

	for p := low + 1; p <= low+9; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			t.Skipf("could not bind probe range: %v", err)
		}
		listeners = append(listeners, ln)
	}

	s := New(Options{BinPath: "/bin/true", ModelsDir: t.TempDir(), PortLow: low, PortHigh: low + 9})
	err = s.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoFreePort)
}

func TestSupervisorStartReadinessTimeout(t *testing.T) {
	// /bin/sleep stays alive but never serves /api/version.
	s := New(Options{BinPath: "/bin/sleep", ModelsDir: t.TempDir()})
	s.readyTimeout = 500 * time.Millisecond

	err := s.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotReady)
	t.Cleanup(func() { _ = s.Stop() })
}
```

(If your `supervisor_test.go` already has imports, **merge** the new ones rather than duplicating the `import (` block.)

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/local/ -run TestSupervisorStart -v
```

Expected: FAIL — `Start`/`Stop`/`ErrNotReady`/`readyTimeout` undefined.

- [ ] **Step 3: Implement Start, Stop, supporting fields**

Modify `internal/local/supervisor.go` — add to the imports:

```go
	"log/slog"
	"net/http"
	"os"
	"time"
)
```

Add `ErrNotReady` near the other errors:

```go
// ErrNotReady is returned when the child fails to respond to /api/version
// within the readiness window.
var ErrNotReady = errors.New("ollama did not become ready in time")
```

Add a `readyTimeout` field to `Supervisor`:

```go
	readyTimeout time.Duration // 0 → 60s
```

Add `Start` and the helper poll method:

```go
// Start spawns ollama serve, waits for it to respond to /api/version, and
// returns nil once ready. On any failure, the child (if started) is killed
// and an error is returned.
func (s *Supervisor) Start(ctx context.Context) error {
	port, err := probeFreePort(s.portLow, s.portHigh)
	if err != nil {
		return err
	}

	childCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(childCtx, s.binPath, "serve")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OLLAMA_HOST=127.0.0.1:%d", port),
		fmt.Sprintf("OLLAMA_MODELS=%s", s.modelsDir),
		fmt.Sprintf("OLLAMA_KEEP_ALIVE=%s", s.keepAlive),
	)
	pipeStderr, _ := cmd.StderrPipe()
	pipeStdout, _ := cmd.StdoutPipe()

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("ollama: start: %w", err)
	}

	go forwardLogs(pipeStdout, "ollama-stdout")
	go forwardLogs(pipeStderr, "ollama-stderr")

	s.mu.Lock()
	s.cmd = cmd
	s.cancelCtx = cancel
	s.boundPort = port
	s.mu.Unlock()
	s.alive.Store(true)

	// Watch the process so Healthy() flips on exit.
	go func() {
		_ = cmd.Wait()
		s.alive.Store(false)
		slog.Warn("ollama exited; local provider is now unhealthy. Restart felix to recover.")
	}()

	timeout := s.readyTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	if err := s.waitReady(ctx, port, timeout); err != nil {
		_ = s.Stop()
		return err
	}
	slog.Info("ollama supervisor ready", "port", port, "models_dir", s.modelsDir)
	return nil
}

func (s *Supervisor) waitReady(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/api/version", port)
	client := &http.Client{Timeout: 1 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if !s.alive.Load() {
			return fmt.Errorf("ollama: process exited during startup")
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return ErrNotReady
}

// forwardLogs pipes a child reader into slog at debug level, line by line.
func forwardLogs(r interface{ Read([]byte) (int, error) }, tag string) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			slog.Debug(tag, "msg", string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}
```

(Stop is added in Task 8 — for now, add a stub so the code compiles.)

```go
// Stop is implemented in the next task.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelCtx != nil {
		s.cancelCtx()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/local/ -v
```

Expected: PASS. The fake-ollama tests require Python 3 on PATH; if your CI lacks Python, replace the script with a tiny Go helper main (out of scope for this task).

- [ ] **Step 5: Commit**

```bash
git add internal/local/supervisor.go internal/local/supervisor_test.go
git commit -m "feat: add Supervisor.Start with port probe and readiness poll"
```

---

## Task 8: Supervisor.Stop — SIGTERM → 5s → SIGKILL

**Files:**
- Modify: `internal/local/supervisor.go`
- Modify: `internal/local/supervisor_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/local/supervisor_test.go`:

```go
func TestSupervisorStopGraceful(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeOllama(t, dir)
	s := New(Options{BinPath: bin, ModelsDir: dir})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, s.Start(ctx))

	start := time.Now()
	require.NoError(t, s.Stop())
	elapsed := time.Since(start)

	assert.False(t, s.Healthy())
	assert.Less(t, elapsed, 5*time.Second, "fake binary should exit on SIGTERM well under the 5s grace")
}

func TestSupervisorStopEscalatesToKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("trap-SIGTERM script is POSIX-only")
	}
	// A binary that ignores SIGTERM, forcing escalation to SIGKILL.
	dir := t.TempDir()
	bin := filepath.Join(dir, "stubborn")
	body := `#!/bin/sh
trap '' TERM
HOST="${OLLAMA_HOST:-127.0.0.1:0}"
PORT="${HOST##*:}"
exec /usr/bin/env python3 - <<PY
import http.server, socketserver, signal
signal.signal(signal.SIGTERM, signal.SIG_IGN)
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/api/version":
            self.send_response(200); self.end_headers(); self.wfile.write(b'{}')
        else:
            self.send_response(404); self.end_headers()
    def log_message(self, *a, **k): pass
with socketserver.TCPServer(("127.0.0.1", ${PORT}), H) as srv:
    srv.serve_forever()
PY
`
	require.NoError(t, os.WriteFile(bin, []byte(body), 0o755))
	s := New(Options{BinPath: bin, ModelsDir: dir})
	s.stopGrace = 500 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, s.Start(ctx))

	start := time.Now()
	require.NoError(t, s.Stop())
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 500*time.Millisecond, "should wait the grace before escalating")
	assert.Less(t, elapsed, 3*time.Second, "should escalate to SIGKILL promptly")
	assert.False(t, s.Healthy())
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/local/ -run TestSupervisorStop -v
```

Expected: FAIL — `stopGrace` undefined; current `Stop` calls `Kill` immediately so the timing assertion fails.

- [ ] **Step 3: Implement graceful Stop**

In `internal/local/supervisor.go`, add the `stopGrace` field:

```go
	stopGrace    time.Duration // 0 → 5s
```

Add the `syscall` import. Replace the stub `Stop` with:

```go
// Stop sends SIGTERM, waits stopGrace, then SIGKILL. Idempotent.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	cmd := s.cmd
	cancel := s.cancelCtx
	s.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	grace := s.stopGrace
	if grace == 0 {
		grace = 5 * time.Second
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait() // returns once the process is reaped (already started in Start)
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(grace):
		_ = cmd.Process.Kill()
		<-exited
	}

	if cancel != nil {
		cancel()
	}
	s.alive.Store(false)
	return nil
}
```

(`Wait` was already called by the goroutine in `Start`. The second `Wait` here returns the cached state immediately; on POSIX `Wait` is safe to "double-call" because we are observing a closed channel rather than the syscall itself. If you prefer, refactor `Start` to expose a `done` channel and reuse it.)

Refactor recommendation — replace the goroutine in `Start` with:

```go
	s.exited = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		s.alive.Store(false)
		close(s.exited)
		slog.Warn("ollama exited; local provider is now unhealthy. Restart felix to recover.")
	}()
```

And add `exited chan struct{}` to the `Supervisor` struct. Then `Stop` waits on `s.exited` instead of calling `cmd.Wait()` itself:

```go
	select {
	case <-s.exited:
	case <-time.After(grace):
		_ = cmd.Process.Kill()
		<-s.exited
	}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/local/ -v
```

Expected: PASS for `TestSupervisorStop*` and all earlier tests.

- [ ] **Step 5: Commit**

```bash
git add internal/local/supervisor.go internal/local/supervisor_test.go
git commit -m "feat: add Supervisor.Stop with SIGTERM/SIGKILL escalation"
```

---

## Task 9: Crash flips Healthy() to false (no restart)

**Files:**
- Modify: `internal/local/supervisor_test.go`

This is a behavioral test confirming the deliberate non-feature: no restart on crash.

- [ ] **Step 1: Write the failing test**

Append to `internal/local/supervisor_test.go`:

```go
func TestSupervisorCrashLeavesUnhealthy(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeOllama(t, dir)
	s := New(Options{BinPath: bin, ModelsDir: dir})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, s.Start(ctx))
	require.True(t, s.Healthy())

	// Kill the child outside the supervisor.
	require.NoError(t, s.cmd.Process.Kill())

	// Wait for the supervisor goroutine to observe the exit.
	require.Eventually(t, func() bool { return !s.Healthy() }, 3*time.Second, 50*time.Millisecond)

	// Confirm BoundPort is still reported (the supervisor doesn't clear it on crash).
	assert.NotZero(t, s.BoundPort())
}
```

- [ ] **Step 2: Run test to verify it passes**

If Tasks 7–8 used the `s.exited` channel pattern, this test should pass without code changes (it verifies the existing behaviour is correct):

```bash
go test ./internal/local/ -run TestSupervisorCrash -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/local/supervisor_test.go
git commit -m "test: confirm supervisor leaves provider unhealthy after crash (no restart)"
```

---

## Task 10: Provider-injection helper

**Files:**
- Create: `internal/local/config.go`
- Create: `internal/local/config_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/local/config_test.go`:

```go
package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/config"
)

func TestInjectLocalProviderWritesBlock(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "felix.json5")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`{
  "agents": { "list": [{"id":"default","model":"openai/gpt-5.4"}] }
}`), 0o600))

	require.NoError(t, InjectLocalProvider(cfgPath, 18790))

	raw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	providers := got["providers"].(map[string]any)
	local := providers["local"].(map[string]any)
	assert.Equal(t, "local", local["kind"])
	assert.Equal(t, "http://127.0.0.1:18790/v1", local["base_url"])
}

func TestInjectLocalProviderUpdatesPort(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "felix.json5")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`{
  "agents": { "list": [{"id":"default","model":"local/qwen2.5:0.5b"}] },
  "providers": { "local": { "kind":"local", "base_url":"http://127.0.0.1:18790/v1" } }
}`), 0o600))

	require.NoError(t, InjectLocalProvider(cfgPath, 18793))

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:18793/v1", cfg.Providers["local"].BaseURL)
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/local/ -run TestInjectLocalProvider -v
```

Expected: FAIL — `InjectLocalProvider` undefined.

- [ ] **Step 3: Implement InjectLocalProvider**

Create `internal/local/config.go`:

```go
package local

import (
	"fmt"

	"github.com/sausheong/felix/internal/config"
)

// InjectLocalProvider loads the felix config at cfgPath, ensures a `local`
// provider entry exists pointing at boundPort, and saves the file in place.
//
// Used at startup once the supervisor has successfully bound a port, so the
// rest of the runtime sees the correct base_url.
func InjectLocalProvider(cfgPath string, boundPort int) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("inject local provider: load: %w", err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.ProviderConfig{}
	}
	cfg.Providers["local"] = config.ProviderConfig{
		Kind:    "local",
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d/v1", boundPort),
	}
	cfg.SetPath(cfgPath)
	return cfg.Save()
}

// DefaultModelsDir returns the default OLLAMA_MODELS path under the felix data dir.
func DefaultModelsDir() string {
	return fmt.Sprintf("%s/ollama/models", config.DefaultDataDir())
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/local/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/local/config.go internal/local/config_test.go
git commit -m "feat: add InjectLocalProvider helper for runtime port writeback"
```

---

## Task 11: Add `local` case to llm.NewProvider

**Files:**
- Modify: `internal/llm/provider.go`
- Modify: `internal/llm/provider_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/llm/provider_test.go`:

```go
func TestNewProviderLocal(t *testing.T) {
	p, err := NewProvider("local", ProviderOptions{
		Kind:    "local",
		BaseURL: "http://127.0.0.1:18790/v1",
	})
	require.NoError(t, err)
	require.NotNil(t, p)
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/llm/ -run TestNewProviderLocal -v
```

Expected: FAIL — `unknown LLM provider kind: "local"`.

- [ ] **Step 3: Add the switch case**

In `internal/llm/provider.go`, in the switch inside `NewProvider`, add:

```go
	case "local":
		return NewOpenAIProvider("", opts.BaseURL), nil
```

Also adjust the BaseURL-fallback near the top so `kind == "local"` is preserved (it already is, because `Kind` is set; verify no regression).

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/llm/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/provider.go internal/llm/provider_test.go
git commit -m "feat: add 'local' provider case routing to OpenAI client"
```

---

## Task 12: `felix model` CLI subcommand

**Files:**
- Create: `cmd/felix/model_cmd.go`
- Create: `cmd/felix/model_cmd_test.go`
- Modify: `cmd/felix/main.go` (register the subcommand)

- [ ] **Step 1: Write the failing test**

Create `cmd/felix/model_cmd_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelListPrintsModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{"name": "qwen2.5:0.5b", "size": 394 << 20}},
		})
	}))
	defer srv.Close()

	var buf bytes.Buffer
	require.NoError(t, runModelList(context.Background(), srv.URL, &buf))
	out := buf.String()
	assert.Contains(t, out, "qwen2.5:0.5b")
	assert.Contains(t, out, "MB")
}

func TestModelStatusReportsBaseURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"version":"x"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	require.NoError(t, runModelStatus(context.Background(), srv.URL, &buf))
	assert.Contains(t, buf.String(), srv.URL)
	assert.Contains(t, buf.String(), "ready")
}

func TestModelRemoveCallsDelete(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/delete", r.URL.Path)
		called = true
		w.WriteHeader(200)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	require.NoError(t, runModelRemove(context.Background(), srv.URL, "qwen2.5:0.5b", &buf))
	assert.True(t, called)
	assert.Contains(t, strings.ToLower(buf.String()), "removed")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/felix/ -run TestModel -v
```

Expected: FAIL — functions undefined.

- [ ] **Step 3: Implement the subcommand and run* helpers**

Create `cmd/felix/model_cmd.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/local"
)

func modelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Manage models served by the bundled Ollama supervisor",
	}
	cmd.AddCommand(
		modelListCmd(),
		modelPullCmd(),
		modelRemoveCmd(),
		modelStatusCmd(),
	)
	return cmd
}

func defaultLocalBaseURL() string {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return "http://127.0.0.1:18790"
	}
	pc, ok := cfg.Providers["local"]
	if !ok || pc.BaseURL == "" {
		return "http://127.0.0.1:18790"
	}
	// Strip the trailing /v1 — the Ollama API uses bare endpoints.
	url := pc.BaseURL
	if len(url) > 3 && url[len(url)-3:] == "/v1" {
		url = url[:len(url)-3]
	}
	return url
}

func modelListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List models pulled into the bundled Ollama",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelList(cmd.Context(), defaultLocalBaseURL(), os.Stdout)
		},
	}
}

func runModelList(ctx context.Context, baseURL string, out io.Writer) error {
	inst := local.NewInstaller(baseURL)
	models, err := inst.List(ctx)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		fmt.Fprintln(out, "No models pulled. Try: felix model pull qwen2.5:0.5b")
		return nil
	}
	for _, m := range models {
		fmt.Fprintf(out, "%-30s %s\n", m.Name, humanizeBytes(m.SizeBytes))
	}
	return nil
}

func modelPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <name>",
		Short: "Pull a model into the bundled Ollama",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelPull(cmd.Context(), defaultLocalBaseURL(), args[0], os.Stdout)
		},
	}
}

func runModelPull(ctx context.Context, baseURL, name string, out io.Writer) error {
	inst := local.NewInstaller(baseURL)
	var lastDigest string
	return inst.Pull(ctx, name, func(ev local.ProgressEvent) {
		if ev.Total > 0 && ev.Digest != lastDigest {
			fmt.Fprintf(out, "\n%s %s\n", ev.Status, ev.Digest)
			lastDigest = ev.Digest
		}
		if ev.Total > 0 {
			pct := float64(ev.Completed) / float64(ev.Total) * 100
			fmt.Fprintf(out, "\r  %.1f%% (%s / %s)", pct,
				humanizeBytes(ev.Completed), humanizeBytes(ev.Total))
		} else if ev.Status != "" {
			fmt.Fprintf(out, "\n%s\n", ev.Status)
		}
	})
}

func modelRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Remove a model from the bundled Ollama",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelRemove(cmd.Context(), defaultLocalBaseURL(), args[0], os.Stdout)
		},
	}
}

func runModelRemove(ctx context.Context, baseURL, name string, out io.Writer) error {
	inst := local.NewInstaller(baseURL)
	if err := inst.Delete(ctx, name); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed %s\n", name)
	return nil
}

func modelStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show bundled-Ollama status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelStatus(cmd.Context(), defaultLocalBaseURL(), os.Stdout)
		},
	}
}

func runModelStatus(ctx context.Context, baseURL string, out io.Writer) error {
	fmt.Fprintf(out, "base_url:  %s\n", baseURL)
	fmt.Fprintf(out, "models_dir: %s\n", local.DefaultModelsDir())

	inst := local.NewInstaller(baseURL)
	if _, err := inst.List(ctx); err != nil {
		fmt.Fprintf(out, "status:    unreachable (%v)\n", err)
		return nil
	}
	fmt.Fprintln(out, "status:    ready")
	return nil
}

func humanizeBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
```

In `cmd/felix/main.go`, find where other subcommands are registered (look for `onboardCmd()` in the root command setup) and add:

```go
		modelCmd(),
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/felix/ -run TestModel -v
go build -o felix ./cmd/felix
./felix model --help
```

Expected: PASS for all three model tests; `--help` shows the four subcommands.

- [ ] **Step 5: Commit**

```bash
git add cmd/felix/model_cmd.go cmd/felix/model_cmd_test.go cmd/felix/main.go
git commit -m "feat: add 'felix model' CLI subcommand for pull/list/rm/status"
```

---

## Task 13: Wire supervisor into gateway startup

**Files:**
- Modify: `internal/startup/startup.go`

- [ ] **Step 1: Sketch the integration**

The supervisor must:
- Start before `InitProviders` so the bound port is known.
- Inject the `local` provider config at the bound port, then reload the in-memory `cfg`.
- Have its `Stop` method registered with the existing `cleanup` closure.

- [ ] **Step 2: Implement startup wiring**

Near the top of `internal/startup/startup.go`, add the import:

```go
	"github.com/sausheong/felix/internal/local"
```

Just after `cfg, err := config.Load(configPath)` succeeds (around line 177), add:

```go
	// Bundled-Ollama supervisor — start before InitProviders so the local
	// provider's BaseURL reflects the bound port.
	var localSup *local.Supervisor
	if cfg.Local.Enabled {
		exeDir, _ := os.Executable()
		exeDir = filepath.Dir(exeDir)
		bin, derr := local.Discover(exeDir)
		switch {
		case derr != nil:
			slog.Warn("bundled ollama not found; local provider disabled", "error", derr)
		default:
			modelsDir := cfg.Local.ModelsDir
			if modelsDir == "" {
				modelsDir = local.DefaultModelsDir()
			}
			_ = os.MkdirAll(modelsDir, 0o755)
			localSup = local.New(local.Options{
				BinPath:   bin,
				ModelsDir: modelsDir,
				KeepAlive: cfg.Local.KeepAlive,
			})
			startCtx, startCancel := context.WithTimeout(context.Background(), 70*time.Second)
			if err := localSup.Start(startCtx); err != nil {
				slog.Warn("failed to start bundled ollama; local provider disabled", "error", err)
				localSup = nil
			} else {
				if ierr := local.InjectLocalProvider(configPath, localSup.BoundPort()); ierr != nil {
					slog.Warn("failed to inject local provider config", "error", ierr)
				}
				// Re-load so InitProviders sees the local block.
				if reloaded, rerr := config.Load(configPath); rerr == nil {
					cfg.UpdateFrom(reloaded)
				}
			}
			startCancel()
		}
	}
```

In the `cleanup` closure (around line 483), add:

```go
		if localSup != nil {
			_ = localSup.Stop()
		}
```

(Place it before `chanMgr.Stop()` so channels are stopped after the LLM supervisor — order is not critical, but this matches the spec's "shutdown LLM child first" intent.)

- [ ] **Step 3: Build and run a smoke check**

```bash
go build -o felix ./cmd/felix
./felix start --help
```

Expected: build succeeds; help still works. (Full integration is covered by Task 19.)

- [ ] **Step 4: Commit**

```bash
git add internal/startup/startup.go
git commit -m "feat: integrate bundled Ollama supervisor into gateway startup"
```

---

## Task 14: Local-first onboarding wizard

**Files:**
- Modify: `cmd/felix/main.go`

- [ ] **Step 1: Inspect the current wizard layout**

Read `cmd/felix/main.go:1097-1300` (the `runOnboard` function). The current first step asks "Which LLM provider do you want to use?" with four options (Anthropic, OpenAI, Ollama, Custom).

- [ ] **Step 2: Add a local-first detection branch**

Add the helper near the top of `runOnboard`:

```go
	hasCloudKey := os.Getenv("OPENAI_API_KEY") != "" ||
		os.Getenv("ANTHROPIC_API_KEY") != "" ||
		os.Getenv("GEMINI_API_KEY") != ""
```

Where the function currently calls `choose("Which LLM provider do you want to use?", ...)`, wrap that with a local-first branch:

```go
	if !hasCloudKey {
		fmt.Println("No cloud API key found in your environment.")
		fmt.Println("Pick a local model to download (you can change this later):")
		fmt.Println()
		localChoice := choose("", []string{
			"Llama 4.1 8B Instruct      ~4.7 GB   (recommended — good general agent)",
			"Qwen 3.5 Coder 7B          ~4.0 GB   (best for code-heavy tasks)",
			"Gemma 4 E4B (multimodal)   ~5.5 GB   (vision-capable)",
			"Skip — I'll configure a cloud key later",
		}, 0)
		if localChoice != 3 {
			models := []string{
				"llama4.1:8b-instruct-q4_K_M",
				"qwen3.5-coder:7b",
				"gemma4:e4b",
			}
			modelTag := models[localChoice]
			if err := pullLocalModel(modelTag); err != nil {
				fmt.Printf("Pull failed: %v\n", err)
				fmt.Println("Falling back to cloud setup.")
			} else {
				cfg.Agents.List[0].Model = "local/" + modelTag
				cfg.Providers["local"] = config.ProviderConfig{
					Kind:    "local",
					BaseURL: "http://127.0.0.1:18790/v1",
				}
				return finishOnboard(cfg)
			}
		}
	}
```

Add the `pullLocalModel` helper at the bottom of the file:

```go
func pullLocalModel(name string) error {
	inst := local.NewInstaller("http://127.0.0.1:18790")
	fmt.Printf("Pulling %s...\n", name)
	var lastDigest string
	return inst.Pull(context.Background(), name, func(ev local.ProgressEvent) {
		if ev.Total > 0 && ev.Digest != lastDigest {
			fmt.Printf("\n%s %s\n", ev.Status, ev.Digest)
			lastDigest = ev.Digest
		}
		if ev.Total > 0 {
			pct := float64(ev.Completed) / float64(ev.Total) * 100
			fmt.Printf("\r  %.1f%% (%d / %d MB)", pct, ev.Completed>>20, ev.Total>>20)
		} else if ev.Status != "" {
			fmt.Printf("\n%s\n", ev.Status)
		}
	})
}
```

`finishOnboard` should wrap the existing "save config + print next steps" block at the end of `runOnboard`. Extract the existing tail of `runOnboard` (from "Save config" onward) into a new `finishOnboard(cfg *config.Config) error` function so both branches reuse it.

Add to imports if missing: `"github.com/sausheong/felix/internal/local"`.

- [ ] **Step 3: Build and run wizard manually**

```bash
go build -o felix ./cmd/felix
unset OPENAI_API_KEY ANTHROPIC_API_KEY GEMINI_API_KEY
./felix onboard
```

Expected: prompts with the four-option local model list. Choosing "Skip" falls through to the existing wizard.

(The pull will fail unless a Felix gateway is already running with the supervisor. For TDD purposes, manual smoke is enough at this stage; Task 19 covers end-to-end.)

- [ ] **Step 4: Commit**

```bash
git add cmd/felix/main.go
git commit -m "feat: add local-first option to onboarding wizard"
```

---

## Task 15: Postinstall — bundled-Ollama provider injection

**Files:**
- Modify: `installer/scripts/postinstall`

- [ ] **Step 1: Locate the bundled binary check**

The postinstall currently runs an osascript provider chooser when no config exists. We add a guard before that: if `bin/ollama` ships in the app bundle, skip the cloud-provider prompt and write the `local` provider directly.

- [ ] **Step 2: Update postinstall**

Insert after the `if [ ! -f "$CONFIG" ]; then` line (around line 40):

```bash
  # Bundled-Ollama path — skip the API-key prompt entirely.
  BUNDLED_OLLAMA="/Applications/Felix.app/Contents/Resources/bin/ollama"
  if [ -x "$BUNDLED_OLLAMA" ]; then
    cat > "$CONFIG" <<EOF
{
  "agents": {
    "list": [{
      "id": "default",
      "name": "Assistant",
      "workspace": "$FELIX_DIR/workspace-default",
      "model": "local/llama4.1:8b-instruct-q4_K_M",
      "sandbox": "none",
      "tools": { "allow": ["read_file","write_file","edit_file","bash","web_fetch","web_search","browser","send_message","cron"] }
    }]
  },
  "bindings": [{ "agentId": "default", "match": { "channel": "cli" } }],
  "channels": { "cli": { "enabled": true, "interactive": true } },
  "local": { "enabled": true, "keep_alive": "5m" },
  "providers": {
    "local": { "kind": "local", "base_url": "http://127.0.0.1:18790/v1" }
  }
}
EOF
    chown "$LOGGED_IN_USER":staff "$CONFIG"
    chmod 600 "$CONFIG"
    exit 0
  fi
```

The first `felix start` after install will run the supervisor, pull the model on first agent invocation (or via `felix model pull`), and continue.

- [ ] **Step 3: Validate the script syntax**

```bash
bash -n installer/scripts/postinstall
```

Expected: no output (clean parse).

- [ ] **Step 4: Commit**

```bash
git add installer/scripts/postinstall
git commit -m "feat: postinstall writes local provider when bundled ollama present"
```

---

## Task 16: Makefile — `ollama-fetch` target

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Add the target**

Append to `Makefile`:

```makefile
OLLAMA_VERSION ?= 0.5.7

## ollama-fetch: download platform Ollama binaries into bin/
ollama-fetch:
	mkdir -p bin
	@for plat in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do \
		os=$${plat%%/*}; arch=$${plat##*/}; \
		case "$$os/$$arch" in \
		  darwin/amd64)  url="https://github.com/ollama/ollama/releases/download/v$(OLLAMA_VERSION)/ollama-darwin"; out="ollama-darwin-amd64";; \
		  darwin/arm64)  url="https://github.com/ollama/ollama/releases/download/v$(OLLAMA_VERSION)/ollama-darwin"; out="ollama-darwin-arm64";; \
		  linux/amd64)   url="https://github.com/ollama/ollama/releases/download/v$(OLLAMA_VERSION)/ollama-linux-amd64"; out="ollama-linux-amd64";; \
		  linux/arm64)   url="https://github.com/ollama/ollama/releases/download/v$(OLLAMA_VERSION)/ollama-linux-arm64"; out="ollama-linux-arm64";; \
		  windows/amd64) url="https://github.com/ollama/ollama/releases/download/v$(OLLAMA_VERSION)/ollama-windows-amd64.exe"; out="ollama-windows-amd64.exe";; \
		esac; \
		echo "Fetching $$out from $$url..."; \
		curl -L -o bin/$$out "$$url" || exit 1; \
		chmod +x bin/$$out 2>/dev/null || true; \
	done
	cd bin && shasum -a 256 ollama-* > ../OLLAMA-SHA256SUMS
	@echo "Pinned Ollama binaries in bin/, checksums in OLLAMA-SHA256SUMS"
```

(Verify the exact asset names against the chosen `OLLAMA_VERSION` release page before merging — naming may have changed.)

Add `ollama-fetch` to the `.PHONY` list at the top of the file.

- [ ] **Step 2: Validate**

```bash
make -n ollama-fetch
```

Expected: prints the for-loop without executing.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "build: add ollama-fetch makefile target with version pin"
```

---

## Task 17: Makefile — bundle Ollama into release zips

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Adjust `build-release`**

In the `build-release` target, after the `go build -o $(RELEASE_DIR)/$$name/$(BINARY)$$ext` line and before the `zip` line, add:

```bash
		case "$$os/$$arch" in \
		  darwin/amd64)  oll="ollama-darwin-amd64";; \
		  darwin/arm64)  oll="ollama-darwin-arm64";; \
		  linux/amd64)   oll="ollama-linux-amd64";; \
		  linux/arm64)   oll="ollama-linux-arm64";; \
		  windows/amd64) oll="ollama-windows-amd64.exe";; \
		esac; \
		if [ -f "bin/$$oll" ]; then \
		  mkdir -p $(RELEASE_DIR)/$$name/bin; \
		  ext2=""; if [ "$$os" = "windows" ]; then ext2=".exe"; fi; \
		  cp bin/$$oll $(RELEASE_DIR)/$$name/bin/ollama$$ext2; \
		fi; \
```

Make `build-release` depend on `ollama-fetch` so the binaries exist:

```makefile
build-release: ollama-fetch
```

- [ ] **Step 2: Validate**

```bash
make -n build-release
```

Expected: prints the loop with the new copy step.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "build: bundle ollama into per-platform release zips"
```

---

## Task 18: Makefile — bundle Ollama into Felix.app

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Update `build-app`, `installer`, and `sign`**

In `build-app`, after the `cp ... icon.icns` line:

```makefile
	mkdir -p Felix.app/Contents/Resources/bin
	@if [ -f bin/ollama-darwin-arm64 ]; then \
	  cp bin/ollama-darwin-arm64 Felix.app/Contents/Resources/bin/ollama; \
	  chmod +x Felix.app/Contents/Resources/bin/ollama; \
	fi
```

Wire `build-app: ollama-fetch` so the binary is fetched when needed:

```makefile
build-app: ollama-fetch
```

In `installer` and `sign`, after the `cp -r Felix.app installer/payload/Applications/Felix.app` line:

```makefile
	@if [ -f bin/ollama-darwin-arm64 ]; then \
	  mkdir -p installer/payload/Applications/Felix.app/Contents/Resources/bin; \
	  cp bin/ollama-darwin-arm64 installer/payload/Applications/Felix.app/Contents/Resources/bin/ollama; \
	fi
```

(The `cp -r Felix.app` already includes the bundled binary if `build-app` ran first; the explicit copy here is defensive.)

- [ ] **Step 2: Validate**

```bash
make -n build-app installer
```

Expected: prints the new mkdir/copy lines.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "build: bundle ollama into Felix.app for macOS installer"
```

---

## Task 19: Integration test — real Ollama against a tiny model

**Files:**
- Create: `internal/local/integration_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/local/integration_test.go`:

```go
//go:build local

package local

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Run with: FELIX_OLLAMA_BIN=$(which ollama) go test -tags local -v ./internal/local/
func TestIntegrationStartAndPullTinyModel(t *testing.T) {
	bin := os.Getenv("FELIX_OLLAMA_BIN")
	if bin == "" {
		t.Skip("set FELIX_OLLAMA_BIN to run integration test")
	}
	dir := t.TempDir()
	s := New(Options{BinPath: bin, ModelsDir: dir})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, s.Start(ctx))
	defer s.Stop()

	port := s.BoundPort()
	require.NotZero(t, port)

	// Hit the version endpoint to confirm the OpenAI-compat surface is mounted.
	resp, err := http.Get(versionURL(port))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	// Pull a very small model and verify it shows up in /api/tags.
	inst := NewInstaller(baseURL(port))
	pullCtx, pullCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer pullCancel()
	require.NoError(t, inst.Pull(pullCtx, "qwen2.5:0.5b", nil))

	models, err := inst.List(context.Background())
	require.NoError(t, err)
	found := false
	for _, m := range models {
		if m.Name == "qwen2.5:0.5b" {
			found = true
		}
	}
	require.True(t, found, "expected qwen2.5:0.5b in /api/tags")
}

func versionURL(port int) string {
	return baseURL(port) + "/api/version"
}

func baseURL(port int) string {
	return "http://127.0.0.1:" + itoa(port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
```

- [ ] **Step 2: Run the integration test (CI gates this off by default)**

```bash
which ollama  # confirm a real ollama is available
FELIX_OLLAMA_BIN=$(which ollama) go test -tags local -v ./internal/local/ -run TestIntegration
```

Expected: PASS (downloads ~400 MB on first run).

- [ ] **Step 3: Commit**

```bash
git add internal/local/integration_test.go
git commit -m "test: add integration test for bundled Ollama against tiny model"
```

---

## Task 20: README + howtouse documentation

**Files:**
- Modify: `README.md`
- Modify: `howtouse.md`

- [ ] **Step 1: Add a "Local LLM (bundled Ollama)" section to README.md**

Insert after the "Quick Start" section:

```markdown
### Bundled local LLM (no API key needed)

Felix ships with a bundled Ollama binary so you can run agents offline with
no API key. On first run, the wizard offers a curated list of local models;
pick one and Felix will download it (~4–6 GB depending on model).

To pull additional models later:

```bash
felix model pull qwen2.5:7b-instruct
felix model list
felix model status
```

The bundled Ollama runs as a child of Felix on `127.0.0.1:18790` (next
free port in `:18790–:18799`) and shuts down when Felix exits. It does
not interfere with any system Ollama you may have on `:11434`.
```

- [ ] **Step 2: Add a "Using the bundled local model" section to howtouse.md**

Append to `howtouse.md`:

```markdown
## Using the bundled local model

Felix ships with a bundled Ollama runtime so you can run agents fully
offline. If you launch Felix with no `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`,
or `GEMINI_API_KEY` set, the onboarding wizard offers three curated local
models:

| Choice | Size | Best for |
|---|---|---|
| Llama 4.1 8B Instruct | ~4.7 GB | General agent tasks (recommended default) |
| Qwen 3.5 Coder 7B | ~4.0 GB | Code-heavy tool use |
| Gemma 4 E4B (multimodal) | ~5.5 GB | Tasks involving images |

After picking a model, Felix downloads it via the bundled Ollama. The
download runs once; subsequent starts reuse the cached weights in
`~/.felix/ollama/models/`.

### Switching between local and cloud

The agent's model is set in `~/.felix/felix.json5` under
`agents.list[*].model`. Use the `local/` prefix for the bundled runtime,
or any of the cloud prefixes:

```json5
{
  "agents": {
    "list": [
      { "id": "default", "model": "local/llama4.1:8b-instruct-q4_K_M" },
      { "id": "online",  "model": "openai/gpt-5.4" }
    ]
  }
}
```

### Adding more local models

Any model in the Ollama registry can be pulled into Felix's bundled
runtime:

```bash
felix model pull qwen2.5:7b-instruct     # add another model
felix model list                         # see what's installed
felix model status                       # check the supervisor
felix model rm gemma4:e4b                # free disk space
```

### Coexistence with system Ollama

Felix's bundled Ollama runs on `127.0.0.1:18790` (or the next free port
in the range `:18790–:18799`) — it does **not** touch a system Ollama on
the default `:11434`. If you already use Ollama for other tools, both
keep running side by side.
```

- [ ] **Step 3: Commit**

```bash
git add README.md howtouse.md
git commit -m "docs: document bundled-ollama feature in README and howtouse"
```

---

## Final verification

- [ ] **Step 1: Full test suite**

```bash
go test -race ./...
golangci-lint run
```

Expected: all green.

- [ ] **Step 2: Manual smoke**

```bash
unset OPENAI_API_KEY ANTHROPIC_API_KEY GEMINI_API_KEY
make ollama-fetch
make build
./felix onboard
# pick "Llama 4.1 8B" — should pull, write config
./felix start
# in another terminal:
./felix model status
./felix model list
```

- [ ] **Step 3: Final commit (if any cleanup needed)**

```bash
git status
# expect clean tree
```
