package llm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeminiResolveSystemPromptPrefersParts(t *testing.T) {
	got := geminiResolveSystemPrompt(ChatRequest{
		SystemPrompt:      "legacy",
		SystemPromptParts: []SystemPromptPart{{Text: "alpha"}, {Text: "beta"}},
	})
	require.Equal(t, "alpha\nbeta", got)
}

func TestGeminiResolveSystemPromptFallsBackToString(t *testing.T) {
	got := geminiResolveSystemPrompt(ChatRequest{SystemPrompt: "only-string"})
	require.Equal(t, "only-string", got)
}

func TestGeminiResolveSystemPromptEmpty(t *testing.T) {
	got := geminiResolveSystemPrompt(ChatRequest{})
	require.Equal(t, "", got)
}
