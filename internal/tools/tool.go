package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/sausheong/felix/internal/llm"
)

// expandHome rewrites a leading "~" or "~/" in p to the user's home directory.
// Other tildes (mid-path, "~user/...") are left alone — Felix never escalates
// privileges, so cross-user tilde expansion would silently fail anyway.
//
// The shell ($BASH/zsh) does this expansion before exec, which is why the
// bash tool works without it. read/write/edit_file call os.Open directly so
// they need to handle "~/" themselves.
func expandHome(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// resolveExistingPath returns a path that actually exists on disk, recovering
// from Unicode-whitespace mismatches between what the LLM supplies and what
// the filesystem holds. Resolution order:
//  1. The path as given.
//  2. The Unicode-sanitized variant (NBSP / narrow-NBSP / ideographic /
//     en/em/figure-space → ASCII space; zero-width chars stripped).
//  3. The parent directory is scanned and an entry whose own sanitized name
//     equals the sanitized basename of the requested path is used — but only
//     if the match is unambiguous.
//
// If nothing resolves, p is returned so the caller's error message reflects
// what the LLM actually supplied. Never used for write paths — writing to a
// "resolved" path could create files in unintended locations.
func resolveExistingPath(p string) string {
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if alt := sanitizeLLMText(p); alt != p {
		if _, err := os.Stat(alt); err == nil {
			return alt
		}
	}
	dir, base := filepath.Split(p)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return p
	}
	target := sanitizeLLMText(base)
	var matches []string
	for _, e := range entries {
		if sanitizeLLMText(e.Name()) == target {
			matches = append(matches, e.Name())
		}
	}
	if len(matches) == 1 {
		return filepath.Join(dir, matches[0])
	}
	return p
}

// resolveExistingPathStrict is like resolveExistingPath but the dir-scan
// fallback only fires when the matched on-disk entry actually contains a
// non-ASCII whitespace character. Used by the bash tool, where freeform
// commands include both read-paths (which we want to recover) and create-
// paths like `mkdir /tmp/newdir` (which must NOT be silently substituted
// with a similarly-named pre-existing entry).
func resolveExistingPathStrict(p string) string {
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if alt := sanitizeLLMText(p); alt != p {
		if _, err := os.Stat(alt); err == nil {
			return alt
		}
	}
	dir, base := filepath.Split(p)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return p
	}
	target := sanitizeLLMText(base)
	var matches []string
	for _, e := range entries {
		if sanitizeLLMText(e.Name()) == target && hasUnicodeWhitespace(e.Name()) {
			matches = append(matches, e.Name())
		}
	}
	if len(matches) == 1 {
		return filepath.Join(dir, matches[0])
	}
	return p
}

// hasUnicodeWhitespace reports whether s contains any of the whitespace or
// invisible characters that sanitizeLLMText normalizes away.
func hasUnicodeWhitespace(s string) bool {
	for _, r := range s {
		switch r {
		case '\u200B', '\u200C', '\u200D', '\u2060', '\uFEFF', '\u2028', '\u2029':
			return true
		}
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' && unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// sanitizeLLMText normalizes Unicode whitespace lookalikes that small or
// quantized LLMs sometimes emit in place of ASCII space and newline, and
// strips zero-width characters that have no shell or filesystem meaning.
// Used as a fallback by resolveExistingPath, not applied eagerly to LLM
// input (eager sanitization breaks the case where a file genuinely
// contains NBSP in its name).
func sanitizeLLMText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\u200B', '\u200C', '\u200D', '\u2060', '\uFEFF':
			continue // zero-width / BOM: drop
		case '\u2028', '\u2029':
			b.WriteByte('\n') // line / paragraph separator → newline
		default:
			if r != ' ' && r != '\t' && r != '\n' && r != '\r' && unicode.IsSpace(r) {
				b.WriteByte(' ') // any other Unicode whitespace → ASCII space
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Tool is the interface that all Felix tools must implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage // JSON Schema
	Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
}

// ToolResult holds the output of a tool execution.
type ToolResult struct {
	Output   string             `json:"output"`
	Error    string             `json:"error,omitempty"`
	Metadata map[string]any     `json:"metadata,omitempty"`
	Images   []llm.ImageContent `json:"-"` // image attachments (not JSON-serialized)
}

// Executor is the interface used by agent runtime for tool operations.
// Both Registry and FilteredRegistry implement this.
type Executor interface {
	Execute(ctx context.Context, name string, input json.RawMessage) (ToolResult, error)
	ToolDefs() []llm.ToolDef
	Names() []string
	Get(name string) (Tool, bool)
}

// Registry manages a collection of available tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates a new tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Execute runs a tool by name with the given input.
func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) (ToolResult, error) {
	t, ok := r.Get(name)
	if !ok {
		return ToolResult{}, fmt.Errorf("unknown tool: %q", name)
	}
	return t.Execute(ctx, input)
}

// ToolDefs returns the tool definitions for the LLM API.
func (r *Registry) ToolDefs() []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]llm.ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}
	return defs
}

// Names returns the names of all registered tools.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// RegisterCoreTools registers the core tools.
// execPolicy is optional — pass nil for unrestricted bash execution.
func RegisterCoreTools(reg *Registry, workDir string, execPolicy *ExecPolicy) {
	reg.Register(&ReadFileTool{WorkDir: workDir})
	reg.Register(&WriteFileTool{WorkDir: workDir})
	reg.Register(&EditFileTool{WorkDir: workDir})
	reg.Register(&BashTool{WorkDir: workDir, ExecPolicy: execPolicy})
	reg.Register(&WebFetchTool{})
	reg.Register(&WebSearchTool{})
	reg.Register(&BrowserTool{})
}

// validatePathInWorkDir ensures that the resolved path is within the workspace.
// It resolves symlinks and normalizes the path to prevent traversal attacks.
func validatePathInWorkDir(path, workDir string) error {
	if workDir == "" {
		return nil
	}

	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("invalid workspace: %w", err)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// Resolve symlinks to prevent symlink-based traversal
	realWork, err := filepath.EvalSymlinks(absWork)
	if err != nil {
		realWork = absWork // workspace might not exist yet
	}

	// For the target path, resolve the parent directory (file may not exist yet)
	parentDir := filepath.Dir(absPath)
	realParent, err := filepath.EvalSymlinks(parentDir)
	if err != nil {
		// Parent doesn't exist — use the unresolved absolute path
		realParent = parentDir
	}
	realPath := filepath.Join(realParent, filepath.Base(absPath))

	if !strings.HasPrefix(realPath, realWork+string(filepath.Separator)) && realPath != realWork {
		return fmt.Errorf("path %q is outside workspace %q", path, workDir)
	}

	return nil
}

// RegisterCron registers the cron tool with the given scheduler.
func RegisterCron(reg *Registry, scheduler JobScheduler) {
	reg.Register(&CronTool{Scheduler: scheduler})
}

// RegisterAskAgent registers the ask_agent tool with the given runner.
func RegisterAskAgent(reg *Registry, runner AgentRunner) {
	reg.Register(&AskAgentTool{Runner: runner})
}
