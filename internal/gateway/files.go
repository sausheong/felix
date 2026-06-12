package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sausheong/felix/internal/config"
)

// FilesHandlers serves /files/* endpoints for the operator-facing file
// explorer panel. All handlers clamp filesystem access to the selected
// agent's workspace via resolveAgentPath.
type FilesHandlers struct {
	cfgFunc   func() *config.Config
	diskCheck func(path string, addBytes int64) (bool, error)
}

// NewFilesHandlers wires the handlers with a closure that returns the live
// config, so agent additions/workspace changes are observed without a
// restart.
func NewFilesHandlers(cfgFunc func() *config.Config) *FilesHandlers {
	return &FilesHandlers{
		cfgFunc:   cfgFunc,
		diskCheck: diskUsageOK,
	}
}

// maxUploadBytes is the per-file upload cap.
const maxUploadBytes = 100 * 1024 * 1024 // 100 MiB

type listEntry struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
	MtimeUnix int64  `json:"mtime_unix"`
}
type listBreadcrumb struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type listResponse struct {
	Cwd         string           `json:"cwd"`
	Entries     []listEntry      `json:"entries"`
	Breadcrumbs []listBreadcrumb `json:"breadcrumbs"`
}

// List returns the contents of a directory inside the agent's workspace.
// Entries are returned with dirs first, then files, alphabetical within
// each group.
func (h *FilesHandlers) List(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent")
	dir := r.URL.Query().Get("dir")

	if err := ensureWorkspace(agentWorkspace(h.cfgFunc(), agentID)); err != nil {
		writeFilesError(w, http.StatusInternalServerError, &fileError{msg: "create workspace: " + err.Error()})
		return
	}
	abs, err := resolveAgentPath(h.cfgFunc(), agentID, dir)
	if err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			writeFilesError(w, http.StatusNotFound, err)
			return
		}
		writeFilesError(w, http.StatusInternalServerError, err)
		return
	}
	if !info.IsDir() {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "not a directory"})
		return
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		writeFilesError(w, http.StatusInternalServerError, err)
		return
	}

	out := make([]listEntry, 0, len(entries))
	for _, e := range entries {
		// Skip dotfiles (mirrors resolveAgentPath rejection).
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ent := listEntry{Name: e.Name()}
		if e.IsDir() {
			ent.Type = "dir"
		} else {
			ent.Type = "file"
		}
		if fi, ferr := e.Info(); ferr == nil {
			ent.Size = fi.Size()
			ent.MtimeUnix = fi.ModTime().Unix()
		}
		out = append(out, ent)
	}
	// Dirs first, then files, alpha within each group.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type == "dir"
		}
		return out[i].Name < out[j].Name
	})

	resp := listResponse{
		Cwd:         dir,
		Entries:     out,
		Breadcrumbs: buildBreadcrumbs(dir),
	}
	writeJSONResp(w, http.StatusOK, resp)
}

func buildBreadcrumbs(dir string) []listBreadcrumb {
	dir = strings.Trim(dir, "/")
	if dir == "" {
		return []listBreadcrumb{}
	}
	parts := strings.Split(dir, "/")
	out := make([]listBreadcrumb, 0, len(parts))
	for i, p := range parts {
		out = append(out, listBreadcrumb{
			Name: p,
			Path: path.Join(parts[:i+1]...),
		})
	}
	return out
}

// inlineSafeTypes are MIME types safe to render inline in the browser. SVG is
// deliberately excluded — it can carry script. Everything else downloads.
var inlineSafeTypes = map[string]bool{
	"image/png":        true,
	"image/jpeg":       true,
	"image/gif":        true,
	"image/webp":       true,
	"text/plain":       true,
	"application/pdf":  true,
	"application/json": true,
}

// rawDisposition returns "inline" only for an explicit allowlist of
// preview-safe content types; everything else (HTML, SVG, unknown binary)
// returns "attachment" so the browser downloads rather than renders it.
func rawDisposition(contentType string) string {
	base := contentType
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSpace(strings.ToLower(base))
	if inlineSafeTypes[base] {
		return "inline"
	}
	return "attachment"
}

// Raw streams a file from the agent's workspace. The Content-Type is
// sniffed; Content-Disposition defaults to attachment, switching to inline
// only for an allowlist of preview-safe types. ?download=1 forces attachment.
func (h *FilesHandlers) Raw(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent")
	rel := r.URL.Query().Get("path")
	download := r.URL.Query().Get("download") == "1"

	if err := ensureWorkspace(agentWorkspace(h.cfgFunc(), agentID)); err != nil {
		writeFilesError(w, http.StatusInternalServerError, &fileError{msg: "create workspace: " + err.Error()})
		return
	}
	abs, err := resolveAgentPath(h.cfgFunc(), agentID, rel)
	if err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			writeFilesError(w, http.StatusNotFound, err)
			return
		}
		writeFilesError(w, http.StatusInternalServerError, err)
		return
	}
	if info.IsDir() {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "path is a directory"})
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		writeFilesError(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()

	// Sniff Content-Type from first 512 bytes.
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	ct := http.DetectContentType(buf[:n])
	w.Header().Set("Content-Type", ct)

	// Security headers: never let the browser sniff a different type, and
	// sandbox any content that does render so it cannot run script or call
	// back into the gateway API.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")

	disposition := rawDisposition(ct)
	if download {
		disposition = "attachment" // explicit ?download=1 always attaches
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename=%q`, disposition, filepath.Base(abs)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(buf[:n]); err != nil {
		return
	}
	_, _ = io.Copy(w, f)
}

// --- response helpers ---

type fileError struct{ msg string }

func (e *fileError) Error() string { return e.msg }

func writeFilesError(w http.ResponseWriter, status int, err error) {
	slog.Warn("files handler error", "status", status, "error", err.Error())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func writeJSONResp(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Upload accepts a single file via multipart form (field name "file") and
// saves it under dir/<filename> in the agent's workspace. Refuses oversize
// uploads (413), disk-full (507), and dotfile filenames (400).
func (h *FilesHandlers) Upload(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent")
	dir := r.URL.Query().Get("dir")

	if err := ensureWorkspace(agentWorkspace(h.cfgFunc(), agentID)); err != nil {
		writeFilesError(w, http.StatusInternalServerError, &fileError{msg: "create workspace: " + err.Error()})
		return
	}

	// Cap body BEFORE parsing, so MultipartReader doesn't load 10 GB into memory.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1024) // +slop for multipart headers
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeFilesError(w, http.StatusRequestEntityTooLarge, err)
			return
		}
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}
	defer file.Close()

	if strings.HasPrefix(header.Filename, ".") {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "dotfile filenames not allowed"})
		return
	}
	if strings.ContainsAny(header.Filename, "/\\") {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "filename may not contain path separators"})
		return
	}
	if header.Size > maxUploadBytes {
		writeFilesError(w, http.StatusRequestEntityTooLarge, &fileError{msg: "file exceeds upload limit"})
		return
	}

	// Pre-create the destination directory if missing. resolveAgentPath
	// rejects paths whose parent dir doesn't exist, so without this every
	// upload to e.g. uploads/<file> would need a separate mkdir round-trip.
	// Safe because we clamp the parent inside the workspace before calling
	// MkdirAll, and the caller's `dir` has already been validated by the
	// dotfile check below at every depth.
	if dir != "" {
		ws := agentWorkspace(h.cfgFunc(), agentID)
		if ws == "" {
			writeFilesError(w, http.StatusBadRequest, &fileError{msg: "unknown agent"})
			return
		}
		for _, part := range strings.Split(filepath.ToSlash(dir), "/") {
			if part == "" || part == "." {
				continue
			}
			if part == ".." || strings.HasPrefix(part, ".") {
				writeFilesError(w, http.StatusBadRequest, &fileError{msg: "invalid path component"})
				return
			}
		}
		parentAbs := filepath.Clean(filepath.Join(ws, dir))
		if !isInside(parentAbs, ws) {
			writeFilesError(w, http.StatusBadRequest, &fileError{msg: "path escapes workspace"})
			return
		}
		if err := os.MkdirAll(parentAbs, 0o755); err != nil {
			writeFilesError(w, http.StatusInternalServerError, err)
			return
		}
	}

	relTarget := path.Join(dir, header.Filename)
	abs, err := resolveAgentPath(h.cfgFunc(), agentID, relTarget)
	if err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}

	ok, err := h.diskCheck(filepath.Dir(abs), header.Size)
	if err != nil {
		writeFilesError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeFilesError(w, http.StatusInsufficientStorage, &fileError{msg: "workspace disk usage would exceed 80%"})
		return
	}

	out, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		writeFilesError(w, http.StatusInternalServerError, err)
		return
	}
	written, copyErr := io.Copy(out, file)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(abs)
		writeFilesError(w, http.StatusInternalServerError, copyErr)
		return
	}
	if closeErr != nil {
		_ = os.Remove(abs)
		writeFilesError(w, http.StatusInternalServerError, closeErr)
		return
	}

	writeJSONResp(w, http.StatusOK, map[string]any{
		"name": header.Filename,
		"size": written,
	})
}

// Delete removes a file or directory in the agent's workspace. For
// non-empty directories, ?recursive=1 is required.
func (h *FilesHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent")
	rel := r.URL.Query().Get("path")
	recursive := r.URL.Query().Get("recursive") == "1"

	if err := ensureWorkspace(agentWorkspace(h.cfgFunc(), agentID)); err != nil {
		writeFilesError(w, http.StatusInternalServerError, &fileError{msg: "create workspace: " + err.Error()})
		return
	}
	abs, err := resolveAgentPath(h.cfgFunc(), agentID, rel)
	if err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}
	if abs == resolvedWorkspace(h.cfgFunc(), agentID) {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "cannot delete workspace root"})
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			writeFilesError(w, http.StatusNotFound, err)
			return
		}
		writeFilesError(w, http.StatusInternalServerError, err)
		return
	}

	if info.IsDir() && !recursive {
		entries, err := os.ReadDir(abs)
		if err != nil {
			writeFilesError(w, http.StatusInternalServerError, err)
			return
		}
		if len(entries) > 0 {
			writeFilesError(w, http.StatusConflict, &fileError{msg: "directory not empty; pass recursive=1 to delete"})
			return
		}
	}

	if recursive {
		err = os.RemoveAll(abs)
	} else {
		err = os.Remove(abs)
	}
	if err != nil {
		writeFilesError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Move relocates a file or directory inside the agent's workspace.
// Refuses if the source is a symlink, or if the destination already
// exists. Both paths must resolve inside the workspace.
func (h *FilesHandlers) Move(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agent string `json:"agent"`
		From  string `json:"from"`
		To    string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}

	if err := ensureWorkspace(agentWorkspace(h.cfgFunc(), body.Agent)); err != nil {
		writeFilesError(w, http.StatusInternalServerError, &fileError{msg: "create workspace: " + err.Error()})
		return
	}
	src, err := resolveAgentPath(h.cfgFunc(), body.Agent, body.From)
	if err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}
	if src == resolvedWorkspace(h.cfgFunc(), body.Agent) {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "cannot move workspace root"})
		return
	}
	dst, err := resolveAgentPath(h.cfgFunc(), body.Agent, body.To)
	if err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}

	// Refuse if the source path is a symlink — we Lstat the un-resolved
	// path because resolveAgentPath already followed any symlinks. Moving
	// a symlink-as-source is rejected to avoid the surprising behaviour
	// of renaming the target rather than the link.
	unresolvedSrc := filepath.Join(agentWorkspace(h.cfgFunc(), body.Agent), body.From)
	if linfo, lerr := os.Lstat(unresolvedSrc); lerr == nil && linfo.Mode()&os.ModeSymlink != 0 {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "cannot move symlink"})
		return
	}

	if _, err := os.Stat(dst); err == nil {
		writeFilesError(w, http.StatusConflict, &fileError{msg: "destination already exists"})
		return
	}
	if err := os.Rename(src, dst); err != nil {
		writeFilesError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Rename changes the basename of a file or directory. newName must not
// contain path separators or start with '.'; a destination collision
// returns 409.
func (h *FilesHandlers) Rename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agent   string `json:"agent"`
		Path    string `json:"path"`
		NewName string `json:"newName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}
	if strings.ContainsAny(body.NewName, "/\\") {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "newName may not contain path separators"})
		return
	}
	if strings.HasPrefix(body.NewName, ".") {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "dotfile names not allowed"})
		return
	}

	if err := ensureWorkspace(agentWorkspace(h.cfgFunc(), body.Agent)); err != nil {
		writeFilesError(w, http.StatusInternalServerError, &fileError{msg: "create workspace: " + err.Error()})
		return
	}
	src, err := resolveAgentPath(h.cfgFunc(), body.Agent, body.Path)
	if err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}
	if src == resolvedWorkspace(h.cfgFunc(), body.Agent) {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "cannot rename workspace root"})
		return
	}
	dstRel := path.Join(path.Dir(body.Path), body.NewName)
	dst, err := resolveAgentPath(h.cfgFunc(), body.Agent, dstRel)
	if err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := os.Stat(dst); err == nil {
		writeFilesError(w, http.StatusConflict, &fileError{msg: "destination already exists"})
		return
	}
	if err := os.Rename(src, dst); err != nil {
		writeFilesError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MkDir creates a new directory inside the agent's workspace. The path
// is workspace-relative; intermediate parents are created if missing.
// Returns 409 if anything (file or dir) already exists at the target.
//
// This handler does NOT use resolveAgentPath because that helper requires
// the leaf (or its parent) to exist on disk — incompatible with mkdir's
// "create nested dirs from nothing" semantics. Instead we validate path
// components inline and check the cleaned absolute path against the
// workspace root before MkdirAll.
func (h *FilesHandlers) MkDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agent string `json:"agent"`
		Path  string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeFilesError(w, http.StatusBadRequest, err)
		return
	}
	rel := strings.TrimSpace(body.Path)
	if rel == "" {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "path is required"})
		return
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			writeFilesError(w, http.StatusBadRequest, &fileError{msg: "path escape not allowed"})
			return
		}
		if strings.HasPrefix(part, ".") {
			writeFilesError(w, http.StatusBadRequest, &fileError{msg: "dotfile path not allowed"})
			return
		}
	}
	workspace := agentWorkspace(h.cfgFunc(), body.Agent)
	if workspace == "" {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "unknown agent"})
		return
	}
	if err := ensureWorkspace(workspace); err != nil {
		writeFilesError(w, http.StatusInternalServerError, &fileError{msg: "create workspace: " + err.Error()})
		return
	}
	abs := filepath.Clean(filepath.Join(workspace, rel))
	if !isInside(abs, workspace) {
		writeFilesError(w, http.StatusBadRequest, &fileError{msg: "path escapes workspace"})
		return
	}
	if _, err := os.Stat(abs); err == nil {
		writeFilesError(w, http.StatusConflict, &fileError{msg: "destination already exists"})
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		writeFilesError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
