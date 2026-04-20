package local

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

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
socketserver.TCPServer.allow_reuse_address = True
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
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary tests are POSIX-only")
	}
	// A script that stays alive but never serves /api/version.
	dir := t.TempDir()
	bin := filepath.Join(dir, "sleeper")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0o755))

	s := New(Options{BinPath: bin, ModelsDir: t.TempDir()})
	s.readyTimeout = 500 * time.Millisecond

	err := s.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotReady)
	t.Cleanup(func() { _ = s.Stop() })
}

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
socketserver.TCPServer.allow_reuse_address = True
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
