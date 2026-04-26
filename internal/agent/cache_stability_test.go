package agent

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/tools"
)

// recordingProvider captures every ChatRequest it receives and emits a
// canned text response. Used to inspect what the runtime sends to the
// LLM across turns.
type recordingProvider struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
	reply    string
}

func (r *recordingProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()

	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: r.reply}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func (r *recordingProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "rec", Name: "Recording", Provider: "rec"}}
}

// requestPrefixSignature renders the cache-relevant portion of a request:
// system prompt, sorted tool definitions, and the message list excluding
// the final user turn. Two calls in the same session that differ only in
// the freshly-arrived user message must produce identical signatures.
func requestPrefixSignature(t *testing.T, req llm.ChatRequest) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("SYS:")
	sb.WriteString(req.SystemPrompt)
	sb.WriteString("\nTOOLS:")
	names := make([]string, len(req.Tools))
	descByName := make(map[string]string, len(req.Tools))
	paramsByName := make(map[string]string, len(req.Tools))
	for i, td := range req.Tools {
		names[i] = td.Name
		descByName[td.Name] = td.Description
		paramsByName[td.Name] = string(td.Parameters)
	}
	sort.Strings(names)
	for _, n := range names {
		sb.WriteString("\n  ")
		sb.WriteString(n)
		sb.WriteString("|")
		sb.WriteString(descByName[n])
		sb.WriteString("|")
		sb.WriteString(paramsByName[n])
	}
	sb.WriteString("\nMSGS_EXCL_LAST:")
	for i := 0; i < len(req.Messages)-1; i++ {
		sb.WriteString("\n  ")
		sb.WriteString(req.Messages[i].Role)
		sb.WriteString(":")
		sb.WriteString(req.Messages[i].Content)
	}
	return sb.String()
}

// TestRequestPrefixIsByteStableAcrossTurns runs two consecutive turns of the
// agent loop with identical inputs. The second request's prefix (system
// prompt + tool defs + all-but-last message) must be byte-identical to the
// first request's full content (system prompt + tool defs + all messages).
//
// This is the cache-stability invariant: turn N+1's prefix is turn N's full
// prompt. Anthropic and OpenAI prompt caches both depend on this. The
// compaction-View bug we shipped a fix for would have been caught by an
// earlier version of this test (it changed the prefix dramatically when
// compaction fired).
func TestRequestPrefixIsByteStableAcrossTurns(t *testing.T) {
	rec := &recordingProvider{reply: "ok"}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()
	reg.Register(&mockTool{name: "zebra", output: "z"})
	reg.Register(&mockTool{name: "alpha", output: "a"})
	reg.Register(&mockTool{name: "mango", output: "m"})

	rt := &Runtime{
		LLM:       rec,
		Tools:     reg,
		Session:   sess,
		Model:     "rec-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	// Turn 1
	events, err := rt.Run(context.Background(), "hello", nil)
	require.NoError(t, err)
	for range events {
	}

	// Turn 2 — same session, same agent, same tools.
	events, err = rt.Run(context.Background(), "world", nil)
	require.NoError(t, err)
	for range events {
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.requests), 2,
		"expected at least 2 ChatStream calls, got %d", len(rec.requests))

	req1 := rec.requests[0]
	req2 := rec.requests[1]

	// turn 2's request layout: [...turn1.Messages, assistantN, userN+1]
	// turn 2's prefix = everything except the freshly-arrived user msg
	//                 = turn 1's full request + the assistant reply we
	//                   captured between turns.
	//
	// Build the expected prefix by appending the assistant reply onto turn 1's
	// message list, then render the resulting request the same way we render
	// turn 2 with its trailing user msg stripped. The two must be byte-equal.
	expected := req1
	expected.Messages = append(append([]llm.Message{}, req1.Messages...),
		llm.Message{Role: "assistant", Content: rec.reply})
	turn1FullPlusAssistant := fullSignature(t, expected)
	turn2Prefix := prefixWithoutLastMessage(t, req2)
	assert.Equal(t, turn1FullPlusAssistant, turn2Prefix,
		"turn 2 prefix must byte-match turn 1's full request plus the assistant reply")

	// Also assert that req.Tools order is byte-stable across turns. The
	// signature above sorts tool names for content-comparison, which would
	// hide an ordering regression from Go's randomized map iteration.
	// This separate check is what catches the Task 2 regression: if
	// Registry.ToolDefs() stops sorting, turn 2 will eventually emit tools
	// in a different order from turn 1 and this assertion will fail.
	require.Equal(t, len(req1.Tools), len(req2.Tools),
		"tool count must not change between turns")
	for i := range req1.Tools {
		assert.Equal(t, req1.Tools[i].Name, req2.Tools[i].Name,
			"tool[%d] name must match across turns (cache-prefix order regression)", i)
	}
}

// fullSignature renders the entire request the same way requestPrefixSignature
// does, but includes the final message too.
func fullSignature(t *testing.T, req llm.ChatRequest) string {
	t.Helper()
	sig := requestPrefixSignature(t, req)
	if len(req.Messages) > 0 {
		last := req.Messages[len(req.Messages)-1]
		sig += "\n  " + last.Role + ":" + last.Content
	}
	return sig
}

// prefixWithoutLastMessage is requestPrefixSignature but with the last
// message of the previous turn (the assistant reply that turn N's run
// appended) re-included, since that's part of turn N+1's prefix.
//
// turn N's request: [sys, tools, ...msgs, userN]
// after turn N: session also contains [...msgs, userN, assistantN]
// turn N+1's request: [sys, tools, ...msgs, userN, assistantN, userN+1]
//                                                  ^^^^^^^^^^^^^^^^^^^^^^
//                                                  the prefix at turn N+1
//                                                  excluding the new user msg
//
// So turn N+1's prefix = turn N's full request + assistantN.
// We test that subset by stripping the last message of turn N+1.
func prefixWithoutLastMessage(t *testing.T, req llm.ChatRequest) string {
	t.Helper()
	if len(req.Messages) == 0 {
		return requestPrefixSignature(t, req)
	}
	clone := req
	clone.Messages = req.Messages[:len(req.Messages)-1]
	return fullSignature(t, clone)
}

// Hint to readers grepping for json: imported above only to keep
// json.RawMessage compile-checkable in mockTool below. Actual test does
// not deserialize anything.
var _ = json.RawMessage(nil)
