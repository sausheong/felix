package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/llm/llmtest"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamThenFailProvider emits some text, then EventError mid-stream.
// On the non-streaming retry it returns clean text. Used to verify
// the runtime discards the partial output and resumes from the retry.
type streamThenFailProvider struct {
	llmtest.Base

	mu             sync.Mutex
	streamCalls    atomic.Int64
	nonstreamCalls atomic.Int64

	// What the failed stream emits before EventError.
	partialText string
	streamErr   error

	// What the non-streaming retry returns.
	finalText string
}

func (p *streamThenFailProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.streamCalls.Add(1)
	ch := make(chan llm.ChatEvent, 4)
	if p.partialText != "" {
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.partialText}
	}
	ch <- llm.ChatEvent{Type: llm.EventError, Error: p.streamErr}
	close(ch)
	return ch, nil
}

func (p *streamThenFailProvider) ChatNonStreaming(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.nonstreamCalls.Add(1)
	ch := make(chan llm.ChatEvent, 4)
	ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: p.finalText}
	ch <- llm.ChatEvent{Type: llm.EventDone}
	close(ch)
	return ch, nil
}

func TestRuntimeStreamFallbackDiscardsPartialAndRetries(t *testing.T) {
	prov := &streamThenFailProvider{
		partialText: "PARTIAL_LOST",
		streamErr:   errors.New("connection reset by peer"),
		finalText:   "RECOVERED_VIA_NONSTREAMING",
	}
	rt := &Runtime{
		LLM:       prov,
		Tools:     tools.NewRegistry(),
		Session:   session.NewSession("a", "k"),
		AgentID:   "a",
		Model:     "claude-sonnet-4-5",
		Provider:  "anthropic",
		MaxTurns:  2,
		Workspace: t.TempDir(),
	}
	_, err := rt.RunSync(context.Background(), "do it", nil)
	require.NoError(t, err)

	// Note: RunSync surfaces ALL EventTextDelta events as they stream,
	// so the partial text DID hit the user's terminal before we
	// detected the failure. That's a UX wart but not a correctness
	// problem — the cache prefix invariant is about what's persisted
	// to the SESSION, not what flashed past on stdout.
	require.EqualValues(t, 1, prov.streamCalls.Load(), "streaming endpoint hit once")
	require.EqualValues(t, 1, prov.nonstreamCalls.Load(), "non-streaming retry hit once")

	// The session's stored assistant message must be the recovered
	// text only — that is what gets re-played as part of the prefix
	// on the NEXT turn, and any partial from the failed stream
	// would invalidate the cache.
	var assistantText string
	for _, e := range rt.Session.Entries() {
		if e.Type == session.EntryTypeMessage {
			var m session.MessageData
			require.NoError(t, decodeMessage(e, &m))
			if m.Text != "" && m.Text != "do it" {
				assistantText = m.Text
			}
		}
	}
	assert.Equal(t, "RECOVERED_VIA_NONSTREAMING", assistantText)
}

// alwaysFailStreamProvider implements ChatStream that fires EventError
// without ever emitting a token. This is the "pre-flight" failure case
// (e.g., 400 Bad Request) — the runtime must NOT retry with
// non-streaming, because a different transport won't fix a malformed
// request, and a retry would mask the real bug.
type alwaysFailStreamProvider struct {
	llmtest.Base
	streamCalls    atomic.Int64
	nonstreamCalls atomic.Int64
}

func (p *alwaysFailStreamProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.streamCalls.Add(1)
	ch := make(chan llm.ChatEvent, 1)
	ch <- llm.ChatEvent{Type: llm.EventError, Error: errors.New("400 invalid input")}
	close(ch)
	return ch, nil
}

func (p *alwaysFailStreamProvider) ChatNonStreaming(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.nonstreamCalls.Add(1)
	ch := make(chan llm.ChatEvent, 1)
	ch <- llm.ChatEvent{Type: llm.EventDone}
	close(ch)
	return ch, nil
}

func TestRuntimeStreamFallbackSkippedWhenNoTokenReceived(t *testing.T) {
	prov := &alwaysFailStreamProvider{}
	rt := &Runtime{
		LLM:       prov,
		Tools:     tools.NewRegistry(),
		Session:   session.NewSession("a", "k"),
		AgentID:   "a",
		Model:     "claude-sonnet-4-5",
		Provider:  "anthropic",
		MaxTurns:  2,
		Workspace: t.TempDir(),
	}
	_, err := rt.RunSync(context.Background(), "do it", nil)
	require.Error(t, err, "pre-flight stream error must surface to caller")
	assert.EqualValues(t, 1, prov.streamCalls.Load())
	assert.EqualValues(t, 0, prov.nonstreamCalls.Load(),
		"non-streaming retry must NOT engage when no token was received — "+
			"otherwise pre-flight 4xx errors get silently retried")
}

// streamingOnlyProvider doesn't implement NonStreamingProvider.
// Verifies the runtime gracefully surfaces the original error rather
// than panicking on the type assertion.
type streamingOnlyProvider struct {
	llmtest.Base
	streamCalls atomic.Int64
}

func (p *streamingOnlyProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.streamCalls.Add(1)
	ch := make(chan llm.ChatEvent, 2)
	ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: "started"}
	ch <- llm.ChatEvent{Type: llm.EventError, Error: errors.New("network broke")}
	close(ch)
	return ch, nil
}

func TestRuntimeStreamFallbackNoOpWhenProviderLacksNonStreaming(t *testing.T) {
	prov := &streamingOnlyProvider{}
	rt := &Runtime{
		LLM:       prov,
		Tools:     tools.NewRegistry(),
		Session:   session.NewSession("a", "k"),
		AgentID:   "a",
		Model:     "x",
		Provider:  "openai", // no NonStreamingProvider impl on streamingOnlyProvider
		MaxTurns:  2,
		Workspace: t.TempDir(),
	}
	_, err := rt.RunSync(context.Background(), "do it", nil)
	require.Error(t, err, "provider without NonStreamingProvider must surface mid-stream errors as before")
	assert.EqualValues(t, 1, prov.streamCalls.Load())
}

// streamFailsAgainProvider fails on stream AND on non-streaming retry.
// The runtime must emit the SECOND error and not loop forever.
type streamFailsAgainProvider struct {
	llmtest.Base
	streamCalls    atomic.Int64
	nonstreamCalls atomic.Int64
}

func (p *streamFailsAgainProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.streamCalls.Add(1)
	ch := make(chan llm.ChatEvent, 2)
	ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: "x"}
	ch <- llm.ChatEvent{Type: llm.EventError, Error: errors.New("stream broke")}
	close(ch)
	return ch, nil
}

func (p *streamFailsAgainProvider) ChatNonStreaming(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	p.nonstreamCalls.Add(1)
	return nil, errors.New("nonstreaming also broke")
}

func TestRuntimeStreamFallbackBothFail(t *testing.T) {
	prov := &streamFailsAgainProvider{}
	rt := &Runtime{
		LLM:       prov,
		Tools:     tools.NewRegistry(),
		Session:   session.NewSession("a", "k"),
		AgentID:   "a",
		Model:     "claude-sonnet-4-5",
		Provider:  "anthropic",
		MaxTurns:  2,
		Workspace: t.TempDir(),
	}
	_, err := rt.RunSync(context.Background(), "do it", nil)
	require.Error(t, err)
	assert.EqualValues(t, 1, prov.streamCalls.Load())
	assert.EqualValues(t, 1, prov.nonstreamCalls.Load(), "must attempt the retry exactly once")
}
