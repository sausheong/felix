package gateway

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sausheong/felix/internal/config"
)

// resolveAgentPath validates rel against agentID's workspace and returns the
// absolute filesystem path. Rejects path-escape attempts, symlinks that
// resolve outside the workspace, and any path component starting with '.'.
// When the target doesn't yet exist (e.g. an upload destination), the parent
// directory must exist and resolve inside the workspace.
func resolveAgentPath(cfg *config.Config, agentID, rel string) (string, error) {
	if cfg == nil {
		return "", errors.New("nil config")
	}
	var workspace string
	for _, a := range cfg.Agents.List {
		if a.ID == agentID {
			workspace = a.Workspace
			break
		}
	}
	if workspace == "" {
		return "", fmt.Errorf("unknown agent %q", agentID)
	}

	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != "" {
			return "", fmt.Errorf("dotfile path not allowed: %q", rel)
		}
	}

	clean := filepath.Clean(filepath.Join(workspace, rel))

	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(clean)
		parentResolved, perr := filepath.EvalSymlinks(parent)
		if perr != nil {
			return "", perr
		}
		if !isInside(parentResolved, workspace) {
			return "", fmt.Errorf("path escapes workspace: %q", rel)
		}
		return filepath.Join(parentResolved, filepath.Base(clean)), nil
	}
	if !isInside(resolved, workspace) {
		return "", fmt.Errorf("path escapes workspace: %q", rel)
	}
	return resolved, nil
}

// isInside reports whether path is workspace itself or a descendant of it.
func isInside(path, workspace string) bool {
	wsAbs, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		wsAbs = workspace
	}
	if path == wsAbs {
		return true
	}
	return strings.HasPrefix(path+string(os.PathSeparator), wsAbs+string(os.PathSeparator))
}

// agentWorkspace returns the configured workspace dir for the agent, or "" if unknown.
func agentWorkspace(cfg *config.Config, agentID string) string {
	if cfg == nil {
		return ""
	}
	for _, a := range cfg.Agents.List {
		if a.ID == agentID {
			return a.Workspace
		}
	}
	return ""
}

// ensureWorkspace creates the workspace directory if it does not yet exist.
// resolveAgentPath's EvalSymlinks call fails on a non-existent workspace,
// producing a misleading error; calling this before resolveAgentPath fixes that.
func ensureWorkspace(ws string) error {
	if ws == "" {
		return nil
	}
	return os.MkdirAll(ws, 0o755)
}
