//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// gateway is a handle to the spawned `felix start` child process. The
// menubar app (which is fragile under macOS Cocoa-level reaps) no
// longer hosts the gateway in-process — when the menubar app exits,
// the subprocess is sent SIGTERM but the user's running chats live
// in the gateway, not here.
//
// On macOS, if the menubar app gets reaped while a chat is in flight,
// the subprocess is reparented to launchd and stays alive. The user's
// browser tab keeps talking to the gateway. Relaunching the menubar
// app detects the live gateway via /health and attaches to it
// instead of spawning a duplicate that would fight for the port.
type gateway struct {
	cmd      *exec.Cmd
	port     int
	owned    bool          // true if we spawned it (and therefore should kill it on Quit)
	exitCh   chan error    // receives Wait() result, then closes
	exitOnce sync.Once
}

// findFelixBinary locates the `felix` binary the menubar should
// spawn. Search order:
//  1. <bundle>/Contents/Resources/bin/felix — production .app layout
//     (mirrors where the bundled ollama lives).
//  2. <felix-app dir>/felix — dev builds where the two binaries are
//     side-by-side after `make build && make build-app`.
//  3. exec.LookPath("felix") — fallback for users running the menubar
//     from a Go workspace without a colocated binary.
//
// Returns the resolved absolute path or an error explaining where we
// looked.
func findFelixBinary() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	exeDir := filepath.Dir(exePath)
	candidates := []string{
		filepath.Join(exeDir, "..", "Resources", "bin", "felix"),
		filepath.Join(exeDir, "felix"),
	}
	tried := make([]string, 0, len(candidates)+1)
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		tried = append(tried, abs)
		if st, err := os.Stat(abs); err == nil && !st.IsDir() {
			return abs, nil
		}
	}
	if path, err := exec.LookPath("felix"); err == nil {
		return path, nil
	}
	tried = append(tried, "$PATH:felix")
	return "", fmt.Errorf("felix binary not found; looked in: %v", tried)
}

// gatewayPort is the port the in-process gateway and CLI both default
// to. felix-app needs to know it before spawn so it can probe /health.
// If the user has overridden it in their config, the spawn will fail
// to bind that port and exit; the watchExit goroutine surfaces the
// failure to the menubar log.
const gatewayPort = 18789

// startOrAttachGateway tries to attach to a gateway that is already
// running on the configured port (e.g. user ran `felix start` from a
// terminal first). If none is running, spawns one as a child and
// waits for /health to return 200 within readyTimeout. The returned
// *gateway records whether we own the process — Quit only kills
// processes we spawned ourselves.
func startOrAttachGateway(ctx context.Context, logWriter io.Writer, readyTimeout time.Duration) (*gateway, error) {
	if probeHealth(gatewayPort) {
		slog.Info("attaching to existing gateway on port", "port", gatewayPort)
		return &gateway{port: gatewayPort, owned: false, exitCh: noExitCh()}, nil
	}
	bin, err := findFelixBinary()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "start")
	// Pipe child stdout/stderr to the menubar log file. Using *os.File
	// here rather than an io.Pipe means the child gets a real fd it
	// can write to even after the parent's go runtime is gone — so a
	// menubar reap does not bring down the subprocess via SIGPIPE.
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	cmd.Env = os.Environ()
	// Setpgid puts the child (and any grandchildren — the bundled
	// Ollama supervisor and its runner) into a new process group. On
	// Quit we signal -pgid so the whole tree gets SIGTERM at once,
	// instead of orphaning ollama. Crucially, it also detaches the
	// child from the menubar app's signal-delivery semantics: when
	// macOS sends Quit Apple Events to felix-app, only the menubar
	// dies; the gateway PG keeps running.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn %s start: %w", bin, err)
	}
	slog.Info("spawned gateway subprocess", "pid", cmd.Process.Pid, "binary", bin)

	g := &gateway{
		cmd:    cmd,
		port:   gatewayPort,
		owned:  true,
		exitCh: make(chan error, 1),
	}
	g.exitOnce.Do(func() {
		go func() {
			g.exitCh <- cmd.Wait()
			close(g.exitCh)
		}()
	})

	if err := waitForReady(ctx, gatewayPort, readyTimeout); err != nil {
		// Spawn started but never became healthy. Kill it so we don't
		// leak a zombie subprocess that's stuck in init.
		g.stop()
		return nil, fmt.Errorf("gateway did not become ready: %w", err)
	}
	return g, nil
}

// stop is a no-op when we did not spawn the gateway (attach mode) or
// when it is already gone. Otherwise SIGTERMs the process group, waits
// up to 8 s for graceful exit, then SIGKILLs as a last resort. 8 s
// covers the gateway's own MCP-shutdown budget (5 s) plus headroom
// for the bundled Ollama supervisor's shutdown.
func (g *gateway) stop() {
	if g == nil || !g.owned || g.cmd == nil || g.cmd.Process == nil {
		return
	}
	pgid, _ := syscall.Getpgid(g.cmd.Process.Pid)
	signalGroup := func(sig syscall.Signal) {
		if pgid > 0 {
			_ = syscall.Kill(-pgid, sig)
		} else {
			_ = g.cmd.Process.Signal(sig)
		}
	}
	signalGroup(syscall.SIGTERM)
	select {
	case <-g.exitCh:
		slog.Info("gateway subprocess exited gracefully")
	case <-time.After(8 * time.Second):
		slog.Warn("gateway subprocess did not exit within 8s, sending SIGKILL")
		signalGroup(syscall.SIGKILL)
		<-g.exitCh
	}
}

// probeHealth makes a single short-timeout GET to /health. Used to
// decide whether to attach vs spawn at startup.
func probeHealth(port int) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// waitForReady polls /health until it returns 200 or the deadline
// passes. Honours ctx so the menubar can interrupt the wait if the
// user quits during startup.
func waitForReady(ctx context.Context, port int, timeout time.Duration) error {
	client := &http.Client{Timeout: 1 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("/health did not return 200 within %s", timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// noExitCh returns a never-fires error channel — used for the
// attach-mode gateway (we have no Wait() to feed the channel) and
// for the post-crash sentinel state in main.go (the subprocess is
// already gone and we must not re-fire the watcher case). Reading
// from a closed channel returns immediately and would put the select
// loop into a hot loop logging the same "exited unexpectedly" error
// thousands of times per second; this channel blocks forever instead.
func noExitCh() chan error {
	return make(chan error)
}
