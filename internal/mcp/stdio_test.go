package mcp

import (
	"context"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergedEnv_OverridesAndAppends(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "HOME=/root", "FOO=old"}
	got := mergedEnv(parent, map[string]string{
		"FOO": "new",       // override
		"BAR": "fresh",     // new
	})

	// Build a quick lookup
	m := make(map[string]string, len(got))
	for _, kv := range got {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				m[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	assert.Equal(t, "/usr/bin", m["PATH"], "PATH should be inherited")
	assert.Equal(t, "/root", m["HOME"], "HOME should be inherited")
	assert.Equal(t, "new", m["FOO"], "FOO should be overridden")
	assert.Equal(t, "fresh", m["BAR"], "BAR should be appended")

	// Length: parent (3) + 1 new key (BAR); FOO is replaced in place.
	assert.Len(t, got, 4)
}

func TestMergedEnv_NilOverridesReturnsParent(t *testing.T) {
	parent := []string{"A=1", "B=2"}
	got := mergedEnv(parent, nil)
	assert.Equal(t, parent, got)
}

func TestConnectStdio_NonexistentBinaryFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := ConnectStdio(ctx, "test-bad", "/no/such/binary-felix-test", nil, nil)
	require.Error(t, err)
}

func TestConnectStdio_EmptyCommandFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := ConnectStdio(ctx, "test-empty", "", nil, nil)
	require.Error(t, err)
}

// Spawn `cat` (which doesn't speak MCP) and assert ConnectStdio fails at the
// JSON-RPC handshake. This validates that the spawn pathway works end-to-end
// — the process is launched, stdin/stdout pipes connect, and the failure
// surfaces from the SDK rather than from our wiring. Skipped on Windows
// (where `cat` is not standard).
func TestConnectStdio_HandshakeFailsOnNonMCPProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cat not standard on Windows")
	}
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not in PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := ConnectStdio(ctx, "cat-test", "cat", nil, nil)
	require.Error(t, err, "cat does not speak MCP; handshake must fail")
}
