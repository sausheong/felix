package compaction

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	assert.Regexp(t, `TOOL_RESULT_[a-f0-9]+ \(untrusted, begin\):`, got)
	assert.Contains(t, got, "file contents here")
	assert.Regexp(t, `TOOL_RESULT_[a-f0-9]+ \(end\)`, got)
}

func TestBuildTranscriptMarksErroredToolResult(t *testing.T) {
	entries := []session.SessionEntry{
		session.ToolCallEntry("tc-1", "bash", []byte(`{"cmd":"false"}`)),
		session.ToolResultEntry("tc-1", "", "exit status 1", nil),
	}
	got := BuildTranscript(entries)
	assert.Regexp(t, `TOOL_RESULT\[error\]_[a-f0-9]+ \(untrusted, begin\):`, got)
	assert.Contains(t, got, "exit status 1")
	assert.Regexp(t, `TOOL_RESULT\[error\]_[a-f0-9]+ \(end\)`, got)
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

func TestBuildTranscriptCapsLargeToolResults(t *testing.T) {
	huge := strings.Repeat("a", 20000)
	entries := []session.SessionEntry{
		session.ToolResultEntry("tc1", huge, "", nil),
	}
	got := BuildTranscript(entries)
	assert.Less(t, len(got), 12000,
		"transcript must cap oversized tool results (got %d bytes)", len(got))
	assert.Contains(t, got, "[truncated",
		"truncation marker must be present so the model knows content was elided")
}

func TestBuildTranscriptLeavesSmallToolResultsIntact(t *testing.T) {
	small := "small output line"
	entries := []session.SessionEntry{
		session.ToolResultEntry("tc1", small, "", nil),
	}
	got := BuildTranscript(entries)
	assert.Contains(t, got, small,
		"small tool results must be preserved verbatim")
}

func TestBuildTranscriptUsesRandomDelimiterSuffix(t *testing.T) {
	// Two separate BuildTranscript calls must produce different
	// delimiter suffixes. Without per-call randomization, a tool result
	// could embed the literal closing delimiter and break out of the
	// untrusted boundary.
	entries := []session.SessionEntry{
		session.ToolResultEntry("tc1", "hello", "", nil),
	}
	got1 := BuildTranscript(entries)
	got2 := BuildTranscript(entries)

	// Extract the suffix from each. The format is "TOOL_RESULT_<hex> (untrusted, begin):".
	re := regexp.MustCompile(`TOOL_RESULT_([a-f0-9]+) \(untrusted, begin\)`)
	m1 := re.FindStringSubmatch(got1)
	m2 := re.FindStringSubmatch(got2)
	require.Len(t, m1, 2, "first transcript must contain a TOOL_RESULT_<suffix> marker")
	require.Len(t, m2, 2, "second transcript must contain a TOOL_RESULT_<suffix> marker")
	assert.NotEqual(t, m1[1], m2[1],
		"per-call suffix must differ across BuildTranscript invocations")
	assert.GreaterOrEqual(t, len(m1[1]), 8,
		"suffix must be at least 8 hex chars to make collisions effectively impossible")
}

func TestBuildTranscriptSuffixIsUniformWithinOneTranscript(t *testing.T) {
	// All tool-result delimiters within one BuildTranscript call must
	// share the same suffix so the prompt structure is uniform.
	entries := []session.SessionEntry{
		session.ToolResultEntry("tc1", "first", "", nil),
		session.ToolResultEntry("tc2", "second", "", nil),
		session.ToolResultEntry("tc3", "third", "an error", nil),
	}
	got := BuildTranscript(entries)
	re := regexp.MustCompile(`TOOL_RESULT(?:\[error\])?_([a-f0-9]+) \((?:untrusted, begin|end)\)`)
	matches := re.FindAllStringSubmatch(got, -1)
	require.GreaterOrEqual(t, len(matches), 6, "expected 3 begin + 3 end markers, got %d", len(matches))
	first := matches[0][1]
	for i, m := range matches {
		assert.Equal(t, first, m[1],
			"marker %d suffix %q must equal first suffix %q (uniform within one transcript)", i, m[1], first)
	}
}

func TestBuildTranscriptErrorMarkerStillUsesUntrustedWrapping(t *testing.T) {
	// Errored tool results retain the [error] label AND the (untrusted) wrapping.
	entries := []session.SessionEntry{
		session.ToolResultEntry("tc1", "ignored", "boom: file not found", nil),
	}
	got := BuildTranscript(entries)
	assert.Contains(t, got, "TOOL_RESULT[error]_", "error label must be preserved")
	assert.Contains(t, got, "(untrusted, begin):", "error results must also use untrusted wrapping")
	assert.Contains(t, got, "boom: file not found", "error text must be present")
}
