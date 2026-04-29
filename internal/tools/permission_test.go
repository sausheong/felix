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
