// Package chatexec runs a single chat turn end-to-end: derives the run
// context, opens the session, drives the harness runtime, fans events
// out to the runs.Registry (for durable replay) and to a live Subscriber
// (for WebSocket clients).
//
// The package is consumed both by the WebSocket chat handler and by the
// inbox worker that delivers agent-to-agent messages, so it must not
// depend on any transport.
package chatexec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/oklog/ulid/v2"
	"github.com/sausheong/cortex"

	"github.com/sausheong/felix/internal/agent"
	"github.com/sausheong/felix/internal/compaction"
	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/gateway/runs"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/skill"
	"github.com/sausheong/felix/internal/tokens"
	"github.com/sausheong/felix/internal/tools"
)

// MetricsLike is the minimal metrics surface chatexec uses for both
// per-turn counters (IncChatTurns) and per-tool-call counters
// (IncToolCalls, called from ChatToolOverlay.Execute). Backed by
// gateway.Metrics in production; tests pass nil and chatexec/overlay
// skip the counter bumps.
type MetricsLike interface {
	IncChatTurns()
	IncToolCalls(toolName string)
}

// CortexProvider resolves a per-agent *cortex.Cortex client keyed on
// the agent's model. Mirrors the production *cortex/Provider; the
// interface lets tests and the inbox worker swap in a fake without
// dragging the cortex package along.
type CortexProvider interface {
	For(agentModel string) (*cortex.Cortex, error)
}

// TurnDeps carries the per-call dependencies a turn needs. All fields
// come from the WebSocketHandler / startup wiring. Pass everything
// non-nil except where documented otherwise.
type TurnDeps struct {
	Runs           *runs.Registry
	Sessions       *session.Store
	SessionsBase   string
	Providers      map[string]llm.LLMProvider
	Tools          tools.Executor
	Permission     tools.PermissionChecker
	Skills         *skill.Loader
	Memory         *memory.Manager
	CompactionProv *compaction.Provider
	Config         *config.Config
	SubagentBuild  agent.SubagentBuildFn
	JobScheduler   tools.JobScheduler
	Metrics        MetricsLike // may be nil
	ServerCtx      context.Context
	CortexProvider CortexProvider // optional; may be nil

	// OnTraceMark, if non-nil, is called for every trace phase mark
	// emitted during the turn. The WS handler uses this to forward
	// phase markers to the conn as JSON-RPC notifications; the inbox
	// worker passes nil (traces are still logged via slog).
	OnTraceMark func(phase string, durMs, atMs int64, attrs []any)
}

// Subscriber receives live events as a turn progresses. Implementations
// must be non-blocking — they're called from the drain goroutine that's
// also writing to disk.
type Subscriber interface {
	OnAttached(runID string)
	OnEvent(e runs.Event)
}

// ErrAgentNotConfigured is returned when scope.AgentID is not in Config.
var ErrAgentNotConfigured = errors.New("agent not configured")

// ErrProviderNotConfigured is returned when the agent's provider is
// missing from Providers.
var ErrProviderNotConfigured = errors.New("LLM provider not configured")

// ErrRunsRegistryMissing is returned when deps.Runs is nil. The
// registry is mandatory for chatexec's per-turn lifecycle (Append/Finish).
var ErrRunsRegistryMissing = errors.New("runs registry not configured")

// RunTurn drives a single chat turn end-to-end. It is the shared
// primitive used by both the WebSocket chat handler and the inbox
// wake-loop worker; both call sites care about the same lifecycle
// guarantees:
//
//   - The run lands in deps.Runs.SupersedeAndCreate before any agent
//     events are emitted, so a concurrent abort can find it.
//   - Every event is persisted via Run.Append (durable replay).
//   - Run.Finish is called exactly once on every exit path — happy
//     path, error path, drain panic, or context cancel.
//   - compactionMgr.ForgetSession runs after Finish on every path, so
//     per-session locks never leak.
//   - runCancel runs last so any straggling goroutines unblock.
//
// Returns the run ID (so callers can record it for later cancellation)
// and a terminal error, if any. The returned error reflects ctx
// cancellation when the caller's ctx is cancelled mid-flight; the run
// itself is always Finished before the function returns.
func RunTurn(ctx context.Context, deps TurnDeps, scope runs.SessionScope, text string, sub Subscriber) (string, error) {
	if deps.Runs == nil {
		return "", ErrRunsRegistryMissing
	}

	// 1. Resolve agent config.
	agentCfg, ok := deps.Config.GetAgent(scope.AgentID)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrAgentNotConfigured, scope.AgentID)
	}

	// 2. Resolve LLM provider.
	providerName, _ := llm.ParseProviderModel(agentCfg.Model)
	provider, ok := deps.Providers[providerName]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrProviderNotConfigured, providerName)
	}

	// 3. Load session.
	sess, err := deps.Sessions.Load(scope.AgentID, scope.SessionKey)
	if err != nil {
		return "", fmt.Errorf("session load: %w", err)
	}

	// 4. Resolve cortex + compaction. Both are optional; nil values are
	// honoured by the harness runtime and by Manager.ForgetSession.
	var cx *cortex.Cortex
	if deps.CortexProvider != nil {
		if c, cerr := deps.CortexProvider.For(agentCfg.Model); cerr != nil {
			slog.Warn("chatexec: cortex resolve failed", "agent", agentCfg.ID, "model", agentCfg.Model, "error", cerr)
		} else {
			cx = c
		}
	}
	var compactionMgr *compaction.Manager
	if deps.CompactionProv != nil {
		compactionMgr = deps.CompactionProv.For(agentCfg.Model)
	}

	if deps.Metrics != nil {
		deps.Metrics.IncChatTurns()
	}

	// 5. Build runtime.
	permission := deps.Permission
	runtimeDeps := agent.RuntimeDeps{
		Skills:     deps.Skills,
		Memory:     deps.Memory,
		Permission: permission,
		CortexFn:   func(_ string) *cortex.Cortex { return cx },
		AgentLoop:  deps.Config.AgentLoop,
		Config:     deps.Config,
	}
	rt, err := agent.BuildRuntimeForAgent(runtimeDeps, agent.RuntimeInputs{
		Provider:   provider,
		Tools:      deps.Tools,
		Session:    sess,
		Compaction: compactionMgr,
	}, agentCfg)
	if err != nil {
		return "", fmt.Errorf("build runtime: %w", err)
	}

	// 5b. Wire per-chat tool overlay: "task" (subagent dispatch capturing
	// THIS rt as parent) and "cron" (capturing THIS agent's ID). The
	// overlay is per-call because both tools have call-site-specific
	// captures — registering them on the shared registry would race-clobber
	// other chats or cross-wire state.
	overlay := &ChatToolOverlay{Base: deps.Tools, Metrics: deps.Metrics}
	if deps.SubagentBuild != nil && deps.Config != nil {
		if eligible := deps.Config.EligibleSubagents(); len(eligible) > 0 {
			factory := agent.MakeSubagentFactory(deps.Config, runtimeDeps, deps.SubagentBuild, rt)
			overlay.Task = tools.NewTaskTool(factory, rt.Depth, eligible)
		}
	}
	if deps.JobScheduler != nil {
		overlay.Cron = &tools.CronTool{AgentID: agentCfg.ID, Scheduler: deps.JobScheduler}
	}
	if overlay.Task != nil || overlay.Cron != nil {
		rt.Tools = overlay
	}

	// 6. Allocate run + supersede.
	//
	// Derive runCtx from deps.ServerCtx (process-wide ctx). Tie the
	// caller's ctx to the same cancel via AfterFunc so an upstream
	// cancellation (the WS conn dying, an inbox worker stop) actually
	// interrupts the harness loop. AfterFunc returns a stop closure we
	// release on exit so we don't leak the goroutine.
	parentCtx := deps.ServerCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	runCtx, runCancel := context.WithCancel(parentCtx)
	if ctx != nil && ctx != parentCtx {
		stop := context.AfterFunc(ctx, runCancel)
		defer stop()
	}

	runID := ulid.Make().String()

	// 6a. Notify subscriber BEFORE SupersedeAndCreate so the client's
	// liveRunID is set before the run_started broadcast fires from
	// OnNewRun. Otherwise the originating conn races: run_started
	// arrives → liveRunID is still nil → frontend fires chat.replay →
	// fanout delivers events that wsSubscriber is also delivering →
	// character-interleaved double rendering.
	//
	// If SupersedeAndCreate then fails, the client has an orphan
	// liveRunID for a runID that will never produce events; the next
	// chat.send overwrites it. No leak.
	if sub != nil {
		sub.OnAttached(runID)
	}

	oldRun, run, err := deps.Runs.SupersedeAndCreate(scope, runID, runCancel)
	if err != nil {
		runCancel()
		if compactionMgr != nil {
			compactionMgr.ForgetSession(sess)
		}
		return "", fmt.Errorf("create run: %w", err)
	}
	if oldRun != nil {
		if oldRun.CancelFn != nil {
			oldRun.CancelFn()
		}
		_ = oldRun.Finish(runs.StatusCancelled, runs.ReasonSuperseded, runID)
	}

	// 7b. Trace setup. The trace emits one slog.Info "perf" line per phase
	// boundary plus a final "perf summary". When OnTraceMark is provided,
	// each mark is also forwarded synchronously to the caller — the WS
	// handler uses this to live-stream phase markers to the conn as
	// JSON-RPC notifications.
	trace := agent.NewTrace(agentCfg.ID, agentCfg.Model)
	if deps.OnTraceMark != nil {
		trace.SetOnMark(deps.OnTraceMark)
	}
	trace.Mark("chat.received", "msg_chars", len(text))
	runCtx = agent.WithTrace(runCtx, trace)

	// 8. Start the agent loop.
	events, err := rt.Run(runCtx, text, nil)
	if err != nil {
		_ = run.Finish(runs.StatusFailed, "", "")
		runCancel()
		if compactionMgr != nil {
			compactionMgr.ForgetSession(sess)
		}
		return runID, fmt.Errorf("run: %w", err)
	}

	// 9. Drain events synchronously (RunTurn blocks until terminal — its
	// caller spawns a goroutine if it wants to be non-blocking).
	//
	// Lifecycle: defers run LIFO. The deferred cleanup runs in this
	// order on every exit path including panic:
	//   1. recover() — promote a drain panic to StatusFailed before
	//      Finish gets a chance to mark Completed.
	//   2. Finish — write the terminal index entry, atomic CAS-once.
	//   3. runCancel + ForgetSession — release ctx + per-session lock.
	finishStatus := runs.StatusCompleted
	var finishReason runs.CancelReason
	gotTerminal := false

	defer func() {
		if compactionMgr != nil {
			compactionMgr.ForgetSession(sess)
		}
		runCancel()
	}()
	defer func() {
		if !gotTerminal {
			if ferr := run.Finish(finishStatus, finishReason, ""); ferr != nil {
				slog.Warn("chatexec: run.Finish", "run", run.ID, "error", ferr)
			}
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("chatexec: drain panic", "runID", run.ID, "panic", r)
			finishStatus = runs.StatusFailed
			// Finish runs in the outer defer above. Mark gotTerminal=false
			// so it actually fires (a happy-path EventDone would have set
			// gotTerminal=true and already called Finish itself).
			gotTerminal = false
		}
	}()

	for event := range events {
		result := buildAgentEventResult(event, agentCfg)
		if result == nil {
			continue
		}
		payload, err := json.Marshal(result)
		if err != nil {
			slog.Error("chatexec: marshal agent event", "type", event.Type, "error", err)
			continue
		}
		evtType := eventTypeFor(event.Type)
		seq, appendErr := run.Append(evtType, payload)
		if appendErr != nil {
			// Append returns an error after Finish has already been called
			// (race with abort/shutdown). Promote to Failed and stop draining.
			slog.Warn("chatexec: run.Append after finish", "run", run.ID, "error", appendErr)
			gotTerminal = true
			finishStatus = runs.StatusFailed
			break
		}
		if sub != nil {
			sub.OnEvent(runs.Event{
				Seq:     seq,
				Type:    evtType,
				Payload: payload,
			})
		}

		switch event.Type {
		case agent.EventDone:
			gotTerminal = true
			finishStatus = runs.StatusCompleted
			_ = run.Finish(runs.StatusCompleted, "", "")
		case agent.EventAborted:
			gotTerminal = true
			finishStatus = runs.StatusCancelled
			finishReason = runs.ReasonUserAbort
			_ = run.Finish(runs.StatusCancelled, runs.ReasonUserAbort, "")
		case agent.EventError:
			gotTerminal = true
			finishStatus = runs.StatusFailed
			_ = run.Finish(runs.StatusFailed, "", "")
		}
	}

	if !gotTerminal {
		// Channel closed without a terminal event — the harness exited
		// (likely ctx cancel from shutdown or caller abort). Mark as
		// interrupted; the deferred Finish writes the index entry.
		finishStatus = runs.StatusInterrupted
	}

	if ctx != nil {
		if cerr := ctx.Err(); cerr != nil {
			return runID, cerr
		}
	}
	return runID, nil
}

// eventTypeFor maps a harness agent.EventType to the runs.EventType
// recorded on disk and broadcast to subscribers. Mirror of the
// equivalent helper in gateway/websocket.go — kept in sync so the
// on-disk wire shape is identical regardless of which caller invoked
// RunTurn.
func eventTypeFor(t agent.EventType) runs.EventType {
	switch t {
	case agent.EventTextDelta:
		return runs.EventTypeTextDelta
	case agent.EventToolCallStart:
		return runs.EventTypeToolCallStart
	case agent.EventToolResult:
		return runs.EventTypeToolResult
	case agent.EventCompactionStart:
		return runs.EventTypeCompactionStart
	case agent.EventCompactionDone:
		return runs.EventTypeCompactionDone
	case agent.EventCompactionSkipped:
		return runs.EventTypeCompactionSkipped
	case agent.EventDone:
		return runs.EventTypeAgentDone
	case agent.EventError:
		return runs.EventTypeError
	case agent.EventAborted:
		return runs.EventTypeAborted
	default:
		return runs.EventTypeError
	}
}

// buildAgentEventResult converts a harness AgentEvent into the flat
// map shape the chat frontend already consumes off the JSON-RPC
// notification's `result` field. Returns nil when the event has no
// renderable payload (e.g. a compaction event with a missing payload).
//
// This MUST match the wire shape produced by the gateway's identical
// helper — the frontend already depends on the field names and types,
// and chat.replay reconstructs the exact same notification from the
// payload bytes we marshal here.
func buildAgentEventResult(event agent.AgentEvent, agentCfg *config.AgentConfig) map[string]any {
	switch event.Type {
	case agent.EventTextDelta:
		return map[string]any{"type": "text_delta", "text": event.Text}
	case agent.EventToolCallStart:
		return map[string]any{
			"type":  "tool_call_start",
			"tool":  event.ToolCall.Name,
			"id":    event.ToolCall.ID,
			"input": safeRawMessage(event.ToolCall.Input),
		}
	case agent.EventToolResult:
		r := map[string]any{
			"type":   "tool_result",
			"tool":   event.ToolCall.Name,
			"id":     event.ToolCall.ID,
			"input":  safeRawMessage(event.ToolCall.Input),
			"output": event.Result.Output,
			"error":  event.Result.Error,
		}
		if id, ok := event.Result.Metadata["auth_required"].(string); ok && id != "" {
			// Surface MCP re-auth signal to the chat client so it can
			// render an inline "Re-authenticate" button bound to this
			// server. See chat.go renderToolResult.
			r["auth_required"] = id
		}
		if len(event.Result.Images) > 0 {
			var imgs []map[string]string
			for _, img := range event.Result.Images {
				imgs = append(imgs, map[string]string{
					"mimeType": img.MimeType,
					"data":     base64.StdEncoding.EncodeToString(img.Data),
				})
			}
			r["images"] = imgs
		}
		return r
	case agent.EventCompactionStart:
		return map[string]any{"type": "compaction.start"}
	case agent.EventCompactionDone:
		if event.Compaction == nil {
			return nil
		}
		return map[string]any{
			"type":           "compaction.done",
			"turnsCompacted": event.Compaction.TurnsCompacted,
			"durationMs":     event.Compaction.DurationMs,
		}
	case agent.EventCompactionSkipped:
		if event.Compaction == nil {
			return nil
		}
		return map[string]any{
			"type":    "compaction.skipped",
			"reason":  string(event.Compaction.Reason),
			"skipped": event.Compaction.Skipped,
		}
	case agent.EventDone:
		done := map[string]any{"type": "done"}
		if event.Usage != nil {
			done["usage"] = map[string]any{
				"input_tokens":                event.Usage.InputTokens,
				"output_tokens":               event.Usage.OutputTokens,
				"cache_creation_input_tokens": event.Usage.CacheCreationInputTokens,
				"cache_read_input_tokens":     event.Usage.CacheReadInputTokens,
			}
			done["context_window"] = tokens.ContextWindowFor(agentCfg.Model, agentCfg.ContextWindow)
			done["model"] = agentCfg.Model
		}
		return done
	case agent.EventError:
		var msg string
		if event.Error != nil {
			msg = event.Error.Error()
		}
		return map[string]any{"type": "error", "message": msg}
	case agent.EventAborted:
		return map[string]any{"type": "aborted"}
	default:
		return nil
	}
}

// safeRawMessage returns the input json.RawMessage when it is non-empty
// and parses as valid JSON, and `nil` otherwise. Mirror of the gateway
// helper — see gateway/websocket.go for the full rationale (bad inputs
// from upstream model glitches would otherwise abort the entire write).
func safeRawMessage(m json.RawMessage) any {
	if len(m) == 0 || !json.Valid(m) {
		return nil
	}
	return m
}
