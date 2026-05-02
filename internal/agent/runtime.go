package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/connector/conversation"
	"github.com/sausheong/felix/internal/compaction"
	"github.com/sausheong/felix/internal/config"
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
	Type EventType
	// AgentID is the emitter's agent identifier. Empty for top-level
	// (Parent==nil) runtimes; populated by Runtime.emit when forwarding a
	// subagent's event up to its parent. Existing readers that ignore the
	// field are unaffected — this is purely additive.
	AgentID    string
	Text       string
	ToolCall   *llm.ToolCall
	Result     *tools.ToolResult
	Error      error
	Compaction *compaction.Result // populated for EventCompaction* events
	Usage      *llm.Usage         // populated for EventDone when the provider reported it
}

// Runtime is the agent think-act loop.
type Runtime struct {
	LLM          llm.LLMProvider
	Tools        tools.Executor
	Session      *session.Session
	AgentID      string // agent identifier (e.g. "default", "coder")
	AgentName    string // human-readable name (e.g. "Assistant", "Coder")
	Model        string
	Reasoning    llm.ReasoningMode // optional; zero value = ReasoningOff
	Workspace    string
	MaxTurns     int                 // safety limit for tool-use loops
	// AgentLoop carries the agentLoop config block (concurrency cap, depth
	// cap, streaming-tools toggle). Zero value → all readers fall back to
	// env vars then compiled-in defaults. Populated by BuildRuntimeForAgent
	// from RuntimeDeps.AgentLoop. Tests construct Runtime directly leave
	// this zero so the env-var fallback continues to work for them.
	AgentLoop    config.AgentLoopConfig
	SystemPrompt string              // optional: inline system prompt (overrides IDENTITY.md)
	Skills       *skill.Loader       // optional: skill loader for selective injection
	Memory       *memory.Manager     // optional: memory manager for context retrieval
	Cortex       *cortex.Cortex      // optional: Cortex knowledge graph for recall/ingest
	Compaction   *compaction.Manager // optional; nil → no compaction

	// Provider is the LLM provider name parsed from the agent's "provider/model"
	// model string (e.g., "anthropic", "openai", "local"). Used by
	// providerSupportsCaching() (Task 13) to decide whether to set
	// CacheLastMessage on outgoing ChatRequests.
	Provider string

	// FallbackModel is the bare model name (no provider prefix) to retry
	// against on a synchronous ChatStream error matching
	// llm.IsRetryableModelError. Empty string disables fallback.
	// Populated from AgentConfig.FallbackModel by BuildRuntimeForAgent
	// (provider prefix stripped — fallback always uses the same provider
	// as the primary, since LLM is bound to a single provider client).
	FallbackModel string

	// StaticSystemPrompt is the cacheable portion of the system prompt
	// (identity, agent metadata, configuration paths, configSummary,
	// skillsIndex). Built once at BuildRuntimeForAgent time; reused
	// verbatim on every turn so the Anthropic prompt cache hits.
	StaticSystemPrompt string

	// Permission gates tool execution at dispatch time. nil → allow-all
	// (matches the no-policy default).
	Permission tools.PermissionChecker

	// Depth is the recursion level: 0 for top-level chat/cron/heartbeat agents;
	// subagents get parent.Depth + 1. Used by maxAgentDepth() enforcement in
	// the subagent factory.
	Depth int

	// Parent points to the Runtime that invoked this Runtime as a subagent.
	// nil for top-level agents. When non-nil, every event emitted via emit()
	// is forwarded (with AgentID set) to Parent.events before being sent to
	// this Runtime's own events channel.
	Parent *Runtime

	// events is the channel returned by Run for the current invocation. It
	// is assigned at the very start of Run (replacing the previous local
	// variable) so that the emit() helper can route forwarded events to the
	// parent. Buffered (100). Read by the caller; written only by Run/emit.
	events chan AgentEvent

	// cortexMu serializes appends to the per-Run cortex thread slice when
	// Phase B's parallel runner invokes dispatchTool from multiple goroutines.
	// Held briefly only around the slice append; never around tool execution.
	cortexMu sync.Mutex

	// touchedFiles is the in-order list of file paths the agent has
	// successfully read/written/edited during this Runtime's lifetime
	// (deduped — re-touching a path moves it to the back). Used by the
	// post-compact file-restore path to re-inject the most recent K
	// files' contents after MaybeCompact rewrites history. Held across
	// turns because compaction can happen at any turn boundary.
	touchedMu    sync.Mutex
	touchedFiles []string

	// IngestSource controls whether this run's thread is ingested into Cortex.
	// "chat" (or empty for backward compatibility) ingests; "cron" / "heartbeat"
	// / any other value skips ingest. Recall always runs regardless — only the
	// write side is gated. Without this, scheduled runs flood the knowledge
	// graph with the agent's own tool-use chatter and queue embed calls that
	// block the next user-initiated chat.
	IngestSource string

	// CalibratorStore persists the per-session token Calibrator so its
	// learned actual/estimated ratio survives chat.send rebuilds of the
	// Runtime. nil means in-memory only — the calibrator still learns
	// across this Run, but the next Run starts fresh at ratio=1.0.
	// Loaded once at construction (BuildRuntimeForAgent) and saved
	// after every llm.EventDone with non-zero usage.
	CalibratorStore *tokens.CalibratorStore

	calibrator *tokens.Calibrator
}

// emit sends ev to this runtime's events channel. When this runtime has a
// Parent (i.e., it's a subagent), emit also forwards a copy of the event
// (with AgentID populated to this runtime's AgentID) to Parent.events.
//
// The forward is non-blocking via select+default: if the parent's channel
// is full (because the parent is mid-tool-execution — TaskTool is what
// invoked us — and not draining), the forwarded event is dropped. This
// avoids a backpressure deadlock. The final result still lands via TaskTool's
// drain of this runtime's events channel.
func (r *Runtime) emit(ev AgentEvent) {
	if r.Parent != nil {
		forward := ev
		forward.AgentID = r.AgentID
		select {
		case r.Parent.events <- forward:
		default:
			// parent's channel full — drop forwarded event; this subagent's
			// own channel still gets the event so its caller (TaskTool) sees it.
		}
	}
	r.events <- ev
}

// maybeKickoffAsyncCompaction conditionally fires Compaction.MaybeCompactAsync
// when the just-finished turn left the session close enough to the trigger
// threshold that the NEXT turn would compact synchronously. The async path
// runs the summarizer in a background goroutine so the next chat.send finds
// the work already done (or briefly waits for it via WaitForInFlight at the
// top of the loop) instead of blocking 10–20s on the user.
//
// Trigger: 80% of the configured threshold OR 80% of the message cap. Both
// are computed against the messages we JUST sent — the next turn will only
// add a couple of entries on top, so this is a close approximation of
// "would the next turn trigger sync compaction."
//
// No-op when:
//   - the manager is nil or already has an in-flight compaction for this session
//   - the model is empty (subagent / test paths)
//   - both trigger conditions are below the preemption threshold
func (r *Runtime) maybeKickoffAsyncCompaction(msgs []llm.Message, parts []llm.SystemPromptPart, toolDefs []llm.ToolDef) {
	if r.Compaction == nil || r.Model == "" {
		return
	}
	if r.Compaction.HasInFlight(r.Session.ID) {
		return
	}
	// Use the same calibrated estimate the sync path uses so the
	// preemption fires consistently with the threshold the user sees.
	if r.calibrator == nil {
		r.calibrator = tokens.NewCalibrator()
	}
	estimate := r.calibrator.Adjust(tokens.Estimate(msgs, llm.JoinSystemPromptParts(parts), toolDefs))
	window := tokens.ContextWindow(r.Model)
	threshold := 0.6
	if r.Compaction.Threshold > 0 {
		threshold = r.Compaction.Threshold
	}
	preemptThresholdHit := window > 0 && float64(estimate) > 0.8*threshold*float64(window)
	msgCap := r.Compaction.MessageCap
	preemptCountHit := msgCap > 0 && len(msgs) > int(0.8*float64(msgCap))
	if !preemptThresholdHit && !preemptCountHit {
		return
	}
	r.Compaction.MaybeCompactAsync(r.Session, compaction.ReasonPreventive)
}

// providerSupportsCaching returns true when the runtime's provider implements
// Anthropic-style explicit prompt caching. Used to decide whether to set
// CacheLastMessage on outgoing ChatRequests.
func (r *Runtime) providerSupportsCaching() bool {
	return r.Provider == "anthropic"
}

// providerSupportsMidLoopCompaction returns true for hosted frontier
// providers that handle mid-loop summary injection cleanly. The
// preventive-compaction comment near runtime.go:339 cites
// "small local model confusion" as the reason for the original
// turn==0-only restriction; that concern is provider-specific. We allow
// mid-loop compaction for anthropic/openai/gemini and keep the hard
// turn==0 gate for "local"/"ollama"/empty (treat empty as local for
// safety so a misconfigured provider doesn't accidentally opt in).
func (r *Runtime) providerSupportsMidLoopCompaction() bool {
	switch r.Provider {
	case "anthropic", "openai", "gemini":
		return true
	default:
		return false
	}
}

// recordFileTouch appends path to the touched-files list for post-compact
// restore, deduping by moving an existing entry to the end so it's
// counted as the most recent. Empty paths and tool calls without a
// "path" field are silently ignored. Safe to call from multiple
// goroutines (Phase B parallel dispatch).
func (r *Runtime) recordFileTouch(path string) {
	if path == "" || r == nil {
		return
	}
	r.touchedMu.Lock()
	defer r.touchedMu.Unlock()
	for i, p := range r.touchedFiles {
		if p == path {
			r.touchedFiles = append(append(r.touchedFiles[:i:i], r.touchedFiles[i+1:]...), path)
			return
		}
	}
	r.touchedFiles = append(r.touchedFiles, path)
}

// snapshotTouchedFiles returns a copy of the current touched-files list
// safe to read without holding touchedMu. Used by the post-compact
// restore path which reads files from disk (slow) without blocking
// concurrent tool dispatches.
func (r *Runtime) snapshotTouchedFiles() []string {
	r.touchedMu.Lock()
	defer r.touchedMu.Unlock()
	out := make([]string, len(r.touchedFiles))
	copy(out, r.touchedFiles)
	return out
}

// isFileTool reports whether a tool name's input contains a "path" field
// whose target file the agent has just touched. Centralised so adding a
// new path-bearing tool (or a future load_skill tool) only requires
// editing this one switch.
func isFileTool(name string) bool {
	switch name {
	case "read_file", "write_file", "edit_file":
		return true
	}
	return false
}

// Run executes the agent loop for a user message, returning a channel of events.
// images is an optional slice of image attachments to include with the user message.
func (r *Runtime) Run(ctx context.Context, userMsg string, images []llm.ImageContent) (<-chan AgentEvent, error) {
	r.events = make(chan AgentEvent, 100)
	tr := TraceFrom(ctx)
	tr.Mark("agent.run.start", "user_msg_len", len(userMsg), "images", len(images))

	go func() {
		defer close(r.events)
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

		// Computed once per Run so the date stays stable across turns of
		// this request (avoids cache misses on long-running loops that
		// happen to cross midnight).
		dateLine := FormatDateLine(time.Now())

		for turn := 0; turn < maxTurns; turn++ {
			// Check for cancellation at the top of each turn
			if ctx.Err() != nil {
				r.emit(AgentEvent{Type: EventAborted})
				return
			}

			// Assemble dynamic suffix only — the static portion was pre-computed
			// at Runtime construction time and lives on r.StaticSystemPrompt.
			// Skill bodies and memory entries are no longer matched here
			// (sub-project 5): the Skills / Memory indices live in the
			// cached static prompt and the agent loads bodies on demand
			// via the load_skill / load_memory tools.
			phaseStart := time.Now()

			// Cortex hint: on first turn wait for the background Recall (with
			// 800ms cap so a slow embedder can't stall the request).
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

			dynamicSuffix := buildDynamicSystemPromptSuffix(dateLine, cortexContext)

			// Build the structured system prompt: static (cached) + dynamic
			// (not cached).
			staticText := r.StaticSystemPrompt
			parts := []llm.SystemPromptPart{
				{Text: staticText, Cache: true},
			}
			if dynamicSuffix != "" {
				parts = append(parts, llm.SystemPromptPart{Text: dynamicSuffix, Cache: false})
			}

			history := r.Session.View()
			msgs := assembleMessages(history)

			// Prune oversized tool results — spill to workspace when both
			// workspace and session key are available, otherwise truncate
			// in place. spillCfg is computed once per turn so the cfg
			// passed to all three prune sites in this iteration is consistent.
			spillCfg := spillConfig{Workspace: r.Workspace, SessionKey: r.Session.Key}
			pruneToolResults(msgs, maxToolResultLen, spillCfg)

			toolDefs := r.Tools.ToolDefs()
			if r.Permission != nil {
				toolDefs = r.Permission.FilterToolDefs(toolDefs, r.AgentID)
			}
			sort.SliceStable(toolDefs, func(i, j int) bool {
				return toolDefs[i].Name < toolDefs[j].Name
			})
			toolDefs, diags := r.LLM.NormalizeToolSchema(toolDefs)
			// Info, not Warn: stripping is the expected pre-flight transform
			// (e.g. Gemini drops format/anyOf on every web_fetch call). Warn
			// implies "investigate"; this is steady-state behavior.
			for _, d := range diags {
				slog.Info("tool schema normalized",
					"tool", d.ToolName,
					"field", d.Field,
					"action", d.Action,
					"reason", d.Reason)
			}
			tr.Mark("context.assemble", "turn", turn, "msgs", len(msgs), "tools", len(toolDefs), "sysprompt_chars", len(staticText)+len(dynamicSuffix), "dur_ms_local", time.Since(phaseStart).Milliseconds())

			// Preventive compaction check.
			// Two triggers, either is sufficient:
			//   - Token estimate exceeds threshold * model context window.
			//   - Hard message-count cap (CompactionConfig.MessageCap; 0
			//     disables). For local models the reported window is huge
			//     (32K tokens default) so the threshold-based check almost
			//     never fires; the count cap keeps prefill bounded for fast TTFT.
			//
			// Mid-loop firing policy:
			//   - Local/ollama/empty providers: turn==0 only. Compacting
			//     between a tool_call and the assistant's final reply
			//     rewrites history under the model and confuses small
			//     local models that conflate the freshly-injected summary
			//     with the in-flight tool result.
			//   - Frontier providers (anthropic/openai/gemini): every
			//     turn. They handle the splice cleanly, and waiting for
			//     turn==0 forces a long session into the reactive
			//     overflow path (one wasted API call per overflow) instead
			//     of compacting proactively when prefill crosses the
			//     threshold mid-turn.
			// The reactive overflow handler below still covers all turns
			// regardless. See CompactionConfig.MessageCap for incident
			// rationale.
			// Wait briefly on any in-flight async compaction kicked off
			// by the previous turn (see end-of-Run). If it completed, the
			// session view already reflects the splice and the threshold
			// check below will likely be a no-op. If it didn't complete
			// in 8s, fall through to synchronous compaction so we don't
			// stall this turn indefinitely.
			//
			// 8s is chosen so a typical haiku-class summarizer finishes
			// within budget on a healthy network, and a stuck
			// summarizer doesn't block more than once per turn.
			if turn == 0 && r.Compaction != nil {
				if res, ok := r.Compaction.WaitForInFlight(r.Session.ID, 8*time.Second); ok && res.Compacted {
					r.emit(AgentEvent{Type: EventCompactionDone, Compaction: &res})
					history = r.Session.View()
					msgs = assembleMessages(history)
					pruneToolResults(msgs, maxToolResultLen, spillCfg)
					msgs = prependPostCompactRestore(msgs, r.snapshotTouchedFiles())
				}
			}

			compactionAllowed := turn == 0 || r.providerSupportsMidLoopCompaction()
			if compactionAllowed && r.Compaction != nil && r.Model != "" {
				if r.calibrator == nil {
					r.calibrator = tokens.NewCalibrator()
				}
				estimate := r.calibrator.Adjust(tokens.Estimate(msgs, llm.JoinSystemPromptParts(parts), toolDefs))
				window := tokens.ContextWindow(r.Model)
				threshold := 0.6
				if r.Compaction != nil && r.Compaction.Threshold > 0 {
					threshold = r.Compaction.Threshold
				}
				thresholdHit := window > 0 && estimate > int(threshold*float64(window))
				msgCap := r.Compaction.MessageCap
				countHit := msgCap > 0 && len(msgs) > msgCap
				if thresholdHit || countHit {
					r.emit(AgentEvent{Type: EventCompactionStart})
					res, _ := r.Compaction.MaybeCompact(ctx, r.Session, compaction.ReasonPreventive, "")
					if res.Compacted {
						r.emit(AgentEvent{Type: EventCompactionDone, Compaction: &res})
						// Re-assemble messages after compaction.
						history = r.Session.View()
						msgs = assembleMessages(history)
						pruneToolResults(msgs, maxToolResultLen, spillCfg)
						msgs = prependPostCompactRestore(msgs, r.snapshotTouchedFiles())
					} else {
						r.emit(AgentEvent{Type: EventCompactionSkipped, Compaction: &res})
					}
				}
			}

			req := llm.ChatRequest{
				Model:             r.Model,
				Messages:          msgs,
				Tools:             toolDefs,
				MaxTokens:         8192,
				SystemPromptParts: parts,
				CacheLastMessage:  r.providerSupportsCaching(),
				Reasoning:         r.Reasoning,
			}

			// Call LLM
			llmStart := time.Now()
			prefillChars := len(staticText) + len(dynamicSuffix)
			for _, m := range msgs {
				prefillChars += len(m.Content)
			}
			tr.Mark("llm.request_sent", "turn", turn, "model", r.Model, "prefill_chars", prefillChars)
			stream, err := r.LLM.ChatStream(ctx, req)
			if err != nil {
				if compaction.IsContextOverflow(err) && r.Compaction != nil {
					r.emit(AgentEvent{Type: EventCompactionStart})
					res, _ := r.Compaction.MaybeCompact(ctx, r.Session, compaction.ReasonReactive, "")
					if res.Compacted {
						r.emit(AgentEvent{Type: EventCompactionDone, Compaction: &res})
						// Re-assemble + retry once.
						history = r.Session.View()
						msgs = assembleMessages(history)
						pruneToolResults(msgs, maxToolResultLen, spillCfg)
						msgs = prependPostCompactRestore(msgs, r.snapshotTouchedFiles())
						req.Messages = msgs
						stream, err = r.LLM.ChatStream(ctx, req)
					} else {
						r.emit(AgentEvent{Type: EventCompactionSkipped, Compaction: &res})
					}
				}
				// Provider fallback (sub-project 6b): swap to FallbackModel
				// and retry once on transient capacity failures
				// (Anthropic 429/529, OpenAI 429/5xx). Only fires for
				// synchronous ChatStream errors — mid-stream failures
				// surface as EventError later and aren't recoverable
				// here without major reshape of the event-collection
				// loop. Single retry only; if the fallback also fails,
				// emit EventError and exit.
				if err != nil && r.FallbackModel != "" && r.FallbackModel != req.Model && llm.IsRetryableModelError(err) {
					slog.Info("llm fallback model engaged",
						"agent", r.AgentID,
						"primary", req.Model,
						"fallback", r.FallbackModel,
						"err", err.Error())
					tr.Mark("llm.fallback", "turn", turn, "primary", req.Model, "fallback", r.FallbackModel)
					req.Model = r.FallbackModel
					stream, err = r.LLM.ChatStream(ctx, req)
				}
				if err != nil {
					r.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("llm error: %w", err)})
					return
				}
			}

			// Collect the response. lastUsage holds the most recent
			// llm.EventDone usage report so the agent's outgoing
			// EventDone can carry it for the chat UI's token widget.
			var textContent strings.Builder
			var lastUsage *llm.Usage
			var toolCalls []llm.ToolCall
			gotFirstToken := false

			// Phase D: streaming tool kickoff state. When streamingOn is true
			// and a concurrency-safe tool_use completes mid-stream, we kick
			// off dispatchTool in a goroutine instead of waiting for the
			// stream to end. The post-stream block awaits each kickoff in
			// stream order. kickoffStopped flips on the first unsafe call so
			// every later call goes through the post-stream batcher (preserves
			// the "unsafe runs alone" invariant).
			streamingOn := r.streamingToolsEnabled()
			kickoffs := map[string]chan kickoffResult{}
			kickoffStopped := false

			// Mid-stream-failure retry state (sub-project 8a). If the stream
			// dies after we've started receiving tokens AND the provider
			// implements NonStreamingProvider, we discard the partial output
			// (so the prompt cache prefix stays byte-identical), retry the
			// same request via the non-streaming endpoint, and resume event
			// collection on the new channel. One retry only.
			streamSource := stream
			retriedNonStreaming := false
		streamLoop:
			for {
				for event := range streamSource {
					switch event.Type {
					case llm.EventTextDelta:
						if !gotFirstToken {
							gotFirstToken = true
							tr.Mark("llm.first_token", "turn", turn, "ttft_ms", time.Since(llmStart).Milliseconds())
						}
						textContent.WriteString(event.Text)
						r.emit(AgentEvent{Type: EventTextDelta, Text: event.Text})

					case llm.EventToolCallStart:
						if !gotFirstToken {
							gotFirstToken = true
							tr.Mark("llm.first_token", "turn", turn, "ttft_ms", time.Since(llmStart).Milliseconds(), "kind", "tool_call")
						}
						r.emit(AgentEvent{Type: EventToolCallStart, ToolCall: event.ToolCall})

					case llm.EventToolCallDone:
						if event.ToolCall == nil {
							continue
						}
						tc := *event.ToolCall
						toolCalls = append(toolCalls, tc)
						if !streamingOn || kickoffStopped {
							continue
						}
						if !isCallConcurrencySafe(tc, r.Tools) {
							kickoffStopped = true
							continue
						}
						ch := make(chan kickoffResult, 1)
						kickoffs[tc.ID] = ch
						tcCopy := tc // capture by value before launching goroutine
						go func() {
							// Phase D: run the tool WITHOUT touching session or
							// cortex thread; the main goroutine appends the
							// paired entries post-stream in stream order so the
							// AssistantMessage save (which happens after the
							// stream ends) lands BEFORE the ToolCall entry. See
							// executeToolKickoff and the post-stream resolve loop.
							result, aborted := r.executeToolKickoff(ctx, tcCopy)
							// Live UI emit happens here; session writes are deferred.
							r.emitToolResult(tr, turn, tcCopy, result, aborted)
							ch <- kickoffResult{tc: tcCopy, result: result, aborted: aborted}
						}()

					case llm.EventDone:
						if event.Usage != nil {
							lastUsage = event.Usage
						}
						if event.Usage != nil && r.calibrator != nil {
							r.calibrator.Update(event.Usage.InputTokens, tokens.Estimate(msgs, llm.JoinSystemPromptParts(parts), toolDefs))
							// Persist the updated ratio so the next chat.send
							// (which rebuilds Runtime from scratch) inherits
							// the calibration. Best-effort; nil store means
							// persistence is disabled and the in-memory
							// learning still works for this Run only.
							if r.CalibratorStore != nil && r.Session != nil {
								ratio, count := r.calibrator.Snapshot()
								r.CalibratorStore.Save(r.AgentID, r.Session.Key, ratio, count)
							}
						}

					case llm.EventError:
						// Mid-stream-failure retry: only attempts once, only
						// when we've started receiving tokens (so the failure
						// is plausibly a connection drop mid-flight, not a
						// pre-flight error like 400 Bad Request) and only when
						// the provider exposes the non-streaming endpoint.
						if gotFirstToken && !retriedNonStreaming {
							if ns, ok := r.LLM.(llm.NonStreamingProvider); ok {
								slog.Warn("stream died mid-flight; retrying as non-streaming",
									"agent", r.AgentID, "turn", turn, "err", event.Error)
								tr.Mark("llm.stream_fallback", "turn", turn, "err", event.Error.Error())
								// Discard partial output. Cancelling
								// in-flight kickoffs is essential — they may
								// still be writing tool_results that would
								// pair with tool_calls we're about to throw
								// away.
								textContent.Reset()
								toolCalls = nil
								drainKickoffs(kickoffs)
								kickoffs = map[string]chan kickoffResult{}
								gotFirstToken = false
								kickoffStopped = false
								nsStream, retryErr := ns.ChatNonStreaming(ctx, req)
								if retryErr != nil {
									r.emit(AgentEvent{Type: EventError, Error: retryErr})
									return
								}
								retriedNonStreaming = true
								streamSource = nsStream
								continue streamLoop
							}
						}
						drainKickoffs(kickoffs)
						r.emit(AgentEvent{Type: EventError, Error: event.Error})
						return
					}
				}
				break streamLoop
			}
			tr.Mark("llm.stream_end", "turn", turn,
				"total_ms", time.Since(llmStart).Milliseconds(),
				"text_chars", textContent.Len(),
				"tool_calls", len(toolCalls))

			// Save assistant response to session
			if textContent.Len() > 0 {
				r.Session.Append(session.AssistantMessageEntry(textContent.String()))
				if r.Cortex != nil {
					// Phase D introduced kickoff goroutines that may still be
					// inside dispatchTool appending to the same `thread` slice
					// under r.cortexMu. Take the lock here so the assistant-
					// text append is serialized against those concurrent writers.
					r.cortexMu.Lock()
					thread = append(thread, conversation.Message{
						Role:    "assistant",
						Content: textContent.String(),
					})
					r.cortexMu.Unlock()
				}
			}

			// If no tool calls, we're done
			if len(toolCalls) == 0 {
				if len(kickoffs) > 0 {
					// Defensive: a kickoff implies a tool_use was added to
					// toolCalls. If somehow not, drain to avoid leaking
					// goroutines.
					drainKickoffs(kickoffs)
				}
				tr.Mark("agent.done", "turn", turn, "reason", "no_tool_calls")
				r.emit(AgentEvent{Type: EventDone, Usage: lastUsage})
				// Fire async compaction between turns when the session
				// is approaching the threshold. Runs in a background
				// goroutine; the next chat.send awaits any in-flight
				// compaction at the top of its loop. Without this, a
				// session that crosses the threshold pays compaction
				// latency (often 10–20s) on the user's next turn.
				r.maybeKickoffAsyncCompaction(msgs, parts, toolDefs)
				return
			}

			// Resolve kickoffs in stream order; collect non-kicked-off tools
			// for the post-stream batcher. Session writes happen HERE (after
			// the assistant text was appended above), in stream order, so
			// every ToolCall entry sits between the AssistantMessage and its
			// matching ToolResult — preserving the API invariant that every
			// tool_result follows an assistant message containing the
			// matching tool_use.
			var pending []llm.ToolCall
			for _, tc := range toolCalls {
				if ch, ok := kickoffs[tc.ID]; ok {
					kp := <-ch
					// Append the ToolCall entry now — after the assistant
					// text has landed in the session.
					r.Session.Append(session.ToolCallEntry(kp.tc.ID, kp.tc.Name, kp.tc.Input))
					if r.Cortex != nil {
						r.cortexMu.Lock()
						thread = append(thread, conversation.Message{
							Role:    "assistant",
							Content: fmt.Sprintf("[tool: %s]\n%s", kp.tc.Name, string(kp.tc.Input)),
						})
						r.cortexMu.Unlock()
					}
					if kp.aborted {
						r.Session.Append(session.AbortedToolResultEntry(kp.tc.ID))
						if r.Cortex != nil {
							r.cortexMu.Lock()
							thread = append(thread, conversation.Message{Role: "user", Content: "[error] aborted by user"})
							r.cortexMu.Unlock()
						}
						// Drain remaining kickoffs so their goroutines exit.
						// For each, append paired entries (call + aborted
						// result) so the session never has an orphan
						// ToolCall — keeps /resume safe.
						for _, tc2 := range toolCalls {
							if tc2.ID == kp.tc.ID {
								continue
							}
							ch2, ok := kickoffs[tc2.ID]
							if !ok {
								continue
							}
							kp2 := <-ch2
							r.Session.Append(session.ToolCallEntry(kp2.tc.ID, kp2.tc.Name, kp2.tc.Input))
							r.Session.Append(session.AbortedToolResultEntry(kp2.tc.ID))
							if r.Cortex != nil {
								r.cortexMu.Lock()
								thread = append(thread, conversation.Message{
									Role:    "assistant",
									Content: fmt.Sprintf("[tool: %s]\n%s", kp2.tc.Name, string(kp2.tc.Input)),
								})
								thread = append(thread, conversation.Message{Role: "user", Content: "[error] aborted by user"})
								r.cortexMu.Unlock()
							}
						}
						r.emit(AgentEvent{Type: EventAborted})
						return
					}
					// Append the paired ToolResult entry.
					imgData := convertToolResultImages(kp.result.Images)
					r.Session.Append(session.ToolResultEntry(kp.tc.ID, kp.result.Output, kp.result.Error, imgData))
					if r.Cortex != nil {
						content := kp.result.Output
						if kp.result.Error != "" {
							content = "[error] " + kp.result.Error
						}
						r.cortexMu.Lock()
						thread = append(thread, conversation.Message{Role: "user", Content: content})
						r.cortexMu.Unlock()
					}
					continue
				}
				pending = append(pending, tc)
			}

			// Partition tool calls into batches. Concurrency-safe consecutive
			// calls run in parallel via runBatch; unsafe calls run sequentially.
			batches := partitionToolCalls(pending, r.Tools)
			for _, b := range batches {
				if r.runBatch(ctx, b, cortexThreadOrNil(r.Cortex, &thread), turn, tr) {
					r.emit(AgentEvent{Type: EventAborted})
					return
				}
			}

			// Loop back for next LLM turn with tool results
		}

		r.emit(AgentEvent{
			Type:  EventError,
			Error: fmt.Errorf("agent exceeded maximum turns (%d)", maxTurns),
		})
	}()

	return r.events, nil
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

// dispatchTool executes one tool call with strict tool_use ↔ tool_result
// pairing. It always appends a ToolCallEntry then exactly one ToolResultEntry
// (real, error, denial, or aborted) before returning, on every code path.
//
// The returned tools.ToolResult is for event emission to the caller. When
// aborted=true, the caller MUST stop dispatching subsequent tool calls in
// this turn and emit EventAborted.
//
// cortexThread, when non-nil, is appended to atomically alongside the
// session writes — both call+result land or neither does.
//
// Safe for concurrent invocation on the same Runtime: Session.Append is
// guarded by Session's own RWMutex (added in Phase B), and the cortex
// thread append is guarded by r.cortexMu.
func (r *Runtime) dispatchTool(
	ctx context.Context,
	tc llm.ToolCall,
	cortexThread *[]conversation.Message,
) (result tools.ToolResult, aborted bool) {
	// 1. Save tool call (paired ownership begins here).
	r.Session.Append(session.ToolCallEntry(tc.ID, tc.Name, tc.Input))
	if cortexThread != nil {
		r.cortexMu.Lock()
		*cortexThread = append(*cortexThread, conversation.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("[tool: %s]\n%s", tc.Name, string(tc.Input)),
		})
		r.cortexMu.Unlock()
	}

	// 2. Permission gate.
	if r.Permission != nil {
		if d := r.Permission.Check(ctx, r.AgentID, tc.Name, tc.Input); d.Behavior == tools.DecisionDeny {
			return r.appendDenialResult(tc.ID, d.Reason, cortexThread), false
		}
	}

	// 3. Pre-execute cancel check.
	if ctx.Err() != nil {
		return r.appendAbortedResult(tc.ID, cortexThread), true
	}

	// 4. Execute.
	result, err := r.Tools.Execute(ctx, tc.Name, tc.Input)
	if err != nil {
		result = tools.ToolResult{Error: err.Error()}
	}

	// 5. Post-execute cancel check. The user pressed Ctrl-C — discard the
	// tool's result (whether it returned cleanly or surfaced ctx.Err()
	// itself) and write the synthetic abort marker so /resume can render
	// it as cancelled rather than as a normal failure.
	if ctx.Err() != nil {
		return r.appendAbortedResult(tc.ID, cortexThread), true
	}

	// 6. Save paired tool result.
	imgData := convertToolResultImages(result.Images)
	r.Session.Append(session.ToolResultEntry(tc.ID, result.Output, result.Error, imgData))
	if cortexThread != nil {
		content := result.Output
		if result.Error != "" {
			content = "[error] " + result.Error
		}
		r.cortexMu.Lock()
		*cortexThread = append(*cortexThread, conversation.Message{Role: "user", Content: content})
		r.cortexMu.Unlock()
	}

	// 7. Track touched files for post-compact restore. Only on success
	// (no Error) — failed read_files don't establish that the path exists,
	// and adding bogus paths would just waste post-compact budget.
	if result.Error == "" && isFileTool(tc.Name) {
		r.recordFileTouch(extractPathFromInput(tc.Input))
	}

	return result, false
}

// executeToolKickoff runs the tool's permission gate, ctx checks, and
// Execute call WITHOUT touching session or cortex thread. Used by Phase D's
// streaming kickoff goroutines so the main loop can write session entries
// in stream order after the assistant text is saved (preserves the API
// invariant that every tool_result follows an assistant message containing
// the matching tool_use).
//
// Returns (result, aborted). aborted=true means ctx was cancelled before or
// after Execute; the caller MUST stop dispatching subsequent tool calls in
// this turn. The returned result includes the appropriate Error message for
// permission-denied or aborted cases.
func (r *Runtime) executeToolKickoff(ctx context.Context, tc llm.ToolCall) (result tools.ToolResult, aborted bool) {
	// Permission gate.
	if r.Permission != nil {
		if d := r.Permission.Check(ctx, r.AgentID, tc.Name, tc.Input); d.Behavior == tools.DecisionDeny {
			return tools.ToolResult{Error: d.Reason}, false
		}
	}
	// Pre-execute cancel check.
	if ctx.Err() != nil {
		return tools.ToolResult{Error: "aborted by user"}, true
	}
	// Execute.
	result, err := r.Tools.Execute(ctx, tc.Name, tc.Input)
	if err != nil {
		result = tools.ToolResult{Error: err.Error()}
	}
	// Post-execute cancel check.
	if ctx.Err() != nil {
		return tools.ToolResult{Error: "aborted by user"}, true
	}
	// Track touched files for post-compact restore (mirrors dispatchTool
	// step 7). Streaming-tool kickoff bypasses dispatchTool, so without
	// this hook a kickoff-spawned read_file would never be tracked.
	if result.Error == "" && isFileTool(tc.Name) {
		r.recordFileTouch(extractPathFromInput(tc.Input))
	}
	return result, false
}

// appendDenialResult writes the tool-result entry for a denied tool call and
// returns a tools.ToolResult mirroring it. Centralised so the deny path stays
// consistent with the result-emit format.
func (r *Runtime) appendDenialResult(toolCallID, reason string, cortexThread *[]conversation.Message) tools.ToolResult {
	r.Session.Append(session.ToolResultEntry(toolCallID, "", reason, nil))
	if cortexThread != nil {
		r.cortexMu.Lock()
		*cortexThread = append(*cortexThread, conversation.Message{
			Role: "user", Content: "[error] " + reason,
		})
		r.cortexMu.Unlock()
	}
	return tools.ToolResult{Error: reason}
}

// appendAbortedResult writes the synthetic abort entry and returns the
// matching tools.ToolResult. Used for both pre- and post-execute cancellation.
func (r *Runtime) appendAbortedResult(toolCallID string, cortexThread *[]conversation.Message) tools.ToolResult {
	r.Session.Append(session.AbortedToolResultEntry(toolCallID))
	if cortexThread != nil {
		r.cortexMu.Lock()
		*cortexThread = append(*cortexThread, conversation.Message{
			Role: "user", Content: "[error] aborted by user",
		})
		r.cortexMu.Unlock()
	}
	return tools.ToolResult{Error: "aborted by user"}
}

// convertToolResultImages adapts tool image attachments to session ImageData.
func convertToolResultImages(imgs []llm.ImageContent) []session.ImageData {
	if len(imgs) == 0 {
		return nil
	}
	out := make([]session.ImageData, 0, len(imgs))
	for _, img := range imgs {
		out = append(out, session.ImageData{
			MimeType: img.MimeType,
			Data:     base64.StdEncoding.EncodeToString(img.Data),
		})
	}
	return out
}

// cortexThreadOrNil returns a pointer to the cortex thread if Cortex is enabled,
// else nil. dispatchTool treats a nil pointer as "skip cortex updates".
func cortexThreadOrNil(cx *cortex.Cortex, thread *[]conversation.Message) *[]conversation.Message {
	if cx == nil {
		return nil
	}
	return thread
}
