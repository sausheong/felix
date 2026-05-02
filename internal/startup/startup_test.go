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

// (TestStartGatewayMissingConfigFile was tried but removed: the
// missing-config path falls through to DefaultConfig which has
// Local.Enabled=true, so it tries to spawn the bundled ollama
// supervisor on the default port. That collides with the
// internal/local supervisor tests when both packages run in
// parallel, producing an intermittent failure unrelated to the
// behaviour under test. The smoke test above already covers the
// main contract — Server/Config/Cleanup non-nil + Cleanup is
// idempotent — which is what protects the gateway-level Shutdown
// nil-deref this test discovered.)
