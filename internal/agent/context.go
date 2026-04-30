package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/skill"
)

const maxToolResultLen = 4000 // truncate tool results longer than this

// detectImageMIME returns the actual MIME type based on magic bytes.
// Falls back to the provided hint if the format is unrecognized.
func detectImageMIME(data []byte, hint string) string {
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 4 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		return "image/png"
	}
	if len(data) >= 4 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' {
		return "image/gif"
	}
	if len(data) >= 4 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' {
		return "image/webp"
	}
	return hint
}

// MaxAgentMemoryBytes caps the total bytes of FELIX.md / AGENTS.md content
// injected into the static system prompt. Mirrors Claude Code's
// MAX_MEMORY_CHARACTER_COUNT (claudemd.ts) at 40 KB.
const MaxAgentMemoryBytes = 40 * 1024

// memoryTruncationNotice is the marker LoadAgentMemoryFiles appends when
// the cumulative memory-file content would push past MaxAgentMemoryBytes.
const memoryTruncationNotice = "\n\n[truncated — over 40 KB total agent memory]"

const defaultIdentityBase = `You are Felix, an AI agent. Conduct yourself professionally and politely. Be concise and direct. When executing tasks, think step by step and use your tools to accomplish the user's goals. When you need to call multiple independent tools to gather information, emit them in a single response (parallel tool calls) rather than waiting for each one — this cuts response latency on local models.`

// toolHints maps tool names to usage guidance injected into the default identity.
var toolHints = map[string]string{
	"read_file":    "You can read files. You have vision capabilities — you can see and analyze images by using read_file on image files. Do not say you cannot see or analyze images.",
	"write_file":   "You can create or overwrite files.",
	"edit_file":    "You can make targeted edits to existing files.",
	"bash":         "You can execute bash commands on the user's machine. ALWAYS wrap file paths in double quotes when invoking bash (e.g. ls \"/path/with spaces/file.txt\") so paths with spaces or special characters survive shell tokenization.",
	"web_fetch":    "You can fetch web pages using the web_fetch tool.",
	"web_search":   "You can search the web using the web_search tool.",
	"browser":      "You can automate a headless browser for interactive pages using the browser tool.",
	"send_message": "You can send messages to other users or channels using the send_message tool.",
	"cron":         "You can schedule recurring tasks using the cron tool.",
}

// buildDefaultIdentity constructs the default identity prompt tailored to
// the tools actually available to this agent.
func buildDefaultIdentity(toolNames []string) string {
	if len(toolNames) == 0 {
		return defaultIdentityBase
	}
	available := make(map[string]bool, len(toolNames))
	for _, name := range toolNames {
		available[name] = true
	}
	var hints []string
	for _, name := range toolNames {
		if h, ok := toolHints[name]; ok {
			hints = append(hints, h)
		}
	}
	if len(hints) == 0 {
		return defaultIdentityBase
	}
	return defaultIdentityBase + " " + strings.Join(hints, " ")
}

// BuildConfigSummary returns the brief summary of agents and channels
// that gets injected into the static portion of the system prompt. Pure:
// no I/O, accepts the already-loaded *config.Config. Replaces the
// per-turn configSummary() that read felix.json5 from disk.
//
// Returns "" for a nil config or one with no agents and no enabled channels.
//
// Lifecycle: deliberately captured at Runtime construction time so the
// cached prompt prefix stays byte-stable across the Runtime's lifetime.
// New Runtimes built after a hot-reload (e.g., fsnotify-driven config
// reload, settings UI save) pick up the updated summary; in-flight
// Runtimes do not.
//
// Concurrency: this reads cfg.Agents.List and cfg.Channels without taking
// cfg's RWMutex. Callers must serialize against config.UpdateFrom — in
// practice this is automatic because BuildConfigSummary is invoked only
// from BuildRuntimeForAgent, which runs synchronously at session start
// before any concurrent reload would race with it.
func BuildConfigSummary(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}

	var sb strings.Builder

	if len(cfg.Agents.List) > 0 {
		sb.WriteString("Configured agents:")
		for _, a := range cfg.Agents.List {
			tools := ""
			if len(a.Tools.Allow) > 0 {
				tools = ", tools: " + strings.Join(a.Tools.Allow, ", ")
			}
			sb.WriteString(fmt.Sprintf("\n- %s (id: %s, model: %s%s)", a.Name, a.ID, a.Model, tools))
		}
	}

	if cfg.Channels.CLI.Enabled {
		sb.WriteString("\n\nConfigured channels: cli")
	}

	return sb.String()
}

// BuildStaticSystemPrompt assembles the portion of the system prompt that
// does not change across turns within a Run: identity (from systemPrompt
// arg, IDENTITY.md, or the built-in default tailored to toolNames), agent
// self-identity, the configuration/data dir paths, the pre-computed
// configSummary, and the pre-computed skillsIndex.
//
// Pure with one allowed exception: it reads IDENTITY.md from workspace
// when systemPrompt is empty. Caller pre-resolves configSummary and
// skillsIndex so neither config.Load nor skill index assembly happens
// per-turn. Suitable to call once at Runtime construction.
func BuildStaticSystemPrompt(
	workspace, systemPrompt, agentID, agentName string,
	toolNames []string,
	configSummary string,
	skillsIndex string,
	memoryFiles string,
) string {
	var base string
	if systemPrompt != "" {
		base = systemPrompt
	} else {
		identityPath := filepath.Join(workspace, "IDENTITY.md")
		data, err := os.ReadFile(identityPath)
		if err != nil {
			base = buildDefaultIdentity(toolNames)
		} else {
			base = string(data)
		}
	}

	if agentID != "" {
		base += fmt.Sprintf("\n\nYou are the %q agent (id: %s).", agentName, agentID)
	}

	base += fmt.Sprintf("\n\nYour configuration file is at %s and your data directory is %s.",
		config.DefaultConfigPath(), config.DefaultDataDir())

	if configSummary != "" {
		base += "\n\n" + configSummary
	}
	if skillsIndex != "" {
		base += skillsIndex
	}
	if memoryFiles != "" {
		base += memoryFiles
	}

	return base
}

// buildDynamicSystemPromptSuffix concatenates the per-turn dynamic context
// — matched skill bodies, matched memory entries, and the cortex hint —
// into a single string the runtime sends as the second (un-cached)
// SystemPromptPart. Returns "" when all inputs are empty/nil.
func buildDynamicSystemPromptSuffix(
	matchedSkills []skill.Skill,
	matchedMemory []memory.Entry,
	cortexContext string,
) string {
	var sb strings.Builder
	if extra := skill.FormatForPrompt(matchedSkills); extra != "" {
		sb.WriteString(extra)
	}
	if extra := memory.FormatForPrompt(matchedMemory); extra != "" {
		sb.WriteString(extra)
	}
	if cortexContext != "" {
		sb.WriteString(cortexContext)
	}
	return sb.String()
}

// assembleMessages converts session history into LLM messages.
// It ensures that every tool_use block in an assistant message has a
// corresponding tool_result in the next user message. Orphaned tool calls
// (e.g. from interrupted sessions) get synthetic error results injected.
func assembleMessages(history []session.SessionEntry) []llm.Message {
	// First pass: collect tool result IDs so we can detect orphaned tool calls.
	resultIDs := make(map[string]bool)
	for _, entry := range history {
		if entry.Type == session.EntryTypeToolResult {
			var tr session.ToolResultData
			if err := json.Unmarshal(entry.Data, &tr); err == nil {
				resultIDs[tr.ToolCallID] = true
			}
		}
	}

	var msgs []llm.Message

	for _, entry := range history {
		switch entry.Type {
		case session.EntryTypeCompaction:
			var cd session.CompactionData
			if err := json.Unmarshal(entry.Data, &cd); err != nil {
				continue
			}
			// The summary is followed by an explicit continuation directive
			// so the model resumes the conversation rather than treating it
			// as a fresh start. Without this, models tend to reply with
			// openers like "I'm ready to help! Our previous conversation
			// covered X" — which loses the in-flight task context that
			// the user's next message implicitly relies on.
			content := "[Previous conversation summary]\n\n" + cd.Summary +
				"\n\nContinue the conversation from where it left off without asking the user any further questions. " +
				"Resume directly — do not acknowledge the summary, do not recap what was happening, " +
				"do not preface with \"I'll continue\" or similar. Pick up the last task as if the break never happened."
			msgs = append(msgs, llm.Message{
				Role:    "user",
				Content: content,
			})

		case session.EntryTypeMeta:
			// Meta entries (e.g. compaction summaries) are treated as system context
			var md session.MessageData
			if err := json.Unmarshal(entry.Data, &md); err != nil {
				continue
			}
			msgs = append(msgs, llm.Message{
				Role:    "user",
				Content: "[Session Summary]\n" + md.Text,
			})

		case session.EntryTypeMessage:
			var md session.MessageData
			if err := json.Unmarshal(entry.Data, &md); err != nil {
				continue
			}
			// Before appending a new message, check if the last assistant
			// message has orphaned tool calls that need synthetic results.
			msgs = injectMissingToolResults(msgs)
			msg := llm.Message{
				Role:    entry.Role,
				Content: md.Text,
			}
			// Convert session images to LLM image content
			if entry.Role == "user" {
				for _, img := range md.Images {
					data, err := base64.StdEncoding.DecodeString(img.Data)
					if err != nil {
						continue
					}
					msg.Images = append(msg.Images, llm.ImageContent{
						MimeType: detectImageMIME(data, img.MimeType),
						Data:     data,
					})
				}
			}
			msgs = append(msgs, msg)

		case session.EntryTypeToolCall:
			var td session.ToolCallData
			if err := json.Unmarshal(entry.Data, &td); err != nil {
				continue
			}
			// Tool calls are part of the assistant turn — merge into the last assistant message
			// or create one if needed
			if len(msgs) == 0 || msgs[len(msgs)-1].Role != "assistant" {
				msgs = append(msgs, llm.Message{Role: "assistant"})
			}
			msgs[len(msgs)-1].ToolCalls = append(msgs[len(msgs)-1].ToolCalls, llm.ToolCall{
				ID:    td.ID,
				Name:  td.Tool,
				Input: td.Input,
			})

		case session.EntryTypeToolResult:
			var tr session.ToolResultData
			if err := json.Unmarshal(entry.Data, &tr); err != nil {
				continue
			}
			content := tr.Output
			if tr.Error != "" {
				content = tr.Error
			}
			if content == "" {
				content = "(no output)"
			}
			msg := llm.Message{
				Role:       "user",
				Content:    content,
				ToolCallID: tr.ToolCallID,
				IsError:    tr.IsError,
			}
			// Convert session images to LLM image content
			for _, img := range tr.Images {
				data, err := base64.StdEncoding.DecodeString(img.Data)
				if err != nil {
					continue
				}
				msg.Images = append(msg.Images, llm.ImageContent{
					MimeType: detectImageMIME(data, img.MimeType),
					Data:     data,
				})
			}
			msgs = append(msgs, msg)
		}
	}

	// Final check: handle orphaned tool calls at the end of history.
	msgs = injectMissingToolResults(msgs)

	return msgs
}

// injectMissingToolResults walks the message sequence and inserts synthetic
// tool_result user messages for any assistant tool_calls that lack a matching
// tool_result in the immediately-following user messages. Handles both
// end-of-history orphans (the original case) and mid-history orphans (which
// can occur when a Phase B parallel dispatch crashes mid-batch and the
// session is later /resume'd).
//
// Algorithm: scan left-to-right; for each assistant message with ToolCalls,
// collect the next k user messages' ToolCallIDs (where k = len(ToolCalls)).
// Any tc.ID missing from that set gets a synthetic error tool_result inserted
// immediately after the assistant message.
func injectMissingToolResults(msgs []llm.Message) []llm.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]llm.Message, 0, len(msgs))
	i := 0
	for i < len(msgs) {
		m := msgs[i]
		out = append(out, m)
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			i++
			continue
		}
		// Collect tool_call_ids present in the next user-role messages
		// immediately after this assistant turn. Stop when we hit a non-user
		// message or a user message lacking ToolCallID (which means it's a
		// regular user prompt, not a tool result).
		seen := map[string]bool{}
		j := i + 1
		for j < len(msgs) && msgs[j].Role == "user" && msgs[j].ToolCallID != "" {
			seen[msgs[j].ToolCallID] = true
			j++
		}
		// For each tool_call without a matching result, append a synthetic.
		for _, tc := range m.ToolCalls {
			if !seen[tc.ID] {
				out = append(out, llm.Message{
					Role:       "user",
					Content:    "(tool execution was interrupted)",
					ToolCallID: tc.ID,
					IsError:    true,
				})
			}
		}
		i++ // advance past this assistant; the for-loop will copy the user tool_results next iteration
	}
	return out
}

// truncationMarker uniquely identifies content that pruneToolResults has
// already shortened, so re-runs across turns become cheap no-ops instead
// of re-scanning multi-hundred-KB tool outputs each time.
const truncationMarker = "[truncated — "

// pruneToolResults truncates oversized tool results in the message history
// to prevent context window overflow and bound prefill growth. Only affects
// tool result messages (identified by having a ToolCallID). Idempotent:
// messages already truncated in a prior turn are skipped via marker
// detection.
//
// The retained slice is the FIRST maxLen chars (cut at the nearest newline
// boundary) plus a compact marker telling the model the original size and
// that it can re-run the tool to fetch more. We keep the head rather than
// the tail because most tool outputs (file reads, command output) are
// front-loaded with the most relevant info.
func pruneToolResults(msgs []llm.Message, maxLen int) {
	for i := range msgs {
		if msgs[i].ToolCallID == "" || len(msgs[i].Content) <= maxLen {
			continue
		}
		// Already-pruned content carries the marker near the end; skip the
		// expensive LastIndex scan over hundreds of KB.
		if strings.Contains(msgs[i].Content, truncationMarker) {
			continue
		}
		originalLen := len(msgs[i].Content)
		truncated := msgs[i].Content[:maxLen]
		if idx := strings.LastIndex(truncated, "\n"); idx > maxLen/2 {
			truncated = truncated[:idx]
		}
		msgs[i].Content = fmt.Sprintf("%s\n\n%s%d of %d chars; re-run the tool with offset/limit to see more]",
			truncated, truncationMarker, len(truncated), originalLen)
	}
}

// LoadAgentMemoryFiles reads FELIX.md and AGENTS.md from workspace and
// from $HOME. Returns the concatenated content with a brief header per
// source, or "" if nothing is found.
//
// Discovery order (highest priority first):
//  1. <workspace>/FELIX.md  — labelled "Project memory"
//  2. <workspace>/AGENTS.md — labelled "Project memory"
//  3. $HOME/FELIX.md        — labelled "User memory"
//  4. $HOME/AGENTS.md       — labelled "User memory"
//
// Empty files, whitespace-only files, missing files, and unreadable files
// are silently skipped. Files at the same absolute path (workspace ==
// $HOME) are deduped — each unique file appears at most once.
//
// Hard cap: total returned content ≤ MaxAgentMemoryBytes. When adding a
// file would push past the cap, the file's content is truncated at the
// last newline before the byte limit and a "[truncated — over 40 KB
// total agent memory]" marker is appended. Subsequent files are skipped
// entirely.
//
// Pure (single I/O exception: reads up to 4 files from disk). The
// returned string starts with "\n\n" so it composes cleanly after
// skillsIndex in the static system prompt.
func LoadAgentMemoryFiles(workspace string) string {
	type candidate struct {
		path  string
		label string // "Project memory" or "User memory"
	}
	var candidates []candidate
	if workspace != "" {
		candidates = append(candidates,
			candidate{filepath.Join(workspace, "FELIX.md"), "Project memory"},
			candidate{filepath.Join(workspace, "AGENTS.md"), "Project memory"},
		)
	}
	if home := os.Getenv("HOME"); home != "" {
		candidates = append(candidates,
			candidate{filepath.Join(home, "FELIX.md"), "User memory"},
			candidate{filepath.Join(home, "AGENTS.md"), "User memory"},
		)
	}

	seen := map[string]bool{}
	var sb strings.Builder
	truncated := false

	for _, c := range candidates {
		if truncated {
			break
		}
		abs, err := filepath.Abs(c.path)
		if err != nil {
			continue
		}
		if seen[abs] {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		seen[abs] = true
		body := strings.TrimSpace(string(data))
		if body == "" {
			continue
		}

		header := fmt.Sprintf("\n\n## %s: %s\n\n", c.label, abs)
		section := header + body

		if sb.Len()+len(section) > MaxAgentMemoryBytes {
			remaining := MaxAgentMemoryBytes - sb.Len() - len(header)
			if remaining > 0 {
				cut := body[:remaining]
				if idx := strings.LastIndex(cut, "\n"); idx > remaining/2 {
					cut = cut[:idx]
				}
				sb.WriteString(header)
				sb.WriteString(cut)
			}
			sb.WriteString(memoryTruncationNotice)
			truncated = true
			continue
		}
		sb.WriteString(section)
	}

	return sb.String()
}

// FormatDateLine returns the canonical date line injected into the
// dynamic system suffix every Run. Single-line, deterministic format.
//
//	"Today's date is YYYY-MM-DD."
//
// Uses the caller's process timezone (no UTC normalization) so "today"
// matches the user's local sense of the day.
func FormatDateLine(now time.Time) string {
	return fmt.Sprintf("Today's date is %s.", now.Format("2006-01-02"))
}
