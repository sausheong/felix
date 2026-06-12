package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPStdioInheritEnvRoundTrips(t *testing.T) {
	raw := `{"command":"npx","args":["x"],"env":{"A":"1"},"inherit_env":true}`
	var b MCPStdioBlock
	require.NoError(t, json.Unmarshal([]byte(raw), &b))
	require.True(t, b.InheritEnv)
	require.Equal(t, "npx", b.Command)

	out, err := json.Marshal(b)
	require.NoError(t, err)
	require.Contains(t, string(out), `"inherit_env":true`)

	var b2 MCPStdioBlock
	require.NoError(t, json.Unmarshal([]byte(`{"command":"x"}`), &b2))
	require.False(t, b2.InheritEnv)
}
