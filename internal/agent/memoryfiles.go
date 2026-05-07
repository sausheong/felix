package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// loadAgentMemoryFilesImpl reads FELIX.md and AGENTS.md from workspace and
// $HOME, concatenating with a brief header per source.
//
// Discovery order (highest priority first):
//  1. <workspace>/FELIX.md  — labelled "Project memory"
//  2. <workspace>/AGENTS.md — labelled "Project memory"
//  3. $HOME/FELIX.md        — labelled "User memory"
//  4. $HOME/AGENTS.md       — labelled "User memory"
//
// Empty / whitespace-only / missing / unreadable files are silently
// skipped. Files at the same absolute path are deduped. Hard cap: total
// returned content ≤ 40 KB; truncates at last newline before the byte
// limit and appends a marker. Subsequent files are skipped after
// truncation.
//
// Pre-extraction this lived in internal/agent/context.go as
// LoadAgentMemoryFiles. Now lives Felix-side because the file names
// (FELIX.md, AGENTS.md) and order are Felix-opinionated.
func loadAgentMemoryFilesImpl(workspace string) string {
	const maxBytes = 40 * 1024
	truncationNotice := fmt.Sprintf("\n\n[truncated — over %d KB total agent memory]", maxBytes/1024)

	type candidate struct {
		path  string
		label string
	}
	var candidates []candidate
	if workspace != "" {
		candidates = append(candidates,
			candidate{filepath.Join(workspace, "FELIX.md"), "Project memory"},
			candidate{filepath.Join(workspace, "AGENTS.md"), "Project memory"},
		)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			candidate{filepath.Join(home, "FELIX.md"), "User memory"},
			candidate{filepath.Join(home, "AGENTS.md"), "User memory"},
		)
	}

	seen := map[string]bool{}
	var sb strings.Builder
	truncated := false

	for _, c := range candidates {
		if truncated {
			break
		}
		abs, err := filepath.Abs(c.path)
		if err != nil {
			continue
		}
		if seen[abs] {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		seen[abs] = true
		body := strings.TrimSpace(string(data))
		if body == "" {
			continue
		}

		header := fmt.Sprintf("\n\n## %s: %s\n\n", c.label, abs)
		section := header + body

		if sb.Len()+len(section) > maxBytes {
			remaining := maxBytes - sb.Len() - len(header)
			if remaining > 0 {
				cut := body[:remaining]
				if idx := strings.LastIndex(cut, "\n"); idx > remaining/2 {
					cut = cut[:idx]
				}
				sb.WriteString(header)
				sb.WriteString(cut)
			}
			sb.WriteString(truncationNotice)
			truncated = true
			continue
		}
		sb.WriteString(section)
	}

	return sb.String()
}
