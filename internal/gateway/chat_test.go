package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewChatHandler_ServesEmbeddedHTML(t *testing.T) {
	rec := httptest.NewRecorder()
	NewChatHandler(18789)(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}

	body := rec.Body.String()
	if !strings.HasPrefix(body, "<!DOCTYPE html>") {
		t.Fatalf("body does not start with a DOCTYPE: %.40q", body)
	}
	if !strings.Contains(body, "</html>") {
		t.Fatal("body missing closing </html>")
	}
	// The served bytes must equal the embedded asset verbatim (no templating).
	if body != string(chatHTML) {
		t.Fatal("served body differs from embedded chat.html")
	}
}

func TestChatHTML_NoLeftoverFormatVerbs(t *testing.T) {
	// The asset is served with w.Write, not fmt.Fprintf, so any stray fmt
	// verb would now render literally. Guard against a regression where
	// someone re-introduces a "%d"/"%s" expecting substitution, and against
	// un-collapsed "%%" escapes left over from the extraction.
	s := string(chatHTML)
	for _, bad := range []string{"%d", "%s", "%v", "%%"} {
		if strings.Contains(s, bad) {
			t.Errorf("embedded chat.html contains stray format token %q", bad)
		}
	}
	// PORT was dead code in the old template; ensure it is not reintroduced.
	if strings.Contains(s, "var PORT") {
		t.Error("embedded chat.html reintroduced the dead PORT declaration")
	}
}
