package llm_test

import (
	"encoding/json"
	"testing"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/llm/llmtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLLMProviderInterfaceHasNormalizeToolSchema(t *testing.T) {
	// Compile-time: any type implementing LLMProvider must have
	// NormalizeToolSchema. Stub embeds Base so this is provided.
	var _ llm.LLMProvider = &llmtest.Stub{}
	s := &llmtest.Stub{}
	tools := []llm.ToolDef{{Name: "x", Description: "y", Parameters: []byte(`{}`)}}
	out, diags := s.NormalizeToolSchema(tools)
	assert.Equal(t, tools, out)
	assert.Nil(t, diags)
}

func TestDiagnosticFields(t *testing.T) {
	d := llm.Diagnostic{
		ToolName: "read_file",
		Field:    "properties.url.format",
		Action:   "stripped",
		Reason:   "gemini does not support format",
	}
	assert.Equal(t, "read_file", d.ToolName)
	assert.Equal(t, "stripped", d.Action)
}

func TestAnthropicNormalizeToolSchemaIsIdentity(t *testing.T) {
	p := llm.NewAnthropicProvider("fake-key", "")
	tools := []llm.ToolDef{
		{
			Name: "complex",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {"type": "string", "format": "uri"},
					"items": {"oneOf": [{"type": "string"}, {"type": "number"}]}
				},
				"$ref": "#/defs/x",
				"definitions": {"x": {"type": "string"}}
			}`),
		},
	}
	out, diags := p.NormalizeToolSchema(tools)
	assert.Equal(t, tools, out, "Anthropic accepts full draft-7; nothing stripped")
	assert.Empty(t, diags)
}

func TestOpenAINormalizeToolSchemaStripsRef(t *testing.T) {
	p := llm.NewOpenAIProvider("fake-key", "")
	tools := []llm.ToolDef{
		{
			Name: "lookup",
			Parameters: json.RawMessage(`{
				"type": "object",
				"$ref": "#/defs/foo",
				"definitions": {"foo": {"type": "string"}},
				"properties": {"q": {"type": "string"}}
			}`),
		},
	}
	out, diags := p.NormalizeToolSchema(tools)
	require.Len(t, out, 1)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(out[0].Parameters, &schema))
	_, hasRef := schema["$ref"]
	_, hasDefs := schema["definitions"]
	assert.False(t, hasRef, "$ref must be stripped")
	assert.False(t, hasDefs, "definitions must be stripped")

	require.GreaterOrEqual(t, len(diags), 2, "expected diagnostics for $ref and definitions")
	fields := make([]string, len(diags))
	for i, d := range diags {
		fields[i] = d.Field
	}
	assert.Contains(t, fields, "$ref")
	assert.Contains(t, fields, "definitions")
	for _, d := range diags {
		assert.Equal(t, "lookup", d.ToolName)
		assert.Equal(t, "stripped", d.Action)
	}
}

func TestOpenAINormalizeKeepsAnyOf(t *testing.T) {
	p := llm.NewOpenAIProvider("fake-key", "")
	tools := []llm.ToolDef{{
		Name: "x",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"v": {"anyOf": [{"type": "string"}, {"type": "number"}]},
				"u": {"oneOf": [{"type": "string"}, {"type": "null"}]},
				"f": {"type": "string", "format": "uri"}
			}
		}`),
	}}
	out, diags := p.NormalizeToolSchema(tools)
	assert.Empty(t, diags, "OpenAI accepts anyOf/oneOf/format; nothing should be stripped")

	// Verify structure preserved.
	var inDoc, outDoc any
	require.NoError(t, json.Unmarshal(tools[0].Parameters, &inDoc))
	require.NoError(t, json.Unmarshal(out[0].Parameters, &outDoc))
	assert.Equal(t, inDoc, outDoc, "structure must be unchanged")
}
