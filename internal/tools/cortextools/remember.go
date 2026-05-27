package cortextools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sausheong/cortex"
	"github.com/sausheong/harness/tool"
)

// RememberTool stores a fact, preference, decision, or note in the knowledge
// graph for future recall.
type RememberTool struct {
	cx *cortex.Cortex
}

// Name returns "remember".
func (t *RememberTool) Name() string { return "remember" }

// Description is shown to the model in the tool list.
func (t *RememberTool) Description() string {
	return "Save a fact, preference, decision, or note to the knowledge graph " +
		"for future recall. Cortex will extract entities and relationships from " +
		"the content automatically. Use when the user shares information worth " +
		"remembering across conversations — preferences, decisions, project " +
		"context, biographical facts."
}

// Parameters returns the JSON-Schema for the tool input.
func (t *RememberTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"content": {
				"type": "string",
				"description": "The fact, preference, or note to remember. Phrase it as a standalone statement (e.g. 'User prefers Go over Python for backend work')."
			},
			"source": {
				"type": "string",
				"description": "Optional source tag (default: 'agent'). Use to distinguish facts told by the user vs. inferred from context."
			}
		},
		"required": ["content"]
	}`)
}

// IsConcurrencySafe returns false — remember mutates the knowledge graph.
func (t *RememberTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

type rememberInput struct {
	Content string `json:"content"`
	Source  string `json:"source"`
}

// Execute writes the content to the knowledge graph via cortex.Remember.
func (t *RememberTool) Execute(ctx context.Context, input json.RawMessage) (tool.ToolResult, error) {
	var in rememberInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.ToolResult{Output: fmt.Sprintf("error: invalid input: %v", err)}, nil
	}
	if in.Content == "" {
		return tool.ToolResult{Output: "error: 'content' is required"}, nil
	}
	source := in.Source
	if source == "" {
		source = "agent"
	}

	if err := t.cx.Remember(ctx, in.Content, cortex.WithSource(source)); err != nil {
		return tool.ToolResult{Output: fmt.Sprintf("error: %s", err.Error())}, nil
	}
	return tool.ToolResult{Output: "Remembered."}, nil
}

// Compile-time assertion that *RememberTool satisfies tool.Tool.
var _ tool.Tool = (*RememberTool)(nil)
