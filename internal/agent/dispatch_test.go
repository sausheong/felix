package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sausheong/cortex/connector/conversation"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/tools"
	"github.com/stretchr/testify/require"
)

// fakeExecutor implements tools.Executor for dispatchTool tests.
type fakeExecutor struct {
	called bool
	result tools.ToolResult
	err    error
	// onExecute, when non-nil, runs before returning. Useful for triggering
	// ctx cancel mid-execution.
	onExecute func(ctx context.Context)
}

func (f *fakeExecutor) Execute(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	f.called = true
	if f.onExecute != nil {
		f.onExecute(ctx)
	}
	return f.result, f.err
}
func (f *fakeExecutor) ToolDefs() []llm.ToolDef             { return nil }
func (f *fakeExecutor) Names() []string                     { return nil }
func (f *fakeExecutor) Get(name string) (tools.Tool, bool) { return nil, false }

// fakeChecker implements tools.PermissionChecker.
type fakeChecker struct {
	decision tools.Decision
}

func (c *fakeChecker) Check(_ context.Context, _, _ string, _ json.RawMessage) tools.Decision {
	return c.decision
}

// newDispatchRuntime returns a Runtime sufficient for dispatchTool tests.
func newDispatchRuntime(exec tools.Executor, perm tools.PermissionChecker) *Runtime {
	return &Runtime{
		AgentID:    "test_agent",
		Tools:      exec,
		Permission: perm,
		Session:    session.NewSession("test_agent", "test_key"),
	}
}

// sampleToolCall returns a representative llm.ToolCall.
func sampleToolCall() llm.ToolCall {
	return llm.ToolCall{ID: "tc_1", Name: "read_file", Input: json.RawMessage(`{"path":"/tmp/x"}`)}
}

// lastEntries returns the final n entries from the session for assertions.
func lastEntries(s *session.Session, n int) []session.SessionEntry {
	all := s.View()
	if len(all) < n {
		return all
	}
	return all[len(all)-n:]
}

// decodeToolResult unmarshals a ToolResult entry's data.
func decodeToolResult(t *testing.T, e session.SessionEntry) session.ToolResultData {
	t.Helper()
	require.Equal(t, session.EntryTypeToolResult, e.Type)
	var d session.ToolResultData
	require.NoError(t, json.Unmarshal(e.Data, &d))
	return d
}

func TestDispatchTool_CleanResult(t *testing.T) {
	exec := &fakeExecutor{
		result: tools.ToolResult{Output: "hello"},
	}
	r := newDispatchRuntime(exec, nil)

	result, aborted := r.dispatchTool(context.Background(), sampleToolCall(), nil)

	require.False(t, aborted)
	require.Equal(t, "hello", result.Output)
	require.Empty(t, result.Error)
	require.True(t, exec.called)

	entries := lastEntries(r.Session, 2)
	require.Equal(t, session.EntryTypeToolCall, entries[0].Type)
	d := decodeToolResult(t, entries[1])
	require.Equal(t, "hello", d.Output)
	require.False(t, d.IsError)
	require.False(t, d.Aborted)
}

func TestDispatchTool_ToolReturnsError(t *testing.T) {
	exec := &fakeExecutor{
		result: tools.ToolResult{Error: "file not found"},
	}
	r := newDispatchRuntime(exec, nil)

	result, aborted := r.dispatchTool(context.Background(), sampleToolCall(), nil)

	require.False(t, aborted)
	require.Equal(t, "file not found", result.Error)

	entries := lastEntries(r.Session, 2)
	d := decodeToolResult(t, entries[1])
	require.Equal(t, "file not found", d.Error)
	require.True(t, d.IsError)
	require.False(t, d.Aborted)
}

func TestDispatchTool_ExecuteReturnsGoError(t *testing.T) {
	exec := &fakeExecutor{
		err: errors.New("transport failure"),
	}
	r := newDispatchRuntime(exec, nil)

	result, aborted := r.dispatchTool(context.Background(), sampleToolCall(), nil)

	require.False(t, aborted)
	require.Contains(t, result.Error, "transport failure")

	entries := lastEntries(r.Session, 2)
	d := decodeToolResult(t, entries[1])
	require.Contains(t, d.Error, "transport failure")
	require.True(t, d.IsError)
}

func TestDispatchTool_PermissionDenied(t *testing.T) {
	exec := &fakeExecutor{
		result: tools.ToolResult{Output: "should not appear"},
	}
	perm := &fakeChecker{decision: tools.Decision{
		Behavior: tools.DecisionDeny,
		Reason:   "policy denies bash",
	}}
	r := newDispatchRuntime(exec, perm)

	result, aborted := r.dispatchTool(context.Background(), sampleToolCall(), nil)

	require.False(t, aborted)
	require.False(t, exec.called, "Execute must not run when denied")
	require.Equal(t, "policy denies bash", result.Error)

	entries := lastEntries(r.Session, 2)
	d := decodeToolResult(t, entries[1])
	require.Equal(t, "policy denies bash", d.Error)
	require.True(t, d.IsError)
	require.False(t, d.Aborted)
}

func TestDispatchTool_CancelledBeforeExecute(t *testing.T) {
	exec := &fakeExecutor{
		result: tools.ToolResult{Output: "should not appear"},
	}
	r := newDispatchRuntime(exec, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	result, aborted := r.dispatchTool(ctx, sampleToolCall(), nil)

	require.True(t, aborted)
	require.False(t, exec.called, "Execute must not run when ctx is already cancelled")
	require.Equal(t, "aborted by user", result.Error)

	entries := lastEntries(r.Session, 2)
	d := decodeToolResult(t, entries[1])
	require.True(t, d.Aborted)
	require.True(t, d.IsError)
	require.Equal(t, "aborted by user", d.Error)
}

func TestDispatchTool_CancelledAfterExecute(t *testing.T) {
	// Executor completes successfully but ctx is cancelled before dispatchTool
	// notices. Real output must be discarded; abort marker written.
	ctx, cancel := context.WithCancel(context.Background())

	exec := &fakeExecutor{
		result: tools.ToolResult{Output: "real output that should be dropped"},
		onExecute: func(_ context.Context) {
			cancel() // cancel during Execute
		},
	}
	r := newDispatchRuntime(exec, nil)

	result, aborted := r.dispatchTool(ctx, sampleToolCall(), nil)

	require.True(t, aborted)
	require.True(t, exec.called)
	require.Equal(t, "aborted by user", result.Error)
	require.Empty(t, result.Output)

	entries := lastEntries(r.Session, 2)
	d := decodeToolResult(t, entries[1])
	require.True(t, d.Aborted)
	require.Empty(t, d.Output)
}

// conversation import retained for use by future tests / fakes.
var _ = conversation.Message{}
