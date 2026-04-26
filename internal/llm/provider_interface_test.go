package llm_test

import (
	"encoding/json"
	"testing"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/llm/llmtest"
	"github.com/stretchr/testify/assert"
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
