package cortex

import (
	"testing"

	"github.com/sausheong/cortex/connector/conversation"
	"github.com/sausheong/felix/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestResolveCortexModelMirrorsAgentWhenEmpty(t *testing.T) {
	cfg := config.CortexConfig{Enabled: true} // Provider and LLMModel both empty
	provider, model := resolveCortexModel(cfg, "local/gemma4:latest")
	if provider != "local" {
		t.Errorf("auto-mirror provider = %q, want \"local\"", provider)
	}
	if model != "gemma4:latest" {
		t.Errorf("auto-mirror model = %q, want \"gemma4:latest\"", model)
	}
}

func TestResolveCortexModelMirrorsChatAgentNotDefault(t *testing.T) {
	// Same empty config but a different chat-agent model — cortex follows
	// whoever's actually chatting, not a single startup-time choice.
	cfg := config.CortexConfig{Enabled: true}
	provider, model := resolveCortexModel(cfg, "anthropic/claude-sonnet-4-6")
	if provider != "anthropic" || model != "claude-sonnet-4-6" {
		t.Errorf("expected mirror of chat agent; got (%q, %q)", provider, model)
	}
}

func TestResolveCortexModelPreservesExplicitConfig(t *testing.T) {
	cfg := config.CortexConfig{Enabled: true, Provider: "openai", LLMModel: "gpt-4o"}
	provider, model := resolveCortexModel(cfg, "local/gemma4:latest")
	if provider != "openai" {
		t.Errorf("explicit provider should be preserved; got %q", provider)
	}
	if model != "gpt-4o" {
		t.Errorf("explicit model should be preserved; got %q", model)
	}
}

func TestResolveCortexModelMirrorsWhenPartial(t *testing.T) {
	// Only one of Provider/LLMModel set isn't a real "pin" — fall back to
	// mirroring the chat agent so cortex doesn't ship a half-configured client.
	cfg := config.CortexConfig{Enabled: true, Provider: "anthropic", LLMModel: ""}
	provider, model := resolveCortexModel(cfg, "local/gemma4:latest")
	if provider != "local" || model != "gemma4:latest" {
		t.Errorf("partial config should mirror chat agent; got (%q, %q)", provider, model)
	}
}

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
		"user", "What are the main principles of clean code architecture, and how do they apply when building maintainable Go services?",
		"assistant", "Clean code follows separation of concerns, single responsibility, and dependency inversion. In Go, prefer small interfaces consumed where they're used, keep packages focused, and avoid premature abstraction.",
	)
	assert.True(t, ShouldIngest(thread))
}

func TestShouldIngestValidWithToolCalls(t *testing.T) {
	thread := msgs(
		"user", "What files are in the project root and what does the layout tell us about the architecture?",
		"assistant", "[tool: bash]\n{\"command\":\"ls -la\"}",
		"user", "main.go\ngo.mod\nREADME.md\ninternal/\ncmd/\npkg/\nMakefile",
		"assistant", "The project contains main.go, go.mod, README.md, plus the internal/, cmd/, and pkg/ directories. This is a standard Go layout: cmd/ holds entry points, internal/ holds private packages, and pkg/ exposes public APIs.",
	)
	assert.True(t, ShouldIngest(thread))
}

func TestShouldIngestTrivialCaseInsensitive(t *testing.T) {
	thread := msgs("user", "THANKS", "assistant", "You are welcome! Glad I could help with that.")
	assert.False(t, ShouldIngest(thread))
}

func TestShouldRecall(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"ok", false},
		{"thanks", false},
		{"Thanks", false},
		{"hi", false},
		{"yes", false},
		{"hello world", false},                                                     // 11 chars, below threshold
		{"what about Hormuz?", true},                                                // 18 chars, substantive
		{"Tell me about the project structure for the new microservice we discussed", true},
	}
	for _, tc := range cases {
		got := ShouldRecall(tc.msg)
		assert.Equal(t, tc.want, got, "msg=%q", tc.msg)
	}
}
