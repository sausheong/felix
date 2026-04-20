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
