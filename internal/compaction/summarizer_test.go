package compaction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/session"
)

// fakeProvider is an llm.LLMProvider stub that emits a fixed text response.
type fakeProvider struct {
	text string
	err  error
}

func (f *fakeProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: f.text}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (f *fakeProvider) Models() []llm.ModelInfo { return nil }

func TestSummarizerReturnsModelOutput(t *testing.T) {
	s := &Summarizer{
		Provider: &fakeProvider{text: "we picked option B for X."},
		Model:    "qwen2.5:3b-instruct",
		Timeout:  5 * time.Second,
	}
	entries := []session.SessionEntry{session.UserMessageEntry("hello")}
	got, err := s.Summarize(context.Background(), entries, "")
	require.NoError(t, err)
	assert.Equal(t, "we picked option B for X.", got)
}

func TestSummarizerTrimsWhitespace(t *testing.T) {
	s := &Summarizer{
		Provider: &fakeProvider{text: "  \n  summary text  \n  "},
		Model:    "qwen2.5:3b-instruct",
		Timeout:  5 * time.Second,
	}
	got, err := s.Summarize(context.Background(), []session.SessionEntry{session.UserMessageEntry("hi")}, "")
	require.NoError(t, err)
	assert.Equal(t, "summary text", got)
}

func TestSummarizerEmptyResponseIsError(t *testing.T) {
	s := &Summarizer{
		Provider: &fakeProvider{text: "   \n  "},
		Model:    "qwen2.5:3b-instruct",
		Timeout:  5 * time.Second,
	}
	_, err := s.Summarize(context.Background(), []session.SessionEntry{session.UserMessageEntry("hi")}, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptySummary)
}

func TestSummarizerProviderErrorPropagates(t *testing.T) {
	s := &Summarizer{
		Provider: &fakeProvider{err: errors.New("ollama down")},
		Model:    "qwen2.5:3b-instruct",
		Timeout:  5 * time.Second,
	}
	_, err := s.Summarize(context.Background(), []session.SessionEntry{session.UserMessageEntry("hi")}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ollama down")
}
