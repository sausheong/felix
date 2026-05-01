package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- LoadSkillTool ---

func TestLoadSkillToolReturnsBody(t *testing.T) {
	tool := &LoadSkillTool{
		Lookup: func(name string) (string, bool) {
			if name == "ffmpeg" {
				return "FFMPEG_BODY", true
			}
			return "", false
		},
	}
	in, _ := json.Marshal(map[string]string{"name": "ffmpeg"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "FFMPEG_BODY", res.Output)
	assert.Empty(t, res.Error)
}

func TestLoadSkillToolNotFoundReturnsError(t *testing.T) {
	tool := &LoadSkillTool{
		Lookup: func(string) (string, bool) { return "", false },
	}
	in, _ := json.Marshal(map[string]string{"name": "ghost"})
	res, _ := tool.Execute(context.Background(), in)
	assert.Empty(t, res.Output)
	assert.Contains(t, res.Error, "skill not found")
	assert.Contains(t, res.Error, `"ghost"`)
}

func TestLoadSkillToolMissingNameRejected(t *testing.T) {
	tool := &LoadSkillTool{Lookup: func(string) (string, bool) { return "", true }}
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.Equal(t, "name is required", res.Error)
}

func TestLoadSkillToolNilLookup(t *testing.T) {
	// Construct without Lookup (e.g., misconfigured runtime). The tool
	// must NOT panic — it needs to surface a clean error so the agent
	// can reason about the failure rather than the run blowing up.
	tool := &LoadSkillTool{}
	in, _ := json.Marshal(map[string]string{"name": "x"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.Error, "no skill loader configured")
}

func TestLoadSkillToolInvalidJSON(t *testing.T) {
	tool := &LoadSkillTool{Lookup: func(string) (string, bool) { return "", true }}
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{not json`))
	assert.Contains(t, res.Error, "invalid input")
}

func TestLoadSkillToolEmptyBody(t *testing.T) {
	// Found but body empty → emit a placeholder Output (not Error). An
	// Error would imply "doesn't exist"; an empty Output would imply
	// "succeeded with nothing useful". The placeholder distinguishes.
	tool := &LoadSkillTool{Lookup: func(string) (string, bool) { return "", true }}
	in, _ := json.Marshal(map[string]string{"name": "blank"})
	res, _ := tool.Execute(context.Background(), in)
	assert.Empty(t, res.Error)
	assert.Contains(t, res.Output, `"blank"`)
	assert.Contains(t, res.Output, "no body content")
}

func TestLoadSkillToolNameAndDescription(t *testing.T) {
	tool := &LoadSkillTool{}
	assert.Equal(t, "load_skill", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.True(t, tool.IsConcurrencySafe(nil), "load_skill is read-only and must be parallel-safe")
}

// --- LoadMemoryTool ---

func TestLoadMemoryToolReturnsBody(t *testing.T) {
	tool := &LoadMemoryTool{
		Lookup: func(id string) (string, bool) {
			if id == "feedback_xyz" {
				return "FEEDBACK_BODY", true
			}
			return "", false
		},
	}
	in, _ := json.Marshal(map[string]string{"id": "feedback_xyz"})
	res, _ := tool.Execute(context.Background(), in)
	assert.Equal(t, "FEEDBACK_BODY", res.Output)
}

func TestLoadMemoryToolNotFound(t *testing.T) {
	tool := &LoadMemoryTool{Lookup: func(string) (string, bool) { return "", false }}
	in, _ := json.Marshal(map[string]string{"id": "ghost"})
	res, _ := tool.Execute(context.Background(), in)
	assert.Contains(t, res.Error, "memory entry not found")
}

func TestLoadMemoryToolMissingIDRejected(t *testing.T) {
	tool := &LoadMemoryTool{Lookup: func(string) (string, bool) { return "x", true }}
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{}`))
	assert.Equal(t, "id is required", res.Error)
}

func TestLoadMemoryToolNameAndDescription(t *testing.T) {
	tool := &LoadMemoryTool{}
	assert.Equal(t, "load_memory", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.True(t, tool.IsConcurrencySafe(nil))
}
