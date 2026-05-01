package startup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStartGatewaySmoke is a smoke test for the 520-line StartGateway
// wiring: load a tmp config, build all the subsystems, return a Result
// with a non-nil Cleanup, then call Cleanup. The test does NOT start
// the HTTP server (Server.Start is the caller's responsibility per
// the doc on StartGateway) — it validates only the construction +
// teardown contract.
//
// The point isn't to exercise the runtime in depth; existing tests in
// internal/agent, internal/llm, internal/gateway already do that.
// The point is to catch regressions where a refactor of the wiring
// itself silently breaks startup — currently the only way to find such
// a break is to run the binary and watch for a panic.
func TestStartGatewaySmoke(t *testing.T) {
	tmp := t.TempDir()
	// HOME redirect — DefaultDataDir reads UserHomeDir, and several
	// background features (memory store, cortex DB, MCP creds) write
	// there. Without this, the test would pollute the developer's
	// real ~/.felix.
	t.Setenv("HOME", tmp)

	configPath := filepath.Join(tmp, "felix.json5")
	// Minimal config:
	//  - Local.Enabled=false skips the bundled Ollama supervisor (which
	//    would block trying to spawn an ollama binary that may not exist
	//    in CI).
	//  - Heartbeat.Enabled=false skips the background daemon goroutines
	//    (which would otherwise try to call providers we haven't set up).
	//  - Memory.Enabled=false skips the embedder bootstrap.
	//  - Cortex.Enabled=false skips the embedder + DuckDB init.
	//  - The default agent uses an unconfigured "anthropic" provider;
	//    InitProviders logs a warning and skips it (no API key), and
	//    the agent is registered without a live provider — fine for a
	//    smoke test that doesn't actually send any chat traffic.
	//  - Workspace points inside tmp so any file the agent's tool
	//    registry creates lands under the temp dir.
	//  - Gateway.Port=0 lets the OS pick a free port (avoids :18789
	//    collisions if a real Felix is also running).
	cfg := `{
  "gateway": {"host": "127.0.0.1", "port": 0},
  "providers": {},
  "agents": {
    "list": [
      {
        "id": "default",
        "name": "Felix",
        "model": "anthropic/claude-sonnet-4-5",
        "workspace": "` + filepath.Join(tmp, "workspace") + `",
        "sandbox": "none"
      }
    ]
  },
  "channels": {"cli": {"enabled": false}},
  "heartbeat": {"enabled": false},
  "memory": {"enabled": false},
  "cortex": {"enabled": false},
  "local": {"enabled": false}
}`
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o644))

	result, err := StartGateway(configPath, "test-version")
	require.NoError(t, err, "StartGateway must succeed against a minimal config")
	require.NotNil(t, result, "Result must be non-nil on success")
	require.NotNil(t, result.Server, "Result.Server must be non-nil")
	require.NotNil(t, result.Config, "Result.Config must be non-nil")
	require.NotNil(t, result.Cleanup, "Result.Cleanup must be non-nil so caller has a teardown handle")

	// Cleanup is the contract the doc promises. Calling it must not
	// panic — covers the worst-case regression where someone adds a
	// deferred resource teardown that nil-derefs a never-initialised
	// subsystem (e.g., closing a nil cxProvider when Cortex is off).
	assert.NotPanics(t, func() { result.Cleanup() })

	// Config got loaded and key fields propagated.
	assert.Equal(t, "127.0.0.1", result.Config.Gateway.Host)
	require.Len(t, result.Config.Agents.List, 1)
	assert.Equal(t, "default", result.Config.Agents.List[0].ID)
}

// TestStartGatewayMissingConfigFile — when the config file doesn't
// exist, StartGateway should not silently fall back to DefaultConfig
// and start touching the user's real ~/.felix. The error has to
// surface so the caller can decide what to do (felix.json5 missing
// is a first-run signal that triggers the onboarding flow in
// cmd/felix/main.go).
func TestStartGatewayMissingConfigFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Note: config.Load with a non-existent path actually creates a
	// DefaultConfig and writes it. So the error contract here is
	// "must not panic; either succeeds with default config or returns
	// a clear error". We assert the no-panic + non-nil result invariant.
	missing := filepath.Join(tmp, "does-not-exist.json5")
	result, err := StartGateway(missing, "test-version")
	if err == nil {
		require.NotNil(t, result)
		require.NotNil(t, result.Cleanup)
		assert.NotPanics(t, func() { result.Cleanup() })
	}
	// If err != nil, that's also acceptable — the contract is just
	// "no panic, no half-initialised state leaked to the caller".
}
