package llm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQwenResolveSystemPromptPrefersParts(t *testing.T) {
	got := qwenResolveSystemPrompt(ChatRequest{
		SystemPrompt:      "legacy",
		SystemPromptParts: []SystemPromptPart{{Text: "new-a"}, {Text: "new-b"}},
	})
	require.Equal(t, "new-a\nnew-b", got)
}

func TestQwenResolveSystemPromptFallback(t *testing.T) {
	got := qwenResolveSystemPrompt(ChatRequest{SystemPrompt: "legacy"})
	require.Equal(t, "legacy", got)
}

func TestQwenResolveSystemPromptEmpty(t *testing.T) {
	got := qwenResolveSystemPrompt(ChatRequest{})
	require.Equal(t, "", got)
}
