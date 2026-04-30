package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	openai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
)

// captureOpenAIRequest points an OpenAI provider at an httptest server,
// drives one ChatStream call, and returns the captured request body.
func captureOpenAIRequest(t *testing.T, req ChatRequest) *openai.ChatCompletionRequest {
	t.Helper()
	var captured openai.ChatCompletionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	p := NewOpenAIProviderWithKind("test-key", srv.URL+"/v1", "openai-compatible")
	stream, err := p.ChatStream(context.Background(), req)
	require.NoError(t, err)
	for range stream {
	}
	return &captured
}

func TestOpenAIChatStreamUsesSystemPromptParts(t *testing.T) {
	captured := captureOpenAIRequest(t, ChatRequest{
		SystemPromptParts: []SystemPromptPart{
			{Text: "static"},
			{Text: "dynamic"},
		},
	})
	require.NotNil(t, captured)
	require.GreaterOrEqual(t, len(captured.Messages), 1)
	require.Equal(t, "system", string(captured.Messages[0].Role))
	require.Equal(t, "static\ndynamic", captured.Messages[0].Content)
}

func TestOpenAIChatStreamFallsBackToSystemPromptString(t *testing.T) {
	captured := captureOpenAIRequest(t, ChatRequest{
		SystemPrompt: "legacy",
	})
	require.NotNil(t, captured)
	require.GreaterOrEqual(t, len(captured.Messages), 1)
	require.Equal(t, "legacy", captured.Messages[0].Content)
}

func TestOpenAIChatStreamPartsBeatString(t *testing.T) {
	captured := captureOpenAIRequest(t, ChatRequest{
		SystemPrompt:      "legacy",
		SystemPromptParts: []SystemPromptPart{{Text: "new"}},
	})
	require.Equal(t, "new", captured.Messages[0].Content)
}
