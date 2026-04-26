package compaction

import (
	"strings"
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
	assert.Contains(t, got, "TOOL_RESULT (untrusted, begin):")
	assert.Contains(t, got, "file contents here")
	assert.Contains(t, got, "TOOL_RESULT (end)")
}

func TestBuildTranscriptMarksErroredToolResult(t *testing.T) {
	entries := []session.SessionEntry{
		session.ToolCallEntry("tc-1", "bash", []byte(`{"cmd":"false"}`)),
		session.ToolResultEntry("tc-1", "", "exit status 1", nil),
	}
	got := BuildTranscript(entries)
	assert.Contains(t, got, "TOOL_RESULT[error] (untrusted, begin):")
	assert.Contains(t, got, "exit status 1")
	assert.Contains(t, got, "TOOL_RESULT[error] (end)")
}

func TestBuildPromptNoExtraInstructions(t *testing.T) {
	transcript := "USER: hi"
	got := BuildPrompt(transcript, "")
	assert.Contains(t, got, "summarizing")
	assert.Contains(t, got, "USER: hi")
	assert.NotContains(t, got, "Additional focus")
}

func TestBuildPromptWithFocusInstructions(t *testing.T) {
	got := BuildPrompt("USER: hi", "focus on API decisions")
	assert.Contains(t, got, "Additional focus: focus on API decisions")
}

func TestBuildTranscriptFoldsPreviousSummary(t *testing.T) {
	entries := []session.SessionEntry{
		session.CompactionEntry("earlier work: built X, decided Y", "", "", "m", 0, 0, 1),
		session.UserMessageEntry("now what about Z?"),
	}
	got := BuildTranscript(entries)
	assert.Contains(t, got, "PREVIOUS_SUMMARY: earlier work: built X, decided Y")
	assert.Contains(t, got, "USER: now what about Z?")
}

func TestPromptIncludesNineSections(t *testing.T) {
	got := BuildPrompt("CONVERSATION HERE", "")
	for _, section := range []string{
		"1. Primary Request and Intent",
		"2. Key Technical Concepts",
		"3. Files and Code Sections",
		"4. Errors and fixes",
		"5. Problem Solving",
		"6. All user messages",
		"7. Pending Tasks",
		"8. Current Work",
		"9. Optional Next Step",
	} {
		assert.Contains(t, got, section, "prompt must include section %q", section)
	}
}

func TestPromptDemandsAnalysisScratchpad(t *testing.T) {
	got := BuildPrompt("CONVERSATION HERE", "")
	assert.Contains(t, got, "<analysis>",
		"prompt must instruct the model to emit an analysis scratchpad")
	assert.Contains(t, got, "<summary>",
		"prompt must instruct the model to emit a summary block")
}

func TestPromptRequiresIdentifierPreservation(t *testing.T) {
	got := BuildPrompt("CONVERSATION HERE", "")
	low := strings.ToLower(got)
	assert.Contains(t, low, "verbatim",
		"prompt must demand verbatim preservation of identifiers")
	for _, kind := range []string{"file path", "uuid", "identifier"} {
		assert.Contains(t, low, kind,
			"prompt must explicitly mention preserving %q-class identifiers", kind)
	}
}

func TestPromptRequiresAllUserMessagesEnumerated(t *testing.T) {
	got := BuildPrompt("CONVERSATION HERE", "")
	low := strings.ToLower(got)
	assert.Contains(t, low, "all user messages",
		"prompt must require enumerating every user message")
}

func TestPromptRequiresVerbatimNextStep(t *testing.T) {
	got := BuildPrompt("CONVERSATION HERE", "")
	low := strings.ToLower(got)
	assert.Contains(t, low, "next step",
		"prompt must include the Optional Next Step section")
	assert.Contains(t, low, "verbatim",
		"prompt must require verbatim quotes from recent messages")
}

func TestPromptIncludesTranscript(t *testing.T) {
	got := BuildPrompt("CONVERSATION GOES HERE", "")
	assert.Contains(t, got, "CONVERSATION GOES HERE",
		"the transcript must be embedded in the prompt")
}

func TestPromptAppendsAdditionalInstructions(t *testing.T) {
	got := BuildPrompt("X", "focus on test failures")
	assert.Contains(t, got, "focus on test failures",
		"additional instructions must appear in the prompt")
}

func TestFormatCompactSummaryStripsAnalysis(t *testing.T) {
	raw := `<analysis>
chain of thought drafting
</analysis>

<summary>
1. Primary Request: Build the thing.
2. Key Tech: Go.
</summary>`

	got := FormatCompactSummary(raw)
	assert.NotContains(t, got, "<analysis>",
		"analysis tags must be stripped")
	assert.NotContains(t, got, "chain of thought drafting",
		"analysis content must be removed")
	assert.NotContains(t, got, "<summary>",
		"summary tags must be replaced with a header")
	assert.Contains(t, got, "Summary:",
		"summary content must be wrapped under a Summary: header")
	assert.Contains(t, got, "Primary Request: Build the thing.")
}

func TestFormatCompactSummaryHandlesMissingTags(t *testing.T) {
	raw := "User asked about X; we did Y."
	got := FormatCompactSummary(raw)
	assert.Contains(t, got, "User asked about X")
}

func TestFormatCompactSummaryHandlesMultipleSummaryBlocks(t *testing.T) {
	raw := `<summary>first</summary>

<summary>second</summary>`
	got := FormatCompactSummary(raw)
	assert.NotContains(t, got, "<summary>", "no <summary> tags should remain")
	assert.Contains(t, got, "first")
	assert.Contains(t, got, "second")
}
