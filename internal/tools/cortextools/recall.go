package cortextools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sausheong/cortex"
	"github.com/sausheong/harness/tool"
)

// RecallTool searches the knowledge graph for context relevant to a query.
type RecallTool struct {
	cx *cortex.Cortex
}

// Name returns "recall".
func (t *RecallTool) Name() string { return "recall" }

// Description is shown to the model in the tool list.
func (t *RecallTool) Description() string {
	return "Search the knowledge graph for context relevant to a query. " +
		"Returns entities, memories, and document chunks ranked by relevance. " +
		"Use at the start of a conversation, or whenever you need to check what " +
		"you already know about a person, project, or topic before asking the user."
}

// Parameters returns the JSON-Schema for the tool input.
func (t *RecallTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Natural-language search query — keywords or a short phrase."
			},
			"limit": {
				"type": "integer",
				"description": "Max number of results. Default 5."
			},
			"min_confidence": {
				"type": "number",
				"description": "Filter out results with confidence below this (0.0–1.0). Omit to include all."
			}
		},
		"required": ["query"]
	}`)
}

// IsConcurrencySafe returns true — recall is a read-only query.
func (t *RecallTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

type recallInput struct {
	Query         string  `json:"query"`
	Limit         int     `json:"limit"`
	MinConfidence float64 `json:"min_confidence"`
}

// Execute runs a cortex Recall and renders the results.
func (t *RecallTool) Execute(ctx context.Context, input json.RawMessage) (tool.ToolResult, error) {
	var in recallInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.ToolResult{Output: fmt.Sprintf("error: invalid input: %v", err)}, nil
	}
	if in.Query == "" {
		return tool.ToolResult{Output: "error: 'query' is required"}, nil
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 5
	}

	opts := []cortex.RecallOption{cortex.WithLimit(limit)}
	if in.MinConfidence > 0 {
		opts = append(opts, cortex.WithMinConfidence(in.MinConfidence))
	}

	results, err := t.cx.Recall(ctx, in.Query, opts...)
	if err != nil {
		return tool.ToolResult{Output: fmt.Sprintf("error: %s", err.Error())}, nil
	}
	if len(results) == 0 {
		return tool.ToolResult{Output: "No results."}, nil
	}
	return tool.ToolResult{Output: formatRecallResults(results)}, nil
}

// Compile-time assertion that *RecallTool satisfies tool.Tool.
var _ tool.Tool = (*RecallTool)(nil)
