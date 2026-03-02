package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// AgentRunner is the interface for delegating tasks to other agents.
// This avoids importing the gateway package (circular dependency).
// The gateway's AgentRunnerImpl implements this interface.
type AgentRunner interface {
	RunAgent(ctx context.Context, agentID, prompt string) (string, error)
	AvailableAgents() []AgentInfo
}

// AgentInfo is a summary of an available agent.
type AgentInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AskAgentTool delegates a task to another agent and returns the result.
type AskAgentTool struct {
	Runner AgentRunner
}

type askAgentInput struct {
	AgentID string `json:"agent_id"` // target agent ID
	Prompt  string `json:"prompt"`   // instruction for the target agent
}

func (t *AskAgentTool) Name() string { return "ask_agent" }

func (t *AskAgentTool) Description() string {
	var b strings.Builder
	b.WriteString(`Delegate a task to another agent and get back the result. Use this when a subtask is better handled by a different agent (e.g. one with different tools, model, or specialization). The delegated agent runs independently with its own session and tools.`)

	if t.Runner != nil {
		agents := t.Runner.AvailableAgents()
		if len(agents) > 0 {
			b.WriteString("\n\nAvailable agents:")
			for _, a := range agents {
				if a.Name != "" {
					fmt.Fprintf(&b, "\n- %s (%s)", a.ID, a.Name)
				} else {
					fmt.Fprintf(&b, "\n- %s", a.ID)
				}
			}
		}
	}

	return b.String()
}

func (t *AskAgentTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"agent_id": {
				"type": "string",
				"description": "The ID of the agent to delegate the task to"
			},
			"prompt": {
				"type": "string",
				"description": "The instruction or task for the target agent to perform"
			}
		},
		"required": ["agent_id", "prompt"]
	}`)
}

func (t *AskAgentTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var in askAgentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}

	if in.AgentID == "" {
		return ToolResult{Error: "agent_id is required"}, nil
	}
	if in.Prompt == "" {
		return ToolResult{Error: "prompt is required"}, nil
	}

	if t.Runner == nil {
		return ToolResult{Error: "agent delegation is not available"}, nil
	}

	response, err := t.Runner.RunAgent(ctx, in.AgentID, in.Prompt)
	if err != nil {
		return ToolResult{Error: fmt.Sprintf("agent %q failed: %v", in.AgentID, err)}, nil
	}

	return ToolResult{
		Output: response,
		Metadata: map[string]any{
			"agent_id": in.AgentID,
		},
	}, nil
}
