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
	exited    chan struct{}

	readyTimeout time.Duration // 0 → 60s
	stopGrace    time.Duration // 0 → 5s
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

// Stop sends SIGTERM, waits stopGrace, then SIGKILL. Idempotent.
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

	_ = cmd.Process.Signal(syscall.SIGTERM)
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
