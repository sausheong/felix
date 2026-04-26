package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMCPWithTools spins up the same fake protocol from client_test.go but
// allows the caller to supply the tool list returned by tools/list.
func fakeMCPWithTools(t *testing.T, tools []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		method, _ := req["method"].(string)
		id := req["id"]
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "fake", "version": "0"},
				},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"tools": tools},
			})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
}

// fakeTokenServer returns a static OAuth token, so Manager can be tested with
// real OAuth wiring (not just http.DefaultClient).
func fakeTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-abc", "token_type": "Bearer", "expires_in": 3600,
		})
	}))
}

func TestManager_OpensAllEnabledServers(t *testing.T) {
	srvA := fakeMCPWithTools(t, []map[string]any{
		{"name": "a_tool", "description": "from A", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srvA.Close()
	srvB := fakeMCPWithTools(t, []map[string]any{
		{"name": "b_tool", "description": "from B", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srvB.Close()
	tok := fakeTokenServer(t)
	defer tok.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mgr, err := NewManager(ctx, []ManagerServerConfig{
		{ID: "a", Transport: "http", ToolPrefix: "a_", HTTP: &HTTPServerConfig{
			URL: srvA.URL,
			Auth: HTTPAuthConfig{
				Kind: "oauth2_client_credentials", TokenURL: tok.URL,
				ClientID: "cid", ClientSecret: "sec",
			},
		}},
		{ID: "b", Transport: "http", HTTP: &HTTPServerConfig{
			URL: srvB.URL,
			Auth: HTTPAuthConfig{
				Kind: "oauth2_client_credentials", TokenURL: tok.URL,
				ClientID: "cid", ClientSecret: "sec",
			},
		}},
	})
	require.NoError(t, err)
	defer mgr.Close()

	servers := mgr.Servers()
	require.Len(t, servers, 2)

	ids := []string{servers[0].ID, servers[1].ID}
	assert.ElementsMatch(t, []string{"a", "b"}, ids)
}

func TestManager_SkipsUnreachableServer(t *testing.T) {
	srvOK := fakeMCPWithTools(t, []map[string]any{
		{"name": "ok", "description": "alive", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srvOK.Close()
	tok := fakeTokenServer(t)
	defer tok.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	mgr, err := NewManager(ctx, []ManagerServerConfig{
		{ID: "ok", Transport: "http", HTTP: &HTTPServerConfig{
			URL: srvOK.URL,
			Auth: HTTPAuthConfig{
				Kind: "oauth2_client_credentials", TokenURL: tok.URL,
				ClientID: "c", ClientSecret: "s",
			},
		}},
		{ID: "dead", Transport: "http", HTTP: &HTTPServerConfig{
			URL: "http://127.0.0.1:1/closed",
			Auth: HTTPAuthConfig{
				Kind: "oauth2_client_credentials", TokenURL: tok.URL,
				ClientID: "c", ClientSecret: "s",
			},
		}},
	})
	require.NoError(t, err) // no hard failure; dead server is logged and skipped
	defer mgr.Close()

	servers := mgr.Servers()
	require.Len(t, servers, 1)
	assert.Equal(t, "ok", servers[0].ID)
}
