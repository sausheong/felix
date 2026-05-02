package mcp

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsAuthFailure_RecognizesCommonSignatures keeps the detection
// list honest. Any provider-specific signature added to isAuthFailure
// should land here so a future refactor doesn't silently regress the
// chat-side re-auth UX.
func TestIsAuthFailure_RecognizesCommonSignatures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"401_status", errors.New(`unexpected status 401: bad token`), true},
		{"403_status", errors.New(`unexpected status 403: forbidden`), true},
		{"unauthorized", errors.New(`HTTP 401: Unauthorized`), true},
		{"unauthenticated_grpc", errors.New(`code = Unauthenticated desc = invalid token`), true},
		{"invalid_token_oauth", errors.New(`oauth2: server response: invalid_token`), true},
		{"token_expired", errors.New(`access token has expired`), true},
		{"session_expired", errors.New(`MCP session expired, please reconnect`), true},
		{"expired_token", errors.New(`expired_token: refresh required`), true},
		{"access_denied", errors.New(`access denied`), true},
		{"permission_denied", errors.New(`permission denied`), true},
		// MCP Streamable-HTTP session-rejection patterns.
		{"session_not_found", errors.New(`mcp tools/call echo: session not found`), true},
		{"session_not_found_underscore", errors.New(`server returned session_not_found`), true},
		{"session_terminated", errors.New(`mcp: session terminated by server`), true},
		{"session_no_longer_valid", errors.New(`mcp: session is no longer valid`), true},
		{"must_reauthenticate", errors.New(`upstream says you must re-authenticate`), true},
		{"please_reauthenticate", errors.New(`please re-authenticate to continue`), true},
		// oauth2 refresh-failure patterns (rotated/revoked refresh token).
		{"invalid_grant", errors.New(`oauth2: server response: {"error":"invalid_grant"}`), true},
		{"oauth2_cannot_fetch_token", errors.New(`oauth2: cannot fetch token: 400 Bad Request`), true},
		// MCP SDK session-terminal-state signals. After the first
		// transport failure the SDK marks the client as closing, and
		// every subsequent call surfaces these wrappers — only Reconnect
		// recovers. See logs from the assistantai/aiap-google-workspace
		// gateway where call N=1 returns plain Bad Request and calls
		// N=2..N all return "client is closing: ... Bad Request".
		{"client_is_closing", errors.New(`mcp tools/call x: connection closed: calling "tools/call": client is closing: sending "tools/call": Bad Request`), true},
		{"connection_closed_calling", errors.New(`connection closed: calling "tools/list": context canceled`), true},
		// First-failure pattern: HTTP 400 on a tools/call from an MCP
		// gateway is almost always Mcp-Session-Id rejection (genuine
		// argument errors come back as JSON-RPC isError=true with HTTP
		// 200, not as transport-level 400s).
		{"sending_tools_call_bad_request", errors.New(`mcp tools/call x: calling "tools/call": sending "tools/call": Bad Request`), true},

		{"nil", nil, false},
		{"context_canceled", errors.New(`context canceled`), false},
		{"network_unreachable", errors.New(`dial tcp: connection refused`), false},
		{"timeout", errors.New(`Client.Timeout exceeded while awaiting headers`), false},
		{"500_server_error", errors.New(`HTTP 500: internal server error`), false},
		{"tool_validation", errors.New(`invalid arguments: missing field "query"`), false},
		// Tighten guard around the new patterns: "no longer valid" must
		// not promote unrelated 5xx prose to auth.
		{"503_no_longer_available", errors.New(`HTTP 503: service is temporarily unavailable`), false},
		// Make sure plain "bad request" without the SDK's `sending "…"`
		// wrap is NOT promoted — a downstream HTTP 400 from a non-MCP
		// path should stay a transport error.
		{"plain_bad_request", errors.New(`HTTP 400: Bad Request from upstream cdn`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isAuthFailure(tc.err))
		})
	}
}

// TestReconnectServer_UnknownIDErrors guards the public manager API:
// callers (the HTTP re-auth endpoint) need to distinguish "server not
// configured" from a transport-level reconnect failure so they can
// return the right HTTP status.
func TestReconnectServer_UnknownIDErrors(t *testing.T) {
	mgr := &Manager{}
	err := mgr.ReconnectServer(t.Context(), "ghost")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `"ghost"`)
}

// TestServerEntry_LiveReturnsCurrentClient verifies that adapters
// holding *ServerEntry observe a Reconnect-driven swap on the next
// Live() call. This is the load-bearing property that makes in-chat
// re-auth work without re-registering tools.
func TestServerEntry_LiveReturnsCurrentClient(t *testing.T) {
	c1 := &Client{}
	c2 := &Client{}
	entry := &ServerEntry{ID: "x", client: c1}

	assert.Same(t, c1, entry.Live(), "Live() should return the initial client")

	// Simulate the swap that Reconnect performs internally.
	entry.mu.Lock()
	entry.client = c2
	entry.mu.Unlock()

	assert.Same(t, c2, entry.Live(), "Live() should return the swapped client without re-registration")
}
