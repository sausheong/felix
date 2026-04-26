package llm

import (
	"encoding/json"
	"sort"
)

// applyStripList runs StripFields over each tool's Parameters with the
// given field-strip list, preserving input order and accumulating
// diagnostics across all tools. The per-provider NormalizeToolSchema
// methods (OpenAI, Qwen, Gemini) are thin wrappers over this helper —
// only the strip list differs. Anthropic doesn't use this (identity).
func applyStripList(tools []ToolDef, fields []string) ([]ToolDef, []Diagnostic) {
	out := make([]ToolDef, len(tools))
	var allDiags []Diagnostic
	for i, t := range tools {
		newParams, diags := StripFields(t.Name, t.Parameters, fields)
		td := t
		td.Parameters = newParams
		out[i] = td
		allDiags = append(allDiags, diags...)
	}
	return out, allDiags
}

// StripFields removes the given field names from a JSON Schema document
// recursively. It descends into "properties.*", "items", and
// "additionalProperties" (the standard JSON Schema schema-bearing
// positions). Returns the rewritten schema as JSON bytes and one
// Diagnostic per stripped occurrence (ToolName set, Field set to the
// dotted JSON path).
//
// Determinism: the walker visits map keys in sorted order so output
// and diagnostics are reproducible across calls. Required for prompt
// cache stability — the agent runtime calls NormalizeToolSchema once
// per turn, and any non-determinism here would invalidate the cache.
//
// Malformed schemas are returned unchanged with no diagnostics; the
// provider SDK will produce its own error.
func StripFields(toolName string, schema json.RawMessage, fields []string) (json.RawMessage, []Diagnostic) {
	if len(fields) == 0 || len(schema) == 0 {
		return schema, nil
	}
	stripSet := make(map[string]bool, len(fields))
	for _, f := range fields {
		stripSet[f] = true
	}
	var doc any
	if err := json.Unmarshal(schema, &doc); err != nil {
		return schema, nil
	}
	var diags []Diagnostic
	stripped := walkStrip(doc, "", toolName, stripSet, &diags)
	out, err := json.Marshal(stripped)
	if err != nil {
		return schema, nil
	}
	return out, diags
}

// walkStrip is the recursive worker. Returns the (possibly rewritten)
// node and appends any diagnostics for stripped fields.
func walkStrip(node any, path string, toolName string, stripSet map[string]bool, diags *[]Diagnostic) any {
	m, ok := node.(map[string]any)
	if !ok {
		return node
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fieldPath := joinPath(path, k)
		if stripSet[k] {
			*diags = append(*diags, Diagnostic{
				ToolName: toolName,
				Field:    fieldPath,
				Action:   "stripped",
				Reason:   "field not supported by provider",
			})
			delete(m, k)
			continue
		}
		switch k {
		case "properties":
			if props, ok := m[k].(map[string]any); ok {
				propKeys := make([]string, 0, len(props))
				for pk := range props {
					propKeys = append(propKeys, pk)
				}
				sort.Strings(propKeys)
				for _, pk := range propKeys {
					props[pk] = walkStrip(props[pk], joinPath(fieldPath, pk), toolName, stripSet, diags)
				}
			}
		case "items", "additionalProperties":
			m[k] = walkStrip(m[k], fieldPath, toolName, stripSet, diags)
		}
	}
	return m
}

func joinPath(base, leaf string) string {
	if base == "" {
		return leaf
	}
	return base + "." + leaf
}
