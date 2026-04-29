package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// stubRunner is a SubagentRunner whose Run returns a pre-built event channel.
// Used to drive TaskTool through specific event sequences without spinning up
// a real agent Runtime.
type stubRunner struct {
	events []AgentEventLike
	runErr error
}

func (s *stubRunner) Run(_ context.Context, _ string) (<-chan AgentEventLike, error) {
	if s.runErr != nil {
		return nil, s.runErr
	}
	ch := make(chan AgentEventLike, len(s.events))
	for _, ev := range s.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func TestTaskTool_UnknownAgentReturnsError(t *testing.T) {
	factory := SubagentFactory(func(_ context.Context, _ string, _ int) (SubagentRunner, error) {
		t.Fatal("factory should not be called for unknown agent_id")
		return nil, nil
	})
	tt := NewTaskTool(factory, 0, map[string]string{"researcher": "Web research"})

	in := json.RawMessage(`{"agent_id": "ghost", "prompt": "hi"}`)
	res, err := tt.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected ToolResult.Error to be set")
	}
	// Should mention what was rejected and what's allowed.
	if !strings.Contains(res.Error, "ghost") || !strings.Contains(res.Error, "researcher") {
		t.Errorf("error should name the rejected agent and the eligible ones: %q", res.Error)
	}
}

func TestTaskTool_DelegatesToFactoryAndCapturesText(t *testing.T) {
	runner := &stubRunner{events: []AgentEventLike{
		{Text: "hello "},
		{Text: "world"},
		{Done: true},
	}}
	factory := SubagentFactory(func(_ context.Context, agentID string, parentDepth int) (SubagentRunner, error) {
		if agentID != "researcher" {
			t.Errorf("factory called with %q, want researcher", agentID)
		}
		if parentDepth != 1 {
			t.Errorf("parentDepth=%d, want 1", parentDepth)
		}
		return runner, nil
	})
	tt := NewTaskTool(factory, 1, map[string]string{"researcher": "Web research"})

	in := json.RawMessage(`{"agent_id": "researcher", "prompt": "summarize Go"}`)
	res, err := tt.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected tool error: %s", res.Error)
	}
	if res.Output != "hello world" {
		t.Errorf("got Output=%q, want %q", res.Output, "hello world")
	}
}

func TestTaskTool_SubagentAbortReturnsErrorResult(t *testing.T) {
	runner := &stubRunner{events: []AgentEventLike{
		{Text: "partial..."},
		{Aborted: true},
	}}
	factory := SubagentFactory(func(_ context.Context, _ string, _ int) (SubagentRunner, error) {
		return runner, nil
	})
	tt := NewTaskTool(factory, 0, map[string]string{"r": "desc"})

	res, _ := tt.Execute(context.Background(), json.RawMessage(`{"agent_id":"r","prompt":"go"}`))
	if res.Error == "" || !strings.Contains(res.Error, "abort") {
		t.Errorf("expected abort error, got: %q", res.Error)
	}
}

func TestTaskTool_FactoryDepthErrorPassesThrough(t *testing.T) {
	factory := SubagentFactory(func(_ context.Context, _ string, _ int) (SubagentRunner, error) {
		return nil, errors.New("subagent depth limit 3 reached")
	})
	tt := NewTaskTool(factory, 3, map[string]string{"r": "desc"})

	res, _ := tt.Execute(context.Background(), json.RawMessage(`{"agent_id":"r","prompt":"go"}`))
	if res.Error == "" || !strings.Contains(res.Error, "depth limit") {
		t.Errorf("expected depth-limit error, got: %q", res.Error)
	}
}

func TestTaskTool_MalformedInputReturnsError(t *testing.T) {
	tt := NewTaskTool(func(_ context.Context, _ string, _ int) (SubagentRunner, error) {
		t.Fatal("factory should not be called for malformed input")
		return nil, nil
	}, 0, map[string]string{"r": "desc"})

	cases := []struct {
		name string
		in   string
	}{
		{"not json", `not json`},
		{"missing agent_id", `{"prompt":"go"}`},
		{"missing prompt", `{"agent_id":"r"}`},
		{"empty fields", `{"agent_id":"","prompt":""}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := tt.Execute(context.Background(), json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if res.Error == "" {
				t.Errorf("expected tool error for %s, got: %+v", c.name, res)
			}
		})
	}
}
