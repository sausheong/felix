package compaction

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/config"
)

// TestBuildManagerWiresThreshold ensures the Threshold value from
// agents.defaults.compaction.threshold round-trips into Manager.Threshold.
func TestBuildManagerWiresThreshold(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Kind: "openai-compatible", BaseURL: "http://127.0.0.1:11434/v1"},
		},
		Agents: config.AgentsConfig{
			Defaults: config.AgentsDefaults{
				Compaction: config.CompactionConfig{
					Enabled:       true,
					Model:         "local/qwen2.5:3b-instruct",
					Threshold:     0.42,
					PreserveTurns: 4,
					TimeoutSec:    60,
				},
			},
		},
	}
	mgr := BuildManager(cfg)
	require.NotNil(t, mgr)
	assert.InDelta(t, 0.42, mgr.Threshold, 0.001)
	assert.Equal(t, 4, mgr.PreserveTurns)
}
