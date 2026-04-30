package mcp

import (
	"context"
	"fmt"

	"github.com/sausheong/felix/internal/tools"
)

// RegisterTools registers every tool exposed by mgr's servers into reg, with
// the per-server ToolPrefix applied. Collisions with names already in reg
// (e.g. core tools) cause a hard error — operators must set tool_prefix to
// disambiguate. Server enumeration order matches Manager.Servers().
//
// Uses a fresh background context for tools/list with no per-call timeout —
// the overall startup deadline (held by the caller) governs total time.
func RegisterTools(reg *tools.Registry, mgr *Manager) ([]string, error) {
	if mgr == nil {
		return nil, nil
	}
	ctx := context.Background()
	var registered []string
	for _, s := range mgr.Servers() {
		toolList, err := s.Client.ListTools(ctx)
		if err != nil {
			return nil, fmt.Errorf("mcp[%s]: list tools: %w", s.ID, err)
		}
		for _, t := range toolList {
			fullName := s.ToolPrefix + t.Name
			if _, exists := reg.Get(fullName); exists {
				return nil, fmt.Errorf("mcp[%s]: tool name collision on %q — set tool_prefix in mcp_servers config", s.ID, fullName)
			}
			reg.Register(newToolAdapter(fullName, t.Name, t.Description, t.InputSchema, s.Client, s.ParallelSafe))
			registered = append(registered, fullName)
		}
	}
	return registered, nil
}
