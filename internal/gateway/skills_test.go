package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sausheong/felix/internal/skill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSkillTest builds a fresh SkillHandlers wired to a temp skills dir.
func newSkillTest(t *testing.T) (*SkillHandlers, string, *skill.Loader) {
	t.Helper()
	dir := t.TempDir()
	loader := skill.NewLoader()
	require.NoError(t, loader.LoadFrom(dir))
	h := NewSkillHandlers(loader, dir, []string{dir})
	return h, dir, loader
}

// writeSkill writes a skill file with the given filename and content into dir.
func writeSkill(t *testing.T, dir, filename, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644))
}

// withChiName attaches a chi RouteContext carrying URL param "name" to the
// request. Handlers under /settings/api/skills/{name} read this via chi.URLParam.
func withChiName(req *http.Request, name string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", name)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestValidateSkillName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "cortex.md", false},
		{"with dashes and underscores", "my-skill_v2.md", false},
		{"with digits", "skill123.md", false},
		{"with dots", "skill.v2.md", false},
		{"empty", "", true},
		{"no .md extension", "cortex", true},
		{"wrong extension", "cortex.txt", true},
		{"path separator forward", "foo/bar.md", true},
		{"path separator back", "foo\\bar.md", true},
		{"parent traversal", "../foo.md", true},
		{"space", "foo bar.md", true},
		{"colon", "foo:bar.md", true},
		{"unicode", "fööö.md", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSkillName(tt.input)
			if tt.wantErr {
				assert.Error(t, err, "input %q", tt.input)
			} else {
				assert.NoError(t, err, "input %q", tt.input)
			}
		})
	}
}

func TestList_Empty(t *testing.T) {
	h, _, _ := newSkillTest(t)

	req := httptest.NewRequest(http.MethodGet, "/settings/api/skills", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"skills":[]}`, w.Body.String())
}

func TestList_ParsesFrontmatter(t *testing.T) {
	h, dir, _ := newSkillTest(t)
	writeSkill(t, dir, "alpha.md", "---\nname: alpha\ndescription: First skill\ntags: [a, b]\n---\nbody1\n")
	writeSkill(t, dir, "beta.md", "---\nname: beta\ndescription: Second skill\n---\nbody2\n")

	req := httptest.NewRequest(http.MethodGet, "/settings/api/skills", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `"name":"alpha"`)
	assert.Contains(t, body, `"description":"First skill"`)
	assert.Contains(t, body, `"filename":"alpha.md"`)
	assert.Contains(t, body, `"name":"beta"`)
	assert.Contains(t, body, `"tags":["a","b"]`)
}

func TestList_MalformedFrontmatter(t *testing.T) {
	h, dir, _ := newSkillTest(t)
	writeSkill(t, dir, "broken.md", "---\nname: [unclosed\n---\nbody\n")

	req := httptest.NewRequest(http.MethodGet, "/settings/api/skills", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	require.Equal(t, http.StatusOK, w.Code, "list must not fail on individual bad files")
	body := w.Body.String()
	assert.Contains(t, body, `"filename":"broken.md"`)
	assert.Contains(t, body, `"parse_error":`)
	assert.NotContains(t, body, `"parse_error":""`)
}

func TestList_MissingBins(t *testing.T) {
	h, dir, _ := newSkillTest(t)
	writeSkill(t, dir, "needsbin.md",
		"---\nname: needsbin\nmetadata:\n  openclaw:\n    requires:\n      bins: [definitely-not-installed-xyz-123]\n---\nbody\n")

	req := httptest.NewRequest(http.MethodGet, "/settings/api/skills", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `"unavailable":true`)
	assert.Contains(t, body, `"missing_bins":["definitely-not-installed-xyz-123"]`)
}

func TestGet_Found(t *testing.T) {
	h, dir, _ := newSkillTest(t)
	const content = "---\nname: cortex\n---\nbody here\n"
	writeSkill(t, dir, "cortex.md", content)

	req := httptest.NewRequest(http.MethodGet, "/settings/api/skills/cortex.md", nil)
	req = withChiName(req, "cortex.md")
	w := httptest.NewRecorder()
	h.Get(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, content, w.Body.String())
}

func TestGet_NotFound(t *testing.T) {
	h, _, _ := newSkillTest(t)

	req := httptest.NewRequest(http.MethodGet, "/settings/api/skills/missing.md", nil)
	req = withChiName(req, "missing.md")
	w := httptest.NewRecorder()
	h.Get(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGet_PathTraversal(t *testing.T) {
	h, _, _ := newSkillTest(t)
	bad := []string{"../etc/passwd", "foo/bar.md", "foo bar.md", "no-extension", "with:colon.md"}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			// Use a fixed innocuous URL path; the value the handler actually
			// reads is the chi param injected via withChiName. Embedding the
			// bad name in the URL would either be path-stripped by routers or
			// (for spaces) cause httptest.NewRequest to panic on the request
			// line — neither is what we're trying to exercise here.
			req := httptest.NewRequest(http.MethodGet, "/settings/api/skills/x", nil)
			req = withChiName(req, name)
			w := httptest.NewRecorder()
			h.Get(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code, "name %q must be rejected", name)
		})
	}
}
