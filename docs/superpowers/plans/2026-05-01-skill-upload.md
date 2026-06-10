# Skill Upload via Settings Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users upload, view, and delete skill markdown files in `~/.felix/skills/` through the existing Settings page, with hot-reload so new skills become active without restarting Felix.

**Architecture:** Four new REST endpoints under `/settings/api/skills*` mounted on the existing chi router in `internal/gateway/server.go`. Handlers live in a new `internal/gateway/skills.go` keyed off the existing `*skill.Loader` from startup — calling `loader.LoadFrom()` after each write/delete refreshes the in-memory skill set in place, propagating to every consumer (wsHandler, heartbeat, cron, subagents) since they all hold the loader by pointer. A new "Skills" tab in the embedded Settings HTML drives the endpoints via `fetch`.

**Tech Stack:** Go 1.25, `github.com/go-chi/chi/v5` router, `gopkg.in/yaml.v3`, `github.com/stretchr/testify`, vanilla JS for the UI tab. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-01-skill-upload-design.md`

---

## File Structure

**New files:**
- `internal/gateway/skills.go` — `SkillHandlers` struct + 4 `http.HandlerFunc`s + `skillReloader` interface + helpers (`validateSkillName`, `writeJSONError`).
- `internal/gateway/skills_test.go` — unit tests driving the handlers via `httptest`.

**Modified files:**
- `internal/skill/skill.go` — export `SplitFrontmatter` and `ParseSkillFile` (currently lowercase), add `MissingBins(s Skill) []string` helper to dedupe the binary-presence check that lives inline in `LoadFrom`.
- `internal/skill/skill_test.go` — update one call site for the rename.
- `internal/gateway/server.go` — add `Skills *SkillHandlers` field to `ServerOptions`, mount four routes.
- `internal/gateway/server_test.go` — add a route-mount smoke test.
- `internal/startup/startup.go` — build `SkillHandlers` after `skillLoader.LoadFrom(...)` and pass it into `gateway.ServerOptions`.
- `internal/gateway/settings.go` — add "Skills" tab button, panel div, `renderSkills()` JS, register in `render()`.

---

## Task 1: Export skill parser helpers and extract MissingBins

**Why first:** The list endpoint needs to parse skill files and check binary availability. Both pieces already exist in `internal/skill/skill.go` but are unexported / inlined in `LoadFrom`. Exporting them keeps the parser in one package and avoids duplicating the YAML/frontmatter logic in `gateway`.

**Files:**
- Modify: `internal/skill/skill.go` — rename `splitFrontmatter` → `SplitFrontmatter`, `parseSkillFile` → `ParseSkillFile`, add `MissingBins(s Skill) []string`, refactor `LoadFrom` to use `MissingBins`.
- Modify: `internal/skill/skill_test.go` — update the `splitFrontmatter` call in `TestSplitFrontmatter`.

- [ ] **Step 1: Read current state**

```bash
grep -n "splitFrontmatter\|parseSkillFile" internal/skill/*.go
```

Expected: hits in `skill.go` (definition + 1-2 call sites) and `skill_test.go` (1 call site in `TestSplitFrontmatter`).

- [ ] **Step 2: Write a failing test for MissingBins**

Add to `internal/skill/skill_test.go`:

```go
func TestMissingBins(t *testing.T) {
	t.Run("no requires returns nil", func(t *testing.T) {
		s := Skill{Name: "foo"}
		assert.Nil(t, MissingBins(s))
	})
	t.Run("present binary returns nil", func(t *testing.T) {
		var s Skill
		s.Metadata.OpenClaw.Requires.Bins = []string{"sh"} // sh is on every POSIX system
		assert.Nil(t, MissingBins(s))
	})
	t.Run("missing binary returned", func(t *testing.T) {
		var s Skill
		s.Metadata.OpenClaw.Requires.Bins = []string{"definitely-not-installed-xyz-123"}
		got := MissingBins(s)
		require.Len(t, got, 1)
		assert.Equal(t, "definitely-not-installed-xyz-123", got[0])
	})
	t.Run("partial — one present one missing", func(t *testing.T) {
		var s Skill
		s.Metadata.OpenClaw.Requires.Bins = []string{"sh", "definitely-not-installed-xyz-123"}
		got := MissingBins(s)
		require.Len(t, got, 1)
		assert.Equal(t, "definitely-not-installed-xyz-123", got[0])
	})
}
```

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test ./internal/skill/ -run TestMissingBins -v
```

Expected: `undefined: MissingBins`.

- [ ] **Step 4: Implement the renames + MissingBins**

In `internal/skill/skill.go`:

a) Rename `splitFrontmatter` to `SplitFrontmatter` (function definition only at line ~231):

```go
// SplitFrontmatter extracts YAML frontmatter (between --- delimiters) from Markdown.
func SplitFrontmatter(content string) (frontmatter, body string) {
```

b) Rename `parseSkillFile` to `ParseSkillFile` (function definition only at line ~200):

```go
// ParseSkillFile reads a SKILL.md file and parses its frontmatter and body.
func ParseSkillFile(path string) (Skill, error) {
```

c) Update internal call sites in `LoadFrom` (line ~74) and inside `ParseSkillFile` itself (line ~206):

- `parseSkillFile(path)` → `ParseSkillFile(path)`
- `splitFrontmatter(string(data))` → `SplitFrontmatter(string(data))`

d) Add `MissingBins` and refactor the inline binary check in `LoadFrom`. Append at end of file:

```go
// MissingBins returns the names of binaries declared in the skill's
// metadata.openclaw.requires.bins that are not currently on $PATH.
// Returns nil when the skill declares no required bins or all are present.
func MissingBins(s Skill) []string {
	bins := s.Metadata.OpenClaw.Requires.Bins
	if len(bins) == 0 {
		return nil
	}
	var missing []string
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	return missing
}
```

Replace the inline block in `LoadFrom` (currently lines ~81-93):

```go
			// Skip skills whose required binaries are not installed
			if missing := MissingBins(skill); len(missing) > 0 {
				slog.Info("skill skipped (missing binary)", "name", skill.Name, "binary", missing[0])
				return nil
			}
```

- [ ] **Step 5: Update the existing splitFrontmatter test call site**

In `internal/skill/skill_test.go` `TestSplitFrontmatter`, change:

```go
fm, body := splitFrontmatter(tt.input)
```

to:

```go
fm, body := SplitFrontmatter(tt.input)
```

- [ ] **Step 6: Run the full skill package test suite**

```bash
go test ./internal/skill/ -v
```

Expected: all tests pass, including the new `TestMissingBins` and the existing `TestSplitFrontmatter`.

- [ ] **Step 7: Build the whole project to catch any other call sites**

```bash
go build ./...
```

Expected: clean build. (`grep` should have caught all callers in step 1, but verify.)

- [ ] **Step 8: Commit**

```bash
git add internal/skill/skill.go internal/skill/skill_test.go
git commit -m "refactor(skill): export SplitFrontmatter, ParseSkillFile; add MissingBins helper"
```

---

## Task 2: Create skills.go scaffold with helpers and stubs

**Why before the endpoints:** Establishes the file, struct, constructor, and shared validation/JSON helpers so each endpoint task can focus on one handler's logic. Stub handlers return 501 so the file compiles.

**Files:**
- Create: `internal/gateway/skills.go`
- Create: `internal/gateway/skills_test.go` — first test for `validateSkillName`.

- [ ] **Step 1: Write the failing test for validateSkillName**

Create `internal/gateway/skills_test.go`:

```go
package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/gateway/ -run TestValidateSkillName -v
```

Expected: `undefined: validateSkillName`.

- [ ] **Step 3: Create skills.go with helpers and stub handlers**

Create `internal/gateway/skills.go`:

```go
package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/sausheong/felix/internal/skill"
)

// skillReloader is the subset of *skill.Loader the handler needs. Defined
// as an interface so tests can inject a fake whose LoadFrom returns an error.
type skillReloader interface {
	LoadFrom(dirs ...string) error
}

// SkillHandlers exposes HTTP handlers for managing user-uploaded skill files
// in ~/.felix/skills/. All routes are mounted under /settings/api/skills* by
// the gateway server and inherit the global bearer-auth middleware.
type SkillHandlers struct {
	List   http.HandlerFunc
	Get    http.HandlerFunc
	Upload http.HandlerFunc
	Delete http.HandlerFunc
}

// NewSkillHandlers builds the handler set.
//
//	loader     — the *skill.Loader from startup; mutated in place via LoadFrom.
//	skillsDir  — ~/.felix/skills/. The only directory writes/deletes touch.
//	reloadDirs — full list initially passed to LoadFrom (skillsDir + agent
//	             workspace skill dirs). Reused on every reload so workspace
//	             skills aren't dropped.
func NewSkillHandlers(loader skillReloader, skillsDir string, reloadDirs []string) *SkillHandlers {
	h := &SkillHandlers{}
	h.List = func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotImplemented, "list not implemented")
	}
	h.Get = func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotImplemented, "get not implemented")
	}
	h.Upload = func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotImplemented, "upload not implemented")
	}
	h.Delete = func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotImplemented, "delete not implemented")
	}
	// Silence unused-arg warnings until subsequent tasks fill in the handlers.
	_ = loader
	_ = skillsDir
	_ = reloadDirs
	_ = skill.Skill{}
	return h
}

// skillNameRE matches a safe skill filename: one or more of [A-Za-z0-9._-]
// followed by a literal ".md". Defends against path traversal and weird chars.
var skillNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+\.md$`)

// validateSkillName returns an error if name is not a safe basename of the
// form `<allowed-chars>.md`. Callers must use the validated name only as a
// basename joined to a fixed directory — never as a path on its own.
func validateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if !skillNameRE.MatchString(name) {
		return fmt.Errorf("name %q is not a valid skill filename", name)
	}
	return nil
}

// writeJSONError writes a JSON error response with the given status.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeJSON writes a JSON response with status 200.
func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// At this point the header is already sent; logging is the best we can do.
		// gateway uses slog elsewhere; stay consistent.
		// (No import added here — slog import is in package; see other gateway files.)
	}
}
```

Note: `writeJSON` references `slog` in the comment but doesn't actually import it; we keep this file dependency-light. If a JSON encode failure becomes a real concern later, swap in the existing `slog` pattern from other gateway files.

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./internal/gateway/ -run TestValidateSkillName -v
```

Expected: PASS for all 13 subtests.

- [ ] **Step 5: Build to verify the rest of the package still compiles**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/skills.go internal/gateway/skills_test.go
git commit -m "feat(gateway): scaffold SkillHandlers with validateSkillName + JSON helpers"
```

---

## Task 3: Implement List endpoint

**Files:**
- Modify: `internal/gateway/skills.go` — replace `List` stub.
- Modify: `internal/gateway/skills_test.go` — add 4 new tests.

- [ ] **Step 1: Add a test helper for building handlers and seeding the dir**

Append to `internal/gateway/skills_test.go`:

```go
import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sausheong/felix/internal/skill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSkillTest builds a fresh SkillHandlers wired to a temp skills dir.
// Returns the handlers, the skills dir path, and the loader (so tests can
// inspect what's loaded after a reload).
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
```

(If the file already has an `import` block from Task 2, merge into it; do not duplicate. The test file's existing imports are just `testing` + `assert` from Task 2.)

- [ ] **Step 2: Write failing tests for List**

Append to `internal/gateway/skills_test.go`:

```go
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
	assert.NotEqual(t, `"parse_error":""`, body)
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
```

- [ ] **Step 3: Run the new tests to verify they fail**

```bash
go test ./internal/gateway/ -run "TestList_" -v
```

Expected: all four FAIL with `501 Not Implemented` returned.

- [ ] **Step 4: Implement the List handler**

In `internal/gateway/skills.go`, replace the `h.List = ...` stub with this. Sort by filename for deterministic output. Add the imports `os`, `path/filepath`, `sort`, `strings`, `time` to the file.

```go
type skillListEntry struct {
	Name        string   `json:"name"`
	Filename    string   `json:"filename"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	SizeBytes   int64    `json:"size_bytes"`
	Modified    string   `json:"modified"`
	Unavailable bool     `json:"unavailable"`
	MissingBins []string `json:"missing_bins"`
	ParseError  string   `json:"parse_error"`
}

h.List = func(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(skillsDir)
	if err != nil && !os.IsNotExist(err) {
		writeJSONError(w, http.StatusInternalServerError, "read skills dir: "+err.Error())
		return
	}

	out := struct {
		Skills []skillListEntry `json:"skills"`
	}{Skills: []skillListEntry{}}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fname := e.Name()
		if !strings.HasSuffix(strings.ToLower(fname), ".md") {
			continue
		}
		full := filepath.Join(skillsDir, fname)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		entry := skillListEntry{
			Filename:    fname,
			Tags:        []string{},
			MissingBins: []string{},
			SizeBytes:   info.Size(),
			Modified:    info.ModTime().UTC().Format(time.RFC3339),
		}
		s, perr := skill.ParseSkillFile(full)
		if perr != nil {
			entry.Name = strings.TrimSuffix(fname, filepath.Ext(fname))
			entry.ParseError = perr.Error()
		} else {
			entry.Name = s.Name
			entry.Description = s.Description
			if s.Tags != nil {
				entry.Tags = s.Tags
			}
			if missing := skill.MissingBins(s); len(missing) > 0 {
				entry.Unavailable = true
				entry.MissingBins = missing
			}
		}
		out.Skills = append(out.Skills, entry)
	}

	sort.Slice(out.Skills, func(i, j int) bool {
		return out.Skills[i].Filename < out.Skills[j].Filename
	})

	writeJSON(w, out)
}
```

- [ ] **Step 5: Run List tests to verify they pass**

```bash
go test ./internal/gateway/ -run "TestList_" -v
```

Expected: all four PASS.

- [ ] **Step 6: Run full gateway test suite to catch regressions**

```bash
go test ./internal/gateway/ -v
```

Expected: no failures.

- [ ] **Step 7: Commit**

```bash
git add internal/gateway/skills.go internal/gateway/skills_test.go
git commit -m "feat(gateway): implement skills list endpoint"
```

---

## Task 4: Implement Get endpoint

**Files:**
- Modify: `internal/gateway/skills.go` — replace `Get` stub, add chi import.
- Modify: `internal/gateway/skills_test.go` — add 3 tests + helper for chi route context.

- [ ] **Step 1: Add chi route-context helper to the test file**

The handler reads the skill name via `chi.URLParam(r, "name")`. In tests we drive handlers directly without the router, so we have to inject a chi RouteContext. Append to `internal/gateway/skills_test.go`:

```go
import (
	"context"
	// ... existing imports ...
	"github.com/go-chi/chi/v5"
)

// withChiName attaches a chi RouteContext carrying URL param "name" to the
// request. Handlers under /settings/api/skills/{name} read this via chi.URLParam.
func withChiName(req *http.Request, name string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", name)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
```

- [ ] **Step 2: Write failing tests for Get**

Append to `internal/gateway/skills_test.go`:

```go
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
			req := httptest.NewRequest(http.MethodGet, "/settings/api/skills/"+name, nil)
			req = withChiName(req, name)
			w := httptest.NewRecorder()
			h.Get(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code, "name %q must be rejected", name)
		})
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/gateway/ -run "TestGet_" -v
```

Expected: FAIL with `501 Not Implemented`.

- [ ] **Step 4: Implement the Get handler**

Add `github.com/go-chi/chi/v5` to the imports in `internal/gateway/skills.go`. Replace the `h.Get = ...` stub:

```go
h.Get = func(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(chi.URLParam(r, "name"))
	if err := validateSkillName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	full := filepath.Join(skillsDir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSONError(w, http.StatusNotFound, "skill not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "read: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
```

- [ ] **Step 5: Run Get tests to verify they pass**

```bash
go test ./internal/gateway/ -run "TestGet_" -v
```

Expected: PASS.

- [ ] **Step 6: Run full gateway suite**

```bash
go test ./internal/gateway/ -v
```

Expected: no failures.

- [ ] **Step 7: Commit**

```bash
git add internal/gateway/skills.go internal/gateway/skills_test.go
git commit -m "feat(gateway): implement skills get endpoint with path-traversal defense"
```

---

## Task 5: Implement Upload endpoint

**Files:**
- Modify: `internal/gateway/skills.go` — replace `Upload` stub.
- Modify: `internal/gateway/skills_test.go` — add 6 tests + helper for multipart bodies.

- [ ] **Step 1: Add a multipart body helper to the test file**

Append to `internal/gateway/skills_test.go`:

```go
import (
	// ... existing imports ...
	"bytes"
	"mime/multipart"
)

// uploadBody builds a multipart/form-data body with a single "file" field.
// Returns the body bytes and the Content-Type header value.
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
```

- [ ] **Step 2: Write failing tests for Upload**

Append to `internal/gateway/skills_test.go`:

```go
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
	h, _, _ := newSkillTest(t)
	// NOTE: Path-containing names like "../foo.md" or "subdir/foo.md" are
	// silently sanitized via filepath.Base per spec — they become valid
	// "foo.md" and are accepted. Path-traversal defense is exercised by
	// TestGet_PathTraversal and TestDelete_PathTraversal where the URL
	// param is the source of truth. Here we only assert that filenames
	// that fail the regex even after sanitization are rejected.
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

// assertErr is a tiny error type so we don't pull in errors.New just for the test.
type assertErr string

func (e assertErr) Error() string { return string(e) }
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/gateway/ -run "TestUpload_" -v
```

Expected: all 6 FAIL with `501 Not Implemented`.

- [ ] **Step 4: Implement the Upload handler**

Replace the `h.Upload = ...` stub in `internal/gateway/skills.go`. Add `gopkg.in/yaml.v3` and `io` to imports. The implementation:

```go
const maxSkillUploadBytes = 256 * 1024

h.Upload = func(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSkillUploadBytes)
	if err := r.ParseMultipartForm(maxSkillUploadBytes); err != nil {
		// MaxBytesReader's "request body too large" surfaces here
		if strings.Contains(err.Error(), "request body too large") || strings.Contains(err.Error(), "http: request body too large") {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "upload exceeds 256KB limit")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "parse multipart: "+err.Error())
		return
	}

	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, `missing "file" field`)
		return
	}
	defer file.Close()

	name := filepath.Base(hdr.Filename)
	if err := validateSkillName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		// MaxBytesReader can also trip here on bodies that slipped past ParseMultipartForm.
		if strings.Contains(err.Error(), "request body too large") {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "upload exceeds 256KB limit")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "read upload: "+err.Error())
		return
	}

	// Validate frontmatter if present.
	fm, _ := skill.SplitFrontmatter(string(data))
	if fm != "" {
		var probe skill.Skill
		if yerr := yaml.Unmarshal([]byte(fm), &probe); yerr != nil {
			writeJSONError(w, http.StatusUnprocessableEntity, "invalid YAML frontmatter: "+yerr.Error())
			return
		}
	}

	target := filepath.Join(skillsDir, name)
	if _, err := os.Stat(target); err == nil {
		writeJSONError(w, http.StatusConflict, fmt.Sprintf("skill %q already exists; delete first to replace", name))
		return
	} else if !os.IsNotExist(err) {
		writeJSONError(w, http.StatusInternalServerError, "stat target: "+err.Error())
		return
	}

	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "write tmp: "+err.Error())
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		writeJSONError(w, http.StatusInternalServerError, "rename: "+err.Error())
		return
	}

	resp := map[string]any{
		"ok":       true,
		"name":     strings.TrimSuffix(name, filepath.Ext(name)),
		"filename": name,
	}
	if rerr := loader.LoadFrom(reloadDirs...); rerr != nil {
		resp["warning"] = "reload failed: " + rerr.Error()
	}
	writeJSON(w, resp)
}
```

- [ ] **Step 5: Run Upload tests to verify they pass**

```bash
go test ./internal/gateway/ -run "TestUpload_" -v
```

Expected: all 6 PASS. If `TestUpload_TooLarge` fails because `ParseMultipartForm` returns a wrapped error, widen the substring match in the implementation accordingly — but the `strings.Contains` check on `"request body too large"` should match Go's stdlib message.

- [ ] **Step 6: Run full gateway suite**

```bash
go test ./internal/gateway/ -v
```

Expected: no failures.

- [ ] **Step 7: Commit**

```bash
git add internal/gateway/skills.go internal/gateway/skills_test.go
git commit -m "feat(gateway): implement skills upload endpoint with validation + hot reload"
```

---

## Task 6: Implement Delete endpoint

**Files:**
- Modify: `internal/gateway/skills.go` — replace `Delete` stub.
- Modify: `internal/gateway/skills_test.go` — add 3 tests.

- [ ] **Step 1: Write failing tests for Delete**

Append to `internal/gateway/skills_test.go`:

```go
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
			req := httptest.NewRequest(http.MethodDelete, "/settings/api/skills/"+name, nil)
			req = withChiName(req, name)
			w := httptest.NewRecorder()
			h.Delete(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code, "name %q must be rejected", name)
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/gateway/ -run "TestDelete_" -v
```

Expected: FAIL with `501 Not Implemented`.

- [ ] **Step 3: Implement the Delete handler**

Replace the `h.Delete = ...` stub in `internal/gateway/skills.go`:

```go
h.Delete = func(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(chi.URLParam(r, "name"))
	if err := validateSkillName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	target := filepath.Join(skillsDir, name)
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			writeJSONError(w, http.StatusNotFound, "skill not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "stat: "+err.Error())
		return
	}
	if err := os.Remove(target); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "remove: "+err.Error())
		return
	}
	resp := map[string]any{"ok": true}
	if rerr := loader.LoadFrom(reloadDirs...); rerr != nil {
		resp["warning"] = "reload failed: " + rerr.Error()
	}
	writeJSON(w, resp)
}
```

- [ ] **Step 4: Run Delete tests to verify they pass**

```bash
go test ./internal/gateway/ -run "TestDelete_" -v
```

Expected: PASS.

- [ ] **Step 5: Run the full skills_test.go file**

```bash
go test ./internal/gateway/ -run "TestList_|TestGet_|TestUpload_|TestDelete_|TestValidateSkillName" -v
```

Expected: every test passes. This is the full handler-level coverage.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/skills.go internal/gateway/skills_test.go
git commit -m "feat(gateway): implement skills delete endpoint"
```

---

## Task 7: Mount routes in the gateway server

**Files:**
- Modify: `internal/gateway/server.go` — add `Skills *SkillHandlers` to `ServerOptions`, mount routes.
- Modify: `internal/gateway/server_test.go` — add a route-mount smoke test.

- [ ] **Step 1: Write a failing test for route mounting**

Append to `internal/gateway/server_test.go`:

```go
func TestSkillRoutesMounted(t *testing.T) {
	dir := t.TempDir()
	loader := skill.NewLoader()
	require.NoError(t, loader.LoadFrom(dir))

	cfg := config.DefaultConfig()
	providers := map[string]llm.LLMProvider{}
	reg := tools.NewRegistry()
	store := session.NewStore(t.TempDir())
	wsHandler := NewWebSocketHandler(providers, reg, store, cfg)

	srv := NewServer("127.0.0.1", 0, wsHandler, ServerOptions{
		Skills: NewSkillHandlers(loader, dir, []string{dir}),
	})

	// GET list — should be 200, not 404
	req := httptest.NewRequest(http.MethodGet, "/settings/api/skills", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "list route not mounted")

	// GET specific — empty dir, should be 404 (route is mounted, file missing)
	req = httptest.NewRequest(http.MethodGet, "/settings/api/skills/anything.md", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code, "get route not mounted")

	// DELETE specific — empty dir, should be 404
	req = httptest.NewRequest(http.MethodDelete, "/settings/api/skills/anything.md", nil)
	w = httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code, "delete route not mounted")
}
```

Add `"github.com/sausheong/felix/internal/skill"` to the imports of `server_test.go` if not present.

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/gateway/ -run TestSkillRoutesMounted -v
```

Expected: compile error if `Skills` field is missing on `ServerOptions`, OR test failure with all routes returning 404.

- [ ] **Step 3: Add Skills field and mount routes**

In `internal/gateway/server.go`, add the field to `ServerOptions` (around line 23):

```go
type ServerOptions struct {
	AuthToken      string   // bearer token for API auth (empty = no auth)
	AllowedOrigins []string // WebSocket allowed origins (empty = localhost only)
	MetricsHandler http.HandlerFunc  // optional /metrics handler
	UIHandler      http.Handler      // optional /ui handler
	ChatHandler    http.HandlerFunc  // optional /chat handler
	JobsHandler    http.HandlerFunc  // optional /jobs handler
	Settings       *SettingsHandlers // optional /settings handlers
	Skills         *SkillHandlers    // optional /settings/api/skills* handlers
	LogBuffer      *LogBuffer        // optional log buffer for /logs
}
```

In `routes()` (after the existing `Settings` block, around line 102), add:

```go
	if s.opts.Skills != nil {
		s.router.Get("/settings/api/skills", s.opts.Skills.List)
		s.router.Get("/settings/api/skills/{name}", s.opts.Skills.Get)
		s.router.Post("/settings/api/skills", s.opts.Skills.Upload)
		s.router.Delete("/settings/api/skills/{name}", s.opts.Skills.Delete)
	}
```

- [ ] **Step 4: Run the route-mount test to verify it passes**

```bash
go test ./internal/gateway/ -run TestSkillRoutesMounted -v
```

Expected: PASS.

- [ ] **Step 5: Run the full gateway suite**

```bash
go test ./internal/gateway/ -v
```

Expected: every test passes.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/server.go internal/gateway/server_test.go
git commit -m "feat(gateway): mount skill upload/list/get/delete routes"
```

---

## Task 8: Wire SkillHandlers into startup

**Files:**
- Modify: `internal/startup/startup.go` — build `SkillHandlers` after the loader, pass into `ServerOptions`.

This task is wiring only. There is no isolated unit test (the existing build + a manual smoke test in Task 10 cover it).

- [ ] **Step 1: Locate the existing skill-loader block**

```bash
grep -n "skillLoader\s*:=\|SetSkills\|skillDirs" internal/startup/startup.go
```

Expected: hits at lines ~440 (loader init), ~520 (SetSkills), and ~441 (skillDirs construction).

- [ ] **Step 2: Locate the ServerOptions construction**

```bash
grep -n "gateway.ServerOptions{" internal/startup/startup.go
```

Expected: one hit around line 761.

- [ ] **Step 3: Add Skills handler construction and pass it in**

In `internal/startup/startup.go`, find the existing block (lines ~439-447):

```go
	// Init skill loader
	skillLoader := skill.NewLoader()
	skillDirs := []string{filepath.Join(dataDir, "skills")}
	for _, a := range cfg.Agents.List {
		skillDirs = append(skillDirs, filepath.Join(a.Workspace, "skills"))
	}
	if err := skillLoader.LoadFrom(skillDirs...); err != nil {
		slog.Warn("failed to load skills", "error", err)
	}
```

Append a new local variable right after that block:

```go
	skillHandlers := gateway.NewSkillHandlers(skillLoader, filepath.Join(dataDir, "skills"), skillDirs)
```

Then in the `gateway.ServerOptions{...}` literal (around line 761-772), add the new field. The current literal:

```go
	srv := gateway.NewServer(cfg.Gateway.Host, port, wsHandler, gateway.ServerOptions{
		AuthToken:      cfg.Gateway.Auth.Token,
		MetricsHandler: metrics.Handler(),
		UIHandler:      gateway.NewUIHandler(cfg, version),
		ChatHandler:    gateway.NewChatHandler(port),
		JobsHandler:    gateway.NewJobsHandler(port),
		Settings: gateway.NewSettingsHandlers(cfg, toolReg, settingsBootstrap(bootstrapTracker), func(newCfg *config.Config) {
			wsHandler.UpdateConfig(newCfg)
			slog.Info("config updated via settings page")
		}),
		LogBuffer: logBuf,
	})
```

becomes:

```go
	srv := gateway.NewServer(cfg.Gateway.Host, port, wsHandler, gateway.ServerOptions{
		AuthToken:      cfg.Gateway.Auth.Token,
		MetricsHandler: metrics.Handler(),
		UIHandler:      gateway.NewUIHandler(cfg, version),
		ChatHandler:    gateway.NewChatHandler(port),
		JobsHandler:    gateway.NewJobsHandler(port),
		Settings: gateway.NewSettingsHandlers(cfg, toolReg, settingsBootstrap(bootstrapTracker), func(newCfg *config.Config) {
			wsHandler.UpdateConfig(newCfg)
			slog.Info("config updated via settings page")
		}),
		Skills:    skillHandlers,
		LogBuffer: logBuf,
	})
```

- [ ] **Step 4: Build the whole project**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 5: Run the full test suite**

```bash
go test ./... -count=1
```

Expected: all tests pass. (No new tests added in this task — this is wiring.)

- [ ] **Step 6: Commit**

```bash
git add internal/startup/startup.go
git commit -m "feat(startup): wire SkillHandlers into gateway server"
```

---

## Task 9: Add the Skills tab to the Settings UI

**Files:**
- Modify: `internal/gateway/settings.go` — extend the embedded `settingsHTML` constant.

This task is HTML/JS edits inside a Go raw-string literal. There is no Go-side test; verification is manual in Task 10.

- [ ] **Step 1: Add the tab button**

Find the `finger-tabs` block (currently around line 488). Add a new button at the end:

```html
<button class="finger-tab" data-tab="skills">Skills</button>
```

So the full block becomes:

```html
<div class="finger-tabs" id="tabs">
    <button class="finger-tab active" data-tab="agents">Agents</button>
    <button class="finger-tab" data-tab="providers">Providers</button>
    <button class="finger-tab" data-tab="models">Models</button>
    <button class="finger-tab" data-tab="intelligence">Intelligence</button>
    <button class="finger-tab" data-tab="security">Security</button>
    <button class="finger-tab" data-tab="messaging">Messaging</button>
    <button class="finger-tab" data-tab="mcp">MCP</button>
    <button class="finger-tab" data-tab="gateway">Gateway</button>
    <button class="finger-tab" data-tab="skills">Skills</button>
</div>
```

- [ ] **Step 2: Add the panel div**

Right after `<div class="finger-panel" id="panel-gateway"></div>` (around line 505), add:

```html
<div class="finger-panel" id="panel-skills"></div>
```

- [ ] **Step 3: Register renderSkills() in the render loop**

Find the `function render() { ... }` block (around line 610). Append `renderSkills();` to the end:

```javascript
function render() {
    renderAgents();
    renderProviders();
    renderModels();
    renderIntelligence();
    renderSecurity();
    renderMessaging();
    renderMCP();
    renderGateway();
    renderSkills();
}
```

- [ ] **Step 4: Add the renderSkills() implementation**

Add this function block somewhere inside the `<script>` IIFE, after `renderGateway()`. The block reads the skills tab, renders a list, an upload button, and a side panel. Use the existing `showStatus()` helper for toasts.

```javascript
// === Skills tab ===
var skillsViewing = null; // currently-open filename in side panel, or null

function renderSkills() {
    var panel = document.getElementById('panel-skills');
    panel.innerHTML =
        '<div style="margin-bottom:1rem; display:flex; gap:0.75rem; align-items:center;">' +
            '<label class="btn btn-primary" style="cursor:pointer;">' +
                'Upload .md' +
                '<input type="file" id="skill-upload-input" accept=".md" style="display:none">' +
            '</label>' +
            '<span style="color: var(--color-text-muted); font-size: 0.85rem;">' +
                'Files go to ~/.felix/skills/ and load on next chat turn.' +
            '</span>' +
        '</div>' +
        '<div id="skills-list">Loading…</div>' +
        '<div id="skill-view-panel" style="margin-top:1.5rem; display:none;">' +
            '<h3 id="skill-view-name" style="margin-bottom:0.5rem;"></h3>' +
            '<pre id="skill-view-body" style="background: var(--color-bg); padding: 1rem; border-radius: var(--radius); border: 1px solid var(--color-border); overflow:auto; max-height:60vh; white-space: pre-wrap; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.85rem;"></pre>' +
        '</div>';

    document.getElementById('skill-upload-input').addEventListener('change', onSkillUpload);
    refreshSkillList();
}

function refreshSkillList() {
    fetch('/settings/api/skills')
        .then(function(r) { return r.json(); })
        .then(function(data) {
            var listDiv = document.getElementById('skills-list');
            if (!data.skills || data.skills.length === 0) {
                listDiv.innerHTML = '<p style="color: var(--color-text-muted);">No skills uploaded yet.</p>';
                return;
            }
            var html = '<table style="width:100%; border-collapse: collapse;">';
            html += '<thead><tr>' +
                '<th style="text-align:left; padding:0.5rem; border-bottom:1px solid var(--color-border);">Name</th>' +
                '<th style="text-align:left; padding:0.5rem; border-bottom:1px solid var(--color-border);">Description</th>' +
                '<th style="text-align:right; padding:0.5rem; border-bottom:1px solid var(--color-border);">Size</th>' +
                '<th style="text-align:right; padding:0.5rem; border-bottom:1px solid var(--color-border);">Actions</th>' +
                '</tr></thead><tbody>';
            data.skills.forEach(function(s) {
                var rowStyle = '';
                var note = '';
                if (s.parse_error) {
                    rowStyle = 'color: var(--color-error);';
                    note = ' <span title="' + escapeAttr(s.parse_error) + '">⚠ parse error</span>';
                } else if (s.unavailable) {
                    rowStyle = 'color: var(--color-text-muted);';
                    note = ' <span title="missing: ' + escapeAttr((s.missing_bins || []).join(', ')) + '">⚠ unavailable</span>';
                }
                html += '<tr style="' + rowStyle + '">' +
                    '<td style="padding:0.5rem; border-bottom:1px solid var(--color-border);"><code>' + escapeHtml(s.filename) + '</code>' + note + '</td>' +
                    '<td style="padding:0.5rem; border-bottom:1px solid var(--color-border);">' + escapeHtml(s.description || '') + '</td>' +
                    '<td style="padding:0.5rem; border-bottom:1px solid var(--color-border); text-align:right; font-variant-numeric:tabular-nums;">' + fmtBytes(s.size_bytes) + '</td>' +
                    '<td style="padding:0.5rem; border-bottom:1px solid var(--color-border); text-align:right;">' +
                        '<button class="btn-link" data-skill-view="' + escapeAttr(s.filename) + '">View</button> ' +
                        '<button class="btn-link" data-skill-delete="' + escapeAttr(s.filename) + '" style="color:var(--color-error);">Delete</button>' +
                    '</td>' +
                '</tr>';
            });
            html += '</tbody></table>';
            listDiv.innerHTML = html;
            listDiv.querySelectorAll('[data-skill-view]').forEach(function(b) {
                b.addEventListener('click', function() { viewSkill(b.dataset.skillView); });
            });
            listDiv.querySelectorAll('[data-skill-delete]').forEach(function(b) {
                b.addEventListener('click', function() { deleteSkill(b.dataset.skillDelete); });
            });
        })
        .catch(function(err) { showStatus('Skills load failed: ' + err.message, true); });
}

function onSkillUpload(ev) {
    var f = ev.target.files[0];
    if (!f) return;
    if (!/\.md$/i.test(f.name)) {
        showStatus('Skill files must end in .md', true);
        ev.target.value = '';
        return;
    }
    var fd = new FormData();
    fd.append('file', f);
    fetch('/settings/api/skills', { method: 'POST', body: fd })
        .then(function(r) { return r.json().then(function(j) { return { ok: r.ok, status: r.status, body: j }; }); })
        .then(function(res) {
            ev.target.value = '';
            if (!res.ok) {
                showStatus('Upload failed: ' + (res.body.error || res.status), true);
                return;
            }
            var msg = 'Uploaded ' + (res.body.filename || f.name);
            if (res.body.warning) msg += ' (' + res.body.warning + ')';
            showStatus(msg, false);
            refreshSkillList();
        })
        .catch(function(err) {
            ev.target.value = '';
            showStatus('Upload failed: ' + err.message, true);
        });
}

function viewSkill(filename) {
    fetch('/settings/api/skills/' + encodeURIComponent(filename))
        .then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.text();
        })
        .then(function(text) {
            skillsViewing = filename;
            document.getElementById('skill-view-name').textContent = filename;
            document.getElementById('skill-view-body').textContent = text;
            document.getElementById('skill-view-panel').style.display = '';
        })
        .catch(function(err) { showStatus('View failed: ' + err.message, true); });
}

function deleteSkill(filename) {
    if (!confirm('Delete ' + filename + '? This cannot be undone.')) return;
    fetch('/settings/api/skills/' + encodeURIComponent(filename), { method: 'DELETE' })
        .then(function(r) { return r.json().then(function(j) { return { ok: r.ok, status: r.status, body: j }; }); })
        .then(function(res) {
            if (!res.ok) {
                showStatus('Delete failed: ' + (res.body.error || res.status), true);
                return;
            }
            var msg = 'Deleted ' + filename;
            if (res.body.warning) msg += ' (' + res.body.warning + ')';
            showStatus(msg, false);
            if (skillsViewing === filename) {
                skillsViewing = null;
                document.getElementById('skill-view-panel').style.display = 'none';
            }
            refreshSkillList();
        })
        .catch(function(err) { showStatus('Delete failed: ' + err.message, true); });
}

function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function(c) {
        return ({ '&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;' })[c];
    });
}
function escapeAttr(s) { return escapeHtml(s); }
```

Note: `fmtBytes` already exists in this file (defined for the Models tab around line 637) and can be reused as-is. `showStatus` is also already defined. If your editor shows duplicate-symbol concerns, check for those existing definitions first.

- [ ] **Step 4b: Verify `fmtBytes` and `showStatus` already exist**

```bash
grep -n "function fmtBytes\|function showStatus" internal/gateway/settings.go
```

Expected: one hit each. If `fmtBytes` is missing, copy this minimal version into the renderSkills block:

```javascript
function fmtBytes(n) {
    if (!n || n < 0) return '';
    if (n < 1024) return n + ' B';
    var u = ['KB','MB','GB','TB'], i = -1;
    do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
    return n.toFixed(1) + ' ' + u[i];
}
```

- [ ] **Step 5: Verify Go still compiles (the HTML is in a Go raw string)**

```bash
go build ./...
```

Expected: clean build. If it fails, it's almost certainly an unescaped backtick in the JS — replace any `` ` `` with string concatenation since the host string is `` ` ... ` ``.

- [ ] **Step 6: Run the full gateway test suite**

```bash
go test ./internal/gateway/ -v
```

Expected: no failures.

- [ ] **Step 7: Commit**

```bash
git add internal/gateway/settings.go
git commit -m "feat(gateway): add Skills tab to Settings page"
```

---

## Task 10: Manual smoke test and final verification

**Files:** None modified — this task is verification only.

- [ ] **Step 1: Build the binary**

```bash
go build -o felix ./cmd/felix
```

Expected: clean build.

- [ ] **Step 2: Run the full test suite once more**

```bash
go test ./... -count=1
```

Expected: every test passes.

- [ ] **Step 3: Run the linter**

```bash
golangci-lint run
```

Expected: no warnings on touched files. (If warnings exist that are unrelated to this PR, note them but don't fix here.)

- [ ] **Step 4: Start Felix and exercise the UI**

```bash
./felix start
```

Then open `http://127.0.0.1:18789/settings` in a browser, click the **Skills** tab, and verify each interaction:

- The list loads, showing pre-existing skills if any (cortex, ffmpeg, etc.).
- Click **Upload .md** and pick a small markdown file with frontmatter. Confirm a success toast appears, the row shows up in the list, and refreshing the page still shows it.
- Click **View** on the new row. Confirm the side panel shows the raw markdown.
- Try uploading the same filename again. Confirm a 409 error toast appears.
- Try uploading a non-`.md` file. Confirm the JS-side check rejects it (no network call).
- Click **Delete** on the new row, confirm the prompt, and verify the row disappears and the side panel closes if it was showing that file.
- Send a chat message that mentions the uploaded skill's name to a running agent. The skill should appear in the system prompt's "Available Skills" / "Skills Index" section on the very next turn — no restart required.

- [ ] **Step 5: Run-through complete — no further commits**

If any step above misbehaves, file the issue, fix it (potentially as a new task appended here), and re-run the smoke test. Otherwise the feature is complete.

---

## Self-Review Notes

- Spec coverage: List/Get/Upload/Delete each get a dedicated TDD task; routes mounted in Task 7; UI covered in Task 9; error matrix covered across Tasks 3-6. Reload-failure path is exercised in Task 5 (`TestUpload_ReloadFailure`).
- Auth is intentionally not retested; the global bearer middleware applied at `server.go:49` covers all `/settings/api/*` routes including the new ones, and there are no per-handler auth shortcuts.
- The hot-reload contract (in-place loader pointer shared across consumers) is verified in `TestUpload_Happy` and `TestDelete_Happy` by reading `loader.Skills()` after the call.
- No fsnotify watcher, no in-browser editor, no agent-workspace skill management, no enable/disable toggle — all explicitly out of scope per the spec.
