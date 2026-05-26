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

// OverlayMetrics is the minimal metrics surface ChatToolOverlay uses.
// Backed by gateway.Metrics in production; tests / inbox worker pass
// nil and the overlay skips the counter bump.
type OverlayMetrics interface {
	IncToolCalls(toolName string)
}

// ChatToolOverlay wraps the shared tool executor and adds per-chat
// tool instances on top. See the package doc for rationale.
type ChatToolOverlay struct {
	Base    tools.Executor  // required
	Task    *tools.TaskTool // optional
	Cron    *tools.CronTool // optional
	Metrics OverlayMetrics  // optional; bumps IncToolCalls on each Execute
}

// Execute dispatches a tool call. Overlay tools (task, cron) win over
// any same-named tool in Base. The Base executor handles everything else.
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
		filtered := defs[:0]
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
	// Re-sort to keep prompt-cache-stable ordering — see Registry.ToolDefs.
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// Names returns the union of Base's tool names plus the overlay tools,
// alphabetically sorted.
func (e *ChatToolOverlay) Names() []string {
	names := e.Base.Names()
	if e.Cron != nil {
		filtered := names[:0]
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
	sort.Strings(names)
	return names
}

// Get returns a tool by name. Overlay tools (task, cron) win over any
// same-named tool in Base.
func (e *ChatToolOverlay) Get(name string) (tools.Tool, bool) {
	if e.Task != nil && name == e.Task.Name() {
		return e.Task, true
	}
	if e.Cron != nil && name == e.Cron.Name() {
		return e.Cron, true
	}
	return e.Base.Get(name)
}
