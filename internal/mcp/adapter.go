package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sausheong/felix/internal/tools"
)

// mcpToolAdapter wraps a remote MCP tool as a Felix tools.Tool. The adapter
// is constructed by RegisterTools (one per remote tool per server) and
// registered into a tools.Registry alongside core tools.
//
// The parallelSafe hint is read live from a closure on each
// IsConcurrencySafe call (rather than snapshotted at construction time) so
// that toggling mcp_servers[].parallelSafe via the settings UI takes effect
// on the next agent run without restart.
type mcpToolAdapter struct {
	fullName     string // name as Felix sees it (with prefix applied)
	remoteName   string // name as the MCP server knows it
	description  string
	schema       json.RawMessage
	client       *Client
	serverID     string         // owning MCP server's ID; passed to parallelSafe on each call
	parallelSafe ParallelSafeFn // nil-safe; nil → IsConcurrencySafe returns false
}

// newToolAdapter is package-private constructor. RegisterTools is the only
// in-package caller; tests may use it via the same package.
func newToolAdapter(fullName, remoteName, description string, schema json.RawMessage,
	client *Client, serverID string, parallelSafe ParallelSafeFn) *mcpToolAdapter {
	return &mcpToolAdapter{
		fullName:     fullName,
		remoteName:   remoteName,
		description:  description,
		schema:       schema,
		client:       client,
		serverID:     serverID,
		parallelSafe: parallelSafe,
	}
}

func (a *mcpToolAdapter) Name() string                { return a.fullName }
func (a *mcpToolAdapter) Description() string         { return a.description }
func (a *mcpToolAdapter) Parameters() json.RawMessage { return a.schema }

// IsConcurrencySafe defers to the live config via the closure passed at
// construction time. Returns false when no closure was provided (preserves
// the conservative "MCP tools have unknown side effects" default for tests
// and call sites that don't wire hot-reload).
func (a *mcpToolAdapter) IsConcurrencySafe(_ json.RawMessage) bool {
	if a.parallelSafe == nil {
		return false
	}
	return a.parallelSafe(a.serverID)
}

func (a *mcpToolAdapter) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return tools.ToolResult{Error: fmt.Sprintf("invalid arguments JSON: %v", err)}, nil
		}
	}
	res, err := a.client.CallTool(ctx, a.remoteName, args)
	if err != nil {
		// Transport-level failure — surface as tool error, not a Go error,
		// so the agent loop can keep going.
		return tools.ToolResult{Error: err.Error()}, nil
	}
	tr := tools.ToolResult{Output: res.Text}
	if res.IsError {
		// Tool ran but reported an error. Put the text in Error so the agent
		// sees it as such; keep Output empty to avoid double-display.
		tr.Output = ""
		tr.Error = res.Text
		if tr.Error == "" {
			tr.Error = "tool returned isError without text"
		}
	}
	return tr, nil
}
