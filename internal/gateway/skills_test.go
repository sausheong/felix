package gateway

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// uploadBody builds a multipart/form-data body with a single "file" field.
// Returns the body buffer and the Content-Type header value.
func uploadBody(t *testing.T, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = fw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return &buf, mw.FormDataContentType()
}

func TestUpload_Happy(t *testing.T) {
	h, dir, loader := newSkillTest(t)
	body, ct := uploadBody(t, "newskill.md", "---\nname: newskill\ndescription: hello\n---\nbody\n")

	req := httptest.NewRequest(http.MethodPost, "/settings/api/skills", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.Upload(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	// File on disk
	data, err := os.ReadFile(filepath.Join(dir, "newskill.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "name: newskill")
	// Loader has it
	found := false
	for _, s := range loader.Skills() {
		if s.Name == "newskill" {
			found = true
			break
		}
	}
	assert.True(t, found, "loader did not pick up uploaded skill after reload")
}

func TestUpload_BadFilename(t *testing.T) {
	// NOTE: Path-containing names like "../foo.md" or "subdir/foo.md" are
	// silently sanitized via filepath.Base per spec — they become valid
	// "foo.md" and are accepted. Path-traversal defense is exercised by
	// TestGet_PathTraversal and TestDelete_PathTraversal where the URL
	// param is the source of truth. Here we only assert that filenames
	// that fail the regex even after sanitization are rejected.
	h, _, _ := newSkillTest(t)
	bad := []string{"foo.txt", "foo bar.md", "with:colon.md", ""}
	for _, fname := range bad {
		t.Run(fname, func(t *testing.T) {
			body, ct := uploadBody(t, fname, "body")
			req := httptest.NewRequest(http.MethodPost, "/settings/api/skills", body)
			req.Header.Set("Content-Type", ct)
			w := httptest.NewRecorder()
			h.Upload(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code, "fname=%q", fname)
		})
	}
}

func TestUpload_PathInFilenameSanitized(t *testing.T) {
	// "../escaped.md" → filepath.Base → "escaped.md" → accepted.
	// Verifies the file lands inside skillsDir, not outside it.
	h, dir, _ := newSkillTest(t)
	body, ct := uploadBody(t, "../escaped.md", "body\n")
	req := httptest.NewRequest(http.MethodPost, "/settings/api/skills", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.Upload(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	_, err := os.Stat(filepath.Join(dir, "escaped.md"))
	assert.NoError(t, err, "sanitized file should land inside skillsDir")
	_, err = os.Stat(filepath.Join(filepath.Dir(dir), "escaped.md"))
	assert.True(t, os.IsNotExist(err), "must NOT escape skillsDir")
}

func TestUpload_TooLarge(t *testing.T) {
	h, _, _ := newSkillTest(t)
	big := strings.Repeat("x", 257*1024) // 257 KB > 256 KB cap
	body, ct := uploadBody(t, "big.md", big)

	req := httptest.NewRequest(http.MethodPost, "/settings/api/skills", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.Upload(w, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestUpload_BadYAML(t *testing.T) {
	h, _, _ := newSkillTest(t)
	body, ct := uploadBody(t, "bad.md", "---\nname: [unclosed\n---\nbody\n")

	req := httptest.NewRequest(http.MethodPost, "/settings/api/skills", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.Upload(w, req)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func TestUpload_AlreadyExists(t *testing.T) {
	h, dir, _ := newSkillTest(t)
	writeSkill(t, dir, "dup.md", "existing\n")

	body, ct := uploadBody(t, "dup.md", "new content")
	req := httptest.NewRequest(http.MethodPost, "/settings/api/skills", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.Upload(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	// Existing file unchanged
	data, _ := os.ReadFile(filepath.Join(dir, "dup.md"))
	assert.Equal(t, "existing\n", string(data))
}

// fakeReloader returns an error from LoadFrom; used to test the warning path.
type fakeReloader struct {
	calls int
	err   error
}

func (f *fakeReloader) LoadFrom(dirs ...string) error {
	f.calls++
	return f.err
}

// assertErr is a tiny error type so we don't pull in errors.New just for the test.
type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestUpload_ReloadFailure(t *testing.T) {
	dir := t.TempDir()
	fr := &fakeReloader{err: assertErr("reload kaboom")}
	h := NewSkillHandlers(fr, dir, []string{dir})

	body, ct := uploadBody(t, "ok.md", "body\n")
	req := httptest.NewRequest(http.MethodPost, "/settings/api/skills", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.Upload(w, req)

	// File write succeeded so we still return 200, but with a warning.
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"warning"`)
	assert.Contains(t, w.Body.String(), "reload kaboom")
	// Disk write happened
	_, err := os.Stat(filepath.Join(dir, "ok.md"))
	assert.NoError(t, err)
	assert.Equal(t, 1, fr.calls)
}

func TestDelete_Happy(t *testing.T) {
	h, dir, loader := newSkillTest(t)
	writeSkill(t, dir, "gone.md", "---\nname: gone\n---\nbody\n")
	require.NoError(t, loader.LoadFrom(dir))
	// Sanity: loader sees it before delete
	preCount := len(loader.Skills())
	require.GreaterOrEqual(t, preCount, 1)

	req := httptest.NewRequest(http.MethodDelete, "/settings/api/skills/gone.md", nil)
	req = withChiName(req, "gone.md")
	w := httptest.NewRecorder()
	h.Delete(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// File gone from disk
	_, err := os.Stat(filepath.Join(dir, "gone.md"))
	assert.True(t, os.IsNotExist(err))
	// Loader no longer reports it
	for _, s := range loader.Skills() {
		assert.NotEqual(t, "gone", s.Name)
	}
}

func TestDelete_NotFound(t *testing.T) {
	h, _, _ := newSkillTest(t)

	req := httptest.NewRequest(http.MethodDelete, "/settings/api/skills/never-here.md", nil)
	req = withChiName(req, "never-here.md")
	w := httptest.NewRecorder()
	h.Delete(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDelete_PathTraversal(t *testing.T) {
	h, _, _ := newSkillTest(t)
	bad := []string{"../etc/passwd", "foo/bar.md", "foo bar.md"}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			// Use a fixed safe URL; inject the bad name via withChiName
			// (since URLs with spaces panic httptest.NewRequest).
			req := httptest.NewRequest(http.MethodDelete, "/settings/api/skills/x", nil)
			req = withChiName(req, name)
			w := httptest.NewRecorder()
			h.Delete(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code, "name %q must be rejected", name)
		})
	}
}
