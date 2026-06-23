package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sausheong/felix/internal/config"
)

func TestRenderAgents_EscapesHTML(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{
					ID:        "a<script>",
					Name:      `Evil "Agent"`,
					Model:     "m&m",
					Workspace: "/tmp/<x>",
					Sandbox:   "none",
				},
			},
		},
	}
	out := renderAgents(cfg)

	// Raw, unescaped markup must not appear.
	if strings.Contains(out, "<script>") {
		t.Errorf("renderAgents did not escape '<script>': %s", out)
	}
	// Escaped entities must appear instead.
	for _, want := range []string{"a&lt;script&gt;", "&#34;Agent&#34;", "m&amp;m", "/tmp/&lt;x&gt;"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderAgents output missing escaped %q\ngot: %s", want, out)
		}
	}
}

func TestUIHandler_ServesEscapedDashboard(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{{ID: "x", Name: "<b>bold</b>", Model: "m"}},
		},
	}
	h := NewUIHandler(cfg, "v9.9.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(body, "<b>bold</b>") {
		t.Errorf("dashboard rendered an unescaped agent name")
	}
	if !strings.Contains(body, "&lt;b&gt;bold&lt;/b&gt;") {
		t.Errorf("dashboard missing escaped agent name")
	}
	if !strings.Contains(body, "v9.9.9") {
		t.Errorf("dashboard missing version")
	}
}
