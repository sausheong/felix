package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProviderModel(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantProvider string
		wantModel    string
	}{
		{
			name:         "provider and model",
			input:        "anthropic/claude-sonnet",
			wantProvider: "anthropic",
			wantModel:    "claude-sonnet",
		},
		{
			name:         "model only",
			input:        "model-only",
			wantProvider: "",
			wantModel:    "model-only",
		},
		{
			name:         "empty string",
			input:        "",
			wantProvider: "",
			wantModel:    "",
		},
		{
			name:         "multiple slashes",
			input:        "openai/gpt-4/turbo",
			wantProvider: "openai",
			wantModel:    "gpt-4/turbo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, model := ParseProviderModel(tt.input)
			assert.Equal(t, tt.wantProvider, provider)
			assert.Equal(t, tt.wantModel, model)
		})
	}
}

func TestNewProviderAnthropic(t *testing.T) {
	p, err := NewProvider("anthropic", ProviderOptions{APIKey: "test-key"})
	require.NoError(t, err)
	assert.NotNil(t, p)
	_, ok := p.(*AnthropicProvider)
	assert.True(t, ok, "expected *AnthropicProvider")
}

func TestNewProviderOpenAI(t *testing.T) {
	p, err := NewProvider("openai", ProviderOptions{APIKey: "test-key"})
	require.NoError(t, err)
	assert.NotNil(t, p)
	_, ok := p.(*OpenAIProvider)
	assert.True(t, ok, "expected *OpenAIProvider")
}

func TestNewProviderOpenAICompatible(t *testing.T) {
	p, err := NewProvider("openai-compatible", ProviderOptions{
		APIKey:  "test-key",
		BaseURL: "http://localhost:8080/v1",
		Kind:    "openai-compatible",
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	_, ok := p.(*OpenAIProvider)
	assert.True(t, ok, "expected *OpenAIProvider for openai-compatible")
}

func TestNewProviderUnknown(t *testing.T) {
	p, err := NewProvider("unknown-provider", ProviderOptions{})
	assert.Nil(t, p)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown LLM provider kind")
}

func TestNewProviderBaseURLDefault(t *testing.T) {
	// When kind is empty but base URL is set, should default to openai-compatible
	p, err := NewProvider("anything", ProviderOptions{
		APIKey:  "test-key",
		BaseURL: "http://localhost:11434/v1",
	})
	require.NoError(t, err)
	assert.NotNil(t, p)
	_, ok := p.(*OpenAIProvider)
	assert.True(t, ok, "expected *OpenAIProvider when base URL is set")
}

func TestAnthropicProviderModels(t *testing.T) {
	p := NewAnthropicProvider("test-key", "")
	models := p.Models()
	assert.NotEmpty(t, models)
	for _, m := range models {
		assert.NotEmpty(t, m.ID)
		assert.NotEmpty(t, m.Name)
		assert.Equal(t, "anthropic", m.Provider)
	}
}

func TestOpenAIProviderModels(t *testing.T) {
	p := NewOpenAIProvider("test-key", "")
	models := p.Models()
	assert.NotEmpty(t, models)
	for _, m := range models {
		assert.NotEmpty(t, m.ID)
		assert.NotEmpty(t, m.Name)
		assert.Equal(t, "openai", m.Provider)
	}
}

func TestNewProviderGemini(t *testing.T) {
	p, err := NewProvider("gemini", ProviderOptions{APIKey: "test-key", Kind: "gemini"})
	require.NoError(t, err)
	assert.NotNil(t, p)
	_, ok := p.(*GeminiProvider)
	assert.True(t, ok, "expected *GeminiProvider")
}

func TestGeminiProviderModels(t *testing.T) {
	p, err := NewGeminiProvider(context.Background(), "test-key")
	require.NoError(t, err)
	models := p.Models()
	assert.NotEmpty(t, models)
	for _, m := range models {
		assert.NotEmpty(t, m.ID)
		assert.NotEmpty(t, m.Name)
		assert.Equal(t, "gemini", m.Provider)
	}
}

func TestNewProviderLocal(t *testing.T) {
	p, err := NewProvider("local", ProviderOptions{
		Kind:    "local",
		BaseURL: "http://127.0.0.1:18790/v1",
	})
	require.NoError(t, err)
	require.NotNil(t, p)
}

func TestOpenAIProviderRequestsUsageStats(t *testing.T) {
	var seenBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		// Minimal SSE stream: one delta + DONE.
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":7,\"total_tokens\":49}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAIProvider("test-key", srv.URL)
	stream, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	var sawUsage bool
	for ev := range stream {
		if ev.Type == EventDone && ev.Usage != nil {
			sawUsage = true
			assert.Equal(t, 42, ev.Usage.InputTokens)
			assert.Equal(t, 7, ev.Usage.OutputTokens)
		}
	}
	assert.True(t, sawUsage, "EventDone must carry Usage when provider returns it")

	// And the outgoing request must have asked for usage stats.
	assert.Contains(t, string(seenBody), `"include_usage":true`)
}

func TestConcatSystemPromptParts(t *testing.T) {
	cases := []struct {
		name string
		in   []SystemPromptPart
		want string
	}{
		{"nil", nil, ""},
		{"empty slice", []SystemPromptPart{}, ""},
		{"single", []SystemPromptPart{{Text: "A"}}, "A"},
		{"two", []SystemPromptPart{{Text: "A"}, {Text: "B"}}, "A\nB"},
		{"skips empty", []SystemPromptPart{{Text: "A"}, {Text: ""}, {Text: "B"}}, "A\nB"},
		{"cache flag ignored by concat", []SystemPromptPart{{Text: "A", Cache: true}, {Text: "B", Cache: false}}, "A\nB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := concatSystemPromptParts(tc.in)
			require.Equal(t, tc.want, got)
		})
	}
}
