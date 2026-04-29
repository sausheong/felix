package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sausheong/felix/internal/llm"
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

func TestStaticChecker_FilterToolDefs_UnknownAgentReturnsFullList(t *testing.T) {
	c := NewStaticChecker(map[string]Policy{
		"agent1": {Allow: []string{"read_file"}},
	})
	defs := []llm.ToolDef{{Name: "read_file"}, {Name: "bash"}}
	out := c.FilterToolDefs(defs, "unknown_agent")
	require.Equal(t, defs, out, "unknown agent must see the full toolset")
}

func TestStaticChecker_FilterToolDefs_AllowList(t *testing.T) {
	c := NewStaticChecker(map[string]Policy{
		"agent1": {Allow: []string{"read_file", "bash"}},
	})
	defs := []llm.ToolDef{
		{Name: "read_file"},
		{Name: "write_file"}, // not in allow list
		{Name: "bash"},
	}
	out := c.FilterToolDefs(defs, "agent1")
	require.Len(t, out, 2)
	require.Equal(t, "read_file", out[0].Name)
	require.Equal(t, "bash", out[1].Name)
}

func TestStaticChecker_FilterToolDefs_DenyList(t *testing.T) {
	c := NewStaticChecker(map[string]Policy{
		"agent1": {Deny: []string{"bash"}},
	})
	defs := []llm.ToolDef{
		{Name: "read_file"},
		{Name: "bash"}, // denied
		{Name: "web_fetch"},
	}
	out := c.FilterToolDefs(defs, "agent1")
	require.Len(t, out, 2)
	require.Equal(t, "read_file", out[0].Name)
	require.Equal(t, "web_fetch", out[1].Name)
}

func TestStaticChecker_FilterToolDefs_EmptyInput(t *testing.T) {
	c := NewStaticChecker(map[string]Policy{
		"agent1": {Allow: []string{"read_file"}},
	})
	out := c.FilterToolDefs([]llm.ToolDef{}, "agent1")
	require.Empty(t, out)
}

func TestStaticChecker_FilterToolDefs_OrderPreserved(t *testing.T) {
	c := NewStaticChecker(map[string]Policy{
		"agent1": {}, // no policy → allow-all
	})
	defs := []llm.ToolDef{
		{Name: "z_tool"},
		{Name: "a_tool"},
		{Name: "m_tool"},
	}
	out := c.FilterToolDefs(defs, "agent1")
	require.Equal(t, defs, out, "order must be preserved")
}
