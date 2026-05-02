package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sausheong/felix/internal/tools"
)

// mcpToolAdapter wraps a remote MCP tool as a Felix tools.Tool. The adapter
// is constructed by RegisterTools (one per remote tool per server) and
// registered into a tools.Registry alongside core tools.
//
// Holds *ServerEntry rather than *Client so that calls always read the
// freshest client via entry.Live() — picking up any in-process Reconnect
// triggered by the Settings/Chat re-auth flow without re-registration.
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
	entry        *ServerEntry
	parallelSafe ParallelSafeFn // nil-safe; nil → IsConcurrencySafe returns false
}

// newToolAdapter is package-private constructor. RegisterTools is the only
// in-package caller; tests may use it via the same package.
func newToolAdapter(fullName, remoteName, description string, schema json.RawMessage,
	entry *ServerEntry, parallelSafe ParallelSafeFn) *mcpToolAdapter {
	return &mcpToolAdapter{
		fullName:     fullName,
		remoteName:   remoteName,
		description:  description,
		schema:       schema,
		entry:        entry,
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
	return a.parallelSafe(a.entry.ID)
}

func (a *mcpToolAdapter) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return tools.ToolResult{Error: fmt.Sprintf("invalid arguments JSON: %v", err)}, nil
		}
	}
	client := a.entry.Live()
	if client == nil {
		return tools.ToolResult{
			Error:    fmt.Sprintf("MCP server %q is not connected. Re-authenticate to reconnect.", a.entry.ID),
			Metadata: map[string]any{"auth_required": a.entry.ID},
		}, nil
	}
	res, err := client.CallTool(ctx, a.remoteName, args)
	if err != nil && isAuthFailure(err) {
		// In-memory MCP session may be dead even though the on-disk token
		// is still valid (token refreshed by the oauth2 RoundTripper but
		// the server-side Streamable-HTTP session was invalidated). Try
		// one Reconnect+retry before surfacing auth_required to the UI.
		// We stay silent on success; the agent sees a clean tool result.
		// On reconnect or retry failure we fall through to the standard
		// auth_required path below so the user can still drive the
		// browser-based PKCE flow.
		if rcErr := a.entry.Reconnect(ctx); rcErr == nil {
			if retryClient := a.entry.Live(); retryClient != nil {
				if rRes, rErr := retryClient.CallTool(ctx, a.remoteName, args); rErr == nil {
					res, err = rRes, nil
				}
			}
		}
	}
	if err != nil {
		// Transport-level failure — surface as tool error, not a Go error,
		// so the agent loop can keep going. If the failure looks like an
		// auth failure (401, expired token, invalid_token, unauthorized),
		// tag the result with metadata so the chat UI can render an
		// inline "Re-authenticate" button bound to this server.
		tr := tools.ToolResult{Error: err.Error()}
		if isAuthFailure(err) {
			tr.Metadata = map[string]any{"auth_required": a.entry.ID}
			tr.Error = fmt.Sprintf("MCP server %q rejected the call (auth expired). Re-authenticate to continue. Underlying error: %v", a.entry.ID, err)
		}
		return tr, nil
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
		// Server-reported tool errors can also signal auth — some MCP
		// servers respond with isError=true and a 401-shaped message
		// rather than failing at the transport.
		if isAuthFailure(fmt.Errorf("%s", tr.Error)) {
			tr.Metadata = map[string]any{"auth_required": a.entry.ID}
		}
	}
	return tr, nil
}

// isAuthFailure reports whether err looks like an MCP/HTTP authentication
// failure that re-authentication would fix. Covers the common signatures
// across providers (Cognito, Okta, Auth0, Azure AD, GitHub, Google) plus
// the Streamable-HTTP session-rejection patterns the MCP go-sdk surfaces
// when a server invalidates an in-flight session and the OAuth refresh
// failure modes from golang.org/x/oauth2.
//
// Conservative on purpose: a false positive turns a real failure into a
// "please re-auth" prompt the user will safely dismiss; a false negative
// leaves the user with a cryptic error and a restart, which we're
// trying to avoid. Transport-level failures (connection refused, DNS,
// raw timeouts) are deliberately NOT classified as auth — those need
// retry/backoff, not re-authentication.
func isAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "401"),
		strings.Contains(s, "403"),
		strings.Contains(s, "unauthorized"),
		strings.Contains(s, "unauthenticated"),
		strings.Contains(s, "invalid_token"),
		strings.Contains(s, "invalid token"),
		strings.Contains(s, "token expired"),
		strings.Contains(s, "token has expired"),
		strings.Contains(s, "session expired"),
		strings.Contains(s, "expired_token"),
		strings.Contains(s, "access denied"),
		strings.Contains(s, "permission denied"),
		// MCP Streamable-HTTP session-level rejections. The server has
		// torn down the Mcp-Session-Id we negotiated at startup; a fresh
		// Connect (Reconnect) is what fixes it, and that's exactly what
		// the auth_required path drives.
		strings.Contains(s, "session not found"),
		strings.Contains(s, "session_not_found"),
		strings.Contains(s, "session terminated"),
		strings.Contains(s, "session is no longer valid"),
		strings.Contains(s, "no longer valid"),
		strings.Contains(s, "must re-authenticate"),
		strings.Contains(s, "must reauthenticate"),
		strings.Contains(s, "please re-authenticate"),
		strings.Contains(s, "re-authentication required"),
		// golang.org/x/oauth2 refresh-failure shapes. These surface when
		// the cached refresh_token is rejected (rotated, revoked, IdP
		// reset) — the user has to drive the interactive PKCE flow again.
		strings.Contains(s, "invalid_grant"),
		strings.Contains(s, "oauth2: cannot fetch token"),
		strings.Contains(s, "oauth2: token expired"):
		return true
	}
	return false
}
