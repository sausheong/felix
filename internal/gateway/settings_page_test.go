package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sausheong/felix/internal/config"
)

func TestSettingsPage_ServesEmbeddedHTML(t *testing.T) {
	h := NewSettingsHandlers(config.DefaultConfig(), nil, nil, func(*config.Config) {})
	rec := httptest.NewRecorder()
	h.Page(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "<!DOCTYPE html>") {
		t.Fatalf("body does not start with a DOCTYPE: %.40q", body)
	}
	if !strings.Contains(body, "</html>") {
		t.Fatal("body missing closing </html>")
	}
	// Served bytes must equal the embedded asset verbatim (no templating).
	if body != string(settingsHTML) {
		t.Fatal("served body differs from embedded settings.html")
	}
}

func TestSettingsHTML_NoLeftoverFormatVerbs(t *testing.T) {
	// The asset is served with w.Write, not fmt.Fprintf, so any stray fmt
	// verb would render literally.
	s := string(settingsHTML)
	for _, bad := range []string{"%d", "%s", "%v", "%%"} {
		if strings.Contains(s, bad) {
			t.Errorf("embedded settings.html contains stray format token %q", bad)
		}
	}
}
