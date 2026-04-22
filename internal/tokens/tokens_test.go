package tokens

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sausheong/felix/internal/llm"
)

func TestEstimateBasic(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "hello world"},   // 11 chars
		{Role: "assistant", Content: "hi there"}, // 8 chars
	}
	got := Estimate(msgs, "", nil)
	// Total = 11 + 8 + len("user")*2 + len("assistant") + len("user") = 11 + 8 + 4 + 9 + 4 = 36
	// /4 = 9
	assert.GreaterOrEqual(t, got, 9)
	assert.LessOrEqual(t, got, 12)
}

func TestEstimateWithSystemPromptAndTools(t *testing.T) {
	msgs := []llm.Message{{Role: "user", Content: "hi"}}
	tools := []llm.ToolDef{
		{Name: "read_file", Description: "read a file", Parameters: []byte(`{"type":"object"}`)},
	}
	withoutSys := Estimate(msgs, "", nil)
	withSys := Estimate(msgs, "you are a helpful assistant", tools)
	assert.Greater(t, withSys, withoutSys, "system prompt + tools should bump estimate")
}

func TestContextWindowKnown(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"anthropic/claude-3-5-sonnet-20241022", 200000},
		{"anthropic/claude-3-opus-20240229", 200000},
		{"anthropic/claude-3-haiku-20240307", 200000},
		{"openai/gpt-4o", 128000},
		{"openai/gpt-4o-mini", 128000},
		{"openai/gpt-4-turbo", 128000},
		{"google/gemini-1.5-pro", 2000000},
		{"google/gemini-1.5-flash", 1000000},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			assert.Equal(t, tc.want, ContextWindow(tc.model))
		})
	}
}

func TestContextWindowUnknownReturnsConservativeFallback(t *testing.T) {
	assert.Equal(t, 32000, ContextWindow("weird/unknown-model"))
	assert.Equal(t, 32000, ContextWindow(""))
}

func TestContextWindowOllamaDefault(t *testing.T) {
	// Without RegisterOllamaContext call, ollama models fall back to a sane default
	assert.Equal(t, 32000, ContextWindow("local/qwen2.5:3b-instruct"))
}

func TestContextWindowOllamaRegistered(t *testing.T) {
	RegisterOllamaContext("qwen2.5:3b-instruct", 32768)
	defer ResetOllamaContexts()
	assert.Equal(t, 32768, ContextWindow("local/qwen2.5:3b-instruct"))
	assert.Equal(t, 32768, ContextWindow("ollama/qwen2.5:3b-instruct"))
}

func TestCalibratorStartsAtOne(t *testing.T) {
	c := NewCalibrator()
	assert.Equal(t, 100, c.Adjust(100))
}

func TestCalibratorConvergesTowardActual(t *testing.T) {
	c := NewCalibrator()
	// After 5 identical samples of (actual=150, estimated=100), the running mean
	// should reach exactly 1.5, so Adjust(100) returns 150.
	c.Update(150, 100)
	c.Update(150, 100)
	c.Update(150, 100)
	c.Update(150, 100)
	c.Update(150, 100)
	got := c.Adjust(100)
	assert.GreaterOrEqual(t, got, 148, "calibrator should learn ratio≈1.5")
	assert.LessOrEqual(t, got, 150)
}

func TestCalibratorIgnoresZeroOrNegative(t *testing.T) {
	c := NewCalibrator()
	c.Update(0, 100)
	c.Update(100, 0)
	c.Update(-5, 100)
	assert.Equal(t, 100, c.Adjust(100), "bad samples must be ignored")
}
