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
	// Pre-flight circuit breaker: if this server has failed
	// MaxConsecutiveAuthFailures times in a row even after auto-Reconnect,
	// stop hitting the network — return a strong "do not call this
	// server again" message so the agent doesn't loop on a server that
	// requires the user to investigate. The breaker resets on successful
	// tool calls and on user-initiated Manager.ReconnectServer (the chat
	// UI's "Re-authenticate" button).
	if n := a.entry.FailureCount(); n >= MaxConsecutiveAuthFailures {
		return tools.ToolResult{
			Error: fmt.Sprintf(
				"MCP server %q has failed %d consecutive auth attempts including automatic reconnection — the server appears to be in a bad state that re-authentication alone is not fixing. Stop calling tools from this server in this conversation. Tell the user to investigate the server-side issue (the gateway may be misconfigured, the user may lack the required scopes, or the upstream service may be down) and try again later. Do NOT call any %s.* tools again until the user confirms the server is fixed.",
				a.entry.ID, n, a.entry.ID,
			),
			Metadata: map[string]any{"auth_required": a.entry.ID, "circuit_breaker": true},
		}, nil
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
			a.entry.RecordFailure()
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
			a.entry.RecordFailure()
		} else {
			// Tool ran and reported a non-auth error — the server is
			// reachable, just unhappy with this call. Reset the breaker
			// so an unrelated bad call doesn't accumulate toward the cap.
			a.entry.RecordSuccess()
		}
	} else {
		// Clean success — server is healthy. Reset the breaker.
		a.entry.RecordSuccess()
	}
	return tr, nil
}

// isAuthFailure reports whether err looks like a failure that
// re-authentication or session reconnection would fix. Covers the
// common auth signatures across providers (Cognito, Okta, Auth0,
// Azure AD, GitHub, Google), the Streamable-HTTP session-rejection
// patterns the MCP go-sdk surfaces, the OAuth refresh failure modes
// from golang.org/x/oauth2, AND the SDK's session-terminal-state
// signals (`client is closing`, `connection closed: calling …`)
// that surface after the SDK has torn down the local session in
// response to any prior failure — once that happens, only Reconnect
// can recover.
//
// Conservative on purpose: a false positive turns a real failure into a
// "please re-auth" prompt the user will safely dismiss; a false negative
// leaves the user with a cryptic error and a restart, which we're
// trying to avoid. Transport-level failures the SDK has not yet acted on
// (raw connection refused, DNS, plain timeouts) are still NOT classified —
// those need retry/backoff, not reconnection.
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
		strings.Contains(s, "oauth2: token expired"),
		// MCP SDK session-terminal-state signals. The SDK closes the
		// local ClientSession after its first transport failure (e.g. a
		// 400 from the gateway), and every subsequent CallTool fails
		// with these wrappers regardless of the original cause. Only
		// Reconnect (which builds a fresh session) recovers — clicking
		// Re-authenticate is the user-facing recovery action even when
		// the underlying issue isn't an auth one (the PKCE flow rebuilds
		// the session as a side effect).
		strings.Contains(s, "client is closing"),
		strings.Contains(s, "connection closed: calling"),
		// First-failure pattern from MCP Streamable-HTTP: a 400 from the
		// gateway on a JSON-RPC call almost always means the server
		// rejected the Mcp-Session-Id (expired or recycled), since
		// genuine argument-validation errors come back as JSON-RPC errors
		// with HTTP 200 + isError=true rather than as transport-level
		// 400s. Match only inside the SDK's `sending "..."` wrap so
		// unrelated 400s from other paths don't trip the heuristic.
		strings.Contains(s, `sending "`) && strings.Contains(s, `: bad request`):
		return true
	}
	return false
}
