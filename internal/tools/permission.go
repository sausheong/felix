package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sausheong/felix/internal/llm"
)

// DecisionBehavior is the outcome of a permission check.
type DecisionBehavior int

const (
	DecisionAllow DecisionBehavior = iota
	DecisionDeny
)

// Decision is the result of a PermissionChecker.Check call. Reason is surfaced
// into the tool result when the behavior is Deny; it is ignored when Allow.
type Decision struct {
	Behavior DecisionBehavior
	Reason   string
}

// PermissionChecker decides whether a tool call may proceed. It is consulted
// once per tool invocation, after the call's input has been parsed but before
// the tool's Execute runs. Implementations must be safe for concurrent use.
//
// The input is passed (and may be ignored by simple implementations) so future
// input-aware checkers can implement the same interface without a signature
// change.
type PermissionChecker interface {
	Check(ctx context.Context, agentID, toolName string, input json.RawMessage) Decision
	// FilterToolDefs returns the subset of toolDefs visible to the given
	// agent. The order of the input is preserved. Used at tool-list-assembly
	// time so the LLM only advertises tools the agent is permitted to call.
	// Implementations must be safe for concurrent use.
	FilterToolDefs(toolDefs []llm.ToolDef, agentID string) []llm.ToolDef
}

// StaticChecker is the default PermissionChecker. It wraps existing per-agent
// tools.Policy values keyed by agent ID. An agent not present in the map is
// treated as allow-all (matches today's behavior when no policy is configured).
type StaticChecker struct {
	perAgent map[string]Policy
}

// NewStaticChecker builds a StaticChecker. A nil or empty map means allow-all
// for every agent.
func NewStaticChecker(perAgent map[string]Policy) *StaticChecker {
	if perAgent == nil {
		perAgent = map[string]Policy{}
	}
	return &StaticChecker{perAgent: perAgent}
}

// Check implements PermissionChecker.
func (c *StaticChecker) Check(_ context.Context, agentID, toolName string, _ json.RawMessage) Decision {
	p, ok := c.perAgent[agentID]
	if !ok || p.IsAllowed(toolName) {
		return Decision{Behavior: DecisionAllow}
	}
	return Decision{
		Behavior: DecisionDeny,
		Reason:   fmt.Sprintf("tool %q is not allowed for agent %q", toolName, agentID),
	}
}

// FilterToolDefs implements PermissionChecker. Unknown agents see the full
// toolDefs list (matches Check's allow-all default). Known agents have their
// list filtered through Policy.IsAllowed.
func (c *StaticChecker) FilterToolDefs(toolDefs []llm.ToolDef, agentID string) []llm.ToolDef {
	p, ok := c.perAgent[agentID]
	if !ok {
		return toolDefs // unknown agent → allow-all (matches Check)
	}
	out := make([]llm.ToolDef, 0, len(toolDefs))
	for _, td := range toolDefs {
		if p.IsAllowed(td.Name) {
			out = append(out, td)
		}
	}
	return out
}
