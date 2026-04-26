package llm_test

import (
	"context"
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

func TestQwenNormalizeToolSchemaStripsRef(t *testing.T) {
	p := llm.NewQwenProvider("fake-key", "")
	tools := []llm.ToolDef{{
		Name: "lookup",
		Parameters: json.RawMessage(`{
			"$ref": "#/x",
			"definitions": {"x": {"type": "string"}},
			"properties": {"q": {"type": "string"}}
		}`),
	}}
	out, diags := p.NormalizeToolSchema(tools)
	require.Len(t, out, 1)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(out[0].Parameters, &schema))
	_, hasRef := schema["$ref"]
	_, hasDefs := schema["definitions"]
	assert.False(t, hasRef, "$ref must be stripped")
	assert.False(t, hasDefs, "definitions must be stripped")
	require.Len(t, diags, 2)
	for _, d := range diags {
		assert.Equal(t, "lookup", d.ToolName)
		assert.Equal(t, "stripped", d.Action)
	}
}

func TestGeminiNormalizeToolSchemaStripsAll(t *testing.T) {
	ctx := context.Background()
	p, err := llm.NewGeminiProvider(ctx, "fake-key")
	if err != nil {
		t.Skipf("Gemini provider construction failed (no API stub available): %v", err)
	}
	tools := []llm.ToolDef{{
		Name: "fetch",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {"type": "string", "format": "uri"},
				"alt": {"anyOf": [{"type": "string"}, {"type": "null"}]},
				"choice": {"oneOf": [{"type": "string"}, {"type": "number"}]},
				"exclude": {"not": {"type": "boolean"}}
			},
			"$ref": "#/defs/x"
		}`),
	}}
	out, diags := p.NormalizeToolSchema(tools)
	require.Len(t, out, 1)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(out[0].Parameters, &schema))

	_, hasRef := schema["$ref"]
	assert.False(t, hasRef, "$ref must be stripped at root")

	props := schema["properties"].(map[string]any)
	url := props["url"].(map[string]any)
	_, hasFormat := url["format"]
	assert.False(t, hasFormat, "format must be stripped under properties.url")

	alt := props["alt"].(map[string]any)
	_, hasAnyOf := alt["anyOf"]
	assert.False(t, hasAnyOf, "anyOf must be stripped under properties.alt")

	choice := props["choice"].(map[string]any)
	_, hasOneOf := choice["oneOf"]
	assert.False(t, hasOneOf, "oneOf must be stripped under properties.choice")

	exclude := props["exclude"].(map[string]any)
	_, hasNot := exclude["not"]
	assert.False(t, hasNot, "not must be stripped under properties.exclude")

	// Five diagnostics expected: $ref, properties.alt.anyOf,
	// properties.choice.oneOf, properties.exclude.not, properties.url.format.
	require.Len(t, diags, 5)
	for _, d := range diags {
		assert.Equal(t, "fetch", d.ToolName)
		assert.Equal(t, "stripped", d.Action)
	}
}

func TestReasoningModeConstants(t *testing.T) {
	assert.Equal(t, llm.ReasoningMode(""), llm.ReasoningOff)
	assert.Equal(t, llm.ReasoningMode("low"), llm.ReasoningLow)
	assert.Equal(t, llm.ReasoningMode("medium"), llm.ReasoningMedium)
	assert.Equal(t, llm.ReasoningMode("high"), llm.ReasoningHigh)
}

func TestChatRequestReasoningZeroValue(t *testing.T) {
	// Existing call sites that don't set Reasoning must keep working —
	// zero value must equal ReasoningOff.
	var req llm.ChatRequest
	assert.Equal(t, llm.ReasoningOff, req.Reasoning)
}

func TestParseReasoningMode(t *testing.T) {
	cases := map[string]llm.ReasoningMode{
		"":       llm.ReasoningOff,
		"off":    llm.ReasoningOff,
		"low":    llm.ReasoningLow,
		"medium": llm.ReasoningMedium,
		"high":   llm.ReasoningHigh,
	}
	for in, want := range cases {
		got, err := llm.ParseReasoningMode(in)
		require.NoError(t, err, "input %q", in)
		assert.Equal(t, want, got, "input %q", in)
	}
	_, err := llm.ParseReasoningMode("ultra")
	assert.Error(t, err, "unknown level must error")
	_, err = llm.ParseReasoningMode("LOW")
	assert.Error(t, err, "case-sensitive: uppercase must error")
}

func TestAnthropicReasoningOff(t *testing.T) {
	p := llm.NewAnthropicProvider("fake", "")
	cfg, ok := p.BuildThinkingConfig("claude-sonnet-4-5", llm.ReasoningOff)
	assert.False(t, ok, "off → no thinking config")
	assert.Nil(t, cfg)
}

func TestAnthropicReasoningLevels(t *testing.T) {
	p := llm.NewAnthropicProvider("fake", "")
	cases := map[llm.ReasoningMode]int64{
		llm.ReasoningLow:    1024,
		llm.ReasoningMedium: 4096,
		llm.ReasoningHigh:   16384,
	}
	for mode, wantBudget := range cases {
		cfg, ok := p.BuildThinkingConfig("claude-sonnet-4-5", mode)
		require.True(t, ok, "mode %s should produce config", mode)
		require.NotNil(t, cfg)
		assert.Equal(t, wantBudget, cfg.BudgetTokens, "mode %s budget", mode)
	}
}

func TestAnthropicReasoningUnsupportedModel(t *testing.T) {
	p := llm.NewAnthropicProvider("fake", "")
	cfg, ok := p.BuildThinkingConfig("claude-3-haiku-20240307", llm.ReasoningHigh)
	assert.False(t, ok, "haiku-3 doesn't support thinking; mode should be ignored")
	assert.Nil(t, cfg)
}

func TestAnthropicReasoningUnknownModelDefaultsSupported(t *testing.T) {
	p := llm.NewAnthropicProvider("fake", "")
	cfg, ok := p.BuildThinkingConfig("claude-future-model-vNEW", llm.ReasoningMedium)
	assert.True(t, ok, "unknown models default to supported (let API decide)")
	require.NotNil(t, cfg)
	assert.Equal(t, int64(4096), cfg.BudgetTokens)
}

func TestOpenAIReasoningOff(t *testing.T) {
	p := llm.NewOpenAIProvider("fake", "")
	effort, ok := p.BuildReasoningEffort("o3-mini", llm.ReasoningOff)
	assert.False(t, ok)
	assert.Empty(t, effort)
}

func TestOpenAIReasoningLevels(t *testing.T) {
	p := llm.NewOpenAIProvider("fake", "")
	cases := map[llm.ReasoningMode]string{
		llm.ReasoningLow:    "low",
		llm.ReasoningMedium: "medium",
		llm.ReasoningHigh:   "high",
	}
	for mode, want := range cases {
		effort, ok := p.BuildReasoningEffort("o3-mini", mode)
		require.True(t, ok, "mode %s", mode)
		assert.Equal(t, want, effort)
	}
}

func TestOpenAIReasoningUnsupportedModel(t *testing.T) {
	p := llm.NewOpenAIProvider("fake", "")
	effort, ok := p.BuildReasoningEffort("gpt-4o", llm.ReasoningHigh)
	assert.False(t, ok, "gpt-4o does not support reasoning_effort")
	assert.Empty(t, effort)
}

func TestOpenAICompatibleSuppressesReasoning(t *testing.T) {
	p := llm.NewOpenAIProviderWithKind("", "http://localhost:11434/v1", "openai-compatible")
	_, ok := p.BuildReasoningEffort("gpt-5-thinking", llm.ReasoningHigh)
	assert.False(t, ok, "openai-compatible kind suppresses reasoning")
}

func TestOpenAILocalKindSuppressesReasoning(t *testing.T) {
	p := llm.NewOpenAIProviderWithKind("", "http://localhost:11434/v1", "local")
	_, ok := p.BuildReasoningEffort("gpt-5-thinking", llm.ReasoningHigh)
	assert.False(t, ok, "local kind suppresses reasoning")
}

func TestOpenAIDefaultConstructorIsOpenAIKind(t *testing.T) {
	// NewOpenAIProvider (no Kind) should behave as kind=openai.
	p := llm.NewOpenAIProvider("fake", "")
	_, ok := p.BuildReasoningEffort("o3-mini", llm.ReasoningLow)
	assert.True(t, ok, "default constructor must support reasoning for o-series models")
}

func TestGeminiReasoningOff(t *testing.T) {
	p, err := llm.NewGeminiProvider(context.Background(), "fake")
	require.NoError(t, err, "Gemini construction should succeed offline with fake key")
	budget, ok := p.BuildThinkingBudget("gemini-2.5-pro", llm.ReasoningOff)
	assert.False(t, ok)
	assert.Equal(t, int32(0), budget)
}

func TestGeminiReasoningLevels(t *testing.T) {
	p, err := llm.NewGeminiProvider(context.Background(), "fake")
	require.NoError(t, err)
	cases := map[llm.ReasoningMode]int32{
		llm.ReasoningLow:    1024,
		llm.ReasoningMedium: 4096,
		llm.ReasoningHigh:   16384,
	}
	for mode, want := range cases {
		budget, ok := p.BuildThinkingBudget("gemini-2.5-pro", mode)
		require.True(t, ok, "mode %s", mode)
		assert.Equal(t, want, budget)
	}
}

func TestGeminiReasoningUnsupportedModel(t *testing.T) {
	p, err := llm.NewGeminiProvider(context.Background(), "fake")
	require.NoError(t, err)
	_, ok := p.BuildThinkingBudget("gemini-1.5-flash", llm.ReasoningHigh)
	assert.False(t, ok, "gemini-1.5-flash does not support thinking")
}

func TestGeminiReasoningSupportsThinkingFamilies(t *testing.T) {
	p, err := llm.NewGeminiProvider(context.Background(), "fake")
	require.NoError(t, err)
	// Both 2.0-flash-thinking and 2.5 are supported.
	for _, model := range []string{
		"gemini-2.0-flash-thinking-exp-1219",
		"gemini-2.5-pro",
		"gemini-2.5-flash",
	} {
		_, ok := p.BuildThinkingBudget(model, llm.ReasoningMedium)
		assert.True(t, ok, "model %s should support thinking", model)
	}
}
