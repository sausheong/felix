// Package chatexec — per-chat tool overlay.
//
// ChatToolOverlay wraps a base tools.Executor (typically the shared
// agent-wide registry) and adds per-call tool instances on top. The
// overlay is required for tools that must capture state from THIS
// chat's runtime — registering them on the shared registry would
// either race-clobber other chats or cross-wire that state.
//
// Currently overlaid:
//
//   - "task": captures the chat's parent Runtime so subagents can be
//     dispatched with the right depth/lineage. Optional (nil when no
//     subagents are eligible).
//   - "cron": captures the chat agent's ID so jobs the LLM schedules
//     run as the agent that scheduled them. Optional (nil when there's
//     no job scheduler wired).
//
// The overlay is read-through for every tool name not in its overlay set.
package chatexec

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/tools"
)

// ChatToolOverlay wraps the shared tool executor and adds per-chat
// tool instances on top. See the package doc for rationale.
type ChatToolOverlay struct {
	Base    tools.Executor  // required
	Task    *tools.TaskTool // optional
	Cron    *tools.CronTool // optional
	Cortex  []tools.Tool    // optional; per-chat cortex tools (recall/remember/find_entities/get_relationships)
	Metrics MetricsLike     // optional; bumps IncToolCalls on each Execute
}

// Execute dispatches a tool call. Overlay tools (task, cron, cortex)
// win over any same-named tool in Base. The Base executor handles
// everything else.
func (e *ChatToolOverlay) Execute(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	if e.Metrics != nil {
		e.Metrics.IncToolCalls(name)
	}
	if e.Task != nil && name == e.Task.Name() {
		return e.Task.Execute(ctx, input)
	}
	if e.Cron != nil && name == e.Cron.Name() {
		return e.Cron.Execute(ctx, input)
	}
	for _, ct := range e.Cortex {
		if ct.Name() == name {
			return ct.Execute(ctx, input)
		}
	}
	return e.Base.Execute(ctx, name, input)
}

// ToolDefs returns the merged list of tool defs from Base plus the
// overlay tools, with stable alphabetical ordering so the prompt cache
// stays warm.
func (e *ChatToolOverlay) ToolDefs() []llm.ToolDef {
	defs := e.Base.ToolDefs()
	// Drop any name we override so the per-chat version wins. The shared
	// registry no longer holds "cron" in production wiring (startup.go
	// stopped registering it globally), but a stale def would still break
	// prompt-cache stability if we duplicated it.
	if e.Cron != nil {
		filtered := make([]llm.ToolDef, 0, len(defs))
		for _, d := range defs {
			if d.Name != e.Cron.Name() {
				filtered = append(filtered, d)
			}
		}
		defs = filtered
		defs = append(defs, llm.ToolDef{
			Name:        e.Cron.Name(),
			Description: e.Cron.Description(),
			Parameters:  e.Cron.Parameters(),
		})
	}
	if e.Task != nil {
		defs = append(defs, llm.ToolDef{
			Name:        e.Task.Name(),
			Description: e.Task.Description(),
			Parameters:  e.Task.Parameters(),
		})
	}
	for _, ct := range e.Cortex {
		// Drop any same-named def from Base so the per-chat cortex tool
		// wins. In normal wiring Base never registers cortex tools, but
		// guarding here keeps prompt-cache stability if it ever does.
		filtered := make([]llm.ToolDef, 0, len(defs))
		for _, d := range defs {
			if d.Name != ct.Name() {
				filtered = append(filtered, d)
			}
		}
		defs = filtered
		defs = append(defs, llm.ToolDef{
			Name:        ct.Name(),
			Description: ct.Description(),
			Parameters:  ct.Parameters(),
		})
	}
	// Re-sort to keep prompt-cache-stable ordering — see Registry.ToolDefs.
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// Names returns the union of Base's tool names plus the overlay tools,
// alphabetically sorted.
func (e *ChatToolOverlay) Names() []string {
	names := e.Base.Names()
	if e.Cron != nil {
		filtered := make([]string, 0, len(names))
		for _, n := range names {
			if n != e.Cron.Name() {
				filtered = append(filtered, n)
			}
		}
		names = filtered
		names = append(names, e.Cron.Name())
	}
	if e.Task != nil {
		names = append(names, e.Task.Name())
	}
	for _, ct := range e.Cortex {
		filtered := make([]string, 0, len(names))
		for _, n := range names {
			if n != ct.Name() {
				filtered = append(filtered, n)
			}
		}
		names = filtered
		names = append(names, ct.Name())
	}
	sort.Strings(names)
	return names
}

// Get returns a tool by name. Overlay tools (task, cron, cortex) win
// over any same-named tool in Base.
func (e *ChatToolOverlay) Get(name string) (tools.Tool, bool) {
	if e.Task != nil && name == e.Task.Name() {
		return e.Task, true
	}
	if e.Cron != nil && name == e.Cron.Name() {
		return e.Cron, true
	}
	for _, ct := range e.Cortex {
		if ct.Name() == name {
			return ct, true
		}
	}
	return e.Base.Get(name)
}
