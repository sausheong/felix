package mcp

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeMCPWithEcho(t *testing.T) *httptest.Server {
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
		case "tools/call":
			params, _ := req["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			text, _ := args["text"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "echo: " + text},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
}

func TestAdapter_Execute(t *testing.T) {
	srv := fakeMCPWithEcho(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := ConnectHTTP(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	defer c.Close()

	entry := &ServerEntry{ID: "ltm", client: c}
	a := newToolAdapter("ltm_echo", "echo", "Echo back text",
		json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		entry, nil)

	assert.Equal(t, "ltm_echo", a.Name())
	assert.Equal(t, "Echo back text", a.Description())
	assert.JSONEq(t, `{"type":"object","properties":{"text":{"type":"string"}}}`, string(a.Parameters()))

	res, err := a.Execute(ctx, json.RawMessage(`{"text":"hi"}`))
	require.NoError(t, err)
	assert.Equal(t, "echo: hi", res.Output)
	assert.Empty(t, res.Error)
}

func TestAdapter_BadInput(t *testing.T) {
	a := newToolAdapter("x", "x", "", nil, &ServerEntry{ID: "x"}, nil)
	res, err := a.Execute(context.Background(), json.RawMessage(`not json`))
	require.NoError(t, err) // tool errors are surfaced via res.Error, not err
	assert.Contains(t, res.Error, "invalid arguments")
}

// TestMcpAdapter_IsConcurrencySafe_FuncReturnsLiveValue verifies that the
// adapter's concurrency-safe report tracks the closure's return value at
// call time — i.e. settings-page toggles take effect on the next call
// without rebuilding the adapter.
func TestMcpAdapter_IsConcurrencySafe_FuncReturnsLiveValue(t *testing.T) {
	var current atomic.Bool
	fn := func(id string) bool {
		if id != "myserver" {
			return false
		}
		return current.Load()
	}
	a := newToolAdapter("x_t", "t", "", json.RawMessage(`{}`), &ServerEntry{ID: "myserver"}, fn)

	require.False(t, a.IsConcurrencySafe(nil))
	current.Store(true)
	require.True(t, a.IsConcurrencySafe(nil), "live read should pick up the toggle without rebuild")
	current.Store(false)
	require.False(t, a.IsConcurrencySafe(nil))
}

// TestMcpAdapter_IsConcurrencySafe_NilFnReturnsFalse verifies the legacy
// "always false" behavior is preserved when no closure is wired (used by
// tests and any call sites that don't need hot-reload).
func TestMcpAdapter_IsConcurrencySafe_NilFnReturnsFalse(t *testing.T) {
	a := newToolAdapter("x_t", "t", "", json.RawMessage(`{}`), &ServerEntry{ID: "anything"}, nil)
	require.False(t, a.IsConcurrencySafe(nil))
}

// fakeMCPWithFlakyAuth returns a server whose `tools/call` returns an
// auth-shaped error until `failuresBeforeOK` calls have landed, then
// switches to normal echo behaviour. `callCount` is incremented on every
// `tools/call` so tests can assert how many calls landed. Set
// failuresBeforeOK to math.MaxInt32 to make every call fail.
func fakeMCPWithFlakyAuth(t *testing.T, callCount *atomic.Int32, failuresBeforeOK int32) *httptest.Server {
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
		case "tools/call":
			n := callCount.Add(1)
			if n <= failuresBeforeOK {
				// JSON-RPC error with an auth-shaped message — must match
				// isAuthFailure so the adapter triggers Reconnect+retry.
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": id,
					"error": map[string]any{
						"code":    -32000,
						"message": "session not found",
					},
				})
				return
			}
			params, _ := req["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			text, _ := args["text"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "echo: " + text},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
}

// TestAdapter_Execute_RetriesAfterAuthFailure verifies the auto-recovery
// path: a tool call that fails with an auth-shaped error triggers
// entry.Reconnect, and the retried call succeeds without surfacing
// auth_required to the agent.
func TestAdapter_Execute_RetriesAfterAuthFailure(t *testing.T) {
	var calls atomic.Int32
	// Fail only the first tools/call; the post-reconnect retry succeeds.
	srv := fakeMCPWithFlakyAuth(t, &calls, 1)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := ConnectHTTP(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	defer c.Close()

	// cfg has to be valid so Reconnect can re-Connect against the same URL.
	entry := &ServerEntry{
		ID:     "flaky",
		client: c,
		cfg: ManagerServerConfig{
			ID:        "flaky",
			Transport: "http",
			HTTP:      &HTTPServerConfig{URL: srv.URL, Auth: HTTPAuthConfig{Kind: "none"}},
		},
	}
	a := newToolAdapter("flaky_echo", "echo", "Echo back text",
		json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		entry, nil)

	res, err := a.Execute(ctx, json.RawMessage(`{"text":"hi"}`))
	require.NoError(t, err)
	assert.Equal(t, "echo: hi", res.Output, "retry should succeed after reconnect")
	assert.Empty(t, res.Error, "retry success must not surface an error to the agent")
	assert.Nil(t, res.Metadata, "retry success must not stamp auth_required (no UI button needed)")
	assert.GreaterOrEqual(t, calls.Load(), int32(2), "expected at least the original + retry calls")
}

// TestAdapter_Execute_AuthFailureSurfacesWhenRetryAlsoFails verifies the
// fallback: if reconnect succeeds but the server keeps returning auth
// errors, the adapter still surfaces auth_required so the chat UI can
// render the "Re-authenticate" button.
func TestAdapter_Execute_AuthFailureSurfacesWhenRetryAlsoFails(t *testing.T) {
	var calls atomic.Int32
	// math.MaxInt32 means every tools/call returns the auth-shaped error.
	srv := fakeMCPWithFlakyAuth(t, &calls, math.MaxInt32)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := ConnectHTTP(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	defer c.Close()

	entry := &ServerEntry{
		ID:     "flaky",
		client: c,
		cfg: ManagerServerConfig{
			ID:        "flaky",
			Transport: "http",
			HTTP:      &HTTPServerConfig{URL: srv.URL, Auth: HTTPAuthConfig{Kind: "none"}},
		},
	}
	a := newToolAdapter("flaky_echo", "echo", "", json.RawMessage(`{}`), entry, nil)

	res, err := a.Execute(ctx, json.RawMessage(`{"text":"hi"}`))
	require.NoError(t, err)
	assert.NotEmpty(t, res.Error, "persistent auth failure must surface as tool error")
	require.NotNil(t, res.Metadata, "auth_required metadata must be set so the chat UI renders the button")
	assert.Equal(t, "flaky", res.Metadata["auth_required"])
	assert.GreaterOrEqual(t, calls.Load(), int32(2), "should attempt original + at least one retry")
}
