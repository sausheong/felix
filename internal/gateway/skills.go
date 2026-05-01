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
		writeSkillJSONError(w, http.StatusNotImplemented, "list not implemented")
	}
	h.Get = func(w http.ResponseWriter, r *http.Request) {
		writeSkillJSONError(w, http.StatusNotImplemented, "get not implemented")
	}
	h.Upload = func(w http.ResponseWriter, r *http.Request) {
		writeSkillJSONError(w, http.StatusNotImplemented, "upload not implemented")
	}
	h.Delete = func(w http.ResponseWriter, r *http.Request) {
		writeSkillJSONError(w, http.StatusNotImplemented, "delete not implemented")
	}
	// Silence unused-arg warnings until subsequent tasks fill in the handlers.
	_ = loader
	_ = skillsDir
	_ = reloadDirs
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
