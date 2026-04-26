package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolInfo is the minimal projection of an MCP tool definition that the
// Stage 1 harness needs. Stage 2 may extend this.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// CallResult is the result of an MCP tools/call.
type CallResult struct {
	IsError bool
	Text    string          // concatenated text-content blocks
	Raw     json.RawMessage // full result for debugging
}

// Client is a thin wrapper over the official MCP SDK's Streamable-HTTP
// session. Keeps Felix code decoupled from SDK type churn.
type Client struct {
	session *mcpsdk.ClientSession
}

// ConnectHTTP opens an MCP session against serverURL over the Streamable
// HTTP transport, using the supplied HTTP client (which is expected to
// inject auth). The returned *Client must be Close()d when done.
func ConnectHTTP(ctx context.Context, serverURL string, httpClient *http.Client) (*Client, error) {
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:   serverURL,
		HTTPClient: httpClient,
	}

	sdkClient := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name:    "felix",
		Version: "0.0.0-stage1-harness",
	}, nil)

	session, err := sdkClient.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp connect: %w", err)
	}
	return &Client{session: session}, nil
}

// Close terminates the MCP session.
func (c *Client) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	return c.session.Close()
}

// ListTools returns the tools exposed by the server.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	res, err := c.session.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}
	out := make([]ToolInfo, 0, len(res.Tools))
	for _, t := range res.Tools {
		schema, _ := json.Marshal(t.InputSchema)
		out = append(out, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out, nil
}

// CallTool invokes a tool by name with the supplied JSON arguments.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	res, err := c.session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return CallResult{}, fmt.Errorf("mcp tools/call %s: %w", name, err)
	}

	var textBuf string
	for _, block := range res.Content {
		if tc, ok := block.(*mcpsdk.TextContent); ok {
			textBuf += tc.Text
		}
	}
	raw, _ := json.Marshal(res)
	return CallResult{
		IsError: res.IsError,
		Text:    textBuf,
		Raw:     raw,
	}, nil
}
