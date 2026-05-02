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

		{"nil", nil, false},
		{"context_canceled", errors.New(`context canceled`), false},
		{"network_unreachable", errors.New(`dial tcp: connection refused`), false},
		{"timeout", errors.New(`Client.Timeout exceeded while awaiting headers`), false},
		{"500_server_error", errors.New(`HTTP 500: internal server error`), false},
		{"tool_validation", errors.New(`invalid arguments: missing field "query"`), false},
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
