package cortex

import (
	"testing"

	"github.com/sausheong/cortex/connector/conversation"
	"github.com/stretchr/testify/assert"
)

// msgs is a helper that builds a []conversation.Message from alternating role/content pairs.
func msgs(pairs ...string) []conversation.Message {
	out := make([]conversation.Message, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, conversation.Message{Role: pairs[i], Content: pairs[i+1]})
	}
	return out
}

func TestShouldIngestNilAndEmpty(t *testing.T) {
	assert.False(t, ShouldIngest(nil))
	assert.False(t, ShouldIngest([]conversation.Message{}))
}

func TestShouldIngestTrivialUserMessage(t *testing.T) {
	thread := msgs("user", "ok", "assistant", "Understood, got it, no problem at all.")
	assert.False(t, ShouldIngest(thread))
}

func TestShouldIngestTooShort(t *testing.T) {
	thread := msgs("user", "hi there", "assistant", "Hello!")
	assert.False(t, ShouldIngest(thread))
}

func TestShouldIngestNoAssistantMessage(t *testing.T) {
	// Only a user message — no assistant reply yet
	thread := msgs("user", "What are the main principles of software architecture and design patterns?")
	assert.False(t, ShouldIngest(thread))
}

func TestShouldIngestValidTwoMessage(t *testing.T) {
	thread := msgs(
		"user", "What are the main principles of clean code architecture?",
		"assistant", "Clean code follows separation of concerns, single responsibility, and dependency inversion.",
	)
	assert.True(t, ShouldIngest(thread))
}

func TestShouldIngestValidWithToolCalls(t *testing.T) {
	thread := msgs(
		"user", "What files are in the project?",
		"assistant", "[tool: bash]\n{\"command\":\"ls -la\"}",
		"user", "main.go\ngo.mod\nREADME.md\ninternal/\ncmd/",
		"assistant", "The project contains main.go, go.mod, README.md, and the internal/ and cmd/ directories.",
	)
	assert.True(t, ShouldIngest(thread))
}

func TestShouldIngestTrivialCaseInsensitive(t *testing.T) {
	thread := msgs("user", "THANKS", "assistant", "You are welcome! Glad I could help with that.")
	assert.False(t, ShouldIngest(thread))
}
