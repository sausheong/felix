package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMCPServer is a hand-rolled HTTP handler that speaks just enough of the
// MCP Streamable-HTTP protocol for ListTools to succeed. It does NOT
// implement the full protocol — the goal is to verify our wrapper sends a
// request, parses a response, and surfaces errors. Real protocol coverage
// comes from the manual end-to-end run in Task 6.
func fakeMCPServer(t *testing.T) *httptest.Server {
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
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "echo",
							"description": "Echo input",
							"inputSchema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"text": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			})
		default:
			// Treat anything else (notifications/initialized, etc.) as ack.
			w.WriteHeader(http.StatusAccepted)
		}
	}))
}

func TestClient_ListTools(t *testing.T) {
	srv := fakeMCPServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := ConnectHTTP(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	defer c.Close()

	tools, err := c.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0].Name)
	assert.Equal(t, "Echo input", tools[0].Description)
	assert.Contains(t, string(tools[0].InputSchema), "text")
}

func TestClient_ConnectFails_OnBadURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := ConnectHTTP(ctx, "http://127.0.0.1:1/definitely-closed", http.DefaultClient)
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "connect") ||
			strings.Contains(err.Error(), "refused") ||
			strings.Contains(err.Error(), "initialize"),
		"unexpected error: %v", err,
	)
}

// fakePaginatedMCPServer serves tools/list in pages of 4 with nextCursor,
// mirroring real-world servers (e.g. Microsoft 365 MCP) that paginate.
// 10 tools across 3 pages: 4 + 4 + 2.
func fakePaginatedMCPServer(t *testing.T, total int, pageSize int) *httptest.Server {
	t.Helper()
	toolAt := func(i int) map[string]any {
		return map[string]any{
			"name":        "tool_" + string(rune('a'+i)),
			"description": "Tool number " + string(rune('a'+i)),
			"inputSchema": map[string]any{"type": "object"},
		}
	}
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
					"serverInfo":      map[string]any{"name": "fake-paged", "version": "0"},
				},
			})
		case "tools/list":
			params, _ := req["params"].(map[string]any)
			start := 0
			if params != nil {
				if cur, ok := params["cursor"].(string); ok && cur != "" {
					// Cursor format: plain integer offset.
					var n int
					_, _ = fmt.Sscanf(cur, "%d", &n)
					start = n
				}
			}
			end := start + pageSize
			if end > total {
				end = total
			}
			tools := make([]map[string]any, 0, pageSize)
			for i := start; i < end; i++ {
				tools = append(tools, toolAt(i))
			}
			result := map[string]any{"tools": tools}
			if end < total {
				result["nextCursor"] = fmt.Sprintf("%d", end)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id, "result": result,
			})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
}

// TestClient_ListTools_FollowsPagination is the Microsoft-365-shaped
// regression: a server with 10 tools served 4 per page must yield all
// 10, not just the first page.
func TestClient_ListTools_FollowsPagination(t *testing.T) {
	srv := fakePaginatedMCPServer(t, 10, 4)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := ConnectHTTP(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	defer c.Close()

	tools, err := c.ListTools(ctx)
	require.NoError(t, err)
	assert.Len(t, tools, 10,
		"ListTools must follow nextCursor across all pages — stopping after page 1 is the '4 tools instead of many' bug")
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
	}
	assert.Len(t, names, 10, "all tools must be distinct (no page repeated)")
}
