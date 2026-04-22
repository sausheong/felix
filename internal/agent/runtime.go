package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

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

	calibrator *tokens.Calibrator
}

// Run executes the agent loop for a user message, returning a channel of events.
// images is an optional slice of image attachments to include with the user message.
func (r *Runtime) Run(ctx context.Context, userMsg string, images []llm.ImageContent) (<-chan AgentEvent, error) {
	events := make(chan AgentEvent, 100)

	go func() {
		defer close(events)

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
		var thread []conversation.Message
		cortexContext := ""
		if r.Cortex != nil {
			thread = []conversation.Message{{Role: "user", Content: userMsg}}

			results, err := r.Cortex.Recall(ctx, userMsg, cortex.WithLimit(5))
			if err != nil {
				slog.Debug("cortex recall error", "error", err)
				cortexContext = cortexadapter.CortexHint
			} else {
				cortexContext = cortexadapter.CortexHint
				if extra := cortexadapter.FormatResults(results); extra != "" {
					cortexContext += extra
				}
			}

			// Deferred ingest fires on every goroutine exit path.
			cx := r.Cortex
			defer func() {
				if len(thread) > 1 {
					cortexadapter.IngestThreadAsync(context.Background(), cx, thread)
				}
			}()
		}

		maxTurns := r.MaxTurns
		if maxTurns == 0 {
			maxTurns = 25
		}

		for turn := 0; turn < maxTurns; turn++ {
			// Check for cancellation at the top of each turn
			if ctx.Err() != nil {
				events <- AgentEvent{Type: EventAborted}
				return
			}

			// Assemble context with skills and memory
			systemPrompt := assembleSystemPrompt(r.Workspace, r.SystemPrompt, r.AgentID, r.AgentName, r.Tools.Names())

			// Inject relevant skills
			if r.Skills != nil {
				matched := r.Skills.MatchSkills(userMsg, 3)
				if extra := skill.FormatForPrompt(matched); extra != "" {
					systemPrompt += extra
				}
			}

			// Inject relevant memory
			if r.Memory != nil {
				memEntries := r.Memory.Search(userMsg, 3)
				if extra := memory.FormatForPrompt(memEntries); extra != "" {
					systemPrompt += extra
				}
			}

			// Inject Cortex context (recalled once before the loop).
			if cortexContext != "" {
				systemPrompt += cortexContext
			}

			history := r.Session.View()
			msgs := assembleMessages(history)

			// Prune oversized tool results
			pruneToolResults(msgs, maxToolResultLen)

			toolDefs := r.Tools.ToolDefs()

			// Preventive compaction check.
			if r.Compaction != nil && r.Model != "" {
				if r.calibrator == nil {
					r.calibrator = tokens.NewCalibrator()
				}
				estimate := r.calibrator.Adjust(tokens.Estimate(msgs, systemPrompt, toolDefs))
				window := tokens.ContextWindow(r.Model)
				if window > 0 && estimate > int(0.6*float64(window)) {
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

			for event := range stream {
				switch event.Type {
				case llm.EventTextDelta:
					textContent.WriteString(event.Text)
					events <- AgentEvent{Type: EventTextDelta, Text: event.Text}

				case llm.EventToolCallStart:
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

				result, err := r.Tools.Execute(ctx, tc.Name, tc.Input)
				if err != nil {
					result = tools.ToolResult{Error: err.Error()}
				}

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
