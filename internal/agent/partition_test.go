package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/tools"
	"github.com/stretchr/testify/require"
)

type classifyExecutor struct {
	safe   map[string]bool
	panics map[string]bool
}

func (e *classifyExecutor) Execute(_ context.Context, _ string, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{}, nil
}
func (e *classifyExecutor) ToolDefs() []llm.ToolDef { return nil }
func (e *classifyExecutor) Names() []string         { return nil }
func (e *classifyExecutor) Get(name string) (tools.Tool, bool) {
	if _, ok := e.safe[name]; !ok {
		return nil, false
	}
	return &classifyTool{name: name, safe: e.safe[name], doPanic: e.panics[name]}, true
}

type classifyTool struct {
	name    string
	safe    bool
	doPanic bool
}

func (t *classifyTool) Name() string                { return t.name }
func (t *classifyTool) Description() string         { return "" }
func (t *classifyTool) Parameters() json.RawMessage { return nil }
func (t *classifyTool) Execute(_ context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{}, nil
}
func (t *classifyTool) IsConcurrencySafe(_ json.RawMessage) bool {
	if t.doPanic {
		panic("test-induced panic")
	}
	return t.safe
}

func tc(name string) llm.ToolCall {
	return llm.ToolCall{ID: "tc_" + name, Name: name, Input: json.RawMessage(`{}`)}
}

func TestPartition_Empty(t *testing.T) {
	ex := &classifyExecutor{safe: map[string]bool{}}
	require.Empty(t, partitionToolCalls(nil, ex))
	require.Empty(t, partitionToolCalls([]llm.ToolCall{}, ex))
}

func TestPartition_AllSafe(t *testing.T) {
	ex := &classifyExecutor{safe: map[string]bool{"r": true}}
	calls := []llm.ToolCall{tc("r"), tc("r"), tc("r")}
	batches := partitionToolCalls(calls, ex)
	require.Len(t, batches, 1)
	require.True(t, batches[0].concurrencySafe)
	require.Len(t, batches[0].calls, 3)
}

func TestPartition_AllUnsafe(t *testing.T) {
	ex := &classifyExecutor{safe: map[string]bool{"w": false}}
	calls := []llm.ToolCall{tc("w"), tc("w"), tc("w")}
	batches := partitionToolCalls(calls, ex)
	require.Len(t, batches, 3)
	for _, b := range batches {
		require.False(t, b.concurrencySafe)
		require.Len(t, b.calls, 1)
	}
}

func TestPartition_Mixed(t *testing.T) {
	ex := &classifyExecutor{safe: map[string]bool{"r": true, "w": false}}
	// [safe, safe, unsafe, safe] → 3 batches: [{safe,2}, {unsafe,1}, {safe,1}]
	calls := []llm.ToolCall{tc("r"), tc("r"), tc("w"), tc("r")}
	batches := partitionToolCalls(calls, ex)
	require.Len(t, batches, 3)

	require.True(t, batches[0].concurrencySafe)
	require.Len(t, batches[0].calls, 2)

	require.False(t, batches[1].concurrencySafe)
	require.Len(t, batches[1].calls, 1)

	require.True(t, batches[2].concurrencySafe)
	require.Len(t, batches[2].calls, 1)
}

func TestPartition_ToolNotFoundIsUnsafe(t *testing.T) {
	ex := &classifyExecutor{safe: map[string]bool{}}
	batches := partitionToolCalls([]llm.ToolCall{tc("missing")}, ex)
	require.Len(t, batches, 1)
	require.False(t, batches[0].concurrencySafe, "unknown tool must be treated as unsafe")
}

func TestPartition_PanicIsRecoveredAsUnsafe(t *testing.T) {
	ex := &classifyExecutor{
		safe:   map[string]bool{"p": true}, // would be safe…
		panics: map[string]bool{"p": true}, // …but IsConcurrencySafe panics
	}
	batches := partitionToolCalls([]llm.ToolCall{tc("p")}, ex)
	require.Len(t, batches, 1)
	require.False(t, batches[0].concurrencySafe, "panic must be recovered and treated as unsafe")
}

func TestMaxToolConcurrency_Default(t *testing.T) {
	t.Setenv("FELIX_MAX_TOOL_CONCURRENCY", "")
	require.Equal(t, 10, maxToolConcurrency())
}

func TestMaxToolConcurrency_EnvOverride(t *testing.T) {
	t.Setenv("FELIX_MAX_TOOL_CONCURRENCY", "3")
	require.Equal(t, 3, maxToolConcurrency())
}

func TestMaxToolConcurrency_InvalidEnvFallsBack(t *testing.T) {
	t.Setenv("FELIX_MAX_TOOL_CONCURRENCY", "garbage")
	require.Equal(t, 10, maxToolConcurrency())
}

func TestMaxToolConcurrency_ZeroFallsBack(t *testing.T) {
	t.Setenv("FELIX_MAX_TOOL_CONCURRENCY", "0")
	require.Equal(t, 10, maxToolConcurrency(), "0 is invalid; fall back to default")
}
