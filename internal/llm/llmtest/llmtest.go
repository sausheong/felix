// Package llmtest provides shared test helpers for LLMProvider stubs.
//
// Two pieces:
//   - Base: an embeddable struct that supplies default no-op
//     implementations of every LLMProvider method except ChatStream.
//     Test stubs that need custom ChatStream behavior should embed Base
//     so the LLMProvider interface can grow without churning every stub.
//   - Stub: a fully-configurable LLMProvider for the common case
//     (canned text response, optional delay, observable requests).
package llmtest

import (
	"context"
	"sync"
	"time"

	"github.com/sausheong/felix/internal/llm"
)

// Base provides default implementations of every LLMProvider method
// except ChatStream. Embed this in test stubs to avoid having to update
// every stub when the interface widens.
type Base struct{}

// Models returns an empty slice (non-nil so callers can range safely).
func (Base) Models() []llm.ModelInfo { return []llm.ModelInfo{} }

// NormalizeToolSchema is identity by default — no fields stripped, no
// diagnostics emitted. Per-provider implementations strip fields their
// API rejects.
func (Base) NormalizeToolSchema(tools []llm.ToolDef) ([]llm.ToolDef, []llm.Diagnostic) {
	return tools, nil
}

// Stub is a configurable LLMProvider for tests.
type Stub struct {
	Base

	// Text is the canned text response emitted on every ChatStream call.
	Text string
	// Delay sleeps before emitting the response. Zero means immediate.
	Delay time.Duration
	// Started is closed (once) just before the response is delayed/emitted.
	// Useful for synchronizing concurrent tests.
	Started chan struct{}
	// ChatErr, if non-nil, is returned synchronously from ChatStream.
	ChatErr error
	// ChatHook, if non-nil, observes every ChatStream request before
	// emission. Hook executes synchronously inside ChatStream.
	ChatHook func(req llm.ChatRequest)

	once sync.Once
}

func (s *Stub) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if s.ChatHook != nil {
		s.ChatHook(req)
	}
	if s.ChatErr != nil {
		return nil, s.ChatErr
	}
	if s.Started != nil {
		s.once.Do(func() { close(s.Started) })
	}
	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		if s.Delay > 0 {
			select {
			case <-time.After(s.Delay):
			case <-ctx.Done():
				ch <- llm.ChatEvent{Type: llm.EventError, Error: ctx.Err()}
				return
			}
		}
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: s.Text}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}
