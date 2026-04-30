package mcp

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/tools"
)

func TestRegisterTools_AddsPrefixedAdapters(t *testing.T) {
	srv := fakeMCPWithTools(t, []map[string]any{
		{"name": "search", "description": "search", "inputSchema": map[string]any{"type": "object"}},
		{"name": "store", "description": "store", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := ConnectHTTP(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	mgr := &Manager{servers: []*ServerEntry{{ID: "ltm", Client: c, ToolPrefix: "ltm_"}}}
	defer mgr.Close()

	reg := tools.NewRegistry()
	names, err := RegisterTools(reg, mgr, nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ltm_search", "ltm_store"}, names)
	assert.ElementsMatch(t, []string{"ltm_search", "ltm_store"}, reg.Names())
}

func TestRegisterTools_NoPrefix_NoCollision(t *testing.T) {
	srv := fakeMCPWithTools(t, []map[string]any{
		{"name": "remote_only", "description": "x", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := ConnectHTTP(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	mgr := &Manager{servers: []*ServerEntry{{ID: "x", Client: c, ToolPrefix: ""}}}
	defer mgr.Close()

	reg := tools.NewRegistry()
	names, err := RegisterTools(reg, mgr, nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"remote_only"}, names)
	assert.ElementsMatch(t, []string{"remote_only"}, reg.Names())
}

// TestRegisterTools_PropagatesParallelSafe verifies the per-server flag flows
// through the closure passed to RegisterTools. The closure is the contract:
// adapter.IsConcurrencySafe defers to it on each call, keyed by server ID.
func TestRegisterTools_PropagatesParallelSafe(t *testing.T) {
	srvSafe := fakeMCPWithTools(t, []map[string]any{
		{"name": "search", "description": "search", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srvSafe.Close()
	srvUnsafe := fakeMCPWithTools(t, []map[string]any{
		{"name": "store", "description": "store", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srvUnsafe.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cSafe, err := ConnectHTTP(ctx, srvSafe.URL, http.DefaultClient)
	require.NoError(t, err)
	cUnsafe, err := ConnectHTTP(ctx, srvUnsafe.URL, http.DefaultClient)
	require.NoError(t, err)

	mgr := &Manager{servers: []*ServerEntry{
		{ID: "safe", Client: cSafe, ToolPrefix: "s_"},
		{ID: "unsafe", Client: cUnsafe, ToolPrefix: "u_"},
	}}
	defer mgr.Close()

	parallelSafe := func(id string) bool { return id == "safe" }

	reg := tools.NewRegistry()
	_, err = RegisterTools(reg, mgr, parallelSafe)
	require.NoError(t, err)

	safe, ok := reg.Get("s_search")
	require.True(t, ok)
	unsafe, ok := reg.Get("u_store")
	require.True(t, ok)

	assert.True(t, safe.IsConcurrencySafe(nil), "tool from parallel-safe server should be concurrency-safe")
	assert.False(t, unsafe.IsConcurrencySafe(nil), "tool from default server should be unsafe")
}

// TestRegisterTools_LiveReadPicksUpToggle verifies the load-bearing property
// of the live-read refactor: toggling the cfg-backed value mid-flight changes
// the next IsConcurrencySafe call without rebuilding the adapter. This is
// what makes settings-UI toggles take effect without a Felix restart.
func TestRegisterTools_LiveReadPicksUpToggle(t *testing.T) {
	srv := fakeMCPWithTools(t, []map[string]any{
		{"name": "search", "description": "search", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := ConnectHTTP(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	mgr := &Manager{servers: []*ServerEntry{{ID: "trusted", Client: c, ToolPrefix: "t_"}}}
	defer mgr.Close()

	var live atomic.Bool
	fn := func(id string) bool { return id == "trusted" && live.Load() }

	reg := tools.NewRegistry()
	_, err = RegisterTools(reg, mgr, fn)
	require.NoError(t, err)

	tool, ok := reg.Get("t_search")
	require.True(t, ok)
	require.False(t, tool.IsConcurrencySafe(nil), "default state is unsafe")

	live.Store(true)
	require.True(t, tool.IsConcurrencySafe(nil), "toggle should be visible on next call without rebuild")

	live.Store(false)
	require.False(t, tool.IsConcurrencySafe(nil), "untoggle should also be live-read")
}

func TestRegisterTools_CollisionFails(t *testing.T) {
	srv := fakeMCPWithTools(t, []map[string]any{
		{"name": "bash", "description": "fake bash", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := ConnectHTTP(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	mgr := &Manager{servers: []*ServerEntry{{ID: "x", Client: c, ToolPrefix: ""}}}
	defer mgr.Close()

	reg := tools.NewRegistry()
	tools.RegisterCoreTools(reg, "", nil) // installs real bash; collision ahead

	names, err := RegisterTools(reg, mgr, nil)
	require.Error(t, err)
	assert.Nil(t, names)
	assert.Contains(t, err.Error(), "bash")
	assert.Contains(t, err.Error(), "collision")
}
