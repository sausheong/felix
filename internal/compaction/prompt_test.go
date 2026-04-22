package compaction

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sausheong/felix/internal/session"
)

func TestBuildTranscriptIncludesAllRoles(t *testing.T) {
	entries := []session.SessionEntry{
		session.UserMessageEntry("how do I read a file?"),
		session.AssistantMessageEntry("use the read_file tool"),
		session.ToolCallEntry("tc-1", "read_file", []byte(`{"path":"/tmp/x"}`)),
		session.ToolResultEntry("tc-1", "file contents here", "", nil),
	}
	got := BuildTranscript(entries)
	assert.Contains(t, got, "USER: how do I read a file?")
	assert.Contains(t, got, "ASSISTANT: use the read_file tool")
	assert.Contains(t, got, "TOOL_CALL[read_file]: ")
	assert.Contains(t, got, "TOOL_RESULT: file contents here")
}

func TestBuildTranscriptMarksErroredToolResult(t *testing.T) {
	entries := []session.SessionEntry{
		session.ToolCallEntry("tc-1", "bash", []byte(`{"cmd":"false"}`)),
		session.ToolResultEntry("tc-1", "", "exit status 1", nil),
	}
	got := BuildTranscript(entries)
	assert.Contains(t, got, "TOOL_RESULT[error]: exit status 1")
}

func TestBuildPromptNoExtraInstructions(t *testing.T) {
	transcript := "USER: hi"
	got := BuildPrompt(transcript, "")
	assert.Contains(t, got, "summarizing")
	assert.Contains(t, got, "Output only the summary")
	assert.Contains(t, got, "USER: hi")
	assert.NotContains(t, got, "Additional focus")
}

func TestBuildPromptWithFocusInstructions(t *testing.T) {
	got := BuildPrompt("USER: hi", "focus on API decisions")
	assert.Contains(t, got, "Additional focus: focus on API decisions")
}
