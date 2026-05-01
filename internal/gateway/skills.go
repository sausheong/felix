package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sausheong/felix/internal/skill"
	"gopkg.in/yaml.v3"
)

// skillListEntry is a single skill returned by the List endpoint.
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

// skillReloader is the subset of *skill.Loader the handler needs. Defined
// as an interface so tests can inject a fake whose LoadFrom returns an error.
type skillReloader interface {
	LoadFrom(dirs ...string) error
}

// Compile-time guarantee that *skill.Loader satisfies skillReloader.
var _ skillReloader = (*skill.Loader)(nil)

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
		entries, err := os.ReadDir(skillsDir)
		if err != nil && !os.IsNotExist(err) {
			writeSkillJSONError(w, http.StatusInternalServerError, "read skills dir: "+err.Error())
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

		writeSkillJSON(w, out)
	}
	h.Get = func(w http.ResponseWriter, r *http.Request) {
		raw := chi.URLParam(r, "name")
		// Validate the raw param first so path separators / spaces / etc. are
		// rejected with 400 instead of being laundered through filepath.Base.
		if err := validateSkillName(raw); err != nil {
			writeSkillJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Defense-in-depth: even after validation, only ever join the basename.
		name := filepath.Base(raw)
		full := filepath.Join(skillsDir, name)
		data, err := os.ReadFile(full)
		if err != nil {
			if os.IsNotExist(err) {
				writeSkillJSONError(w, http.StatusNotFound, "skill not found")
				return
			}
			writeSkillJSONError(w, http.StatusInternalServerError, "read: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
	h.Upload = func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxSkillUploadBytes)
		if err := r.ParseMultipartForm(maxSkillUploadBytes); err != nil {
			// MaxBytesReader's "request body too large" surfaces here.
			if strings.Contains(err.Error(), "request body too large") {
				writeSkillJSONError(w, http.StatusRequestEntityTooLarge, "upload exceeds 256KB limit")
				return
			}
			writeSkillJSONError(w, http.StatusBadRequest, "parse multipart: "+err.Error())
			return
		}

		file, hdr, err := r.FormFile("file")
		if err != nil {
			writeSkillJSONError(w, http.StatusBadRequest, `missing "file" field`)
			return
		}
		defer file.Close()

		// Spec: silently sanitize the multipart filename via filepath.Base (browsers
		// already strip path info; this is defense in depth). Then validate the
		// regex on the sanitized name. Path-traversal protection comes from Base.
		name := filepath.Base(hdr.Filename)
		if err := validateSkillName(name); err != nil {
			writeSkillJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			if strings.Contains(err.Error(), "request body too large") {
				writeSkillJSONError(w, http.StatusRequestEntityTooLarge, "upload exceeds 256KB limit")
				return
			}
			writeSkillJSONError(w, http.StatusBadRequest, "read upload: "+err.Error())
			return
		}

		// Validate frontmatter if present (body-only files are valid).
		fm, _ := skill.SplitFrontmatter(string(data))
		if fm != "" {
			var probe skill.Skill
			if yerr := yaml.Unmarshal([]byte(fm), &probe); yerr != nil {
				writeSkillJSONError(w, http.StatusUnprocessableEntity, "invalid YAML frontmatter: "+yerr.Error())
				return
			}
		}

		target := filepath.Join(skillsDir, name)
		if _, err := os.Stat(target); err == nil {
			writeSkillJSONError(w, http.StatusConflict, fmt.Sprintf("skill %q already exists; delete first to replace", name))
			return
		} else if !os.IsNotExist(err) {
			writeSkillJSONError(w, http.StatusInternalServerError, "stat target: "+err.Error())
			return
		}

		tmp := target + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			writeSkillJSONError(w, http.StatusInternalServerError, "write tmp: "+err.Error())
			return
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			writeSkillJSONError(w, http.StatusInternalServerError, "rename: "+err.Error())
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
		writeSkillJSON(w, resp)
	}
	h.Delete = func(w http.ResponseWriter, r *http.Request) {
		writeSkillJSONError(w, http.StatusNotImplemented, "delete not implemented")
	}
	return h
}

// maxSkillUploadBytes caps a single skill upload at 256 KB.
const maxSkillUploadBytes = 256 * 1024

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

// writeSkillJSONError writes a JSON error response with the given status.
// Named with a "Skill" prefix to avoid colliding with the websocket helper
// writeJSON(*websocket.Conn, any) defined in websocket.go.
func writeSkillJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeSkillJSON writes a JSON response with status 200. See note on
// writeSkillJSONError for why this isn't called writeJSON.
func writeSkillJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Header already sent at this point; nothing useful to do besides
		// what the stdlib does. Other gateway files use slog for logging
		// but we keep this file dependency-light.
		_ = err
	}
}
