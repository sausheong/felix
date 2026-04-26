package llmtest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/llm/llmtest"
)

func TestBaseDefaults(t *testing.T) {
	type stub struct {
		llmtest.Base
	}
	s := &stub{}
	// Models must be non-nil-empty (callers may range over it).
	assert.NotNil(t, s.Models())
}

func TestStubCannedText(t *testing.T) {
	s := &llmtest.Stub{Text: "hello"}
	ch, err := s.ChatStream(context.Background(), llm.ChatRequest{})
	require.NoError(t, err)
	var got string
	for ev := range ch {
		if ev.Type == llm.EventTextDelta {
			got += ev.Text
		}
	}
	assert.Equal(t, "hello", got)
}

func TestStubChatHookObservesRequests(t *testing.T) {
	var seen []llm.ChatRequest
	s := &llmtest.Stub{
		Text:     "ok",
		ChatHook: func(req llm.ChatRequest) { seen = append(seen, req) },
	}
	_, _ = s.ChatStream(context.Background(), llm.ChatRequest{Model: "m1"})
	_, _ = s.ChatStream(context.Background(), llm.ChatRequest{Model: "m2"})
	require.Len(t, seen, 2)
	assert.Equal(t, "m1", seen[0].Model)
	assert.Equal(t, "m2", seen[1].Model)
}

func TestStubChatErrShortCircuits(t *testing.T) {
	s := &llmtest.Stub{ChatErr: assert.AnError}
	_, err := s.ChatStream(context.Background(), llm.ChatRequest{})
	assert.Equal(t, assert.AnError, err)
}
