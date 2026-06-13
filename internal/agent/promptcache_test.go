package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigGenerationBumpInvalidates(t *testing.T) {
	start := configGeneration.Load()
	BumpConfigGeneration()
	require.Equal(t, start+1, configGeneration.Load(), "bump must increment the generation")
}

func TestPromptCacheReusesWithinGeneration(t *testing.T) {
	agentID := "cache-test-agent"
	gen := configGeneration.Load()
	_, hit1 := promptCacheGet(agentID, gen)
	require.False(t, hit1, "first get is a miss")

	promptCachePut(agentID, gen, cachedPrompt{configSummary: "S", memoryFiles: "M"})
	parts2, hit2 := promptCacheGet(agentID, gen)
	require.True(t, hit2, "second get at same gen is a hit")
	require.Equal(t, "S", parts2.configSummary)

	BumpConfigGeneration()
	_, hit3 := promptCacheGet(agentID, configGeneration.Load())
	require.False(t, hit3, "after bump, new gen is a miss")
}
