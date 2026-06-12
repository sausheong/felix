package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuardedRoutesRejectCrossSite(t *testing.T) {
	wsHandler := &WebSocketHandler{}
	s := NewServer("127.0.0.1", 0, wsHandler, ServerOptions{})

	srv := httptest.NewServer(s.router)
	defer srv.Close()

	// Cross-site POST to a mutating route must be 403.
	req, _ := http.NewRequest("POST", srv.URL+"/admin/restart", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}
