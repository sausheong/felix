package cortextools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sausheong/cortex"
	"github.com/sausheong/harness/tool"
)

// FindEntitiesTool looks up entities in the knowledge graph by name, type,
// or source.
type FindEntitiesTool struct {
	cx *cortex.Cortex
}

// Name returns "find_entities".
func (t *FindEntitiesTool) Name() string { return "find_entities" }

// Description is shown to the model in the tool list.
func (t *FindEntitiesTool) Description() string {
	return "Look up entities in the knowledge graph by name (substring) or type. " +
		"Use when the user mentions a specific person, project, organization, " +
		"or concept and you want to surface what cortex already knows about it."
}

// Parameters returns the JSON-Schema for the tool input.
func (t *FindEntitiesTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "Filter by entity name (substring match)."
			},
			"type": {
				"type": "string",
				"description": "Filter by entity type (e.g. 'person', 'project', 'organization', 'concept')."
			},
			"source": {
				"type": "string",
				"description": "Filter by source tag (e.g. 'user', 'agent')."
			},
			"limit": {
				"type": "integer",
				"description": "Max results returned to the caller. Default 10."
			}
		}
	}`)
}

// IsConcurrencySafe returns true — find_entities is a read-only query.
func (t *FindEntitiesTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

type findEntitiesInput struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Source string `json:"source"`
	Limit  int    `json:"limit"`
}

// Execute runs FindEntities and renders the results.
func (t *FindEntitiesTool) Execute(ctx context.Context, input json.RawMessage) (tool.ToolResult, error) {
	var in findEntitiesInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.ToolResult{Output: fmt.Sprintf("error: invalid input: %v", err)}, nil
	}

	filter := cortex.EntityFilter{
		Type:     in.Type,
		NameLike: in.Name,
		Source:   in.Source,
	}

	entities, err := t.cx.FindEntities(ctx, filter)
	if err != nil {
		return tool.ToolResult{Output: fmt.Sprintf("error: %s", err.Error())}, nil
	}
	if len(entities) == 0 {
		return tool.ToolResult{Output: "No entities found."}, nil
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	if len(entities) > limit {
		entities = entities[:limit]
	}
	return tool.ToolResult{Output: formatEntities(entities)}, nil
}

// Compile-time assertion that *FindEntitiesTool satisfies tool.Tool.
var _ tool.Tool = (*FindEntitiesTool)(nil)
