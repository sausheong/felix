package mcp

import (
	"context"
	"fmt"

	"github.com/sausheong/felix/internal/tools"
)

// ParallelSafeFn is the live-read callback an mcpToolAdapter uses to query
// its server's current parallelSafe flag. Implementations must be safe
// for concurrent calls and should read from the live Config (which is
// updated in-place by hot-reload, so values change between calls).
//
// Pass nil from tests or call sites that don't need hot-reload semantics —
// adapters built with a nil function report IsConcurrencySafe == false.
type ParallelSafeFn func(serverID string) bool

// RegisterTools registers every tool exposed by mgr's servers into reg, with
// the per-server ToolPrefix applied. Collisions with names already in reg
// (e.g. core tools) cause a hard error — operators must set tool_prefix to
// disambiguate. Server enumeration order matches Manager.Servers().
//
// parallelSafe is a live-read callback the adapter consults on every
// IsConcurrencySafe call so that toggling mcp_servers[].parallelSafe via
// the settings UI takes effect on the next agent run without restart.
// Pass nil to preserve the legacy "always false" behavior (used by tests).
//
// Uses a fresh background context for tools/list with no per-call timeout —
// the overall startup deadline (held by the caller) governs total time.
func RegisterTools(reg *tools.Registry, mgr *Manager, parallelSafe ParallelSafeFn) ([]string, error) {
	if mgr == nil {
		return nil, nil
	}
	ctx := context.Background()
	var registered []string
	for _, s := range mgr.Servers() {
		toolList, err := s.Live().ListTools(ctx)
		if err != nil {
			return nil, fmt.Errorf("mcp[%s]: list tools: %w", s.ID, err)
		}
		for _, t := range toolList {
			fullName := s.ToolPrefix + t.Name
			if _, exists := reg.Get(fullName); exists {
				return nil, fmt.Errorf("mcp[%s]: tool name collision on %q — set tool_prefix in mcp_servers config", s.ID, fullName)
			}
			reg.Register(newToolAdapter(fullName, t.Name, t.Description, t.InputSchema,
				s, parallelSafe))
			registered = append(registered, fullName)
		}
	}
	return registered, nil
}
