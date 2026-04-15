package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/tools"
)

func testConfig() *config.Config {
	return &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{
					ID:        "supervisor",
					Name:      "Supervisor",
					Model:     "anthropic/claude-sonnet-4-5-20250514",
					Workspace: "/tmp/goclaw-test-supervisor",
				},
				{
					ID:        "assistant",
					Name:      "Assistant",
					Model:     "anthropic/claude-haiku-3-5-20241022",
					Workspace: "/tmp/goclaw-test-assistant",
					Tools: config.ToolPolicy{
						Allow: []string{"read_file", "bash"},
					},
				},
			},
		},
	}
}

func TestAvailableAgents(t *testing.T) {
	cfg := testConfig()
	runner := NewAgentRunner(nil, cfg, nil)

	agents := runner.AvailableAgents()
	require.Len(t, agents, 2)

	assert.Equal(t, "supervisor", agents[0].ID)
	assert.Equal(t, "Supervisor", agents[0].Name)
	assert.Equal(t, "assistant", agents[1].ID)
	assert.Equal(t, "Assistant", agents[1].Name)
}

func TestAvailableAgentsEmpty(t *testing.T) {
	cfg := &config.Config{}
	runner := NewAgentRunner(nil, cfg, nil)

	agents := runner.AvailableAgents()
	assert.Empty(t, agents)
}

func TestRunAgentUnknownAgent(t *testing.T) {
	cfg := testConfig()
	runner := NewAgentRunner(nil, cfg, nil)

	_, err := runner.RunAgent(t.Context(), "nonexistent", "do something")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `agent "nonexistent" not found`)
}

func TestRunAgentMissingProvider(t *testing.T) {
	cfg := testConfig()
	// Empty providers map — no providers available
	providers := map[string]llm.LLMProvider{}
	store := session.NewStore(t.TempDir())
	runner := NewAgentRunner(providers, cfg, store)

	_, err := runner.RunAgent(t.Context(), "assistant", "do something")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `provider "anthropic" not available`)
}

func TestRunAgentImplementsInterface(t *testing.T) {
	cfg := testConfig()
	runner := NewAgentRunner(nil, cfg, nil)

	// Verify AgentRunnerImpl satisfies tools.AgentRunner
	var _ tools.AgentRunner = runner
}

func TestRunAgentSetters(t *testing.T) {
	cfg := testConfig()
	runner := NewAgentRunner(nil, cfg, nil)

	// Verify setters don't panic
	runner.SetSender(nil)
	runner.SetSkills(nil)
	runner.SetMemory(nil)
}
