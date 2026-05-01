package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// LoadSkillTool fetches a skill body by name. Decoupled from the skill
// package via the Lookup func — keeps internal/tools free of skill/memory
// imports. Caller wires Lookup at construction (typically in
// agent.BuildRuntimeForAgent where the *skill.Loader is already in scope).
type LoadSkillTool struct {
	// Lookup returns (body, true) if a skill with the given name exists,
	// (_, false) otherwise. Implementations should match by exact name —
	// fuzzy matching belongs to the agent index, not the loader.
	Lookup func(name string) (body string, ok bool)
}

func (t *LoadSkillTool) Name() string { return "load_skill" }

func (t *LoadSkillTool) Description() string {
	return "Load the full body of a skill by name. Use after consulting the Skills Index to read a skill's instructions; the body is returned as the tool output and becomes available in your next response. Pass the exact skill name from the index."
}

func (t *LoadSkillTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "Exact skill name as listed in the Skills Index"
			}
		},
		"required": ["name"]
	}`)
}

func (t *LoadSkillTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *LoadSkillTool) Execute(_ context.Context, input json.RawMessage) (ToolResult, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if in.Name == "" {
		return ToolResult{Error: "name is required"}, nil
	}
	if t.Lookup == nil {
		return ToolResult{Error: "no skill loader configured"}, nil
	}
	body, ok := t.Lookup(in.Name)
	if !ok {
		return ToolResult{Error: fmt.Sprintf("skill not found: %q", in.Name)}, nil
	}
	if body == "" {
		// Found but empty body — surface explicitly so the agent doesn't
		// loop trying to load a placeholder skill.
		return ToolResult{Output: fmt.Sprintf("(skill %q loaded but has no body content)", in.Name)}, nil
	}
	return ToolResult{Output: body}, nil
}

// LoadMemoryTool fetches a memory entry body by id. Same decoupling
// pattern as LoadSkillTool.
type LoadMemoryTool struct {
	Lookup func(id string) (body string, ok bool)
}

func (t *LoadMemoryTool) Name() string { return "load_memory" }

func (t *LoadMemoryTool) Description() string {
	return "Load the full body of a memory entry by id. Use after consulting the Memory Index to read an entry's content; the body is returned as the tool output. Pass the exact id from the index."
}

func (t *LoadMemoryTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {
				"type": "string",
				"description": "Exact memory entry id as listed in the Memory Index"
			}
		},
		"required": ["id"]
	}`)
}

func (t *LoadMemoryTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *LoadMemoryTool) Execute(_ context.Context, input json.RawMessage) (ToolResult, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{Error: fmt.Sprintf("invalid input: %v", err)}, nil
	}
	if in.ID == "" {
		return ToolResult{Error: "id is required"}, nil
	}
	if t.Lookup == nil {
		return ToolResult{Error: "no memory manager configured"}, nil
	}
	body, ok := t.Lookup(in.ID)
	if !ok {
		return ToolResult{Error: fmt.Sprintf("memory entry not found: %q", in.ID)}, nil
	}
	if body == "" {
		return ToolResult{Output: fmt.Sprintf("(memory entry %q loaded but is empty)", in.ID)}, nil
	}
	return ToolResult{Output: body}, nil
}
