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

	c, err := Connect(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	defer c.Close()

	a := newToolAdapter("ltm_echo", "echo", "Echo back text",
		json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		c)

	assert.Equal(t, "ltm_echo", a.Name())
	assert.Equal(t, "Echo back text", a.Description())
	assert.JSONEq(t, `{"type":"object","properties":{"text":{"type":"string"}}}`, string(a.Parameters()))

	res, err := a.Execute(ctx, json.RawMessage(`{"text":"hi"}`))
	require.NoError(t, err)
	assert.Equal(t, "echo: hi", res.Output)
	assert.Empty(t, res.Error)
}

func TestAdapter_BadInput(t *testing.T) {
	a := newToolAdapter("x", "x", "", nil, nil)
	res, err := a.Execute(context.Background(), json.RawMessage(`not json`))
	require.NoError(t, err) // tool errors are surfaced via res.Error, not err
	assert.Contains(t, res.Error, "invalid arguments")
}
