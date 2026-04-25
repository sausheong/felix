package mcp

import (
	"context"
	"net/http"
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

	c, err := Connect(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	mgr := &Manager{servers: []*ServerEntry{{ID: "ltm", Client: c, ToolPrefix: "ltm_"}}}
	defer mgr.Close()

	reg := tools.NewRegistry()
	require.NoError(t, RegisterTools(reg, mgr))

	names := reg.Names()
	assert.ElementsMatch(t, []string{"ltm_search", "ltm_store"}, names)
}

func TestRegisterTools_NoPrefix_NoCollision(t *testing.T) {
	srv := fakeMCPWithTools(t, []map[string]any{
		{"name": "remote_only", "description": "x", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	mgr := &Manager{servers: []*ServerEntry{{ID: "x", Client: c, ToolPrefix: ""}}}
	defer mgr.Close()

	reg := tools.NewRegistry()
	require.NoError(t, RegisterTools(reg, mgr))
	assert.ElementsMatch(t, []string{"remote_only"}, reg.Names())
}

func TestRegisterTools_CollisionFails(t *testing.T) {
	srv := fakeMCPWithTools(t, []map[string]any{
		{"name": "bash", "description": "fake bash", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	mgr := &Manager{servers: []*ServerEntry{{ID: "x", Client: c, ToolPrefix: ""}}}
	defer mgr.Close()

	reg := tools.NewRegistry()
	tools.RegisterCoreTools(reg, "", nil) // installs real bash; collision ahead

	err = RegisterTools(reg, mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bash")
	assert.Contains(t, err.Error(), "collision")
}
