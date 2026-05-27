package cortextools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sausheong/cortex"
	"github.com/sausheong/harness/tool"
)

// GetRelationshipsTool returns edges connected to a given entity in the
// knowledge graph.
type GetRelationshipsTool struct {
	cx *cortex.Cortex
}

// Name returns "get_relationships".
func (t *GetRelationshipsTool) Name() string { return "get_relationships" }

// Description is shown to the model in the tool list.
func (t *GetRelationshipsTool) Description() string {
	return "Get edges connected to an entity in the knowledge graph. " +
		"Use after find_entities to explore how an entity is connected to " +
		"other people, projects, or concepts."
}

// Parameters returns the JSON-Schema for the tool input.
func (t *GetRelationshipsTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"entity_id": {
				"type": "string",
				"description": "Entity ID from find_entities or a previous recall."
			},
			"direction": {
				"type": "string",
				"enum": ["in", "out", "both"],
				"description": "Direction of edges relative to entity_id: 'out' for edges where entity_id is the source, 'in' for edges where entity_id is the target, 'both' for either. Default 'both'."
			},
			"type": {
				"type": "string",
				"description": "Optional relationship type filter (e.g. 'works_for', 'authored', 'depends_on')."
			}
		},
		"required": ["entity_id"]
	}`)
}

// IsConcurrencySafe returns true — get_relationships is a read-only query.
func (t *GetRelationshipsTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

type getRelationshipsInput struct {
	EntityID  string `json:"entity_id"`
	Direction string `json:"direction"`
	Type      string `json:"type"`
}

// Execute calls cortex.GetRelationships and renders the results.
//
// Direction filtering is applied client-side because the cortex RelFilter
// API only supports type filters; GetRelationships itself returns edges
// where entity_id appears as either source OR target.
func (t *GetRelationshipsTool) Execute(ctx context.Context, input json.RawMessage) (tool.ToolResult, error) {
	var in getRelationshipsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return tool.ToolResult{Output: fmt.Sprintf("error: invalid input: %v", err)}, nil
	}
	if in.EntityID == "" {
		return tool.ToolResult{Output: "error: 'entity_id' is required"}, nil
	}
	direction := in.Direction
	if direction == "" {
		direction = "both"
	}

	var filters []cortex.RelFilter
	if in.Type != "" {
		filters = append(filters, cortex.RelTypeFilter(in.Type))
	}

	rels, err := t.cx.GetRelationships(ctx, in.EntityID, filters...)
	if err != nil {
		return tool.ToolResult{Output: fmt.Sprintf("error: %s", err.Error())}, nil
	}

	if direction != "both" {
		filtered := rels[:0]
		for _, r := range rels {
			switch direction {
			case "out":
				if r.SourceID == in.EntityID {
					filtered = append(filtered, r)
				}
			case "in":
				if r.TargetID == in.EntityID {
					filtered = append(filtered, r)
				}
			}
		}
		rels = filtered
	}

	if len(rels) == 0 {
		return tool.ToolResult{Output: fmt.Sprintf("No relationships found for %s.", in.EntityID)}, nil
	}
	return tool.ToolResult{Output: formatRelationships(ctx, t.cx, rels)}, nil
}

// Compile-time assertion that *GetRelationshipsTool satisfies tool.Tool.
var _ tool.Tool = (*GetRelationshipsTool)(nil)
