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
