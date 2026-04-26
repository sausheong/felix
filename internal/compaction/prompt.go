package compaction

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sausheong/felix/internal/session"
)

const summarizationPromptHeader = `You are summarizing an AI assistant's conversation so it can continue past
the context window.

Preserve: facts established, decisions made, file paths, code snippets
discussed, ongoing tasks, the user's stated preferences and constraints.

Errors are tricky. Preserve an error only if it is still unresolved at the
end of the transcript and the next turn must act on it. If an error was
followed by a successful retry, a workaround, a different tool, a corrected
parameter, or simply moved past, drop the error and record only the
resolution (e.g. "queried contacts via column X"). Stale errors carried
forward as "facts" mislead the next turn into re-litigating problems that
were already solved — do not include them.

Drop: chitchat, intermediate tool exploration, retried-then-abandoned
approaches.

Output only the summary. No preamble. No "Here is the summary:". No closing
remarks.`

// BuildTranscript renders a list of session entries as a labeled plain-text
// transcript for the summarizer prompt. Tool results are NOT truncated here —
// the summarizer needs full content to extract durable facts.
func BuildTranscript(entries []session.SessionEntry) string {
	var sb strings.Builder
	for _, e := range entries {
		switch e.Type {
		case session.EntryTypeMessage:
			var md session.MessageData
			if err := json.Unmarshal(e.Data, &md); err != nil {
				continue
			}
			label := strings.ToUpper(e.Role)
			fmt.Fprintf(&sb, "%s: %s\n", label, md.Text)
		case session.EntryTypeToolCall:
			var tc session.ToolCallData
			if err := json.Unmarshal(e.Data, &tc); err != nil {
				continue
			}
			fmt.Fprintf(&sb, "TOOL_CALL[%s]: %s\n", tc.Tool, string(tc.Input))
		case session.EntryTypeToolResult:
			var tr session.ToolResultData
			if err := json.Unmarshal(e.Data, &tr); err != nil {
				continue
			}
			content := tr.Output
			label := "TOOL_RESULT"
			if tr.Error != "" {
				content = tr.Error
				label = "TOOL_RESULT[error]"
			}
			fmt.Fprintf(&sb, "%s: %s\n", label, content)
		case session.EntryTypeCompaction:
			// A previous summary in the to-be-compacted range — fold it in.
			var cd session.CompactionData
			if err := json.Unmarshal(e.Data, &cd); err != nil {
				continue
			}
			fmt.Fprintf(&sb, "PREVIOUS_SUMMARY: %s\n", cd.Summary)
		}
	}
	return sb.String()
}

// BuildPrompt assembles the full compaction prompt from a transcript and
// optional user-provided focus instructions.
func BuildPrompt(transcript, additionalInstructions string) string {
	var sb strings.Builder
	sb.WriteString(summarizationPromptHeader)
	if strings.TrimSpace(additionalInstructions) != "" {
		sb.WriteString("\n\nAdditional focus: ")
		sb.WriteString(additionalInstructions)
	}
	sb.WriteString("\n\nCONVERSATION TO SUMMARIZE:\n")
	sb.WriteString(transcript)
	return sb.String()
}
