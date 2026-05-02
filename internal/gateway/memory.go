package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/sausheong/felix/internal/memory"
)

// memoryListEntry is a single entry returned by the List endpoint.
type memoryListEntry struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Modified string `json:"modified"`
	Bytes    int    `json:"bytes"`
}

// memoryManager is the subset of *memory.Manager the handler needs. Defined
// as an interface so tests can inject a fake.
type memoryManager interface {
	Entries() []memory.Entry
	Get(id string) (memory.Entry, bool)
	Save(id, content string) error
	Delete(id string) error
}

// Compile-time guarantee that *memory.Manager satisfies memoryManager.
var _ memoryManager = (*memory.Manager)(nil)

// MemoryHandlers exposes HTTP handlers for managing memory entries in
// ~/.felix/memory/entries/. All routes are mounted under
// /settings/api/memory* by the gateway server and inherit the global
// bearer-auth middleware.
type MemoryHandlers struct {
	List   http.HandlerFunc
	Get    http.HandlerFunc
	Save   http.HandlerFunc
	Delete http.HandlerFunc
}

// maxMemoryEntryBytes caps a single memory entry write at 256 KB. The same
// number used for skill uploads — large enough for any realistic note,
// small enough that a UI bug can't pile MB onto disk by accident.
const maxMemoryEntryBytes = 256 * 1024

// memoryIDRE matches a safe memory entry id. Mirrors validateSkillName
// without the .md suffix requirement (memory ids are bare names; the .md
// extension is added by Manager.Save internally).
var memoryIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validateMemoryID(id string) error {
	if id == "" {
		return fmt.Errorf("id is empty")
	}
	if !memoryIDRE.MatchString(id) {
		return fmt.Errorf("id %q is not a valid memory id (allowed: letters, digits, dot, dash, underscore)", id)
	}
	return nil
}

// NewMemoryHandlers builds the handler set. mgr may be nil (memory
// disabled by config) — in that case all handlers return a 503.
func NewMemoryHandlers(mgr memoryManager) *MemoryHandlers {
	h := &MemoryHandlers{}

	disabled := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "memory is disabled in config (set memory.enabled=true)",
		})
	}

	h.List = func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			disabled(w)
			return
		}
		entries := mgr.Entries()
		out := struct {
			Entries []memoryListEntry `json:"entries"`
		}{Entries: make([]memoryListEntry, 0, len(entries))}
		for _, e := range entries {
			out.Entries = append(out.Entries, memoryListEntry{
				ID:       e.ID,
				Title:    e.Title,
				Modified: e.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
				Bytes:    len(e.Content),
			})
		}
		sort.Slice(out.Entries, func(i, j int) bool {
			return out.Entries[i].ID < out.Entries[j].ID
		})
		writeMemoryJSON(w, http.StatusOK, out)
	}

	h.Get = func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			disabled(w)
			return
		}
		id := chi.URLParam(r, "id")
		if err := validateMemoryID(id); err != nil {
			writeMemoryJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		e, ok := mgr.Get(id)
		if !ok {
			writeMemoryJSONError(w, http.StatusNotFound, "entry not found")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(e.Content))
	}

	h.Save = func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			disabled(w)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxMemoryEntryBytes+1))
		if err != nil {
			writeMemoryJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
			return
		}
		if len(body) > maxMemoryEntryBytes {
			writeMemoryJSONError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("entry exceeds %d byte limit", maxMemoryEntryBytes))
			return
		}
		var payload struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			writeMemoryJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := validateMemoryID(payload.ID); err != nil {
			writeMemoryJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.TrimSpace(payload.Content) == "" {
			writeMemoryJSONError(w, http.StatusBadRequest, "content is empty")
			return
		}
		if err := mgr.Save(payload.ID, payload.Content); err != nil {
			writeMemoryJSONError(w, http.StatusInternalServerError, "save: "+err.Error())
			return
		}
		writeMemoryJSON(w, http.StatusOK, map[string]any{"ok": true, "id": payload.ID})
	}

	h.Delete = func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			disabled(w)
			return
		}
		id := chi.URLParam(r, "id")
		if err := validateMemoryID(id); err != nil {
			writeMemoryJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := mgr.Delete(id); err != nil {
			if strings.Contains(err.Error(), "not found") {
				writeMemoryJSONError(w, http.StatusNotFound, err.Error())
				return
			}
			writeMemoryJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeMemoryJSON(w, http.StatusOK, map[string]any{"ok": true})
	}

	return h
}

func writeMemoryJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeMemoryJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
