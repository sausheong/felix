//go:build windows

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
	"time"
)

// gateway is the Windows counterpart to process_unix.go's gateway.
// Same shape, simpler signal handling — Windows has no process groups,
// so on Quit we just terminate the single child PID. The bundled
// Ollama supervisor inside the gateway handles its own subprocess
// cleanup before exiting.
type gateway struct {
	cmd      *exec.Cmd
	port     int
	owned    bool
	exitCh   chan error
	exitOnce sync.Once
}

func findFelixBinary() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	exeDir := filepath.Dir(exePath)
	candidates := []string{
		filepath.Join(exeDir, "felix.exe"),
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

const gatewayPort = 18789

func startOrAttachGateway(ctx context.Context, logWriter io.Writer, readyTimeout time.Duration) (*gateway, error) {
	if probeHealth(gatewayPort) {
		slog.Info("attaching to existing gateway on port", "port", gatewayPort)
		return &gateway{port: gatewayPort, owned: false, exitCh: closedExitCh()}, nil
	}
	bin, err := findFelixBinary()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "start")
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	cmd.Env = os.Environ()
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
		g.stop()
		return nil, fmt.Errorf("gateway did not become ready: %w", err)
	}
	return g, nil
}

func (g *gateway) stop() {
	if g == nil || !g.owned || g.cmd == nil || g.cmd.Process == nil {
		return
	}
	if err := g.cmd.Process.Kill(); err != nil {
		slog.Warn("kill gateway subprocess", "error", err)
	}
	<-g.exitCh
	slog.Info("gateway subprocess exited")
}

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

func closedExitCh() chan error {
	ch := make(chan error)
	close(ch)
	return ch
}
