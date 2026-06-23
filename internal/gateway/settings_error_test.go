package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sausheong/felix/internal/config"
)

// TestSaveConfig_MalformedJSON_ReturnsValidJSONError verifies the regression
// fix: a json.Unmarshal error message contains characters ('"', '\n', etc.)
// that, when interpolated into a hand-built JSON string, produced an invalid
// body the client could not parse. The error body must itself be valid JSON.
func TestSaveConfig_MalformedJSON_ReturnsValidJSONError(t *testing.T) {
	cfg := config.DefaultConfig()
	h := NewSettingsHandlers(cfg, nil, nil, func(*config.Config) {})

	// Body that is invalid JSON in a way that yields a quote-bearing error
	// message (e.g. `invalid character 'x' ...`).
	req := httptest.NewRequest(http.MethodPut, "/settings/api/config", strings.NewReader("{not valid json"))
	rec := httptest.NewRecorder()
	h.SaveConfig(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	// The body must be parseable JSON with an "error" field carrying the
	// underlying message verbatim (now properly escaped).
	var payload struct {
		Error string `json:"error"`
	}
	body := rec.Body.Bytes()
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("response body is not valid JSON: %v\nbody: %s", err, body)
	}
	if !strings.HasPrefix(payload.Error, "invalid JSON:") {
		t.Fatalf("error = %q, want prefix %q", payload.Error, "invalid JSON:")
	}
}

// TestWriteJSONError_EscapesSpecialCharacters is a focused unit test on the
// helper: a message containing a double-quote and newline must round-trip
// through JSON.parse intact.
func TestWriteJSONError_EscapesSpecialCharacters(t *testing.T) {
	rec := httptest.NewRecorder()
	msg := "boom: \"quoted\" and\nnewline and \\backslash"
	writeJSONError(rec, http.StatusInternalServerError, msg)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body not valid JSON: %v\nbody: %s", err, rec.Body.String())
	}
	if payload["error"] != msg {
		t.Fatalf("error = %q, want %q (must survive escaping verbatim)", payload["error"], msg)
	}
}
