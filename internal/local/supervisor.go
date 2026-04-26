package local

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrNoFreePort is returned when no port in the configured range is free.
var ErrNoFreePort = errors.New("no free port in range")

// ErrNotReady is returned when the child fails to respond to /api/version
// within the readiness window.
var ErrNotReady = errors.New("ollama did not become ready in time")

// Options configures a Supervisor.
type Options struct {
	BinPath   string // absolute path to the ollama binary
	ModelsDir string // OLLAMA_MODELS
	KeepAlive string // OLLAMA_KEEP_ALIVE; empty → "24h"
	PortLow   int    // first port to try; 0 → 18790
	PortHigh  int    // last port to try inclusive; 0 → 18799
	// PIDFile, when non-empty, is the path used to record the spawned ollama
	// PID. On Start the file is consulted first and any leftover process from
	// a prior run that still matches BinPath is killed. On Stop the file is
	// removed. Set to "" to disable the orphan-reap behavior.
	PIDFile string
}

// Supervisor manages a single ollama serve child process.
type Supervisor struct {
	binPath   string
	modelsDir string
	keepAlive string
	portLow   int
	portHigh  int
	pidFile   string

	mu        sync.Mutex
	cmd       *exec.Cmd
	cancelCtx context.CancelFunc
	boundPort int
	alive     atomic.Bool
	exited    chan struct{}

	readyTimeout time.Duration // 0 → 60s
	stopGrace    time.Duration // 0 → 5s
	reapGrace    time.Duration // 0 → 2s; SIGTERM→SIGKILL window for orphans
}

// New constructs a Supervisor with defaults applied.
func New(opt Options) *Supervisor {
	if opt.KeepAlive == "" {
		// Hold both chat and embedder models resident across calls so back-
		// to-back chat / cortex turns don't pay model-load latency.
		opt.KeepAlive = "24h"
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
		pidFile:   opt.PIDFile,
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

// Start spawns ollama serve, waits for it to respond to /api/version, and
// returns nil once ready. On any failure, the child (if started) is killed
// and an error is returned.
func (s *Supervisor) Start(ctx context.Context) error {
	// Reap any leftover ollama child from a prior felix run that exited
	// without cleanup (crash, force-quit, menu-bar timeout). Must happen
	// before probeFreePort so the orphan's port is freed first.
	s.reapStaleChild()

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
	// Put ollama in its own process group so Stop() can take down the
	// `ollama runner` children that get spawned per loaded model. Without
	// this, runners survive the parent and keep its stdout/stderr pipes
	// open, which blocks cmd.Wait() forever — see the comment in Stop().
	setProcAttr(cmd)
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
	s.writePIDFile(cmd.Process.Pid)

	s.exited = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		s.alive.Store(false)
		close(s.exited)
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

// Stop signals the supervised ollama (and its runner children, via process
// group) with SIGTERM, waits stopGrace, then SIGKILL. After SIGKILL it waits
// only briefly for exit because the underlying cmd.Wait() can hang
// indefinitely if pipe-inheriting children keep our stdout/stderr FDs open
// — group-kill normally takes them down too, but this bound is the safety
// net so a single misbehaving child can never hang shutdown again.
//
// Idempotent.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	cmd := s.cmd
	cancel := s.cancelCtx
	exited := s.exited
	s.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	grace := s.stopGrace
	if grace == 0 {
		grace = 5 * time.Second
	}

	_ = signalGroup(cmd, syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(grace):
		_ = signalGroup(cmd, syscall.SIGKILL)
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			// cmd.Wait() is wedged on inherited pipes from a child that
			// somehow survived SIGKILL of the group. The kernel has reaped
			// the process; we just don't get the goroutine confirmation.
			// Leave it; return so the caller's shutdown chain proceeds.
			slog.Warn("ollama supervisor: Wait() did not return after SIGKILL; returning anyway")
		}
	}

	if cancel != nil {
		cancel()
	}
	s.alive.Store(false)
	s.removePIDFile()
	return nil
}

// writePIDFile records the spawned child's PID alongside our binPath so a
// future Start invocation can sanity-check before killing.
func (s *Supervisor) writePIDFile(pid int) {
	if s.pidFile == "" {
		return
	}
	line := fmt.Sprintf("%d %s\n", pid, s.binPath)
	if err := os.WriteFile(s.pidFile, []byte(line), 0o644); err != nil {
		slog.Warn("ollama supervisor: failed to write pid file", "path", s.pidFile, "error", err)
	}
}

// removePIDFile is best-effort cleanup on graceful shutdown.
func (s *Supervisor) removePIDFile() {
	if s.pidFile == "" {
		return
	}
	if err := os.Remove(s.pidFile); err != nil && !os.IsNotExist(err) {
		slog.Debug("ollama supervisor: failed to remove pid file", "path", s.pidFile, "error", err)
	}
}

// reapStaleChild reads the PID file (if any) and kills any leftover ollama
// process from a prior felix run that didn't clean up. Best-effort: any
// error path leaves the situation as-is, since spawning a new ollama with
// port-probing handles the worst case (we'll just fail with ErrNoFreePort
// if every port in the range is held by orphans).
func (s *Supervisor) reapStaleChild() {
	if s.pidFile == "" {
		return
	}
	data, err := os.ReadFile(s.pidFile)
	if err != nil {
		return
	}
	parts := strings.SplitN(strings.TrimSpace(string(data)), " ", 2)
	if len(parts) == 0 {
		return
	}
	pid, err := strconv.Atoi(parts[0])
	if err != nil || pid <= 0 {
		return
	}
	var savedBin string
	if len(parts) == 2 {
		savedBin = parts[1]
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	// Signal 0 probes whether the PID is alive and signalable by us.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return
	}
	// Sanity-check: only kill if the process command matches what we
	// previously spawned. Protects against PID recycling killing an
	// unrelated process the user is running.
	expected := savedBin
	if expected == "" {
		expected = s.binPath
	}
	if !commandMatches(pid, expected) {
		slog.Debug("ollama supervisor: stale pid file but command mismatch; not killing", "pid", pid)
		return
	}
	slog.Info("ollama supervisor: reaping orphan from prior run", "pid", pid)
	_ = proc.Signal(syscall.SIGTERM)
	grace := s.reapGrace
	if grace == 0 {
		grace = 2 * time.Second
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if proc.Signal(syscall.Signal(0)) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = proc.Kill()
}

// commandMatches checks whether the running process at pid was launched with
// expected as its argv[0]. Uses ps so it works on macOS and Linux without
// /proc dependencies.
func commandMatches(pid int, expected string) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return false
	}
	return fields[0] == expected
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
