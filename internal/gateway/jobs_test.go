package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJobsHandler_DerivesWebSocketFromOrigin(t *testing.T) {
	rec := httptest.NewRecorder()
	NewJobsHandler(18789)(rec, httptest.NewRequest(http.MethodGet, "/jobs", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	// The page must build the WS URL from location, not a hardcoded host.
	if strings.Contains(body, "ws://localhost:") {
		t.Error("jobs page still hardcodes ws://localhost — should derive from origin")
	}
	if !strings.Contains(body, "location.host + '/ws'") {
		t.Error("jobs page missing origin-derived WebSocket URL")
	}
	if !strings.Contains(body, "wss://") {
		t.Error("jobs page must select wss:// under TLS")
	}
	// The dead PORT template should be gone (no stray format verbs in a page
	// now served with fmt.Fprint).
	if strings.Contains(body, "var PORT") || strings.Contains(body, "%d") {
		t.Error("jobs page still references the removed PORT template or a stray format verb")
	}
}
