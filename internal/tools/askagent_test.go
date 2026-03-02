package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAgentRunner records calls and returns canned data.
type mockAgentRunner struct {
	lastAgentID string
	lastPrompt  string
	response    string
	err         error
	agents      []AgentInfo
}

func (m *mockAgentRunner) RunAgent(ctx context.Context, agentID, prompt string) (string, error) {
	m.lastAgentID = agentID
	m.lastPrompt = prompt
	return m.response, m.err
}

func (m *mockAgentRunner) AvailableAgents() []AgentInfo {
	return m.agents
}

func TestAskAgentToolName(t *testing.T) {
	tool := &AskAgentTool{}
	assert.Equal(t, "ask_agent", tool.Name())
}

func TestAskAgentToolParameters(t *testing.T) {
	tool := &AskAgentTool{}
	params := tool.Parameters()
	assert.True(t, json.Valid(params), "Parameters() should return valid JSON")
}

func TestAskAgentToolSuccess(t *testing.T) {
	runner := &mockAgentRunner{response: "The answer is 42."}
	tool := &AskAgentTool{Runner: runner}
	input, _ := json.Marshal(askAgentInput{
		AgentID: "assistant",
		Prompt:  "What is the meaning of life?",
	})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Empty(t, result.Error)
	assert.Equal(t, "The answer is 42.", result.Output)
	assert.Equal(t, "assistant", result.Metadata["agent_id"])

	assert.Equal(t, "assistant", runner.lastAgentID)
	assert.Equal(t, "What is the meaning of life?", runner.lastPrompt)
}

func TestAskAgentToolMissingAgentID(t *testing.T) {
	runner := &mockAgentRunner{}
	tool := &AskAgentTool{Runner: runner}
	input, _ := json.Marshal(askAgentInput{
		Prompt: "do something",
	})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "agent_id is required")
}

func TestAskAgentToolMissingPrompt(t *testing.T) {
	runner := &mockAgentRunner{}
	tool := &AskAgentTool{Runner: runner}
	input, _ := json.Marshal(askAgentInput{
		AgentID: "assistant",
	})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "prompt is required")
}

func TestAskAgentToolNilRunner(t *testing.T) {
	tool := &AskAgentTool{Runner: nil}
	input, _ := json.Marshal(askAgentInput{
		AgentID: "assistant",
		Prompt:  "do something",
	})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Error, "not available")
}

func TestAskAgentToolRunnerError(t *testing.T) {
	runner := &mockAgentRunner{err: errors.New("model overloaded")}
	tool := &AskAgentTool{Runner: runner}
	input, _ := json.Marshal(askAgentInput{
		AgentID: "assistant",
		Prompt:  "do something",
	})

	result, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)
	assert.Contains(t, result.Error, `agent "assistant" failed`)
	assert.Contains(t, result.Error, "model overloaded")
}

func TestAskAgentToolInvalidJSON(t *testing.T) {
	runner := &mockAgentRunner{}
	tool := &AskAgentTool{Runner: runner}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{invalid`))
	require.NoError(t, err)
	assert.Contains(t, result.Error, "invalid input")
}

func TestAskAgentToolDescriptionListsAgents(t *testing.T) {
	runner := &mockAgentRunner{
		agents: []AgentInfo{
			{ID: "supervisor", Name: "Supervisor Agent"},
			{ID: "assistant", Name: ""},
		},
	}
	tool := &AskAgentTool{Runner: runner}

	desc := tool.Description()
	assert.Contains(t, desc, "Available agents:")
	assert.Contains(t, desc, "supervisor (Supervisor Agent)")
	assert.Contains(t, desc, "- assistant")
}

func TestAskAgentToolDescriptionNoAgents(t *testing.T) {
	runner := &mockAgentRunner{agents: []AgentInfo{}}
	tool := &AskAgentTool{Runner: runner}

	desc := tool.Description()
	assert.Contains(t, desc, "Delegate a task")
	assert.NotContains(t, desc, "Available agents:")
}

func TestAskAgentToolDescriptionNilRunner(t *testing.T) {
	tool := &AskAgentTool{Runner: nil}

	desc := tool.Description()
	assert.Contains(t, desc, "Delegate a task")
	assert.NotContains(t, desc, "Available agents:")
}

func TestRegisterAskAgent(t *testing.T) {
	reg := NewRegistry()
	runner := &mockAgentRunner{}
	RegisterAskAgent(reg, runner)

	tool, ok := reg.Get("ask_agent")
	assert.True(t, ok)
	assert.Equal(t, "ask_agent", tool.Name())
}
