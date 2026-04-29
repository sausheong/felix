package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SubagentFactory builds a SubagentRunner for the given subagent ID. The
// implementation enforces the recursion-depth cap and validates that agentID
// refers to an opt-in subagent (Subagent: true in AgentConfig). The
// parentDepth argument lets the factory check `parentDepth+1 <= max` before
// constructing anything.
type SubagentFactory func(ctx context.Context, agentID string, parentDepth int) (SubagentRunner, error)

// SubagentRunner is the narrow interface TaskTool calls. It exists so that
// internal/tools can stay decoupled from internal/agent — agent.Runtime
// satisfies this contract via a small adapter (built in T6).
type SubagentRunner interface {
	// Run executes the subagent with the given prompt. It returns a channel
	// that the caller drains until close. Each event signals a step:
	//   - Text != "": a text delta to accumulate into the final tool output
	//   - Done == true: subagent finished cleanly
	//   - Aborted == true: subagent terminated due to cancellation
	//   - Err != nil: subagent failed
	Run(ctx context.Context, prompt string) (<-chan AgentEventLike, error)
}

// AgentEventLike is the minimal event shape TaskTool needs from the agent
// package's AgentEvent. Defined here to avoid an import cycle between
// internal/tools and internal/agent. The subagent's Run goroutine adapts its
// AgentEvent into AgentEventLike for the channel TaskTool consumes.
type AgentEventLike struct {
	Type    int // matches agent.EventType (caller-defined int constants)
	Text    string
	Done    bool
	Aborted bool
	Err     error
}

// TaskTool is the "task" tool — lets a parent agent delegate work to a
// subagent registered in the config (AgentConfig.Subagent: true). The
// subagent's final assistant text becomes this tool's Output.
//
// Not concurrency-safe (returns false from IsConcurrencySafe): a parent
// running multiple parallel "task" calls would spawn multiple subagent
// runtimes that all forward events to the same parent event channel,
// which is fine in principle but currently untested. Mark unsafe to be
// conservative; can be loosened later if integration tests prove parallel
// subagents work.
type TaskTool struct {
	factory     SubagentFactory
	parentDepth int
	eligible    map[string]string // agent_id → description (for the tool-description block)
	descBlock   string            // pre-formatted block listing available subagents
}

// NewTaskTool constructs a TaskTool. parentDepth is the depth of the
// invoking Runtime — passed straight through to factory() at execute time
// so depth-cap enforcement is centralised in the factory. eligible is the
// subset of agents flagged Subagent: true; it must be non-empty (caller
// is responsible for the empty-map gate per spec Section 6).
func NewTaskTool(factory SubagentFactory, parentDepth int, eligible map[string]string) *TaskTool {
	return &TaskTool{
		factory:     factory,
		parentDepth: parentDepth,
		eligible:    eligible,
		descBlock:   formatEligibleBlock(eligible),
	}
}

// formatEligibleBlock renders the alphabetically-sorted list of available
// subagents that gets appended to the tool's Description. Sorting matters
// for prompt-cache stability — see Registry.ToolDefs for the same reason.
func formatEligibleBlock(eligible map[string]string) string {
	if len(eligible) == 0 {
		return ""
	}
	ids := make([]string, 0, len(eligible))
	for id := range eligible {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("\n\nAvailable subagents:\n")
	for _, id := range ids {
		fmt.Fprintf(&b, "  - %s: %s\n", id, eligible[id])
	}
	return b.String()
}

// Name returns the tool name as advertised to the LLM.
func (*TaskTool) Name() string { return "task" }

// Description tells the parent LLM what the tool does and which subagents are
// available. The subagent listing is appended at construction time so the
// description is stable across invocations of the same TaskTool instance.
func (t *TaskTool) Description() string {
	return "Delegate a subtask to a specialized subagent. The subagent runs " +
		"independently with its own tools and system prompt; its final " +
		"response is returned as this tool's output." + t.descBlock
}

// Parameters returns the JSON Schema for the tool input.
func (*TaskTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["agent_id", "prompt"],
  "properties": {
    "agent_id": {
      "type": "string",
      "description": "ID of the subagent to invoke. Must be one of the listed subagents in this tool's description."
    },
    "prompt": {
      "type": "string",
      "description": "The instruction to send to the subagent. Be self-contained — the subagent has no access to the parent conversation."
    }
  }
}`)
}

// IsConcurrencySafe is false: see TaskTool comment for rationale.
func (*TaskTool) IsConcurrencySafe(json.RawMessage) bool { return false }

// Execute parses the tool input, invokes the factory to construct a subagent
// runner, drains its events, and returns the accumulated assistant text as
// the tool result. Returns a tool error (in ToolResult.Error) for invalid
// input, unknown agent_id, depth-cap exceeded, subagent abort, and subagent
// error. Returns a Go error only if the factory itself returns one in a way
// that must propagate — currently never (factory errors land in ToolResult.Error).
func (t *TaskTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var args struct {
		AgentID string `json:"agent_id"`
		Prompt  string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid task input: %v", err)}, nil
	}
	if args.AgentID == "" || args.Prompt == "" {
		return ToolResult{Error: "task: agent_id and prompt are required"}, nil
	}
	if _, ok := t.eligible[args.AgentID]; !ok {
		ids := make([]string, 0, len(t.eligible))
		for id := range t.eligible {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return ToolResult{
			Error: fmt.Sprintf("task: unknown subagent %q (eligible: %s)", args.AgentID, strings.Join(ids, ", ")),
		}, nil
	}

	runner, err := t.factory(ctx, args.AgentID, t.parentDepth)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("task: %v", err)}, nil
	}

	events, err := runner.Run(ctx, args.Prompt)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("task: subagent run failed: %v", err)}, nil
	}

	var out strings.Builder
	for ev := range events {
		switch {
		case ev.Aborted:
			return ToolResult{Error: "task: subagent aborted"}, nil
		case ev.Err != nil:
			return ToolResult{Error: fmt.Sprintf("task: subagent error: %v", ev.Err)}, nil
		case ev.Text != "":
			out.WriteString(ev.Text)
		}
		// Done is implicit on channel close after a Done event; we just continue draining.
	}

	return ToolResult{Output: out.String()}, nil
}
