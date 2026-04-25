package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/connector/conversation"
	"github.com/sausheong/felix/internal/compaction"
	cortexadapter "github.com/sausheong/felix/internal/cortex"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/skill"
	"github.com/sausheong/felix/internal/tokens"
	"github.com/sausheong/felix/internal/tools"
)

// EventType identifies the kind of agent event.
type EventType int

const (
	EventTextDelta EventType = iota
	EventToolCallStart
	EventToolResult
	EventDone
	EventError
	EventAborted
	EventCompactionStart
	EventCompactionDone
	EventCompactionSkipped
)

// AgentEvent is a single streaming event from the agent.
type AgentEvent struct {
	Type       EventType
	Text       string
	ToolCall   *llm.ToolCall
	Result     *tools.ToolResult
	Error      error
	Compaction *compaction.Result // populated for EventCompaction* events
}

// Runtime is the agent think-act loop.
type Runtime struct {
	LLM          llm.LLMProvider
	Tools        tools.Executor
	Session      *session.Session
	AgentID      string // agent identifier (e.g. "default", "coder")
	AgentName    string // human-readable name (e.g. "Assistant", "Coder")
	Model        string
	Workspace    string
	MaxTurns     int                 // safety limit for tool-use loops
	SystemPrompt string              // optional: inline system prompt (overrides IDENTITY.md)
	Skills       *skill.Loader       // optional: skill loader for selective injection
	Memory       *memory.Manager     // optional: memory manager for context retrieval
	Cortex       *cortex.Cortex      // optional: Cortex knowledge graph for recall/ingest
	Compaction   *compaction.Manager // optional; nil → no compaction

	// IngestSource controls whether this run's thread is ingested into Cortex.
	// "chat" (or empty for backward compatibility) ingests; "cron" / "heartbeat"
	// / any other value skips ingest. Recall always runs regardless — only the
	// write side is gated. Without this, scheduled runs flood the knowledge
	// graph with the agent's own tool-use chatter and queue embed calls that
	// block the next user-initiated chat.
	IngestSource string

	calibrator *tokens.Calibrator
}

// Run executes the agent loop for a user message, returning a channel of events.
// images is an optional slice of image attachments to include with the user message.
func (r *Runtime) Run(ctx context.Context, userMsg string, images []llm.ImageContent) (<-chan AgentEvent, error) {
	events := make(chan AgentEvent, 100)
	tr := TraceFrom(ctx)
	tr.Mark("agent.run.start", "user_msg_len", len(userMsg), "images", len(images))

	go func() {
		defer close(events)
		defer tr.Summary()

		// Append user message to session (with optional images)
		if len(images) > 0 {
			var imgData []session.ImageData
			for _, img := range images {
				imgData = append(imgData, session.ImageData{
					MimeType: img.MimeType,
					Data:     base64.StdEncoding.EncodeToString(img.Data),
				})
			}
			r.Session.Append(session.UserMessageWithImagesEntry(userMsg, imgData))
		} else {
			r.Session.Append(session.UserMessageEntry(userMsg))
		}

		// Initialise Cortex thread and recall (once, before the loop).
		// Recall runs in a background goroutine so it overlaps with skill
		// matching, memory search, and message assembly. The main goroutine
		// waits for it (with a hard cap) right before invoking the LLM.
		// On timeout the sub-context is cancelled so the goroutine doesn't
		// keep tying up the embedder after the user wait has elapsed.
		//
		// Recall is skipped for trivial messages ("ok", "thanks", greetings,
		// very short replies) since they will not match anything useful and
		// each call costs an embed.
		//
		// Ingest is gated by IngestSource — only "chat" (or empty for
		// backward compat) writes to the graph. Cron and heartbeat runs
		// would otherwise flood the graph with agent self-talk.
		var thread []conversation.Message
		cortexContext := ""
		var cortexCh chan string // receives the formatted hint+results
		var cortexCancel context.CancelFunc
		shouldIngest := r.IngestSource == "" || r.IngestSource == "chat"
		if r.Cortex != nil {
			thread = []conversation.Message{{Role: "user", Content: userMsg}}
			if cortexadapter.ShouldRecall(userMsg) {
				cortexCh = make(chan string, 1)
				var cortexCtx context.Context
				cortexCtx, cortexCancel = context.WithCancel(ctx)
				cxStart := time.Now()
				cxModel := r.Cortex
				go func() {
					results, err := cxModel.Recall(cortexCtx, userMsg, cortex.WithLimit(5))
					tr.Mark("cortex.recall", "hits", len(results), "err", err != nil, "dur_ms_local", time.Since(cxStart).Milliseconds())
					out := cortexadapter.CortexHint
					if err != nil {
						slog.Debug("cortex recall error", "error", err)
					} else if extra := cortexadapter.FormatResults(results); extra != "" {
						out += extra
					}
					cortexCh <- out
				}()
			} else {
				tr.Mark("cortex.recall.skipped", "reason", "trivial")
				cortexContext = cortexadapter.CortexHint
			}

			// Deferred ingest fires on every goroutine exit path. Skipped for
			// non-chat runs (cron / heartbeat) so the graph stays focused on
			// human-initiated conversations.
			cx := r.Cortex
			defer func() {
				if cortexCancel != nil {
					cortexCancel()
				}
				if shouldIngest && len(thread) > 1 {
					cortexadapter.IngestThreadAsync(context.Background(), cx, thread)
				}
			}()
		}

		maxTurns := r.MaxTurns
		if maxTurns == 0 {
			maxTurns = 25
		}

		// Cached per-request: skill / memory matches don't change between
		// turns since they're keyed on userMsg.
		var matchedSkills []skill.Skill
		var matchedMemory []memory.Entry

		for turn := 0; turn < maxTurns; turn++ {
			// Check for cancellation at the top of each turn
			if ctx.Err() != nil {
				events <- AgentEvent{Type: EventAborted}
				return
			}

			// Assemble context with skills and memory
			phaseStart := time.Now()
			systemPrompt := assembleSystemPrompt(r.Workspace, r.SystemPrompt, r.AgentID, r.AgentName, r.Tools.Names())

			// Inject relevant skills. We only match once per request (turn 0)
			// because the user message — what we match against — doesn't
			// change across turns. Re-matching on every turn was costing
			// 3–7s of prefill (skills add ~10 K chars to the system prompt).
			if r.Skills != nil {
				if turn == 0 {
					skillStart := time.Now()
					matchedSkills = r.Skills.MatchSkills(userMsg, 1)
					tr.Mark("skills.match", "turn", turn, "matched", len(matchedSkills), "dur_ms_local", time.Since(skillStart).Milliseconds())
				}
				if extra := skill.FormatForPrompt(matchedSkills); extra != "" {
					systemPrompt += extra
				}
			}

			// Inject relevant memory. Same per-request caching as skills:
			// the query is the user message, which doesn't change between
			// turns, so the search result doesn't either.
			if r.Memory != nil {
				if turn == 0 {
					memStart := time.Now()
					matchedMemory = r.Memory.Search(userMsg, 3)
					tr.Mark("memory.search", "turn", turn, "hits", len(matchedMemory), "dur_ms_local", time.Since(memStart).Milliseconds())
				}
				if extra := memory.FormatForPrompt(matchedMemory); extra != "" {
					systemPrompt += extra
				}
			}

			// Inject Cortex context. On the first turn we need to wait for the
			// background Recall (with a hard 800ms cap so a slow embedder can't
			// stall the entire request). Subsequent turns reuse the result.
			// On timeout we cancel the goroutine so it stops consuming embedder
			// capacity after the user wait elapses.
			if cortexCh != nil && cortexContext == "" {
				select {
				case cortexContext = <-cortexCh:
				case <-time.After(800 * time.Millisecond):
					tr.Mark("cortex.recall.timeout", "turn", turn, "budget_ms", 800)
					if cortexCancel != nil {
						cortexCancel()
						cortexCancel = nil
					}
					cortexContext = cortexadapter.CortexHint
				}
				cortexCh = nil
			}
			if cortexContext != "" {
				systemPrompt += cortexContext
			}

			history := r.Session.View()
			msgs := assembleMessages(history)

			// Prune oversized tool results
			pruneToolResults(msgs, maxToolResultLen)

			toolDefs := r.Tools.ToolDefs()
			tr.Mark("context.assemble", "turn", turn, "msgs", len(msgs), "tools", len(toolDefs), "sysprompt_chars", len(systemPrompt), "dur_ms_local", time.Since(phaseStart).Milliseconds())

			// Preventive compaction check.
			// Two triggers, either is sufficient:
			//   - Token estimate exceeds threshold * model context window.
			//   - Hard message-count cap (compactMsgsTrigger). For local
			//     models the reported window is huge (32K tokens default)
			//     so the threshold-based check almost never fires; the
			//     count cap keeps prefill bounded for fast TTFT.
			if r.Compaction != nil && r.Model != "" {
				if r.calibrator == nil {
					r.calibrator = tokens.NewCalibrator()
				}
				estimate := r.calibrator.Adjust(tokens.Estimate(msgs, systemPrompt, toolDefs))
				window := tokens.ContextWindow(r.Model)
				threshold := 0.6
				if r.Compaction != nil && r.Compaction.Threshold > 0 {
					threshold = r.Compaction.Threshold
				}
				thresholdHit := window > 0 && estimate > int(threshold*float64(window))
				countHit := len(msgs) > compactMsgsTrigger
				if thresholdHit || countHit {
					events <- AgentEvent{Type: EventCompactionStart}
					res, _ := r.Compaction.MaybeCompact(ctx, r.Session, compaction.ReasonPreventive, "")
					if res.Compacted {
						events <- AgentEvent{Type: EventCompactionDone, Compaction: &res}
						// Re-assemble messages after compaction.
						history = r.Session.View()
						msgs = assembleMessages(history)
						pruneToolResults(msgs, maxToolResultLen)
					} else {
						events <- AgentEvent{Type: EventCompactionSkipped, Compaction: &res}
					}
				}
			}

			req := llm.ChatRequest{
				Model:        r.Model,
				Messages:     msgs,
				Tools:        toolDefs,
				MaxTokens:    8192,
				SystemPrompt: systemPrompt,
			}

			// Call LLM
			llmStart := time.Now()
			prefillChars := len(systemPrompt)
			for _, m := range msgs {
				prefillChars += len(m.Content)
			}
			tr.Mark("llm.request_sent", "turn", turn, "model", r.Model, "prefill_chars", prefillChars)
			stream, err := r.LLM.ChatStream(ctx, req)
			if err != nil {
				if compaction.IsContextOverflow(err) && r.Compaction != nil {
					events <- AgentEvent{Type: EventCompactionStart}
					res, _ := r.Compaction.MaybeCompact(ctx, r.Session, compaction.ReasonReactive, "")
					if res.Compacted {
						events <- AgentEvent{Type: EventCompactionDone, Compaction: &res}
						// Re-assemble + retry once.
						history = r.Session.View()
						msgs = assembleMessages(history)
						pruneToolResults(msgs, maxToolResultLen)
						req.Messages = msgs
						stream, err = r.LLM.ChatStream(ctx, req)
					} else {
						events <- AgentEvent{Type: EventCompactionSkipped, Compaction: &res}
					}
				}
				if err != nil {
					events <- AgentEvent{Type: EventError, Error: fmt.Errorf("llm error: %w", err)}
					return
				}
			}

			// Collect the response
			var textContent strings.Builder
			var toolCalls []llm.ToolCall
			gotFirstToken := false

			for event := range stream {
				switch event.Type {
				case llm.EventTextDelta:
					if !gotFirstToken {
						gotFirstToken = true
						tr.Mark("llm.first_token", "turn", turn, "ttft_ms", time.Since(llmStart).Milliseconds())
					}
					textContent.WriteString(event.Text)
					events <- AgentEvent{Type: EventTextDelta, Text: event.Text}

				case llm.EventToolCallStart:
					if !gotFirstToken {
						gotFirstToken = true
						tr.Mark("llm.first_token", "turn", turn, "ttft_ms", time.Since(llmStart).Milliseconds(), "kind", "tool_call")
					}
					events <- AgentEvent{Type: EventToolCallStart, ToolCall: event.ToolCall}

				case llm.EventToolCallDone:
					if event.ToolCall != nil {
						toolCalls = append(toolCalls, *event.ToolCall)
					}

				case llm.EventDone:
					if event.Usage != nil && r.calibrator != nil {
						r.calibrator.Update(event.Usage.InputTokens, tokens.Estimate(msgs, systemPrompt, toolDefs))
					}

				case llm.EventError:
					events <- AgentEvent{Type: EventError, Error: event.Error}
					return
				}
			}
			tr.Mark("llm.stream_end", "turn", turn,
				"total_ms", time.Since(llmStart).Milliseconds(),
				"text_chars", textContent.Len(),
				"tool_calls", len(toolCalls))

			// Save assistant response to session
			if textContent.Len() > 0 {
				r.Session.Append(session.AssistantMessageEntry(textContent.String()))
				if r.Cortex != nil {
					thread = append(thread, conversation.Message{
						Role:    "assistant",
						Content: textContent.String(),
					})
				}
			}

			// If no tool calls, we're done
			if len(toolCalls) == 0 {
				tr.Mark("agent.done", "turn", turn, "reason", "no_tool_calls")
				events <- AgentEvent{Type: EventDone}
				return
			}

			// Save tool calls to session and accumulate in Cortex thread.
			for _, tc := range toolCalls {
				r.Session.Append(session.ToolCallEntry(tc.ID, tc.Name, tc.Input))
				if r.Cortex != nil {
					thread = append(thread, conversation.Message{
						Role:    "assistant",
						Content: fmt.Sprintf("[tool: %s]\n%s", tc.Name, string(tc.Input)),
					})
				}
			}

			// Execute tools
			for _, tc := range toolCalls {
				// Check for cancellation before each tool
				if ctx.Err() != nil {
					events <- AgentEvent{Type: EventAborted}
					return
				}

				slog.Debug("executing tool", "tool", tc.Name, "id", tc.ID, "input", string(tc.Input))

				toolStart := time.Now()
				result, err := r.Tools.Execute(ctx, tc.Name, tc.Input)
				if err != nil {
					result = tools.ToolResult{Error: err.Error()}
				}
				tr.Mark("tool.exec",
					"turn", turn,
					"tool", tc.Name,
					"dur_ms_local", time.Since(toolStart).Milliseconds(),
					"err", result.Error != "",
					"output_chars", len(result.Output))

				// Check for cancellation after tool execution
				if ctx.Err() != nil {
					events <- AgentEvent{Type: EventAborted}
					return
				}

				// Log tool result
				if result.Error != "" {
					slog.Warn("tool error", "tool", tc.Name, "id", tc.ID, "error", result.Error)
				} else {
					outPreview := result.Output
					if len(outPreview) > 500 {
						outPreview = outPreview[:500] + "...(truncated)"
					}
					slog.Debug("tool result", "tool", tc.Name, "id", tc.ID, "output_len", len(result.Output), "output", outPreview)
				}

				// Convert tool result images to session image data
				var imgData []session.ImageData
				for _, img := range result.Images {
					imgData = append(imgData, session.ImageData{
						MimeType: img.MimeType,
						Data:     base64.StdEncoding.EncodeToString(img.Data),
					})
				}

				// Save tool result to session and accumulate in Cortex thread.
				r.Session.Append(session.ToolResultEntry(tc.ID, result.Output, result.Error, imgData))
				if r.Cortex != nil {
					content := result.Output
					if result.Error != "" {
						content = "[error] " + result.Error
					}
					thread = append(thread, conversation.Message{Role: "user", Content: content})
				}

				events <- AgentEvent{
					Type:     EventToolResult,
					ToolCall: &tc,
					Result:   &result,
				}
			}

			// Loop back for next LLM turn with tool results
		}

		events <- AgentEvent{
			Type:  EventError,
			Error: fmt.Errorf("agent exceeded maximum turns (%d)", maxTurns),
		}
	}()

	return events, nil
}

// RunSync is a convenience method that runs the agent and collects the full text response.
func (r *Runtime) RunSync(ctx context.Context, userMsg string, images []llm.ImageContent) (string, error) {
	events, err := r.Run(ctx, userMsg, images)
	if err != nil {
		return "", err
	}

	var response strings.Builder
	for event := range events {
		switch event.Type {
		case EventTextDelta:
			response.WriteString(event.Text)
		case EventError:
			return response.String(), event.Error
		}
	}

	return response.String(), nil
}
