package llm_test

import (
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
