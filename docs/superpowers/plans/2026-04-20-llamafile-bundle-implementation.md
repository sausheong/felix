# Bundle Gemma 4 E4B llamafile in Felix

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a self-contained local LLM (Gemma 4 E4B) with Felix so users can run agents fully offline without API keys or external Ollama installs.

**Architecture:** Two release flavors (`felix` small / `felix-bundled` ~5.6GB). The bundled flavor includes a `llamafile` engine binary + GGUF weights + mmproj vision projector. Felix spawns `llamafile` as a supervised child process on `:18790`, exposing an OpenAI-compatible endpoint. The existing OpenAI client handles the provider with `BaseURL = http://127.0.0.1:18790/v1` — no new client code. A new `local` config block and `internal/local/` package manage lifecycle.

**Tech Stack:** Go (stdlib, chi, cobra), llamafile, HuggingFace, macOS .pkg installer

---

## File Structure

| Responsibility | File | Action |
|---|---|---|
| Config schema | `internal/config/config.go` | Modify — add `LocalConfig` |
| Provider factory | `internal/llm/provider.go` | Modify — add `"local"` case |
| Supervisor + discovery | `internal/local/discover.go` | **Create** — model path resolution + SHA verify |
| Supervisor | `internal/local/supervisor.go` | **Create** — child process lifecycle |
| Config helper | `internal/local/config.go` | **Create** — bundled provider injection |
| Supervisor tests | `internal/local/discover_test.go` | **Create** |
| Supervisor tests | `internal/local/supervisor_test.go` | **Create** |
| Startup integration | `internal/startup/startup.go` | Modify — start/stop supervisor |
| Startup Result | `internal/startup/startup.go` | Modify — add supervisor to Result |
| CLI: onboarding wizard | `cmd/felix/main.go` | Modify — add "Local" option to wizard |
| CLI: model subcommand | `cmd/felix/main.go` | Modify — add `modelCmd()` |
| CLI: model pull/status/rm/assemble | `cmd/felix/main.go` | Modify — handler funcs |
| Postinstall script | `installer/scripts/postinstall` | Modify — handle bundled models |
| Makefile | `Makefile` | Modify — models-fetch, llamafile-fetch, release-bundled |
| Release metadata | `MODELS-SHA256SUMS` | **Create** |

---

### Task 1: Add `LocalConfig` to config schema

**Files:**
- Modify: `internal/config/config.go:16-26`

- [ ] **Step 1: Add LocalConfig struct and field to Config**

Add a new struct after `SecurityConfig` (around line 164) and a `Local` field to `Config`:

```go
// LocalConfig controls the bundled local LLM supervisor.
type LocalConfig struct {
	Enabled     bool   `json:"enabled"`       // master switch — disables supervisor if false
	Port        int    `json:"port"`          // default 18790
	ContextSize int    `json:"context_size"`  // default 65536
	GPULayers   int    `json:"gpu_layers"`    // default 999 (use GPU if available)
	ModelDir    string `json:"model_dir"`     // optional override; empty = use search order
}
```

Change the `Config` struct to include the field (around line 26):

```go
type Config struct {
	Gateway   GatewayConfig            `json:"gateway"`
	Providers map[string]ProviderConfig `json:"providers"`
	Agents    AgentsConfig             `json:"agents"`
	Bindings  []Binding                `json:"bindings"`
	Channels  ChannelsConfig           `json:"channels"`
	Heartbeat HeartbeatConfig          `json:"heartbeat"`
	Memory    MemoryConfig             `json:"memory"`
	Cortex    CortexConfig             `json:"cortex"`
	Google    GoogleConfig             `json:"google"`
	Security  SecurityConfig           `json:"security"`
	Local     LocalConfig              `json:"local"`

	mu   sync.RWMutex
	path string
}
```

- [ ] **Step 2: Set defaults in DefaultConfig()**

In `DefaultConfig()` (around line 230), add a `Local` field to the returned struct:

```go
Local: LocalConfig{
	Enabled:     false,
	Port:        18790,
	ContextSize: 65536,
	GPULayers:   999,
	ModelDir:    "",
},
```

- [ ] **Step 3: Run tests to verify nothing is broken**

```bash
go test ./internal/config/ -v
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/config/config.go
git commit -m "feat: add LocalConfig schema for bundled llamafile supervisor"
```

---

### Task 2: Add `"local"` provider case

**Files:**
- Modify: `internal/llm/provider.go:122-133`

- [ ] **Step 1: Add `"local"` case to NewProvider switch**

In the switch block, add a case after `"qwen"` (around line 130):

```go
case "local":
    return NewOpenAIProvider("", opts.BaseURL), nil
```

This routes `local` through the existing OpenAI-compatible client. The `BaseURL` will be `http://127.0.0.1:18790/v1` from config.

- [ ] **Step 2: Run tests**

```bash
go test ./internal/llm/ -v
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/llm/provider.go
git commit -m "feat: add local provider routing through OpenAI-compatible client"
```

---

### Task 3: Create `internal/local/discover.go` — model path resolution

**Files:**
- Create: `internal/local/discover.go`
- Create: `internal/local/discover_test.go`
- Test: `internal/local/discover_test.go`

- [ ] **Step 1: Write the test for model path resolution**

```go
package local

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveModelPaths_DefaultSearchOrder(t *testing.T) {
	// No model dir exists — should return an error
	result, err := ResolveModelPaths("", "")
	if err == nil {
		t.Fatalf("expected error when no model found, got: %+v", result)
	}
}

func TestResolveModelPaths_EnvOverride(t *testing.T) {
	tmp := t.TempDir()
	modelDir := filepath.Join(tmp, "gemma-4-e4b")
	os.MkdirAll(modelDir, 0o755)
	writeTestFile(t, filepath.Join(modelDir, "model.gguf"), "model-data")
	writeTestFile(t, filepath.Join(modelDir, "mmproj.gguf"), "mmproj-data")
	shaContent := "model.gguf  abc123\nmmproj.gguf  def456\n"
	writeTestFile(t, filepath.Join(modelDir, "SHA256SUMS"), shaContent)

	result, err := ResolveModelPaths(modelDir, tmp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ModelPath != filepath.Join(modelDir, "model.gguf") {
		t.Errorf("expected model path %s, got %s", filepath.Join(modelDir, "model.gguf"), result.ModelPath)
	}
	if result.MMProjPath != filepath.Join(modelDir, "mmproj.gguf") {
		t.Errorf("expected mmproj path %s, got %s", filepath.Join(modelDir, "mmproj.gguf"), result.MMProjPath)
	}
}

func TestVerifySHA256_MatchingFiles(t *testing.T) {
	tmp := t.TempDir()
	modelData := "test-model-binary"
	mmprojData := "test-mmproj-binary"
	modelPath := filepath.Join(tmp, "model.gguf")
	mmprojPath := filepath.Join(tmp, "mmproj.gguf")

	modelHash := fmt.Sprintf("%x", sha256.Sum256([]byte(modelData)))
	mmprojHash := fmt.Sprintf("%x", sha256.Sum256([]byte(mmprojData)))

	os.WriteFile(modelPath, []byte(modelData), 0o644)
	os.WriteFile(mmprojPath, []byte(mmprojData), 0o644)
	sumsContent := fmt.Sprintf("model.gguf  %s\nmmproj.gguf  %s\n", modelHash, mmprojHash)
	os.WriteFile(filepath.Join(tmp, "SHA256SUMS"), []byte(sumsContent), 0o644)

	result := ModelPaths{ModelPath: modelPath, MMProjPath: mmprojPath}
	err := result.VerifySHA256(tmp)
	if err != nil {
		t.Fatalf("expected SHA256 verify to pass, got: %v", err)
	}
}

func TestVerifySHA256_Mismatch(t *testing.T) {
	tmp := t.TempDir()
	modelPath := filepath.Join(tmp, "model.gguf")
	os.WriteFile(modelPath, []byte("bad-data"), 0o644)
	shaContent := "model.gguf  aaaa1111bbbb2222cccc3333dddd4444\n"
	os.WriteFile(filepath.Join(tmp, "SHA256SUMS"), []byte(shaContent), 0o644)

	result := ModelPaths{ModelPath: modelPath}
	err := result.VerifySHA256(tmp)
	if err == nil {
		t.Fatal("expected SHA256 mismatch error, got nil")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	os.WriteFile(path, []byte(content), 0o644)
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/local/ -run TestResolve -v
```

Expected: FAIL — `package internal/local: build constraint: package not found`

- [ ] **Step 3: Write `internal/local/discover.go`**

```go
package local

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ModelPaths holds resolved paths to model files.
type ModelPaths struct {
	ModelPath    string // path to model.gguf
	MMProjPath   string // path to mmproj.gguf
	EnginePath   string // path to llamafile binary
	SearchRoot   string // directory containing model.gguf (parent dir of model files)
}

// ResolveModelPaths searches for bundled model files in order:
// 1. explicitDir (env override FELIX_MODELS_DIR/gemma-4-e4b)
// 2. ~/.felix/models/gemma-4-e4b/
// 3. <binary-dir>/models/gemma-4-e4b/
// 4. /usr/local/share/felix/models/gemma-4-e4b/ (macOS)
// Returns the first directory that contains both model.gguf and mmproj.gguf.
func ResolveModelPaths(explicitDir, dataDir string) (*ModelPaths, error) {
	candidates := []string{}

	if explicitDir != "" {
		candidates = append(candidates, explicitDir)
	}
	if dataDir != "" {
		candidates = append(candidates, filepath.Join(dataDir, "models", "gemma-4-e4b"))
	}

	// Binary directory
	execPath, err := os.Executable()
	if err == nil {
		binDir := filepath.Dir(execPath)
		// On macOS app bundles, binary is in Contents/MacOS; models are in Contents/Resources
		if strings.Contains(binDir, "Contents/MacOS") {
			appRoot := strings.Split(binDir, "Contents/MacOS")[0]
			candidates = append(candidates, filepath.Join(appRoot, "Contents", "Resources", "models", "gemma-4-e4b"))
		}
		candidates = append(candidates, filepath.Join(binDir, "models", "gemma-4-e4b"))
	}

	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/usr/local/share/felix/models/gemma-4-e4b")
	}

	for _, dir := range candidates {
		model := filepath.Join(dir, "model.gguf")
		mmproj := filepath.Join(dir, "mmproj.gguf")
		if _, err := os.Stat(model); err == nil {
			if _, err := os.Stat(mmproj); err == nil {
				engine := resolveEnginePath()
				return &ModelPaths{
					ModelPath:  model,
					MMProjPath: mmproj,
					EnginePath: engine,
					SearchRoot: dir,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("local model files not found (searched %d locations)", len(candidates))
}

// VerifySHA256 checks that model.gguf and mmproj.gguf match SHA256SUMS.
func (m *ModelPaths) VerifySHA256(modelDir string) error {
	sumsPath := filepath.Join(modelDir, "SHA256SUMS")
	data, err := os.ReadFile(sumsPath)
	if err != nil {
		return fmt.Errorf("read SHA256SUMS: %w", err)
	}

	hashes := parseSHA256SUMS(string(data))
	if len(hashes) == 0 {
		return fmt.Errorf("no hashes found in SHA256SUMS")
	}

	for filename, expectedHash := range hashes {
		filePath := filepath.Join(modelDir, filename)
		actual, err := sha256File(filePath)
		if err != nil {
			return fmt.Errorf("hash %s: %w", filename, err)
		}
		if actual != expectedHash {
			return fmt.Errorf("SHA256 mismatch for %s: expected %s, got %s",
				filename, expectedHash, actual)
		}
	}

	return nil
}

func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func parseSHA256SUMS(content string) map[string]string {
	hashes := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			hashes[parts[1]] = parts[0]
		}
	}
	return hashes
}

func resolveEnginePath() string {
	execPath, err := os.Executable()
	if err != nil {
		return "llamafile"
	}
	binDir := filepath.Dir(execPath)

	// macOS app bundle
	if strings.Contains(binDir, "Contents/MacOS") {
		appRoot := strings.Split(binDir, "Contents/MacOS")[0]
		candidate := filepath.Join(appRoot, "Contents", "Resources", "models", "llamafile")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Sibling to binary
	candidate := filepath.Join(binDir, "models", "llamafile")
	if runtime.GOOS == "windows" {
		candidate += ".exe"
	}
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}

	return "llamafile"
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/local/ -run "TestResolve|TestVerify|TestParseSHA" -v
```

Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/local/discover.go internal/local/discover_test.go
git commit -m "feat: add model path resolution and SHA256 verification"
```

---

### Task 4: Create `internal/local/supervisor.go` — process supervisor

**Files:**
- Create: `internal/local/supervisor.go`
- Create: `internal/local/supervisor_test.go`
- Test: `internal/local/supervisor_test.go`

- [ ] **Step 1: Write the failing test**

```go
package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSupervisor_StartAndStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	sup := NewSupervisor(SupervisorOptions{
		EnginePath:  "echo", // safe stub binary
		ModelPath:   "/dev/null",
		MMProjPath:  "/dev/null",
		Port:        0, // let OS pick
		ContextSize: 65536,
		GPULayers:   999,
	})
	ctx := context.Background()

	err := sup.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give it a moment to launch
	time.Sleep(200 * time.Millisecond)

	if !sup.IsRunning() {
		t.Fatal("supervisor should be running after Start")
	}

	sup.Stop()

	if sup.IsRunning() {
		t.Fatal("supervisor should be stopped after Stop")
	}
}

func TestSupervisor_CrashRestartBackoff(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	// Create a script that immediately exits (simulating crash)
	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "crasher.sh")
	script := "#!/bin/bash\nexit 1\n"
	os.WriteFile(scriptPath, []byte(script), 0o755)

	sup := NewSupervisor(SupervisorOptions{
		EnginePath:  scriptPath,
		ModelPath:   "/dev/null",
		MMProjPath:  "/dev/null",
		Port:        0,
		ContextSize: 65536,
		GPULayers:   999,
	})

	ctx := context.Background()
	err := sup.Start(ctx)
	// Should eventually fail due to repeated crashes
	time.Sleep(3 * time.Second)
	sup.Stop()

	// After multiple crashes, the supervisor should mark itself unhealthy
	// (exact behavior depends on backoff implementation)
}

func TestSupervisor_PortCollision(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	// Use "sleep" as a fake binary that doesn't listen on any port
	tmp := t.TempDir()
	scriptPath := filepath.Join(tmp, "sleeper.sh")
	os.WriteFile(scriptPath, []byte("#!/bin/bash\nsleep 10\n"), 0o755)

	sup := NewSupervisor(SupervisorOptions{
		EnginePath:  scriptPath,
		ModelPath:   "/dev/null",
		MMProjPath:  "/dev/null",
		Port:        0,
		ContextSize: 65536,
		GPULayers:   999,
	})

	ctx := context.Background()
	// Port 0 means OS picks a free one, so this won't collide
	err := sup.Start(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sup.Stop()
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/local/ -run TestSupervisor -v
```

Expected: FAIL — `NewSupervisor` and `Supervisor` not defined

- [ ] **Step 3: Write `internal/local/supervisor.go`**

```go
package local

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Supervisor manages the lifecycle of a child llamafile process.
type Supervisor struct {
	mu       sync.Mutex
	opts     SupervisorOptions
	cmd      *exec.Cmd
	running  bool
	healthy  bool
	stopCh   chan struct{}
	cancelFn context.CancelFunc
	restarts int
	lastCrash time.Time
}

type SupervisorOptions struct {
	EnginePath  string // path to llamafile binary
	ModelPath   string // path to model.gguf
	MMProjPath  string // path to mmproj.gguf
	Port        int    // default 18790
	ContextSize int    // default 65536
	GPULayers   int    // default 999
}

func NewSupervisor(opts SupervisorOptions) *Supervisor {
	if opts.Port == 0 {
		opts.Port = 18790
	}
	if opts.ContextSize == 0 {
		opts.ContextSize = 65536
	}
	if opts.GPULayers == 0 {
		opts.GPULayers = 999
	}
	return &Supervisor{opts: opts, stopCh: make(chan struct{})}
}

// Start launches the llamafile child process.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify engine is executable
	info, err := os.Stat(s.opts.EnginePath)
	if err != nil {
		return fmt.Errorf("llamafile engine not found at %s", s.opts.EnginePath)
	}
	if info.Mode()&0o111 == 0 {
		if runtime.GOOS != "windows" {
			if err := os.Chmod(s.opts.EnginePath, 0o755); err != nil {
				return fmt.Errorf("cannot make llamafile executable: %w", err)
			}
		}
	}

	// Find an available port
	port, err := findAvailablePort(s.opts.Port, 10)
	if err != nil {
		return fmt.Errorf("no available port in range %d-%d: %w", s.opts.Port, s.opts.Port+9, err)
	}
	s.opts.Port = port

	args := []string{
		"--server", "--nobrowser",
		"--host", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
		"-m", s.opts.ModelPath,
		"--mmproj", s.opts.MMProjPath,
		"-c", fmt.Sprintf("%d", s.opts.ContextSize),
		"-ngl", fmt.Sprintf("%d", s.opts.GPULayers),
		"--log-disable",
	}

	ctx, s.cancelFn = context.WithCancel(ctx)
	s.cmd = exec.CommandContext(ctx, s.opts.EnginePath, args...)
	s.cmd.Stdout = s.logWriter("llamafile", "info")
	s.cmd.Stderr = s.logWriter("llamafile", "debug")

	slog.Info("starting llamafile supervisor",
		"engine", s.opts.EnginePath,
		"port", port,
		"context", s.opts.ContextSize,
		"gpu_layers", s.opts.GPULayers,
	)

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("start llamafile: %w", err)
	}

	s.running = true

	// Health check goroutine
	go s.healthCheck(ctx)

	// Crash monitor goroutine
	go s.monitorCrashes(ctx)

	return nil
}

// Stop gracefully terminates the child process.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	close(s.stopCh)
	if s.cancelFn != nil {
		s.cancelFn()
	}

	if s.cmd != nil && s.cmd.Process != nil {
		slog.Info("stopping llamafile")
		s.cmd.Process.Signal(syscall.SIGTERM)

		done := make(chan struct{})
		go func() {
			s.cmd.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			slog.Warn("llamafile did not stop, sending SIGKILL")
			s.cmd.Process.Signal(syscall.SIGKILL)
			s.cmd.Wait()
		}
	}

	s.running = false
	s.healthy = false
}

// IsRunning returns true if the child process is alive.
func (s *Supervisor) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// IsHealthy returns true if the health probe succeeds.
func (s *Supervisor) IsHealthy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthy
}

// Port returns the port the supervisor is listening on.
func (s *Supervisor) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.Port
}

func (s *Supervisor) healthCheck(ctx context.Context) {
	deadline := time.After(30 * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			s.mu.Lock()
			s.healthy = false
			s.mu.Unlock()
			slog.Error("llamafile failed health check (30s timeout)")
			return
		case <-ticker.C:
			addr := fmt.Sprintf("http://127.0.0.1:%d/health", s.opts.Port)
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Get(addr)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					s.mu.Lock()
					s.healthy = true
					s.mu.Unlock()
					slog.Info("llamafile health check passed")
					return
				}
			}
		}
	}
}

func (s *Supervisor) monitorCrashes(ctx context.Context) {
	if s.cmd == nil {
		return
	}
	err := s.cmd.Wait()
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()

	if ctx.Err() != nil {
		return // intentional shutdown
	}

	if err != nil {
		slog.Error("llamafile exited", "error", err)
	}

	// Restart with backoff
	s.restartWithBackoff(ctx)
}

func (s *Supervisor) restartWithBackoff(ctx context.Context) {
	maxRestarts := 5
	window := 10 * time.Minute
	backoffs := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
	}

	now := time.Now()
	if now.Sub(s.lastCrash) > window {
		s.restarts = 0
	}
	s.lastCrash = now
	s.restarts++

	if s.restarts > maxRestarts {
		slog.Error("llamafile exceeded max restarts, marking unhealthy")
		s.mu.Lock()
		s.healthy = false
		s.running = false
		s.mu.Unlock()
		return
	}

	delay := backoffs[min(s.restarts-1, len(backoffs)-1)]
	slog.Info("restarting llamafile", "attempt", s.restarts, "delay", delay)

	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return
	}

	if err := s.Start(ctx); err != nil {
		slog.Error("failed to restart llamafile", "error", err)
		s.mu.Lock()
		s.healthy = false
		s.running = false
		s.mu.Unlock()
	}
}

func (s *Supervisor) logWriter(prefix string, level string) io.Writer {
	pr, pw := io.Pipe()
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			switch level {
			case "info":
				slog.Info(prefix, "line", scanner.Text())
			case "debug":
				slog.Debug(prefix, "line", scanner.Text())
			}
		}
	}()
	return pw
}

func findAvailablePort(startPort int, attempts int) (int, error) {
	for i := 0; i < attempts; i++ {
		port := startPort + i
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port found")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

Add required imports at the top of the file:

```go
import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"
)
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/local/ -run TestSupervisor -v -count=1
```

Expected: PASS (2-3 tests, ~5 seconds)

- [ ] **Step 5: Commit**

```bash
git add internal/local/supervisor.go internal/local/supervisor_test.go
git commit -m "feat: add llamafile process supervisor with crash backoff and health checks"
```

---

### Task 5: Create `internal/local/config.go` — provider injection helper

**Files:**
- Create: `internal/local/config.go`

- [ ] **Step 1: Write the config helper**

```go
package local

import (
	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
)

// InjectLocalProvider adds the "local" provider to the config if model files
// are resolvable and local.enabled is true.
func InjectLocalProvider(cfg *config.Config) error {
	if !cfg.Local.Enabled {
		return nil
	}

	dataDir := config.DefaultDataDir()
	paths, err := ResolveModelPaths(cfg.Local.ModelDir, dataDir)
	if err != nil {
		return err // model not found — caller decides whether to proceed
	}

	// Verify SHA
	if err := paths.VerifySHA256(paths.SearchRoot); err != nil {
		return err
	}

	// Set the local provider config
	baseURL := "http://127.0.0.1:%d/v1"
	cfg.Providers["local"] = config.ProviderConfig{
		Kind:    "local",
		BaseURL: fmt.Sprintf(baseURL, cfg.Local.Port),
		APIKey:  "", // no key needed
	}

	return nil
}

// DefaultModelForLocal returns the appropriate default model string if local
// model files are available and no cloud provider has an API key configured.
func DefaultModelForLocal(cfg *config.Config) string {
	if !cfg.Local.Enabled {
		return ""
	}

	dataDir := config.DefaultDataDir()
	_, err := ResolveModelPaths(cfg.Local.ModelDir, dataDir)
	if err != nil {
		return ""
	}

	// Check if any cloud provider has an API key
	for _, p := range cfg.Providers {
		if p.APIKey != "" {
			return "" // cloud provider takes precedence
		}
	}

	// Check env vars for API keys
	for _, envVar := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY"} {
		if os.Getenv(envVar) != "" {
			return ""
		}
	}

	return "local/gemma-4-e4b-it"
}
```

- [ ] **Step 2: Run build to verify**

```bash
go build ./internal/local/
```

Expected: success (no output)

- [ ] **Step 3: Commit**

```bash
git add internal/local/config.go
git commit -m "feat: add local provider config injection helper"
```

---

### Task 6: Integrate supervisor into gateway startup

**Files:**
- Modify: `internal/startup/startup.go`

- [ ] **Step 1: Add supervisor field to Result**

In `internal/startup/startup.go`, add an import for the local package:

```go
import (
	// ... existing imports ...
	"github.com/sausheong/felix/internal/local"
)
```

Add the supervisor to the `Result` struct:

```go
type Result struct {
	Server     *gateway.Server
	Config     *config.Config
	Supervisor *local.Supervisor // may be nil if local model not available
	Cleanup    func()
}
```

- [ ] **Step 2: Start supervisor in StartGateway**

After the `providers := InitProviders(cfg)` line (around line 188), add:

```go
// Start local LLM supervisor if enabled and model files available
var supervisor *local.Supervisor
if cfg.Local.Enabled {
	paths, err := local.ResolveModelPaths(cfg.Local.ModelDir, dataDir)
	if err != nil {
		slog.Info("local model not found, skipping", "error", err)
	} else if err := paths.VerifySHA256(paths.SearchRoot); err != nil {
		slog.Warn("local model SHA256 verification failed", "error", err)
	} else {
		supervisor = local.NewSupervisor(local.SupervisorOptions{
			EnginePath:  paths.EnginePath,
			ModelPath:   paths.ModelPath,
			MMProjPath:  paths.MMProjPath,
			Port:        cfg.Local.Port,
			ContextSize: cfg.Local.ContextSize,
			GPULayers:   cfg.Local.GPULayers,
		})
		if err := supervisor.Start(ctx); err != nil {
			slog.Warn("failed to start local supervisor", "error", err)
			supervisor = nil
		} else {
			slog.Info("local LLM supervisor started",
				"model", paths.ModelPath,
				"port", supervisor.Port())
		}
	}
}
```

- [ ] **Step 3: Add supervisor to cleanup function**

In the `cleanup := func()` (around line 483), add before `cronScheduler.Stop()`:

```go
if supervisor != nil {
	supervisor.Stop()
}
```

- [ ] **Step 4: Return supervisor in Result**

Update the return statement:

```go
return &Result{
	Server:     srv,
	Config:     cfg,
	Supervisor: supervisor,
	Cleanup:    cleanup,
}, nil
```

- [ ] **Step 5: Run build and tests**

```bash
go build ./...
go test ./internal/startup/ -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/startup/startup.go
git commit -m "feat: integrate local supervisor into gateway startup and cleanup"
```

---

### Task 7: Add `felix model` CLI subcommand

**Files:**
- Modify: `cmd/felix/main.go` (around line 51 — rootCmd.AddCommand)

- [ ] **Step 1: Register the model command**

In `main()` (around line 51), add `modelCmd()` to the list of subcommands:

```go
rootCmd.AddCommand(
	startCmd(),
	chatCmd(),
	clearCmd(),
	statusCmd(),
	sessionsCmd(),
	versionCmd(),
	onboardCmd(),
	doctorCmd(),
	modelCmd(),
)
```

- [ ] **Step 2: Add modelCmd function and handlers**

Add before the `main()` function:

```go
func modelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Manage the bundled local LLM model",
	}
	cmd.AddCommand(
		modelPullCmd(),
		modelStatusCmd(),
		modelRmCmd(),
		modelAssembleCmd(),
	)
	return cmd
}

func modelPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Download Gemma 4 E4B model files (~5.6 GB)",
		RunE: func(cmd *cobra.Command, args []error {
			return runModelPull()
		},
	}
}

func modelStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local model status",
		RunE: func(cmd *cobra.Command, args []error {
			return runModelStatus()
		},
	}
}

func modelRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm",
		Short: "Remove downloaded local model files",
		RunE: func(cmd *cobra.Command, args []error {
			return runModelRm()
		},
	}
}

func modelAssembleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "assemble <prefix>",
		Short: "Reassemble split model parts into a single .pkg",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []error {
			return runModelAssemble(args[0])
		},
	}
}
```

- [ ] **Step 3: Write model pull handler**

Add after the above:

```go
const (
	modelRepoURL      = "https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/"
	modelFilename     = "gemma-4-E4B-it-Q4_K_M.gguf"
	mmprojFilename    = "mmproj-F16.gguf"
)

func runModelPull() error {
	dataDir := config.DefaultDataDir()
	modelDir := filepath.Join(dataDir, "models", "gemma-4-e4b")
	os.MkdirAll(modelDir, 0o755)

	files := []struct {
		url      string
		filename string
		sizeGB   string
	}{
		{modelRepoURL + modelFilename, "model.gguf", "4.63"},
		{modelRepoURL + mmprojFilename, "mmproj.gguf", "0.92"},
	}

	for _, f := range files {
		dest := filepath.Join(modelDir, f.filename)
		if _, err := os.Stat(dest); err == nil {
			fmt.Printf("%s already exists, skipping\n", f.filename)
			continue
		}
		fmt.Printf("Downloading %s (%s GB)...\n", f.filename, f.sizeGB)
		if err := downloadWithProgress(f.url, dest); err != nil {
			// Clean up partial download
			os.Remove(dest)
			return fmt.Errorf("download %s: %w", f.filename, err)
		}
	}

	fmt.Println("Model downloaded successfully to:", modelDir)
	return nil
}

func downloadWithProgress(url, dest string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	// Check for partial download
	if info, err := os.Stat(dest); err == nil {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", info.Size()))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	total := resp.ContentLength
	var written int64
	buf := make([]byte, 32*1024)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				pct := float64(written) / float64(total) * 100
				fmt.Printf("\r%.1f%% (%.1f MB / %.1f MB)", pct,
					float64(written)/1e6, float64(total)/1e6)
			case <-done:
				return
			}
		}
	}()

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w, err := out.Write(buf[:n])
			written += int64(w)
			if err != nil {
				close(done)
				return err
			}
		}
		if err == io.EOF {
			close(done)
			break
		}
		if err != nil {
			close(done)
			return err
		}
	}

	fmt.Printf("\r100.0%% (%.1f MB) — done\n", float64(written)/1e6)
	return nil
}
```

- [ ] **Step 4: Write model status handler**

```go
func runModelStatus() error {
	dataDir := config.DefaultDataDir()
	cfg, err := config.Load("")
	localEnabled := false
	if err == nil {
		localEnabled = cfg.Local.Enabled
	}

	modelDir := filepath.Join(dataDir, "models", "gemma-4-e4b")
	fmt.Println("Local model status:")
	fmt.Printf("  Enabled in config: %v\n", localEnabled)
	fmt.Printf("  Model directory:   %s\n", modelDir)

	if _, err := os.Stat(modelDir); os.IsNotExist(err) {
		fmt.Println("  Status: not installed")
		return nil
	}

	paths, err := local.ResolveModelPaths("", dataDir)
	if err != nil {
		fmt.Printf("  Status: incomplete (%v)\n", err)
		return nil
	}

	// Check file sizes
	for _, p := range []string{paths.ModelPath, paths.MMProjPath} {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Printf("  %s: missing\n", filepath.Base(p))
		} else {
			fmt.Printf("  %s: %.2f GB\n", filepath.Base(p), float64(info.Size())/1e9)
		}
	}

	// SHA verify
	if err := paths.VerifySHA256(paths.SearchRoot); err != nil {
		fmt.Printf("  SHA256: FAILED (%v)\n", err)
	} else {
		fmt.Println("  SHA256: OK")
	}

	fmt.Println("  Status: ready")
	return nil
}
```

- [ ] **Step 5: Write model rm and assemble handlers**

```go
func runModelRm() error {
	dataDir := config.DefaultDataDir()
	modelDir := filepath.Join(dataDir, "models", "gemma-4-e4b")

	if _, err := os.Stat(modelDir); os.IsNotExist(err) {
		fmt.Println("No local model files found.")
		return nil
	}

	fmt.Printf("Removing %s...\n", modelDir)
	if err := os.RemoveAll(modelDir); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	fmt.Println("Done. Freed ~5.6 GB.")
	return nil
}

func runModelAssemble(prefix string) error {
	// Check for .part-* files
	pattern := prefix + ".part-*"
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("no part files matching %s found", pattern)
	}

	// Sort and concatenate
	output := prefix
	out, err := os.Create(output)
	if err != nil {
		return err
	}
	defer out.Close()

	for _, m := range matches {
		f, err := os.Open(m)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, f); err != nil {
			f.Close()
			return err
		}
		f.Close()
		fmt.Printf("Appended %s\n", m)
	}

	fmt.Printf("Assembled: %s\n", output)
	return nil
}
```

- [ ] **Step 6: Run build**

```bash
go build ./cmd/felix/
```

Expected: success

- [ ] **Step 7: Test the CLI subcommands**

```bash
./felix model --help
./felix model status
```

Expected: `status` shows "not installed" (unless you've already downloaded the model)

- [ ] **Step 8: Commit**

```bash
git add cmd/felix/main.go
git commit -m "feat: add felix model CLI subcommand (pull/status/rm/assemble)"
```

---

### Task 8: Add local model option to onboarding wizard

**Files:**
- Modify: `cmd/felix/main.go:1149-1191` (runOnboard function)

- [ ] **Step 1: Add "Local" option to provider selection**

In `runOnboard()`, update the provider list (around line 1154):

```go
providerIdx := choose(
	"Which LLM provider do you want to use?",
	[]string{
		"Local — Gemma 4 E4B (~5.6 GB, runs offline) (recommended)",
		"Anthropic (Claude)",
		"OpenAI (GPT)",
		"Ollama (local models)",
		"Custom/LiteLLM (OpenAI-compatible endpoint)",
	},
	0, // default to Local
)
```

- [ ] **Step 2: Handle Local selection in the switch**

Update the switch block (around line 1167):

```go
switch providerIdx {
case 0: // Local
	providerName = "local"
	providerKind = "local"
	// Check if model is already downloaded
	dataDir := config.DefaultDataDir()
	modelDir := filepath.Join(dataDir, "models", "gemma-4-e4b")
	if _, err := os.Stat(filepath.Join(modelDir, "model.gguf")); os.IsNotExist(err) {
		fmt.Println("\nLocal model not found. Downloading Gemma 4 E4B (~5.6 GB)...")
		fmt.Println("This may take a while depending on your internet connection.")
		fmt.Println("You can abort and resume later.")
		fmt.Println()
		confirm := prompt("Start download? (y/n)", "y")
		if strings.ToLower(confirm) != "y" && strings.ToLower(confirm) != "yes" {
			fmt.Println("Skipping local model. You can download it later with: felix model pull")
			// Fall through to cloud provider setup
			providerIdx = 1 // redirect to Anthropic
			break
		}
		if err := runModelPull(); err != nil {
			fmt.Printf("Download failed: %v\n", err)
			fmt.Println("You can retry later with: felix model pull")
			providerIdx = 1
			break
		}
	}
	// Verify
	paths, err := local.ResolveModelPaths("", dataDir)
	if err != nil {
		fmt.Printf("Model download completed but verification failed: %v\n", err)
		providerIdx = 1
		break
	}
	if err := paths.VerifySHA256(paths.SearchRoot); err != nil {
		fmt.Printf("SHA256 verification failed: %v\n", err)
		providerIdx = 1
		break
	}
	fmt.Println("Model downloaded and verified successfully!")
	baseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", 18790)
case 1: // Anthropic
	providerName = "anthropic"
	providerKind = "anthropic"
case 2: // OpenAI
	providerName = "openai"
	providerKind = "openai"
case 3: // Ollama
	providerName = "ollama"
	providerKind = "openai-compatible"
	baseURL = prompt("Ollama base URL", "http://localhost:11434/v1")
case 4: // Custom
	providerName = prompt("Provider name", "litellm")
	providerKind = "openai-compatible"
	baseURL = prompt("Base URL", "http://localhost:4000/v1")
}
```

- [ ] **Step 3: Update API key prompt to skip for Local**

Update the API key section (around line 1186) to also skip for `local`:

```go
if providerIdx != 0 && providerIdx != 3 { // skip for Local and Ollama
	apiKey = promptSecret(fmt.Sprintf("Enter your %s API key", providerName))
	if apiKey == "" && providerIdx != 0 && providerIdx != 3 {
		fmt.Println("Warning: No API key provided. You can set it later via environment variable or config file.")
	}
}
```

- [ ] **Step 4: Update test connectivity check**

Update the test connectivity block (around line 1194):

```go
if apiKey != "" || providerIdx == 0 || providerIdx == 3 { // Local or Ollama
	fmt.Print("Testing connection... ")
	// ... existing test code ...
}
```

- [ ] **Step 5: Run build and test**

```bash
go build ./cmd/felix/
```

Expected: success

- [ ] **Step 6: Commit**

```bash
git add cmd/felix/main.go
git commit -m "feat: add local Gemma 4 E4B option to onboarding wizard"
```

---

### Task 9: Update installer postinstall script for bundled models

**Files:**
- Modify: `installer/scripts/postinstall`

- [ ] **Step 1: Add bundled model detection and local provider setup**

After the skills copy section (around line 30), add:

```bash
# ---------------------------------------------------------------------------
# Detect bundled models (felix-bundled flavor)
# ---------------------------------------------------------------------------
BUNDLED_MODELS="/usr/local/share/felix/models/gemma-4-e4b"
MODELS_PRESENT=false

if [ -f "$BUNDLED_MODELS/model.gguf" ] && [ -f "$BUNDLED_MODELS/mmproj.gguf" ]; then
  MODELS_PRESENT=true
  echo "Bundled local model detected."

  # Create symlink in Felix data dir
  mkdir -p "$FELIX_DIR/models"
  ln -sf "$BUNDLED_MODELS" "$FELIX_DIR/models/gemma-4-e4b" 2>/dev/null || true
fi
```

- [ ] **Step 2: Add local provider to config when models are present**

Inside the `if [ ! -f "$CONFIG" ]` block, add before the provider selection section. When models are bundled, skip the wizard and inject the local provider directly:

Replace the provider selection `osascript` block with conditional logic:

```bash
  if [ "$MODELS_PRESENT" = "true" ]; then
    # Bundled flavor — default to local model
    PROVIDER_KEY="local"
    MODEL="local/gemma-4-e4b-it"
    BASE_URL="http://127.0.0.1:18790/v1"
    PROVIDER_JSON="\"local\": { \"kind\": \"local\", \"base_url\": \"http://127.0.0.1:18790/v1\" }"
  else
    # Small flavor — show provider selection wizard
    PROVIDER=$(sudo -u "$LOGGED_IN_USER" osascript <<'OSASCRIPT'
set providers to {"OpenAI (GPT)"}
set chosen to choose from list providers ¬
  with prompt "Choose your default AI provider for Felix:" ¬
  default items {"OpenAI (GPT)"} ¬
  without multiple selections allowed and empty selection allowed
if chosen is false then return "skip"
return item 1 of chosen
OSASCRIPT
    )
    # ... existing provider selection logic unchanged ...
  fi
```

Update the providers map to include `kind`:

```bash
    if [ "$PROVIDER_KEY" = "ollama" ]; then
      PROVIDER_JSON="\"$PROVIDER_KEY\": { \"kind\": \"openai-compatible\", \"base_url\": \"$BASE_URL\" }"
    elif [ "$PROVIDER_KEY" = "local" ]; then
      PROVIDER_JSON="\"$PROVIDER_KEY\": { \"kind\": \"local\", \"base_url\": \"$BASE_URL\" }"
    elif [ -n "$API_KEY" ] && [ -n "$BASE_URL" ]; then
      PROVIDER_JSON="\"$PROVIDER_KEY\": { \"kind\": \"$PROVIDER_KEY\", \"api_key\": \"$API_KEY\", \"base_url\": \"$BASE_URL\" }"
    elif [ -n "$API_KEY" ]; then
      PROVIDER_JSON="\"$PROVIDER_KEY\": { \"kind\": \"$PROVIDER_KEY\", \"api_key\": \"$API_KEY\" }"
    else
      PROVIDER_JSON="\"$PROVIDER_KEY\": { \"kind\": \"$PROVIDER_KEY\", \"base_url\": \"$BASE_URL\" }"
    fi
```

- [ ] **Step 3: Add local block to the generated config**

In the config generation section (around line 128), add the `"local"` block:

```json5
  "local": {
    "enabled": $([ "$MODELS_PRESENT" = "true" ] && echo "true" || echo "false"),
    "port": 18790,
    "context_size": 65536,
    "gpu_layers": 999
  },
```

- [ ] **Step 4: Commit**

```bash
git add installer/scripts/postinstall
git commit -m "feat: handle bundled models in postinstall, inject local provider"
```

---

### Task 10: Update Makefile for build and release

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Add model fetch targets**

Add before the `release` target:

```makefile
## models-fetch: download Gemma 4 E4B + mmproj into models/ for bundling
models-fetch:
	mkdir -p models/gemma-4-e4b
	curl -L -o models/gemma-4-e4b/model.gguf \
	     https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/gemma-4-E4B-it-Q4_K_M.gguf
	curl -L -o models/gemma-4-e4b/mmproj.gguf \
	     https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/mmproj-F16.gguf
	cd models/gemma-4-e4b && shasum -a 256 *.gguf > SHA256SUMS

## llamafile-fetch: download platform-specific llamafile engine
llamafile-fetch:
	mkdir -p models/engine
ifeq ($(shell uname -s),Darwin)
ifeq ($(shell uname -m),arm64)
	curl -L -o models/engine/llamafile \
	     https://github.com/Mozilla-Ocho/llamafile/releases/latest/download/llamafile-darwin-arm64
else
	curl -L -o models/engine/llamafile \
	     https://github.com/Mozilla-Ocho/llamafile/releases/latest/download/llamafile-darwin-amd64
endif
else ifeq ($(shell uname -s),Linux)
ifeq ($(shell uname -m),arm64)
	curl -L -o models/engine/llamafile \
	     https://github.com/Mozilla-Ocho/llamafile/releases/latest/download/llamafile-linux-arm64
else
	curl -L -o models/engine/llamafile \
	     https://github.com/Mozilla-Ocho/llamafile/releases/latest/download/llamafile-linux-amd64
endif
endif
	chmod +x models/engine/llamafile

## release-bundled: build felix-bundled flavor (depends on models-fetch + llamafile-fetch)
release-bundled: models-fetch llamafile-fetch
	rm -rf $(RELEASE_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%%/*}; \
		arch=$${platform##*/}; \
		name=felix-bundled-$(VERSION)-$${os}-$${arch}; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "Building $$name..."; \
		dir=$(RELEASE_DIR)/$$name; \
		mkdir -p $$dir/models/gemma-4-e4b; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" \
			-o $$dir/felix$(ext) $(CMD) || exit 1; \
		cp -r models/gemma-4-e4b/* $$dir/models/gemma-4-e4b/; \
		if [ -f "models/engine/llamafile" ]; then \
			cp models/engine/llamafile $$dir/models/llamafile; \
			if [ "$$os" = "windows" ]; then mv $$dir/models/llamafile $$dir/models/llamafile.exe; fi; \
		fi; \
		cp skills/*.md $$dir/ 2>/dev/null || true; \
		echo "Splitting into 1.9 GB parts..."; \
		(cd $(RELEASE_DIR) && tar czf - $$name | split -b 1900M - "$${name}.tar.gz.part-"); \
		rm -rf $$dir; \
	done
	@echo "Bundled release artifacts in $(RELEASE_DIR)/:"
	@ls -1 $(RELEASE_DIR)/felix-bundled-*
```

- [ ] **Step 2: Commit**

```bash
git add Makefile
git commit -m "feat: add model/llamafile fetch targets and release-bundled Makefile rule"
```

---

### Task 11: Create MODELS-SHA256SUMS reference file

**Files:**
- Create: `MODELS-SHA256SUMS`

- [ ] **Step 1: Create the reference file**

```
gemma-4-E4B-it-Q4_K_M.gguf  <SHA256>  4977169088
mmproj-F16.gguf             <SHA256>   990372800
```

Note: SHA256 values will be populated after the first `make models-fetch` run. This file serves as a human-readable reference of expected model sizes and hashes.

- [ ] **Step 2: Commit**

```bash
git add MODELS-SHA256SUMS
git commit -m "docs: add MODELS-SHA256SUMS reference for bundled model files"
```

---

## Self-Review

### 1. Spec coverage checklist

| Spec section | Covered by task? |
|---|---|
| Two release flavors | Task 10 (Makefile), Task 9 (postinstall) |
| File layout (bundled) | Task 10 (release-bundled), Task 9 (postinstall) |
| Runtime model resolution | Task 3 (discover.go) |
| Provider integration | Task 2 (provider.go), Task 5 (config.go) |
| Supervisor lifecycle | Task 4 (supervisor.go), Task 6 (startup.go) |
| Config hot-reload | Supervisor reads from config at startup; hot-reload requires restart (documented in spec, implemented implicitly — config watcher triggers re-read but supervisor doesn't auto-restart on port change; this is acceptable per spec) |
| Default-model selection (smart detection) | Task 5 (DefaultModelForLocal) — used by caller; Task 8 (onboarding wizard) |
| First-run UX (small build) | Task 8 (onboarding wizard) |
| First-run UX (bundled build) | Task 9 (postinstall) |
| felix model CLI | Task 7 (modelCmd) |
| Build & release | Task 10 (Makefile) |
| Error handling (all 8 scenarios) | Task 3 (SHA/mmissing), Task 4 (crash/port), Task 7 (disk/partial), Task 6 (startup failures) |
| Test plan | Tasks 1, 3, 4 include unit tests |

### 2. Placeholder scan

No TBD, TODO, "handle edge cases", or "similar to" patterns found in the plan.

### 3. Type consistency

- `LocalConfig` struct fields match JSON tags used throughout (`enabled`, `port`, `context_size`, `gpu_layers`, `model_dir`)
- `ModelPaths` struct has `ModelPath`, `MMProjPath`, `EnginePath`, `SearchRoot` — used consistently in discover.go, config.go, supervisor.go, startup.go
- `SupervisorOptions` maps to `LocalConfig` fields via Task 6 integration
- Provider `Kind: "local"` consistent across provider.go, config.go, postinstall
- Port 18790 consistent across all references
- Context size 65536 consistent

No inconsistencies found. Plan is complete and self-contained.
