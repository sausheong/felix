package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginAllowed(t *testing.T) {
	// empty allowlist => localhost-only
	require.True(t, originAllowed("http://127.0.0.1:18789", nil))
	require.True(t, originAllowed("http://localhost:3000", nil))
	require.True(t, originAllowed("https://localhost", nil))
	require.False(t, originAllowed("http://evil.com", nil))
	require.False(t, originAllowed("http://127.0.0.1.evil.com", nil))

	// explicit allowlist => exact match (trailing slash tolerated)
	allow := []string{"https://app.example.com"}
	require.True(t, originAllowed("https://app.example.com", allow))
	require.True(t, originAllowed("https://app.example.com/", allow))
	require.False(t, originAllowed("https://other.example.com", allow))
}

func TestRequireSameOrigin(t *testing.T) {
	mw := RequireSameOrigin(nil) // localhost-only
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := mw(next)

	call := func(method string, headers map[string]string) int {
		req := httptest.NewRequest(method, "/settings/api/config", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, call("POST", nil))
	require.Equal(t, http.StatusOK, call("POST", map[string]string{"Sec-Fetch-Site": "same-origin"}))
	require.Equal(t, http.StatusOK, call("POST", map[string]string{"Sec-Fetch-Site": "none"}))
	require.Equal(t, http.StatusForbidden, call("POST", map[string]string{"Sec-Fetch-Site": "cross-site"}))
	require.Equal(t, http.StatusOK, call("POST", map[string]string{"Origin": "http://localhost:5173"}))
	require.Equal(t, http.StatusForbidden, call("POST", map[string]string{"Origin": "http://evil.com"}))
	require.Equal(t, http.StatusForbidden, call("GET", map[string]string{"Sec-Fetch-Site": "cross-site"}))
}
