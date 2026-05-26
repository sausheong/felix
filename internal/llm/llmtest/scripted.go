package llmtest

import (
	"context"
	"sync"

	hllm "github.com/sausheong/harness/llm"
)

// NewScriptedProvider returns an in-memory LLMProvider that emits one
// canned text reply per ChatStream call, drawn FIFO from replies. After
// the slice is exhausted every subsequent call returns the empty string
// followed by EventDone, which lets long-running tests probe whether
// the runtime would attempt extra turns.
//
// This is the chatexec-test workhorse: deterministic, no goroutine
// dependencies, no network. For more elaborate stubs (custom request
// hooks, error injection, delays) embed the harness Stub directly.
func NewScriptedProvider(replies ...string) hllm.LLMProvider {
	return &Scripted{replies: append([]string(nil), replies...)}
}

// Scripted is the concrete provider returned by NewScriptedProvider. The
// type is exported so tests that need to inspect Calls (the requests the
// runtime made against it, in order) can do so without reflection.
type Scripted struct {
	Base

	mu      sync.Mutex
	replies []string
	Calls   []hllm.ChatRequest
}

// ChatStream emits the next scripted reply followed by EventDone. When
// the script is exhausted it emits the empty string + EventDone so the
// runtime sees a clean "no more output" turn rather than blocking.
func (s *Scripted) ChatStream(ctx context.Context, req hllm.ChatRequest) (<-chan hllm.ChatEvent, error) {
	s.mu.Lock()
	s.Calls = append(s.Calls, req)
	var text string
	if len(s.replies) > 0 {
		text = s.replies[0]
		s.replies = s.replies[1:]
	}
	s.mu.Unlock()

	ch := make(chan hllm.ChatEvent, 3)
	go func() {
		defer close(ch)
		if text != "" {
			select {
			case ch <- hllm.ChatEvent{Type: hllm.EventTextDelta, Text: text}:
			case <-ctx.Done():
				ch <- hllm.ChatEvent{Type: hllm.EventError, Error: ctx.Err()}
				return
			}
		}
		select {
		case ch <- hllm.ChatEvent{Type: hllm.EventDone, Usage: &hllm.Usage{InputTokens: 1, OutputTokens: 1}}:
		case <-ctx.Done():
			ch <- hllm.ChatEvent{Type: hllm.EventError, Error: ctx.Err()}
		}
	}()
	return ch, nil
}
