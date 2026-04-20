package local

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

// Supervisor manages the lifecycle of a child llamafile process.
type Supervisor struct {
	mu        sync.Mutex
	opts      SupervisorOptions
	cmd       *exec.Cmd
	running   bool
	healthy   bool
	stopCh    chan struct{}
	cancelFn  context.CancelFunc
	restarts  int
	lastCrash time.Time
}

// SupervisorOptions holds configuration for the supervisor.
type SupervisorOptions struct {
	EnginePath  string // path to llamafile binary
	ModelPath   string // path to model.gguf
	MMProjPath  string // path to mmproj.gguf
	Port        int    // default 18790
	ContextSize int    // default 65536
	GPULayers   int    // default 999
}

// NewSupervisor creates a new Supervisor with defaults applied.
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

	go s.healthCheck(ctx)
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
		return
	}

	if err != nil {
		slog.Error("llamafile exited", "error", err)
	}

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

	delay := backoffs[minInt(s.restarts-1, len(backoffs)-1)]
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
