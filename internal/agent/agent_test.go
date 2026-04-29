package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/felix/internal/compaction"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/llm/llmtest"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLLMProvider returns canned ChatEvent streams for testing.
type mockLLMProvider struct {
	llmtest.Base
	events []llm.ChatEvent
}

func (m *mockLLMProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	ch := make(chan llm.ChatEvent, len(m.events))
	for _, e := range m.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

// --- assembleSystemPrompt tests ---

func TestAssembleSystemPromptWithIdentity(t *testing.T) {
	dir := t.TempDir()
	identityContent := "You are a test assistant."
	err := os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte(identityContent), 0o644)
	require.NoError(t, err)

	result := assembleSystemPrompt(dir, "", "test", "Test Agent", []string{"read_file", "bash"})
	assert.Contains(t, result, identityContent)
	assert.Contains(t, result, "configuration file")
}

func TestAssembleSystemPromptDefault(t *testing.T) {
	// Isolate HOME so configSummary() doesn't read the developer's real
	// ~/.felix/felix.json5 — which may list agents whose tool allowlists
	// include "web_fetch", breaking the NotContains assertion below.
	// We also write an empty agents/channels config so the fallback
	// DefaultConfig() (which includes a Felix agent allowing web_fetch)
	// doesn't get used.
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".felix"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(home, ".felix", "felix.json5"),
		[]byte(`{"agents":{"list":[]},"channels":{}}`),
		0o600,
	))

	dir := t.TempDir() // workspace, no IDENTITY.md
	result := assembleSystemPrompt(dir, "", "default", "Assistant", []string{"read_file", "bash"})
	assert.Contains(t, result, defaultIdentityBase)
	assert.Contains(t, result, "read files")
	assert.Contains(t, result, "bash commands")
	assert.NotContains(t, result, "web_fetch")
	assert.Contains(t, result, "data directory")
}

func TestAssembleSystemPromptConfigOverride(t *testing.T) {
	dir := t.TempDir()
	// Even with IDENTITY.md present, config system_prompt takes priority
	err := os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("identity file content"), 0o644)
	require.NoError(t, err)

	configPrompt := "You are a custom agent from config."
	result := assembleSystemPrompt(dir, configPrompt, "custom", "Custom Agent", []string{"read_file"})
	assert.Contains(t, result, configPrompt)
	assert.NotContains(t, result, "identity file content")
}

func TestAssembleSystemPromptSelfIdentity(t *testing.T) {
	dir := t.TempDir()
	result := assembleSystemPrompt(dir, "", "supervisor", "Supervisor", nil)
	assert.Contains(t, result, `"Supervisor" agent (id: supervisor)`)
}

func TestBuildDefaultIdentityToolSpecific(t *testing.T) {
	result := buildDefaultIdentity([]string{"read_file", "web_search", "web_fetch"})
	assert.Contains(t, result, "read files")
	assert.Contains(t, result, "search the web")
	assert.Contains(t, result, "fetch web pages")
	assert.NotContains(t, result, "bash commands")
	assert.NotContains(t, result, "send_message")
}

// --- assembleMessages tests ---

func TestAssembleMessagesUserAndAssistant(t *testing.T) {
	history := []session.SessionEntry{
		session.UserMessageEntry("hello"),
		session.AssistantMessageEntry("hi there"),
	}

	msgs := assembleMessages(history)
	require.Len(t, msgs, 2)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "hello", msgs[0].Content)
	assert.Equal(t, "assistant", msgs[1].Role)
	assert.Equal(t, "hi there", msgs[1].Content)
}

func TestAssembleMessagesToolCallAndResult(t *testing.T) {
	tc := session.ToolCallEntry("tc_1", "bash", json.RawMessage(`{"command":"echo hi"}`))
	tr := session.ToolResultEntry("tc_1", "hi\n", "", nil)

	history := []session.SessionEntry{
		session.UserMessageEntry("run echo hi"),
		tc,
		tr,
	}

	msgs := assembleMessages(history)
	require.Len(t, msgs, 3)

	// User message
	assert.Equal(t, "user", msgs[0].Role)

	// Tool call should be an assistant message with tool calls
	assert.Equal(t, "assistant", msgs[1].Role)
	require.Len(t, msgs[1].ToolCalls, 1)
	assert.Equal(t, "tc_1", msgs[1].ToolCalls[0].ID)
	assert.Equal(t, "bash", msgs[1].ToolCalls[0].Name)

	// Tool result should be a user message with ToolCallID
	assert.Equal(t, "user", msgs[2].Role)
	assert.Equal(t, "tc_1", msgs[2].ToolCallID)
	assert.Equal(t, "hi\n", msgs[2].Content)
}

func TestAssembleMessagesMeta(t *testing.T) {
	summaryData, _ := json.Marshal(session.MessageData{Text: "previous conversation summary"})
	meta := session.SessionEntry{
		Type: session.EntryTypeMeta,
		Role: "system",
		Data: summaryData,
	}

	msgs := assembleMessages([]session.SessionEntry{meta})
	require.Len(t, msgs, 1)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Contains(t, msgs[0].Content, "[Session Summary]")
	assert.Contains(t, msgs[0].Content, "previous conversation summary")
}

func TestAssembleMessagesEmpty(t *testing.T) {
	msgs := assembleMessages(nil)
	assert.Nil(t, msgs)

	msgs = assembleMessages([]session.SessionEntry{})
	assert.Nil(t, msgs)
}

func TestAssembleMessagesOrphanedToolCall(t *testing.T) {
	// Simulate an interrupted session: tool_call without tool_result, followed by a new user message
	tc := session.ToolCallEntry("tc_orphan", "bash", json.RawMessage(`{"command":"pwd"}`))

	history := []session.SessionEntry{
		session.UserMessageEntry("run pwd"),
		tc,
		session.UserMessageEntry("hello again"),
	}

	msgs := assembleMessages(history)

	// Should have: user, assistant(tool_call), synthetic tool_result, user
	require.Len(t, msgs, 4)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "assistant", msgs[1].Role)
	require.Len(t, msgs[1].ToolCalls, 1)
	// Synthetic result injected
	assert.Equal(t, "user", msgs[2].Role)
	assert.Equal(t, "tc_orphan", msgs[2].ToolCallID)
	assert.True(t, msgs[2].IsError)
	assert.Contains(t, msgs[2].Content, "interrupted")
	// New user message
	assert.Equal(t, "user", msgs[3].Role)
	assert.Equal(t, "hello again", msgs[3].Content)
}

func TestAssembleMessagesOrphanedToolCallAtEnd(t *testing.T) {
	// Tool call at end of history with no result and no following message
	tc := session.ToolCallEntry("tc_end", "bash", json.RawMessage(`{"command":"ls"}`))

	history := []session.SessionEntry{
		session.UserMessageEntry("list files"),
		tc,
	}

	msgs := assembleMessages(history)

	// Should have: user, assistant(tool_call), synthetic tool_result
	require.Len(t, msgs, 3)
	assert.Equal(t, "tc_end", msgs[2].ToolCallID)
	assert.True(t, msgs[2].IsError)
}

// --- pruneToolResults tests ---

func TestPruneToolResults(t *testing.T) {
	longContent := strings.Repeat("a", 20000)
	msgs := []llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "user", Content: longContent, ToolCallID: "tc_1"},
	}

	pruneToolResults(msgs, 10000)

	// User message should be unchanged
	assert.Equal(t, "hello", msgs[0].Content)

	// Tool result should be truncated
	assert.Less(t, len(msgs[1].Content), 20000)
	assert.Contains(t, msgs[1].Content, truncationMarker)
}

func TestPruneToolResultsShort(t *testing.T) {
	msgs := []llm.Message{
		{Role: "user", Content: "short output", ToolCallID: "tc_1"},
	}

	pruneToolResults(msgs, 10000)

	assert.Equal(t, "short output", msgs[0].Content)
}

func TestPruneToolResultsNewlineBoundary(t *testing.T) {
	// Build content with newlines so truncation prefers a newline boundary
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(strings.Repeat("x", 80))
		b.WriteString("\n")
	}
	content := b.String() // ~16200 chars

	msgs := []llm.Message{
		{Role: "user", Content: content, ToolCallID: "tc_1"},
	}

	pruneToolResults(msgs, 10000)

	// Should be truncated and contain the truncation marker
	truncated := msgs[0].Content
	assert.Contains(t, truncated, truncationMarker)
	assert.Less(t, len(truncated), len(content))

	// The truncated content (before the suffix) should end at a newline boundary
	suffixIdx := strings.Index(truncated, "\n\n"+truncationMarker)
	assert.Greater(t, suffixIdx, 0, "should contain truncation suffix")
}

// --- Runtime tests ---

func TestRuntimeRun(t *testing.T) {
	mock := &mockLLMProvider{
		events: []llm.ChatEvent{
			{Type: llm.EventTextDelta, Text: "Hello "},
			{Type: llm.EventTextDelta, Text: "world!"},
			{Type: llm.EventDone},
		},
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()

	rt := &Runtime{
		LLM:       mock,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	events, err := rt.Run(context.Background(), "hi", nil)
	require.NoError(t, err)

	var textParts []string
	var gotDone bool
	for e := range events {
		switch e.Type {
		case EventTextDelta:
			textParts = append(textParts, e.Text)
		case EventDone:
			gotDone = true
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}

	assert.Equal(t, []string{"Hello ", "world!"}, textParts)
	assert.True(t, gotDone)
}

func TestRuntimeRunWithToolCalls(t *testing.T) {
	callCount := 0

	// Use a stateful mock that returns different responses
	statefulMock := &statefulMockLLMProvider{
		responses: [][]llm.ChatEvent{
			// First response: tool call
			{
				{Type: llm.EventToolCallStart, ToolCall: &llm.ToolCall{ID: "tc_1", Name: "read_file"}},
				{Type: llm.EventToolCallDone, ToolCall: &llm.ToolCall{
					ID:    "tc_1",
					Name:  "read_file",
					Input: json.RawMessage(`{"path":"/tmp/test.txt"}`),
				}},
				{Type: llm.EventDone},
			},
			// Second response: text
			{
				{Type: llm.EventTextDelta, Text: "File contents: hello"},
				{Type: llm.EventDone},
			},
		},
		callCount: &callCount,
	}

	sess := session.NewSession("test-agent", "test-key")

	// Create a registry with a mock tool
	reg := tools.NewRegistry()
	reg.Register(&mockTool{name: "read_file", output: "hello"})

	rt := &Runtime{
		LLM:       statefulMock,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	events, err := rt.Run(context.Background(), "read test.txt", nil)
	require.NoError(t, err)

	var gotToolResult bool
	var gotDone bool
	for e := range events {
		switch e.Type {
		case EventToolResult:
			gotToolResult = true
			assert.Equal(t, "read_file", e.ToolCall.Name)
		case EventDone:
			gotDone = true
		case EventError:
			t.Fatalf("unexpected error: %v", e.Error)
		}
	}

	assert.True(t, gotToolResult, "should have received tool result")
	assert.True(t, gotDone, "should have received done event")
}

func TestRuntimeRunSync(t *testing.T) {
	mock := &mockLLMProvider{
		events: []llm.ChatEvent{
			{Type: llm.EventTextDelta, Text: "Hello "},
			{Type: llm.EventTextDelta, Text: "world!"},
			{Type: llm.EventDone},
		},
	}

	sess := session.NewSession("test-agent", "test-key")
	reg := tools.NewRegistry()

	rt := &Runtime{
		LLM:       mock,
		Tools:     reg,
		Session:   sess,
		Model:     "mock-model",
		Workspace: t.TempDir(),
		MaxTurns:  5,
	}

	text, err := rt.RunSync(context.Background(), "hi", nil)
	require.NoError(t, err)
	assert.Equal(t, "Hello world!", text)
}

// --- Helpers ---

// statefulMockLLMProvider returns different responses on successive calls.
type statefulMockLLMProvider struct {
	llmtest.Base
	responses [][]llm.ChatEvent
	callCount *int
}

func (m *statefulMockLLMProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	idx := *m.callCount
	*m.callCount++
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	events := m.responses[idx]
	ch := make(chan llm.ChatEvent, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

// mockTool is a simple tool that returns a canned output.
type mockTool struct {
	name   string
	output string
}

func (t *mockTool) Name() string        { return t.name }
func (t *mockTool) Description() string { return "mock tool" }
func (t *mockTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *mockTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{Output: t.output}, nil
}

func TestAssembleMessagesEntryTypeCompaction(t *testing.T) {
	history := []session.SessionEntry{
		session.CompactionEntry("we discussed feature X and chose option B", "", "", "m", 0, 0, 4),
		session.UserMessageEntry("now what about feature Y?"),
	}
	msgs := assembleMessages(history)
	require.Len(t, msgs, 2)
	assert.Equal(t, "user", msgs[0].Role)
	assert.Contains(t, msgs[0].Content, "[Previous conversation summary]")
	assert.Contains(t, msgs[0].Content, "we discussed feature X")
	assert.Equal(t, "user", msgs[1].Role)
	assert.Equal(t, "now what about feature Y?", msgs[1].Content)
}

func TestAssembleMessagesLegacyEntryTypeMetaStillWorks(t *testing.T) {
	// Old sessions written with the legacy Compact() rewrite still produce
	// EntryTypeMeta entries. Verify backward compatibility.
	history := []session.SessionEntry{
		// Build a legacy meta entry by hand.
		{
			Type: session.EntryTypeMeta,
			Role: "system",
			Data: mustMarshalMessageData(t, "old style summary"),
		},
		session.UserMessageEntry("then a question"),
	}
	msgs := assembleMessages(history)
	require.Len(t, msgs, 2)
	assert.Contains(t, msgs[0].Content, "Session Summary")
	assert.Contains(t, msgs[0].Content, "old style summary")
}

// mustMarshalMessageData serializes a MessageData blob for legacy meta tests.
func mustMarshalMessageData(t *testing.T, text string) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(session.MessageData{Text: text})
	require.NoError(t, err)
	return data
}

// fakeLLM is a minimal llm.LLMProvider that returns a scripted response.
//
// overflow is the call index (0-based) at which the provider returns a
// context-overflow error instead of streaming. Defaults to -1 (never).
// On the overflow path the call index is NOT advanced, so the next call
// (the retry after compaction) consumes the responses[0] entry.
type fakeLLM struct {
	llmtest.Base
	responses []string // one per turn; no tool calls
	idx       int
	overflow  int // call index at which to return a context-overflow error; -1 disables
}

func (f *fakeLLM) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	if f.idx == f.overflow {
		// Mark overflow as consumed so we only fail once.
		f.overflow = -1
		return nil, errors.New("context length exceeded")
	}
	resp := "(silent)"
	if f.idx < len(f.responses) {
		resp = f.responses[f.idx]
	}
	f.idx++
	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: resp}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

// alwaysSummary: every call returns "compacted summary".
type alwaysSummary struct{ llmtest.Base }

func (alwaysSummary) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	ch := make(chan llm.ChatEvent, 2)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: "compacted summary"}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

func newCompactionMgr() *compaction.Manager {
	return &compaction.Manager{
		Summarizer:    &compaction.Summarizer{Provider: alwaysSummary{}, Model: "m", Timeout: time.Second},
		PreserveTurns: 4,
	}
}

// noopExecutor is a tools.Executor with no tools registered.
type noopExecutor struct{}

func (noopExecutor) Execute(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{}, errors.New("no tools")
}
func (noopExecutor) Names() []string                    { return nil }
func (noopExecutor) ToolDefs() []llm.ToolDef            { return nil }
func (noopExecutor) Get(name string) (tools.Tool, bool) { return nil, false }

func TestRuntimeReactiveCompactionRetriesOnce(t *testing.T) {
	sess := session.NewSession("default", "test")
	for i := 0; i < 6; i++ {
		sess.Append(session.UserMessageEntry("u"))
		sess.Append(session.AssistantMessageEntry("a"))
	}
	rt := &Runtime{
		LLM:        &fakeLLM{responses: []string{"final reply"}, overflow: 0},
		Tools:      noopExecutor{},
		Session:    sess,
		Model:      "anthropic/claude-3-5-sonnet-20241022",
		Workspace:  t.TempDir(),
		Compaction: newCompactionMgr(),
	}
	out, err := rt.RunSync(context.Background(), "next question", nil)
	require.NoError(t, err)
	assert.Equal(t, "final reply", out)

	// Session should now contain a compaction entry.
	view := sess.View()
	require.NotEmpty(t, view)
	assert.Equal(t, session.EntryTypeCompaction, view[0].Type)
}

func TestRuntimeShortSessionDoesNotCompactOnPreventive(t *testing.T) {
	sess := session.NewSession("default", "test")
	sess.Append(session.UserMessageEntry("only msg"))
	rt := &Runtime{
		LLM:        &fakeLLM{responses: []string{"hi"}, overflow: -1},
		Tools:      noopExecutor{},
		Session:    sess,
		Model:      "anthropic/claude-3-5-sonnet-20241022",
		Workspace:  t.TempDir(),
		Compaction: newCompactionMgr(),
	}
	out, err := rt.RunSync(context.Background(), "hi", nil)
	require.NoError(t, err)
	assert.Equal(t, "hi", out, "LLM should still have been called when no compaction fires")

	// No compaction entry should have been added.
	for _, e := range sess.Entries() {
		assert.NotEqual(t, session.EntryTypeCompaction, e.Type)
	}
}

func TestCompactionMessageIncludesContinuationDirective(t *testing.T) {
	sess := session.NewSession("test-agent", "test-key")
	sess.Append(session.UserMessageEntry("first user msg"))
	sess.Append(session.AssistantMessageEntry("first reply"))
	sess.Append(session.CompactionEntry(
		"User asked about Wasm; we recommended Extism. They then asked for details on how it works.",
		"", "", "test-model", 0, 0, 2,
	))

	msgs := assembleMessages(sess.View())
	require.NotEmpty(t, msgs)

	// The compaction summary becomes a user message. It must include both
	// the summary text and the continuation directive that tells the model
	// to resume rather than restart.
	var summaryMsg *llm.Message
	for i := range msgs {
		if strings.Contains(msgs[i].Content, "Previous conversation summary") {
			summaryMsg = &msgs[i]
			break
		}
	}
	require.NotNil(t, summaryMsg, "compaction entry must produce a user message")

	assert.Contains(t, summaryMsg.Content, "Wasm", "summary text must be present")
	assert.Contains(t, summaryMsg.Content, "Resume directly",
		"continuation directive must instruct the model to resume")
	assert.Contains(t, summaryMsg.Content, "do not acknowledge the summary",
		"continuation directive must forbid restarting")
}

// TestCompactionMessageCapHonored verifies the runtime reads the message-count
// cap from CompactionConfig.MessageCap rather than the previously-hardcoded
// constant. With MessageCap=10 and msgs > 10, compaction must trigger; with
// MessageCap=0 (cap disabled) and msgs > 10 but threshold not hit, compaction
// must NOT trigger.
func TestCompactionMessageCapHonored(t *testing.T) {
	mock := &mockLLMProvider{events: []llm.ChatEvent{
		{Type: llm.EventTextDelta, Text: "ok"},
		{Type: llm.EventDone},
	}}

	makeRT := func(cap int) *Runtime {
		sess := session.NewSession("a", "k")
		// Pre-populate session with enough messages to exceed cap=10.
		for i := 0; i < 12; i++ {
			sess.Append(session.UserMessageEntry("u"))
			sess.Append(session.AssistantMessageEntry("a"))
		}
		mgr := &compaction.Manager{
			Summarizer: &compaction.Summarizer{
				Provider: &cannedSummarizer{text: "summary"},
				Model:    "m",
				Timeout:  time.Second,
			},
			PreserveTurns: 4,
			MessageCap:    cap,
		}
		return &Runtime{
			LLM:     mock,
			Tools:   tools.NewRegistry(),
			Session: sess,
			// Model picks an "anthropic/claude-*" alias so
			// tokens.ContextWindow returns 200000 (any modelID containing
			// "claude" hits the Anthropic 200k branch). With the test's
			// tiny messages, the 60% threshold of a 200k window is
			// unreachable — isolating MessageCap as the variable under
			// test.
			Model:      "anthropic/claude-mock",
			Workspace:  t.TempDir(),
			MaxTurns:   3,
			Compaction: mgr,
		}
	}

	// With cap=10, the existing 24+ messages exceed it; compaction MUST fire.
	rt := makeRT(10)
	events, err := rt.Run(context.Background(), "go", nil)
	require.NoError(t, err)
	var sawCompaction bool
	for e := range events {
		if e.Type == EventCompactionDone || e.Type == EventCompactionStart {
			sawCompaction = true
		}
	}
	assert.True(t, sawCompaction, "MessageCap=10 with 24+ msgs must fire compaction")

	// With cap=0 (disabled) and a high-window model that won't hit the
	// token threshold, compaction MUST NOT fire.
	rt = makeRT(0)
	events, err = rt.Run(context.Background(), "go", nil)
	require.NoError(t, err)
	sawCompaction = false
	for e := range events {
		if e.Type == EventCompactionDone || e.Type == EventCompactionStart {
			sawCompaction = true
		}
	}
	assert.False(t, sawCompaction, "MessageCap=0 with no threshold hit must NOT fire compaction")
}

// cannedSummarizer is a minimal LLMProvider stub for tests that need a
// summarizer-shaped fake; it returns a fixed text reply on every call.
type cannedSummarizer struct {
	llmtest.Base
	text string
}

func (f *cannedSummarizer) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	ch := make(chan llm.ChatEvent, 4)
	go func() {
		defer close(ch)
		ch <- llm.ChatEvent{Type: llm.EventTextDelta, Text: f.text}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}


// TestRun_AbortMidDispatchProducesPairedSession verifies that when ctx is
// cancelled while iterating over a multi-tool batch, the loop breaks at the
// first abort and the session ends with consistent tool_use/tool_result
// pairing. Tools never dispatched do NOT appear in the session.
func TestRun_AbortMidDispatchProducesPairedSession(t *testing.T) {
	threeToolCalls := []llm.ToolCall{
		{ID: "tc_0", Name: "noop", Input: json.RawMessage(`{}`)},
		{ID: "tc_1", Name: "noop", Input: json.RawMessage(`{}`)},
		{ID: "tc_2", Name: "noop", Input: json.RawMessage(`{}`)},
	}
	llmFake := &threeToolCallLLM{toolCalls: threeToolCalls}

	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	exec := &cancelOnNthExecutor{n: 1, cancel: cancel, count: &count}

	r := &Runtime{
		LLM:      llmFake,
		Tools:    exec,
		Session:  session.NewSession("a", "k"),
		AgentID:  "a",
		Model:    "test-model",
		MaxTurns: 5,
	}

	events, err := r.Run(ctx, "go", nil)
	require.NoError(t, err)

	var toolResultEvents, abortedEvents int
	for ev := range events {
		switch ev.Type {
		case EventToolResult:
			toolResultEvents++
		case EventAborted:
			abortedEvents++
		}
	}
	require.Equal(t, 1, toolResultEvents, "exactly one EventToolResult expected (only tc_0 dispatched)")
	require.Equal(t, 1, abortedEvents, "exactly one EventAborted expected")

	// Walk the final session: every ToolCall must be immediately followed by a
	// ToolResult with the matching tool_call_id. Tools that were never
	// dispatched (tc_1, tc_2) must NOT appear in the session.
	entries := r.Session.View()
	var calls, results int
	for i, e := range entries {
		if e.Type == session.EntryTypeToolCall {
			calls++
			require.Less(t, i+1, len(entries), "ToolCallEntry has no following entry")
			next := entries[i+1]
			require.Equal(t, session.EntryTypeToolResult, next.Type, "ToolCall must be paired with ToolResult")
			results++

			// Decode call + result, assert ID match and that tc_0's result is marked aborted.
			var callData session.ToolCallData
			require.NoError(t, json.Unmarshal(e.Data, &callData))
			var resultData session.ToolResultData
			require.NoError(t, json.Unmarshal(next.Data, &resultData))
			require.Equal(t, callData.ID, resultData.ToolCallID, "tool_call_id must match across call/result pair")
			if callData.ID == "tc_0" {
				require.True(t, resultData.Aborted, "tc_0 result must be marked Aborted")
				require.True(t, resultData.IsError, "tc_0 result must be marked IsError")
			}
		}
	}
	require.Equal(t, calls, results, "every tool_use must have a paired tool_result")

	for _, e := range entries {
		if e.Type == session.EntryTypeToolCall {
			var d session.ToolCallData
			require.NoError(t, json.Unmarshal(e.Data, &d))
			require.NotEqual(t, "tc_1", d.ID, "undispatched tool tc_1 must not be saved")
			require.NotEqual(t, "tc_2", d.ID, "undispatched tool tc_2 must not be saved")
		}
	}
}

// threeToolCallLLM emits the configured tool_calls as one assistant turn,
// then EventDone. On the next call (after tool results, if the runtime
// loops back), emits only EventDone with no tool_calls so Run terminates.
//
// Embeds llmtest.Base for the boilerplate Models / NormalizeToolSchema methods.
type threeToolCallLLM struct {
	llmtest.Base
	toolCalls []llm.ToolCall
	calls     int
}

func (f *threeToolCallLLM) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	ch := make(chan llm.ChatEvent, len(f.toolCalls)*2+2)
	first := f.calls == 0
	f.calls++
	go func() {
		defer close(ch)
		if first {
			for i := range f.toolCalls {
				tc := f.toolCalls[i]
				ch <- llm.ChatEvent{Type: llm.EventToolCallStart, ToolCall: &tc}
				ch <- llm.ChatEvent{Type: llm.EventToolCallDone, ToolCall: &tc}
			}
		}
		ch <- llm.ChatEvent{Type: llm.EventDone}
	}()
	return ch, nil
}

// cancelOnNthExecutor cancels the provided context after the nth Execute call
// completes (1-indexed: n=1 means cancel after the first call). Output is "ok".
//
// NOTE: count is mutated without a lock — single-goroutine use only. Phase B
// parallel-dispatch tests must use atomic.Int32 or a mutex.
type cancelOnNthExecutor struct {
	n      int
	cancel context.CancelFunc
	count  *int
}

func (e *cancelOnNthExecutor) Execute(_ context.Context, _ string, _ json.RawMessage) (tools.ToolResult, error) {
	*e.count++
	if *e.count == e.n {
		e.cancel()
	}
	return tools.ToolResult{Output: "ok"}, nil
}
func (e *cancelOnNthExecutor) ToolDefs() []llm.ToolDef       { return []llm.ToolDef{{Name: "noop"}} }
func (e *cancelOnNthExecutor) Names() []string               { return []string{"noop"} }
func (e *cancelOnNthExecutor) Get(string) (tools.Tool, bool) { return nil, false }

// TestRun_ResumeAfterAbortIsValidAPIRequest persists a session aborted mid-
// dispatch, reassembles it through assembleMessages (the same code path
// /resume uses), and verifies the resulting llm.Message sequence is valid:
// every assistant message with N tool_calls is followed by N user messages
// whose ToolCallIDs cover every tc.ID in the assistant message.
//
// This guards against the pre-Phase-A bug where the pre-loop batch
// ToolCallEntry save left orphan tool_use entries in the session that
// produced an unpairable assembleMessages output and 400'd the next API
// call on /resume.
func TestRun_ResumeAfterAbortIsValidAPIRequest(t *testing.T) {
	threeCalls := []llm.ToolCall{
		{ID: "tc_0", Name: "noop", Input: json.RawMessage(`{}`)},
		{ID: "tc_1", Name: "noop", Input: json.RawMessage(`{}`)},
		{ID: "tc_2", Name: "noop", Input: json.RawMessage(`{}`)},
	}
	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	r := &Runtime{
		LLM:      &threeToolCallLLM{toolCalls: threeCalls},
		Tools:    &cancelOnNthExecutor{n: 1, cancel: cancel, count: &count},
		Session:  session.NewSession("a", "k"),
		AgentID:  "a",
		Model:    "test-model",
		MaxTurns: 5,
	}

	events, err := r.Run(ctx, "go", nil)
	require.NoError(t, err)

	// Count events to harden against double-emit / swallow regressions.
	var toolResultEvents, abortedEvents int
	for ev := range events {
		switch ev.Type {
		case EventToolResult:
			toolResultEvents++
		case EventAborted:
			abortedEvents++
		}
	}
	require.Equal(t, 1, toolResultEvents, "exactly one EventToolResult expected (only tc_0 was dispatched)")
	require.Equal(t, 1, abortedEvents, "exactly one EventAborted expected")

	// Simulate /resume: append the user's next prompt AFTER the abort. This
	// pushes any orphan tool_calls into the MIDDLE of history, where
	// assembleMessages's end-of-history rescue (injectMissingToolResults)
	// does NOT patch them. Under Phase A's atomic pairing the session
	// already has no orphans; under pre-Phase-A behavior the assembled
	// sequence would have unpaired tool_calls and this test would fail.
	r.Session.Append(session.UserMessageEntry("continue"))

	msgs := assembleMessages(r.Session.View())

	// Walk the assembled message sequence. For every assistant message with
	// tool_calls, the IMMEDIATELY following N messages must be user-role
	// tool_result messages whose ToolCallIDs collectively cover every tool_call.
	for i, m := range msgs {
		if len(m.ToolCalls) == 0 {
			continue
		}
		expected := map[string]bool{}
		for _, tc := range m.ToolCalls {
			expected[tc.ID] = true
		}
		end := i + 1 + len(m.ToolCalls)
		require.LessOrEqual(t, end, len(msgs),
			"assistant message at idx %d has %d tool_calls but only %d messages remain",
			i, len(m.ToolCalls), len(msgs)-i-1)

		for j := i + 1; j < end; j++ {
			require.Equal(t, "user", msgs[j].Role,
				"message at idx %d must be a user-role tool_result", j)
			require.NotEmpty(t, msgs[j].ToolCallID,
				"message at idx %d must carry a tool_call_id", j)
			require.True(t, expected[msgs[j].ToolCallID],
				"tool_call_id %q at idx %d does not match any tool_call in the preceding assistant message",
				msgs[j].ToolCallID, j)
			delete(expected, msgs[j].ToolCallID)
		}
		require.Empty(t, expected,
			"assistant message at idx %d has tool_calls without paired tool_results: %v",
			i, keysOf(expected))
	}
}

// keysOf returns the keys of a map[string]bool as a slice, for assertion messages.
func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestRun_DenyPolicyShortCircuitsExecution drives the full Run loop with a
// real StaticChecker that denies the tool the LLM tries to call. Verifies
// the executor is never invoked and the session contains a single paired
// call+deny entry.
func TestRun_DenyPolicyShortCircuitsExecution(t *testing.T) {
	denyChecker := tools.NewStaticChecker(map[string]tools.Policy{
		"a": {Deny: []string{"noop"}},
	})
	executorCalled := false
	exec := &recordingExecutor{
		called:   &executorCalled,
		toolDefs: []llm.ToolDef{{Name: "noop"}},
	}

	r := &Runtime{
		LLM:        &threeToolCallLLM{toolCalls: []llm.ToolCall{{ID: "tc_0", Name: "noop", Input: json.RawMessage(`{}`)}}},
		Tools:      exec,
		Session:    session.NewSession("a", "k"),
		AgentID:    "a",
		Permission: denyChecker,
		Model:      "test-model",
		MaxTurns:   2,
	}

	events, err := r.Run(context.Background(), "go", nil)
	require.NoError(t, err)

	var resultEvents int
	var lastResult *tools.ToolResult
	for ev := range events {
		if ev.Type == EventToolResult {
			resultEvents++
			lastResult = ev.Result
		}
	}

	require.Equal(t, 1, resultEvents, "expected one EventToolResult for the denied call")
	require.NotNil(t, lastResult)
	require.Contains(t, lastResult.Error, "not allowed for agent")
	require.False(t, executorCalled, "Execute must not run when StaticChecker denies")

	entries := r.Session.View()
	var hasCall, hasResult bool
	for _, e := range entries {
		if e.Type == session.EntryTypeToolCall {
			hasCall = true
		}
		if e.Type == session.EntryTypeToolResult {
			var trd session.ToolResultData
			require.NoError(t, json.Unmarshal(e.Data, &trd))
			require.True(t, trd.IsError, "denied result must be marked IsError")
			require.False(t, trd.Aborted, "denied result must NOT be marked Aborted")
			require.Contains(t, trd.Error, "not allowed for agent")
			hasResult = true
		}
	}
	require.True(t, hasCall, "session must contain the ToolCallEntry")
	require.True(t, hasResult, "session must contain the paired denial ToolResultEntry")
}

// recordingExecutor records whether Execute was called. Differs from
// fakeExecutor (in dispatch_test.go) by exposing a ToolDefs slice so the
// runtime advertises the test tool to the LLM.
type recordingExecutor struct {
	called   *bool
	toolDefs []llm.ToolDef
}

func (e *recordingExecutor) Execute(_ context.Context, _ string, _ json.RawMessage) (tools.ToolResult, error) {
	*e.called = true
	return tools.ToolResult{Output: "should not appear"}, nil
}
func (e *recordingExecutor) ToolDefs() []llm.ToolDef        { return e.toolDefs }
func (e *recordingExecutor) Names() []string                { return []string{"noop"} }
func (e *recordingExecutor) Get(string) (tools.Tool, bool)  { return nil, false }
