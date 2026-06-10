# Phase A — Agent Loop Foundations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor Felix's tool-dispatch path so every emitted `ToolCallEntry` is paired with a `ToolResultEntry` on every exit path, and a single `PermissionChecker` interface gates every tool call before execution.

**Architecture:** Extract per-tool execution from `runtime.go`'s Run goroutine into a single `dispatchTool(ctx, tc)` method that owns ToolCallEntry/ToolResultEntry pairing atomically. Introduce a `tools.PermissionChecker` interface; default impl wraps the existing per-agent `tools.Policy`. New `Aborted bool` field on `ToolResultData` distinguishes user-cancelled tool calls from real errors.

**Tech Stack:** Go (stdlib `context`, `encoding/json`, `time`), `github.com/stretchr/testify` for tests.

**Spec:** `docs/superpowers/specs/2026-04-29-agent-loop-phase-a-design.md`

---

### Task 1: Session schema — `Aborted` field + `AbortedToolResultEntry` helper

**Files:**
- Modify: `internal/session/session.go` (lines 51–58, 314–327)
- Test: `internal/session/session_test.go`

- [ ] **Step 1: Write failing test for `Aborted` field round-trip**

Add to `internal/session/session_test.go`:

```go
func TestToolResultData_AbortedFieldRoundTrip(t *testing.T) {
	entry := AbortedToolResultEntry("tc_abc")
	require.Equal(t, EntryTypeToolResult, entry.Type)

	var data ToolResultData
	require.NoError(t, json.Unmarshal(entry.Data, &data))

	require.Equal(t, "tc_abc", data.ToolCallID)
	require.Equal(t, "aborted by user", data.Error)
	require.True(t, data.IsError)
	require.True(t, data.Aborted)
	require.Empty(t, data.Output)
}
```

If `require` and `json` aren't already imported in this file, add them:
```go
import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/ -run TestToolResultData_AbortedFieldRoundTrip -v`
Expected: FAIL — `undefined: AbortedToolResultEntry` and (after that's fixed) `data.Aborted` field doesn't exist.

- [ ] **Step 3: Add `Aborted` field to `ToolResultData`**

In `internal/session/session.go`, modify the `ToolResultData` struct (around line 51):

```go
// ToolResultData holds the result of a tool call.
type ToolResultData struct {
	ToolCallID string      `json:"tool_call_id"`
	Output     string      `json:"output"`
	Error      string      `json:"error,omitempty"`
	IsError    bool        `json:"is_error,omitempty"`
	Aborted    bool        `json:"aborted,omitempty"` // true when the user cancelled mid-dispatch
	Images     []ImageData `json:"images,omitempty"`
}
```

- [ ] **Step 4: Add `AbortedToolResultEntry` constructor**

Add immediately after `ToolResultEntry` in `internal/session/session.go` (around line 327):

```go
// AbortedToolResultEntry creates a synthetic tool result for a tool call that
// was cancelled before completion. Pairs with a previously-appended ToolCallEntry
// to satisfy the API invariant that every tool_use has a matching tool_result.
func AbortedToolResultEntry(toolCallID string) SessionEntry {
	data, _ := json.Marshal(ToolResultData{
		ToolCallID: toolCallID,
		Error:      "aborted by user",
		IsError:    true,
		Aborted:    true,
	})
	return SessionEntry{
		Type: EntryTypeToolResult,
		Data: data,
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/session/ -run TestToolResultData_AbortedFieldRoundTrip -v`
Expected: PASS.

- [ ] **Step 6: Add backwards-compatibility test**

Add to `internal/session/session_test.go`:

```go
func TestToolResultData_OldJSONLWithoutAbortedField(t *testing.T) {
	// Simulate an old session entry written before the Aborted field existed.
	oldJSON := []byte(`{"tool_call_id":"tc_old","output":"hello","is_error":false}`)
	var data ToolResultData
	require.NoError(t, json.Unmarshal(oldJSON, &data))

	require.Equal(t, "tc_old", data.ToolCallID)
	require.Equal(t, "hello", data.Output)
	require.False(t, data.IsError)
	require.False(t, data.Aborted, "missing field must default to false")
}
```

- [ ] **Step 7: Run all session tests**

Run: `go test ./internal/session/ -v`
Expected: PASS for all tests, including the new ones.

- [ ] **Step 8: Commit**

```bash
git add internal/session/session.go internal/session/session_test.go
git commit -m "feat(session): add Aborted field to ToolResultData

Distinguishes user-cancelled tool calls from real errors so /resume
and the CLI can render them differently. Backwards-compatible: old
JSONL parses with Aborted defaulting to false."
```

---

### Task 2: `PermissionChecker` interface + `StaticChecker` impl

**Files:**
- Create: `internal/tools/permission.go`
- Test: `internal/tools/permission_test.go`

- [ ] **Step 1: Write failing tests for `StaticChecker`**

Create `internal/tools/permission_test.go`:

```go
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStaticChecker_AllowsListedTool(t *testing.T) {
	c := NewStaticChecker(map[string]Policy{
		"agent1": {Allow: []string{"read_file", "bash"}},
	})
	d := c.Check(context.Background(), "agent1", "read_file", json.RawMessage(`{}`))
	require.Equal(t, DecisionAllow, d.Behavior)
	require.Empty(t, d.Reason)
}

func TestStaticChecker_DeniesUnlistedTool(t *testing.T) {
	c := NewStaticChecker(map[string]Policy{
		"agent1": {Allow: []string{"read_file"}},
	})
	d := c.Check(context.Background(), "agent1", "bash", json.RawMessage(`{}`))
	require.Equal(t, DecisionDeny, d.Behavior)
	require.Contains(t, d.Reason, "bash")
	require.Contains(t, d.Reason, "agent1")
}

func TestStaticChecker_DeniesExplicitlyDeniedTool(t *testing.T) {
	c := NewStaticChecker(map[string]Policy{
		"agent1": {Deny: []string{"bash"}},
	})
	d := c.Check(context.Background(), "agent1", "bash", json.RawMessage(`{}`))
	require.Equal(t, DecisionDeny, d.Behavior)
}

func TestStaticChecker_UnknownAgentDefaultsToAllow(t *testing.T) {
	// An agent not present in the map is treated as allow-all. This matches
	// today's behavior when no policy is configured: tools just run.
	c := NewStaticChecker(map[string]Policy{})
	d := c.Check(context.Background(), "agent_unknown", "bash", json.RawMessage(`{}`))
	require.Equal(t, DecisionAllow, d.Behavior)
}

func TestStaticChecker_NilCheckerNotPossible(t *testing.T) {
	// Sanity: ensure NewStaticChecker handles a nil map by treating it as empty.
	c := NewStaticChecker(nil)
	d := c.Check(context.Background(), "any", "any", nil)
	require.Equal(t, DecisionAllow, d.Behavior)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tools/ -run TestStaticChecker -v`
Expected: FAIL — `undefined: StaticChecker`, `undefined: NewStaticChecker`, `undefined: DecisionAllow`, `undefined: DecisionDeny`.

- [ ] **Step 3: Create `internal/tools/permission.go`**

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// DecisionBehavior is the outcome of a permission check.
type DecisionBehavior int

const (
	DecisionAllow DecisionBehavior = iota
	DecisionDeny
)

// Decision is the result of a PermissionChecker.Check call. Reason is surfaced
// into the tool result when the behavior is Deny; it is ignored when Allow.
type Decision struct {
	Behavior DecisionBehavior
	Reason   string
}

// PermissionChecker decides whether a tool call may proceed. It is consulted
// once per tool invocation, after the call's input has been parsed but before
// the tool's Execute runs. Implementations must be safe for concurrent use.
//
// The input is passed (and may be ignored by simple implementations) so future
// input-aware checkers can implement the same interface without a signature
// change.
type PermissionChecker interface {
	Check(ctx context.Context, agentID, toolName string, input json.RawMessage) Decision
}

// StaticChecker is the default PermissionChecker. It wraps existing per-agent
// tools.Policy values keyed by agent ID. An agent not present in the map is
// treated as allow-all (matches today's behavior when no policy is configured).
type StaticChecker struct {
	perAgent map[string]Policy
}

// NewStaticChecker builds a StaticChecker. A nil or empty map means allow-all
// for every agent.
func NewStaticChecker(perAgent map[string]Policy) *StaticChecker {
	if perAgent == nil {
		perAgent = map[string]Policy{}
	}
	return &StaticChecker{perAgent: perAgent}
}

// Check implements PermissionChecker.
func (c *StaticChecker) Check(_ context.Context, agentID, toolName string, _ json.RawMessage) Decision {
	p, ok := c.perAgent[agentID]
	if !ok || p.IsAllowed(toolName) {
		return Decision{Behavior: DecisionAllow}
	}
	return Decision{
		Behavior: DecisionDeny,
		Reason:   fmt.Sprintf("tool %q is not allowed for agent %q", toolName, agentID),
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tools/ -run TestStaticChecker -v`
Expected: PASS — all 5 tests.

- [ ] **Step 5: Verify the rest of the package still compiles and passes**

Run: `go test ./internal/tools/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tools/permission.go internal/tools/permission_test.go
git commit -m "feat(tools): add PermissionChecker interface + StaticChecker

Single-seam permission gate consulted at tool dispatch time.
StaticChecker wraps existing per-agent tools.Policy. Foundation
for Phase A's runtime.dispatchTool refactor."
```

---

### Task 3: `dispatchTool` method on `Runtime` (TDD'd in isolation)

**Files:**
- Modify: `internal/agent/runtime.go` (add field + method, do not touch Run yet)
- Test: `internal/agent/dispatch_test.go` (new file)

This task develops `dispatchTool` in isolation with full TDD coverage of every exit-matrix row. The Run goroutine still uses its current per-tool block — that's wired in Task 4.

- [ ] **Step 1: Add `Permission` field and a stub `dispatchTool` method**

In `internal/agent/runtime.go`, add to the imports (if not already present):
```go
"github.com/sausheong/cortex/connector/conversation"
```
(Already imported — confirm it stays.)

Add a new field to the `Runtime` struct (after the existing `Compaction` field, around line 63):

```go
// Permission gates tool execution at dispatch time. nil → allow-all
// (matches the no-policy default).
Permission tools.PermissionChecker
```

Add a stub method at the bottom of the file (after `RunSync`):

```go
// dispatchTool executes one tool call with strict tool_use ↔ tool_result
// pairing. It always appends a ToolCallEntry then exactly one ToolResultEntry
// (real, error, denial, or aborted) before returning, on every code path.
//
// The returned tools.ToolResult is for event emission to the caller. When
// aborted=true, the caller MUST stop dispatching subsequent tool calls in
// this turn and emit EventAborted.
//
// cortexThread, when non-nil, is appended to atomically alongside the
// session writes — both call+result land or neither does.
func (r *Runtime) dispatchTool(
	ctx context.Context,
	tc llm.ToolCall,
	cortexThread *[]conversation.Message,
) (result tools.ToolResult, aborted bool) {
	panic("not implemented")
}
```

Run `go build ./...` to confirm the file still compiles.

- [ ] **Step 2: Create the test file with shared fakes**

Create `internal/agent/dispatch_test.go`:

```go
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
func (f *fakeExecutor) ToolDefs() []llm.ToolDef         { return nil }
func (f *fakeExecutor) Names() []string                 { return nil }
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
```

- [ ] **Step 3: Write failing test — clean execution path**

Append to `internal/agent/dispatch_test.go`:

```go
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
```

- [ ] **Step 4: Run, verify it fails**

Run: `go test ./internal/agent/ -run TestDispatchTool_CleanResult -v`
Expected: FAIL — panic "not implemented".

- [ ] **Step 5: Implement `dispatchTool` for the clean path**

Replace the `dispatchTool` body in `internal/agent/runtime.go`:

```go
func (r *Runtime) dispatchTool(
	ctx context.Context,
	tc llm.ToolCall,
	cortexThread *[]conversation.Message,
) (result tools.ToolResult, aborted bool) {
	// 1. Save tool call (paired ownership begins here).
	r.Session.Append(session.ToolCallEntry(tc.ID, tc.Name, tc.Input))
	if cortexThread != nil {
		*cortexThread = append(*cortexThread, conversation.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("[tool: %s]\n%s", tc.Name, string(tc.Input)),
		})
	}

	// 2. Execute. (Permission gate + cancel checks added in later steps.)
	result, err := r.Tools.Execute(ctx, tc.Name, tc.Input)
	if err != nil {
		result = tools.ToolResult{Error: err.Error()}
	}

	// 3. Save paired tool result.
	imgData := convertToolResultImages(result.Images)
	r.Session.Append(session.ToolResultEntry(tc.ID, result.Output, result.Error, imgData))
	if cortexThread != nil {
		content := result.Output
		if result.Error != "" {
			content = "[error] " + result.Error
		}
		*cortexThread = append(*cortexThread, conversation.Message{Role: "user", Content: content})
	}

	return result, false
}

// convertToolResultImages adapts tool image attachments to session ImageData.
func convertToolResultImages(imgs []llm.ImageContent) []session.ImageData {
	if len(imgs) == 0 {
		return nil
	}
	out := make([]session.ImageData, 0, len(imgs))
	for _, img := range imgs {
		out = append(out, session.ImageData{
			MimeType: img.MimeType,
			Data:     base64.StdEncoding.EncodeToString(img.Data),
		})
	}
	return out
}
```

The imports `fmt` and `base64` should already be present in runtime.go.

- [ ] **Step 6: Run, verify it passes**

Run: `go test ./internal/agent/ -run TestDispatchTool_CleanResult -v`
Expected: PASS.

- [ ] **Step 7: Write failing test — tool result carries `Error`**

Append to `internal/agent/dispatch_test.go`:

```go
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
```

- [ ] **Step 8: Run, verify it passes (no impl change needed)**

Run: `go test ./internal/agent/ -run TestDispatchTool_ToolReturnsError -v`
Expected: PASS — current impl already handles this.

- [ ] **Step 9: Write failing test — Executor returns Go error**

Append to `internal/agent/dispatch_test.go`:

```go
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
```

- [ ] **Step 10: Run, verify it passes (no impl change needed)**

Run: `go test ./internal/agent/ -run TestDispatchTool_ExecuteReturnsGoError -v`
Expected: PASS.

- [ ] **Step 11: Write failing test — Permission denies**

Append to `internal/agent/dispatch_test.go`:

```go
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
```

- [ ] **Step 12: Run, verify it fails**

Run: `go test ./internal/agent/ -run TestDispatchTool_PermissionDenied -v`
Expected: FAIL — Execute was called (no gate yet).

- [ ] **Step 13: Add the permission gate to `dispatchTool`**

Modify `dispatchTool` in `internal/agent/runtime.go` — insert the gate after the tool-call save and Cortex-call append, before `Execute`:

```go
func (r *Runtime) dispatchTool(
	ctx context.Context,
	tc llm.ToolCall,
	cortexThread *[]conversation.Message,
) (result tools.ToolResult, aborted bool) {
	// 1. Save tool call.
	r.Session.Append(session.ToolCallEntry(tc.ID, tc.Name, tc.Input))
	if cortexThread != nil {
		*cortexThread = append(*cortexThread, conversation.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("[tool: %s]\n%s", tc.Name, string(tc.Input)),
		})
	}

	// 2. Permission gate.
	if r.Permission != nil {
		if d := r.Permission.Check(ctx, r.AgentID, tc.Name, tc.Input); d.Behavior == tools.DecisionDeny {
			return r.appendDenialResult(tc.ID, d.Reason, cortexThread), false
		}
	}

	// 3. Execute.
	result, err := r.Tools.Execute(ctx, tc.Name, tc.Input)
	if err != nil {
		result = tools.ToolResult{Error: err.Error()}
	}

	// 4. Save paired tool result.
	imgData := convertToolResultImages(result.Images)
	r.Session.Append(session.ToolResultEntry(tc.ID, result.Output, result.Error, imgData))
	if cortexThread != nil {
		content := result.Output
		if result.Error != "" {
			content = "[error] " + result.Error
		}
		*cortexThread = append(*cortexThread, conversation.Message{Role: "user", Content: content})
	}

	return result, false
}

// appendDenialResult writes the tool-result entry for a denied tool call and
// returns a tools.ToolResult mirroring it. Centralised so the deny path stays
// consistent with the result-emit format.
func (r *Runtime) appendDenialResult(toolCallID, reason string, cortexThread *[]conversation.Message) tools.ToolResult {
	r.Session.Append(session.ToolResultEntry(toolCallID, "", reason, nil))
	if cortexThread != nil {
		*cortexThread = append(*cortexThread, conversation.Message{
			Role: "user", Content: "[error] " + reason,
		})
	}
	return tools.ToolResult{Error: reason}
}
```

- [ ] **Step 14: Run, verify deny test passes**

Run: `go test ./internal/agent/ -run TestDispatchTool_PermissionDenied -v`
Expected: PASS.

- [ ] **Step 15: Write failing test — pre-execute cancellation**

Append to `internal/agent/dispatch_test.go`:

```go
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
```

- [ ] **Step 16: Run, verify it fails**

Run: `go test ./internal/agent/ -run TestDispatchTool_CancelledBeforeExecute -v`
Expected: FAIL — Execute was called.

- [ ] **Step 17: Add pre-execute cancel check + abort helper**

In `internal/agent/runtime.go`, modify `dispatchTool` to insert a cancel check after the permission gate and before `Execute`. Also add a helper for the aborted-result path. The full updated method:

```go
func (r *Runtime) dispatchTool(
	ctx context.Context,
	tc llm.ToolCall,
	cortexThread *[]conversation.Message,
) (result tools.ToolResult, aborted bool) {
	// 1. Save tool call.
	r.Session.Append(session.ToolCallEntry(tc.ID, tc.Name, tc.Input))
	if cortexThread != nil {
		*cortexThread = append(*cortexThread, conversation.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("[tool: %s]\n%s", tc.Name, string(tc.Input)),
		})
	}

	// 2. Permission gate.
	if r.Permission != nil {
		if d := r.Permission.Check(ctx, r.AgentID, tc.Name, tc.Input); d.Behavior == tools.DecisionDeny {
			return r.appendDenialResult(tc.ID, d.Reason, cortexThread), false
		}
	}

	// 3. Pre-execute cancel check.
	if ctx.Err() != nil {
		return r.appendAbortedResult(tc.ID, cortexThread), true
	}

	// 4. Execute.
	result, err := r.Tools.Execute(ctx, tc.Name, tc.Input)
	if err != nil {
		result = tools.ToolResult{Error: err.Error()}
	}

	// 5. Post-execute cancel check. The user pressed Ctrl-C — discard real output.
	if ctx.Err() != nil && result.Error == "" {
		return r.appendAbortedResult(tc.ID, cortexThread), true
	}

	// 6. Save paired tool result.
	imgData := convertToolResultImages(result.Images)
	r.Session.Append(session.ToolResultEntry(tc.ID, result.Output, result.Error, imgData))
	if cortexThread != nil {
		content := result.Output
		if result.Error != "" {
			content = "[error] " + result.Error
		}
		*cortexThread = append(*cortexThread, conversation.Message{Role: "user", Content: content})
	}

	return result, false
}

// appendAbortedResult writes the synthetic abort entry and returns the
// matching tools.ToolResult. Used for both pre- and post-execute cancellation.
func (r *Runtime) appendAbortedResult(toolCallID string, cortexThread *[]conversation.Message) tools.ToolResult {
	r.Session.Append(session.AbortedToolResultEntry(toolCallID))
	if cortexThread != nil {
		*cortexThread = append(*cortexThread, conversation.Message{
			Role: "user", Content: "[error] aborted by user",
		})
	}
	return tools.ToolResult{Error: "aborted by user"}
}
```

- [ ] **Step 18: Run, verify pre-execute cancel test passes**

Run: `go test ./internal/agent/ -run TestDispatchTool_CancelledBeforeExecute -v`
Expected: PASS.

- [ ] **Step 19: Write failing test — post-execute cancellation**

Append to `internal/agent/dispatch_test.go`:

```go
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
```

- [ ] **Step 20: Run, verify it passes (impl already handles it)**

Run: `go test ./internal/agent/ -run TestDispatchTool_CancelledAfterExecute -v`
Expected: PASS.

- [ ] **Step 21: Verify the entire dispatch test suite passes**

Run: `go test ./internal/agent/ -run TestDispatchTool -v`
Expected: PASS for all 6 tests.

- [ ] **Step 22: Run the full test suite to confirm no regressions**

Run: `go test ./...`
Expected: PASS. (The Run loop has not been touched yet — its existing tests continue to pass against the unchanged Run path.)

- [ ] **Step 23: Commit**

```bash
git add internal/agent/runtime.go internal/agent/dispatch_test.go
git commit -m "feat(agent): add Runtime.dispatchTool with paired session writes

Single-call helper that owns ToolCallEntry/ToolResultEntry pairing
atomically. Handles all six exit paths: clean, tool-error, executor-
error, deny, pre-execute cancel, post-execute cancel. Not yet wired
into Run — that follows in the next commit."
```

---

### Task 4: Wire `dispatchTool` into `Run`, delete dead code

**Files:**
- Modify: `internal/agent/runtime.go` (Run goroutine, lines ~399–472)
- Test: `internal/agent/agent_test.go`

- [ ] **Step 1: Write failing integration test for paired-session-on-abort**

Append to `internal/agent/agent_test.go`:

```go
// TestRun_AbortMidDispatchProducesPairedSession verifies that when ctx is
// cancelled while iterating over a multi-tool batch, the loop breaks at the
// first abort and the session ends with consistent tool_use/tool_result
// pairing. Tools never dispatched do NOT appear in the session.
func TestRun_AbortMidDispatchProducesPairedSession(t *testing.T) {
	// Build a fake LLM that emits 3 tool calls in one assistant turn.
	threeToolCalls := []llm.ToolCall{
		{ID: "tc_0", Name: "noop", Input: json.RawMessage(`{}`)},
		{ID: "tc_1", Name: "noop", Input: json.RawMessage(`{}`)},
		{ID: "tc_2", Name: "noop", Input: json.RawMessage(`{}`)},
	}
	llmFake := &fakeStreamingLLM{
		toolCalls: threeToolCalls,
	}
	// Executor whose noop tool cancels ctx after the first call completes.
	ctx, cancel := context.WithCancel(context.Background())
	callCount := 0
	exec := &cancelOnNthExecutor{
		n:      1,
		cancel: cancel,
		count:  &callCount,
	}

	r := &Runtime{
		LLM:       llmFake,
		Tools:     exec,
		Session:   session.NewSession("a", "k"),
		AgentID:   "a",
		Model:     "test-model",
		MaxTurns:  5,
	}

	events, err := r.Run(ctx, "go", nil)
	require.NoError(t, err)
	for range events { /* drain */ }

	// Walk the final session: every ToolCall must be immediately followed by a
	// ToolResult with the matching tool_call_id.
	entries := r.Session.View()
	var calls, results int
	for i, e := range entries {
		if e.Type == session.EntryTypeToolCall {
			calls++
			require.Less(t, i+1, len(entries), "ToolCallEntry has no following entry")
			next := entries[i+1]
			require.Equal(t, session.EntryTypeToolResult, next.Type, "ToolCall must be paired with ToolResult")
			results++
		}
	}
	require.Equal(t, calls, results, "every tool_use must have a paired tool_result")
	// tc_2 was never dispatched (loop broke after tc_1's abort), so it must NOT
	// appear in the session.
	for _, e := range entries {
		if e.Type == session.EntryTypeToolCall {
			var d session.ToolCallData
			require.NoError(t, json.Unmarshal(e.Data, &d))
			require.NotEqual(t, "tc_2", d.ID, "undispatched tool must not be saved")
		}
	}
}

// cancelOnNthExecutor cancels the provided context after the nth Execute call
// completes (1-indexed: n=1 means cancel after the first call).
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
func (e *cancelOnNthExecutor) ToolDefs() []llm.ToolDef          { return []llm.ToolDef{{Name: "noop"}} }
func (e *cancelOnNthExecutor) Names() []string                  { return []string{"noop"} }
func (e *cancelOnNthExecutor) Get(string) (tools.Tool, bool)    { return nil, false }
```

If `agent_test.go` doesn't already define `fakeStreamingLLM`, check the file for an equivalent and reuse it. If not present, add this minimal one:

```go
// fakeStreamingLLM yields one assistant turn with the configured tool_calls,
// then a Done event. The second turn (after tool results) yields no tool
// calls so Run terminates with EventDone.
type fakeStreamingLLM struct {
	toolCalls    []llm.ToolCall
	turnsServed  int
}

func (f *fakeStreamingLLM) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, len(f.toolCalls)+2)
	if f.turnsServed == 0 {
		for _, tc := range f.toolCalls {
			tc := tc
			ch <- llm.StreamEvent{Type: llm.EventToolCallStart, ToolCall: &tc}
			ch <- llm.StreamEvent{Type: llm.EventToolCallDone, ToolCall: &tc}
		}
	}
	ch <- llm.StreamEvent{Type: llm.EventDone}
	close(ch)
	f.turnsServed++
	return ch, nil
}
func (f *fakeStreamingLLM) Embed(context.Context, llm.EmbedRequest) (llm.EmbedResponse, error) {
	return llm.EmbedResponse{}, nil
}
func (f *fakeStreamingLLM) Models(context.Context) ([]string, error) { return nil, nil }
func (f *fakeStreamingLLM) NormalizeToolSchema(d []llm.ToolDef) ([]llm.ToolDef, []llm.SchemaDiagnostic) {
	return d, nil
}
```

(If your `llm.LLMProvider` interface differs, mirror its real signatures from `internal/llm/llm.go`.)

- [ ] **Step 2: Run, verify it fails**

Run: `go test ./internal/agent/ -run TestRun_AbortMidDispatchProducesPairedSession -v`
Expected: FAIL — currently the pre-loop batch save at `runtime.go:399–407` writes `tc_2` to the session before any Execute runs, leaving it as an orphan ToolCall after the abort.

- [ ] **Step 3: Replace the per-tool block in `Run` with a `dispatchTool` loop**

In `internal/agent/runtime.go`, locate the block from line ~399 ("Save tool calls to session and accumulate in Cortex thread") through line ~472 (end of the per-tool execute loop). Replace **the entire block** with:

```go
// Dispatch each tool call. dispatchTool guarantees one ToolCallEntry +
// one ToolResultEntry per tc, on every exit path (clean / error /
// deny / aborted). Cortex thread accumulation is atomic alongside the
// session writes — both land or neither does.
for i := range toolCalls {
	tc := toolCalls[i]
	result, aborted := r.dispatchTool(ctx, tc, cortexThreadPtr(r.Cortex, &thread))

	tr.Mark("tool.exec",
		"turn", turn,
		"tool", tc.Name,
		"err", result.Error != "",
		"output_chars", len(result.Output),
		"aborted", aborted)

	if result.Error != "" {
		slog.Warn("tool error", "tool", tc.Name, "id", tc.ID, "error", result.Error)
	} else {
		outPreview := result.Output
		if len(outPreview) > 500 {
			outPreview = outPreview[:500] + "...(truncated)"
		}
		slog.Debug("tool result", "tool", tc.Name, "id", tc.ID, "output_len", len(result.Output), "output", outPreview)
	}

	events <- AgentEvent{
		Type:     EventToolResult,
		ToolCall: &tc,
		Result:   &result,
	}

	if aborted {
		events <- AgentEvent{Type: EventAborted}
		return
	}
}
```

Add the helper at the bottom of the file (just above `RunSync`):

```go
// cortexThreadPtr returns a pointer to the cortex thread if Cortex is enabled,
// else nil. dispatchTool treats a nil pointer as "skip cortex updates".
func cortexThreadPtr(cx *cortex.Cortex, thread *[]conversation.Message) *[]conversation.Message {
	if cx == nil {
		return nil
	}
	return thread
}
```

- [ ] **Step 4: Run the integration test, verify it passes**

Run: `go test ./internal/agent/ -run TestRun_AbortMidDispatchProducesPairedSession -v`
Expected: PASS.

- [ ] **Step 5: Run the full agent package tests**

Run: `go test ./internal/agent/ -v`
Expected: PASS for all tests.

- [ ] **Step 6: Run the full test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/runtime.go internal/agent/agent_test.go
git commit -m "refactor(agent): wire dispatchTool into Run loop

Replaces the pre-loop batch ToolCallEntry save and per-tool ctx
checks with a single dispatchTool call. Aborted dispatches now
produce a synthetic ToolResultEntry instead of orphaning the
ToolCallEntry. Cortex thread updates move into dispatchTool so
they stay atomic with session writes."
```

---

### Task 5: Wire `StaticChecker` into startup

**Files:**
- Modify: `internal/startup/startup.go`

- [ ] **Step 1: Locate Runtime construction sites in startup**

Run: `grep -n "Runtime{" internal/startup/startup.go`

Expected output: lines where `agent.Runtime{...}` is constructed (likely 1–3 sites — main agent, heartbeat agent, cron agent).

- [ ] **Step 2: Build a single `StaticChecker` from config and inject it**

In `internal/startup/startup.go`, find the function that loads the config and constructs runtimes. Near the top of that function (after `cfg` is loaded), build the per-agent policy map:

```go
// Build a single PermissionChecker from per-agent tool policies. Injected
// into every Runtime constructed below — same checker, different agent IDs.
agentPolicies := map[string]tools.Policy{}
for _, a := range cfg.Agents {
	agentPolicies[a.ID] = tools.Policy{
		Allow: a.Tools.Allow,
		Deny:  a.Tools.Deny,
	}
}
permission := tools.NewStaticChecker(agentPolicies)
```

(Add the `tools` import if missing — likely already imported.)

For **every** `agent.Runtime{...}` literal in this file, add a `Permission: permission,` field. Example:

```go
rt := &agent.Runtime{
	LLM:        llm,
	Tools:      toolReg,
	Session:    sess,
	AgentID:    a.ID,
	// ...existing fields
	Permission: permission, // NEW
}
```

- [ ] **Step 3: Build and run**

Run: `go build ./...`
Expected: success.

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/startup/startup.go
git commit -m "feat(startup): inject StaticChecker into every Runtime

Builds one PermissionChecker from per-agent tools.Policy values in
felix.json5 and shares it across the main, heartbeat, and cron
runtimes. Same checker, different agent IDs."
```

---

### Task 6: Integration test — resume after abort produces valid API request

**Files:**
- Test: `internal/agent/agent_test.go`

This test guards the most important real-world consequence of Phase A: a session that was aborted mid-tool can be reloaded and immediately produce a valid Anthropic API request, with no orphan `tool_use` blocks.

- [ ] **Step 1: Locate `assembleMessages` to understand the API-shape contract**

Run: `grep -n "func assembleMessages" internal/agent/context.go`

Read the function briefly to confirm it converts `[]session.SessionEntry` → `[]llm.Message` and that the API requires every `tool_use` content block to be followed by a matching `tool_result` block in the next message.

- [ ] **Step 2: Write the test**

Append to `internal/agent/agent_test.go`:

```go
// TestRun_ResumeAfterAbortIsValidAPIRequest persists a session aborted mid-
// dispatch, reloads it, and verifies that assembleMessages produces a sequence
// where every tool_use has a matching tool_result. This guards against the
// pre-Phase-A bug where pre-loop batch ToolCallEntry saves left orphan
// tool_use blocks that 400'd the next API call on /resume.
func TestRun_ResumeAfterAbortIsValidAPIRequest(t *testing.T) {
	threeCalls := []llm.ToolCall{
		{ID: "tc_0", Name: "noop", Input: json.RawMessage(`{}`)},
		{ID: "tc_1", Name: "noop", Input: json.RawMessage(`{}`)},
		{ID: "tc_2", Name: "noop", Input: json.RawMessage(`{}`)},
	}
	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	r := &Runtime{
		LLM:      &fakeStreamingLLM{toolCalls: threeCalls},
		Tools:    &cancelOnNthExecutor{n: 1, cancel: cancel, count: &count},
		Session:  session.NewSession("a", "k"),
		AgentID:  "a",
		Model:    "test-model",
		MaxTurns: 5,
	}

	events, err := r.Run(ctx, "go", nil)
	require.NoError(t, err)
	for range events { /* drain */ }

	// Simulate /resume: feed the session view back into assembleMessages.
	msgs := assembleMessages(r.Session.View())

	// Walk msgs and verify every tool_use is followed by a tool_result with
	// the matching id. Implementation detail: we look at msgs[i] for any
	// tool_use IDs and require msgs[i+1] to contain a matching tool_result.
	for i, m := range msgs {
		toolUseIDs := extractToolUseIDs(m)
		if len(toolUseIDs) == 0 {
			continue
		}
		require.Less(t, i+1, len(msgs), "tool_use message has no follow-up")
		toolResultIDs := extractToolResultIDs(msgs[i+1])
		for _, id := range toolUseIDs {
			require.Contains(t, toolResultIDs, id,
				"tool_use %s lacks matching tool_result in next message", id)
		}
	}
}

// extractToolUseIDs returns the IDs of any tool_use blocks in msg. Adjust to
// match Felix's llm.Message shape: if Felix represents tool calls as a
// dedicated field rather than content blocks, return those IDs instead.
func extractToolUseIDs(msg llm.Message) []string {
	var ids []string
	for _, tc := range msg.ToolCalls {
		ids = append(ids, tc.ID)
	}
	return ids
}

// extractToolResultIDs returns the tool_call_ids of any tool_result blocks in msg.
func extractToolResultIDs(msg llm.Message) []string {
	var ids []string
	for _, tr := range msg.ToolResults {
		ids = append(ids, tr.ToolCallID)
	}
	return ids
}
```

If `llm.Message` uses different field names than `ToolCalls` / `ToolResults`, run `grep -n "type Message" internal/llm/*.go` and adjust the helpers to match the actual fields.

- [ ] **Step 3: Run, verify it passes**

Run: `go test ./internal/agent/ -run TestRun_ResumeAfterAbortIsValidAPIRequest -v`
Expected: PASS — every saved tool_use has a paired tool_result.

- [ ] **Step 4: Run the full test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/agent_test.go
git commit -m "test(agent): resume after abort produces valid API request

Guards against orphan tool_use blocks in the session after a mid-
dispatch abort. Reproduces the pre-Phase-A bug condition (3-tool
batch, abort after first) and asserts assembleMessages produces a
valid (paired) request."
```

---

## Plan Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| Architecture: extract `dispatchTool` | T3 (isolation) + T4 (wiring) |
| Architecture: pre-loop batch save deleted | T4, Step 3 |
| Permission gate: `PermissionChecker` interface | T2 |
| Permission gate: `StaticChecker` default impl | T2 |
| Permission gate: nil-safe Permission field | T3, Step 1 |
| Permission gate: wired in `Run` via dispatch | T3, Step 13 + T4 |
| Permission gate: wired in startup | T5 |
| Permission gate: FilteredRegistry retained as defence | No code change required (existing) |
| Invariant: `Aborted` field on ToolResultData | T1 |
| Invariant: `AbortedToolResultEntry` constructor | T1 |
| Invariant: pre-execute cancel synth | T3, Step 17 |
| Invariant: post-execute cancel discards real output | T3, Step 17 (post-check) + T3, Step 19 (test) |
| Invariant: deny is paired (not aborted) | T3, Steps 11–14 |
| Invariant: every exit-matrix row covered | T3, Steps 3–20 (six tests) |
| Cortex thread atomicity | T3, Step 5 (initial) + Step 17 (final) + T4, Step 3 (helper) |
| Backwards compat (old JSONL parses) | T1, Step 6 |
| Resume-after-abort integration | T6 |

**Placeholder scan:** No `TBD`, no "implement later", every step shows the actual code. The one judgement call (LLM Message field names in T6, Step 2) is flanked by an explicit fallback instruction.

**Type consistency:**
- `dispatchTool` signature: `(ctx context.Context, tc llm.ToolCall, cortexThread *[]conversation.Message) (result tools.ToolResult, aborted bool)` — used identically in all tasks.
- `Decision.Behavior` field name and `DecisionAllow`/`DecisionDeny` constants — consistent across T2 and T3.
- `AbortedToolResultEntry(toolCallID string)` signature — consistent T1 and T3.
- `cortexThreadPtr` helper introduced in T4, used in the Run loop only.
- Test helpers (`fakeExecutor`, `fakeChecker`, `cancelOnNthExecutor`, `fakeStreamingLLM`) are defined where first used.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-04-29-agent-loop-phase-a.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
