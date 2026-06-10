# Phase C — Agent Loop: Subagents (Task Tool)

**Status:** Draft
**Date:** 2026-04-29
**Scope:** `internal/agent/`, `internal/tools/`, `internal/config/config.go`, `internal/startup/startup.go`, `cmd/felix/main.go`
**Builds on:** Phase A (`docs/superpowers/specs/2026-04-29-agent-loop-phase-a-design.md`, merged `c5bbcf9`) and Phase B (`docs/superpowers/specs/2026-04-29-agent-loop-phase-b-design.md`, merged `45ab904`).

## Context

Felix today supports multiple agents via `config.AgentConfig` and routes inbound messages to one agent per session. There is no mechanism for an agent to delegate work to another agent mid-turn.

Claude Code's analogue (`src/tools/AgentTool/runAgent.ts:748`) registers a `Task` tool that recursively calls `query()` with a fresh `agentToolUseContext`, letting the parent agent invoke a specialized subagent and use the subagent's final response as the tool result. This unlocks compositional patterns: a "default" agent delegates research to a "researcher" agent that has only web tools, summary to a "summarizer" with tighter token budgets, etc.

Phase C ports this pattern to Felix using the foundations Phase A (`dispatchTool` seam) and Phase B (parallel-safe Runtime, PermissionChecker single source of truth) already provide.

## Goals

- A new `task` tool lets a parent agent invoke any subagent-eligible agent with a prompt and use the subagent's final assistant text as the tool result.
- Subagents are opt-in per `AgentConfig` (new `Subagent bool` field).
- Subagent invocation runs in-memory only — no JSONL sidechain files; the parent's own session is the only persistent record.
- Subagent intermediate events (text deltas, tool calls, tool results) are forwarded to the parent's events channel with the subagent's `AgentID` attached, so a UI can render `[researcher →]`-style prefixes.
- Recursion depth is capped (default 3, env-overridable) to prevent runaway delegation.
- Parent ctx cancellation propagates to in-flight subagents; subagent failures don't cancel the parent (just produce a tool-result error).

## Non-Goals

- Persistent subagent sessions / sidechain JSONL files.
- Per-parent allow-lists (any opt-in subagent is callable by any parent that has the `task` tool).
- Subagent-to-parent communication beyond the final tool result (no streaming of partial results, no callback APIs).
- New session-DAG semantics for subagent turns.
- Per-call subagent overrides (model, system prompt, tools) — subagent uses its own AgentConfig as-is.

## Design

### 1. Config schema additions

`internal/config/config.go`:

```go
type AgentConfig struct {
    // ... existing fields
    Subagent    bool   `json:"subagent,omitempty"`
    Description string `json:"description,omitempty"`
}
```

`Subagent` defaults to `false` (existing agents keep their behavior — chat-routing only). When `Subagent: true`, `Description` is required at config-load time (non-empty string). Validation error otherwise.

`Description` is shown to the parent's LLM in the `task` tool's description so it knows which agent to pick. Format:

```
Available subagents:
  - researcher: Web research specialist. Pass a topic; returns a summary with citations.
  - summarizer: Compresses long text into 200-word summaries.
  - coder: Writes Go code in this project's style; returns a diff.
```

### 2. TaskTool

New file `internal/tools/task.go`:

```go
type TaskTool struct {
    factory     SubagentFactory
    eligibleIDs map[string]string // agent_id → description
    descBlock   string            // formatted block for tool description
}

// SubagentFactory builds a Runner for the given subagent. Implementations
// enforce the depth cap and validate that agentID is opt-in.
type SubagentFactory func(
    ctx context.Context,
    agentID string,
    parentDepth int,
) (SubagentRunner, error)

// SubagentRunner is the narrow interface TaskTool calls. Avoids the import
// cycle that would otherwise exist between tools and agent.
type SubagentRunner interface {
    Run(ctx context.Context, prompt string) (<-chan AgentEventLike, error)
}

// AgentEventLike is the minimal shape TaskTool needs from agent.AgentEvent.
// Defined in this package to keep tools standalone.
type AgentEventLike struct {
    Type    int    // matches agent.EventType
    Text    string
    Done    bool   // true when terminal event arrives
    Aborted bool   // true if EventAborted
    Err     error
}

func NewTaskTool(factory SubagentFactory, eligible map[string]string) *TaskTool

// Tool interface methods:
func (t *TaskTool) Name() string                              { return "task" }
func (t *TaskTool) Description() string                       { /* fixed prefix + descBlock */ }
func (t *TaskTool) Parameters() json.RawMessage               { /* {agent_id, prompt} schema */ }
func (t *TaskTool) IsConcurrencySafe(_ json.RawMessage) bool  { return false }
func (t *TaskTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
```

`TaskTool.Execute`:
1. Parse `{agent_id, prompt}` from input. Reject malformed.
2. If `agent_id` not in `eligibleIDs`: return tool result with error message listing valid IDs.
3. Call `factory(ctx, agent_id, parentDepth)`. Factory returns error on depth-cap violation.
4. Call `runner.Run(ctx, prompt)` → events channel.
5. Drain events:
   - Accumulate `EventTextDelta` text into a `strings.Builder`.
   - On `EventAborted`: return `ToolResult{Error: "subagent aborted"}`.
   - On `EventError`: return `ToolResult{Error: ev.Err.Error()}`.
   - On `EventDone`: return `ToolResult{Output: builder.String()}`.

Event forwarding to the parent's events channel happens INSIDE the subagent Runtime (Section 4) — TaskTool doesn't manage that.

The narrow `AgentEventLike` type lets `internal/tools` stay decoupled from `internal/agent`. The subagent's Run goroutine adapts its `AgentEvent` into `AgentEventLike` for the channel TaskTool consumes. Parent event forwarding still uses the full `AgentEvent` type via the `Parent *Runtime` pointer (Section 4).

### 3. Recursion depth

`internal/agent/runtime.go`:

```go
type Runtime struct {
    // ... existing
    Depth int // 0 for top-level; subagents get parent.Depth + 1
}
```

`internal/agent/depth.go` (new):

```go
// maxAgentDepth reads FELIX_MAX_AGENT_DEPTH (default 3).
func maxAgentDepth() int {
    if v := os.Getenv("FELIX_MAX_AGENT_DEPTH"); v != "" {
        if n, err := strconv.Atoi(v); err == nil && n > 0 {
            return n
        }
    }
    return 3
}
```

The factory enforces the cap before constructing a subagent Runtime:

```go
func makeSubagentFactory(...) tools.SubagentFactory {
    return func(ctx context.Context, agentID string, parentDepth int) (tools.SubagentRunner, error) {
        if parentDepth+1 > maxAgentDepth() {
            return nil, fmt.Errorf("subagent depth limit %d reached", maxAgentDepth())
        }
        // ...
    }
}
```

The factory closure captures `parent.Depth` at registration time. When TaskTool calls the factory, it passes `parentDepth` from this captured value.

### 4. Event forwarding + final-text capture

`AgentEvent` gains an `AgentID` field (additive, backwards-compatible — existing code reading other fields is unaffected):

```go
type AgentEvent struct {
    Type       EventType
    AgentID    string  // NEW: emitter agent ID; "" for top-level
    Text       string
    ToolCall   *llm.ToolCall
    Result     *tools.ToolResult
    Error      error
    Compaction *compaction.Result
}
```

`Runtime` gains two new fields:

```go
type Runtime struct {
    // ... existing
    Depth     int                // 0 for top-level; subagents = parent.Depth + 1
    Parent    *Runtime           // nil for top-level; set by factory for subagents
    events    chan AgentEvent    // assigned at the start of Run; lowercase = unexported
}
```

`Runtime.Run` assigns `r.events = make(chan AgentEvent, 100)` at the very start (replacing the current local `events := make(chan AgentEvent, 100)`) and returns it as today. Storing it as a field exposes it to subagents constructed mid-Run, which need to push forwarded events into the parent's channel.

`Runtime.Run` forwards every event it produces to `r.Parent.events` (if Parent != nil) BEFORE sending to its own events channel. The forwarded event has `AgentID` set to `r.AgentID`. The local-only event (consumed by TaskTool) has `AgentID` empty (TaskTool doesn't need it).

```go
// Helper used at every event emission site in Run:
func (r *Runtime) emit(ev AgentEvent) {
    if r.Parent != nil {
        forward := ev
        forward.AgentID = r.AgentID
        select {
        case r.Parent.events <- forward:
        default:
            // parent's channel full — drop forwarded event; subagent continues.
            // Loss is logged at debug level. The final result still lands via TaskTool.
        }
    }
    r.events <- ev
}
```

The non-blocking forward prevents a backpressure deadlock: if the parent's event channel is full because the parent is mid-tool-execution (it IS — TaskTool is running), the subagent doesn't block on it.

This refactor replaces every `events <- AgentEvent{...}` site in Run with `r.emit(AgentEvent{...})`. ~10 sites today. The change is mechanical; the contract for top-level callers (a buffered channel returned from `Run`) is unchanged.

**Final-text capture**: TaskTool drains the local events channel, accumulating `EventTextDelta` text. When `EventDone` arrives, returns the accumulated text. If the subagent emits multiple text blocks in its final assistant message (rare), they're concatenated.

### 5. Cancellation

- Parent ctx → factory passes same ctx into subagent's Run → subagent's `ctx.Err()` checks (in dispatchTool, partition, runBatch) propagate cancellation naturally.
- Subagent abort (EventAborted) does NOT cancel the parent. TaskTool returns a tool result with `Error: "subagent aborted"`; parent loop continues to the next tool call (or to the next assistant turn).
- Subagent's parallel batch (Phase B) inherits the same ctx — its goroutines see cancellation.

### 6. Registration flow

A new helper in `internal/agent/builder.go` (new file) builds a Runtime for a subagent:

```go
type RuntimeDeps struct {
    Cfg         *config.Config
    Providers   map[string]llm.LLMProvider
    SkillLoader *skill.Loader
    MemMgr      *memory.Manager
    CortexFn    func(model string) *cortex.Cortex
    Permission  tools.PermissionChecker
    Compaction  *compaction.Manager
    MCPMgr      *mcp.Manager
}

// BuildRuntimeForAgent constructs a Runtime configured for the given AgentConfig.
// Used both by startup.go's chat/cron/heartbeat paths AND by makeSubagentFactory.
func BuildRuntimeForAgent(deps *RuntimeDeps, a *config.AgentConfig) (*Runtime, error)
```

Phase C extracts the per-Runtime construction logic that's currently duplicated across the 6 sites in startup.go and main.go into this single helper. Each site reduces to:

```go
rt, err := agent.BuildRuntimeForAgent(deps, &agentCfg)
```

This refactor isn't strictly Phase C scope but is necessary to keep `makeSubagentFactory` from re-implementing the construction. The helper is small (~40 lines) and shared.

`makeSubagentFactory` then:

```go
func makeSubagentFactory(deps *RuntimeDeps, parent *Runtime) tools.SubagentFactory {
    return func(ctx context.Context, agentID string, parentDepth int) (tools.SubagentRunner, error) {
        if parentDepth+1 > maxAgentDepth() {
            return nil, fmt.Errorf("subagent depth limit %d reached", maxAgentDepth())
        }
        a, ok := deps.Cfg.GetAgent(agentID)
        if !ok || !a.Subagent {
            return nil, fmt.Errorf("agent %q is not registered as a subagent", agentID)
        }
        rt, err := BuildRuntimeForAgent(deps, a)
        if err != nil {
            return nil, err
        }
        rt.Parent = parent
        rt.Depth = parentDepth + 1
        return &subagentRunnerAdapter{rt: rt}, nil
    }
}

// subagentRunnerAdapter satisfies tools.SubagentRunner by adapting Runtime.Run.
type subagentRunnerAdapter struct{ rt *Runtime }

func (s *subagentRunnerAdapter) Run(ctx context.Context, prompt string) (<-chan tools.AgentEventLike, error) {
    raw, err := s.rt.Run(ctx, prompt, nil)
    if err != nil {
        return nil, err
    }
    out := make(chan tools.AgentEventLike, 16)
    go func() {
        defer close(out)
        for ev := range raw {
            out <- adaptEvent(ev) // converts agent.AgentEvent → tools.AgentEventLike
        }
    }()
    return out, nil
}
```

**Conditional registration**: TaskTool is only built when `cfg` has at least one agent with `Subagent: true`. If no subagents are configured, the parent's tool list doesn't include `task` at all. Detection happens in a small `eligibleSubagents(cfg) map[string]string` helper.

Each top-level Runtime construction site adds:

```go
rt := agent.BuildRuntimeForAgent(deps, &agentCfg)

if eligible := eligibleSubagents(deps.Cfg); len(eligible) > 0 {
    factory := makeSubagentFactory(deps, rt)
    taskTool := tools.NewTaskTool(factory, eligible)
    rt.Tools.Register(taskTool) // post-construction registration to break the circular dep
}
```

The `Register` on the existing `tools.Registry` is already public — no `AddTool` mutator needed. The original Section 5 design proposed `AddTool` but `Registry.Register` already serves the purpose.

## Testing

### Unit tests (`internal/tools/task_test.go`)

1. `TestTaskTool_UnknownAgentReturnsError` — agent_id not in eligible list → tool result with error message
2. `TestTaskTool_DelegatesToFactoryAndCapturesText` — factory returns a stub runner emitting `EventTextDelta("hello world")` then `EventDone` → tool result Output == "hello world"
3. `TestTaskTool_SubagentAbortReturnsErrorResult` — runner emits `EventAborted` → tool result Error == "subagent aborted"
4. `TestTaskTool_FactoryDepthErrorPassesThrough` — factory returns depth-cap error → tool result carries that error message
5. `TestTaskTool_MalformedInputReturnsError` — input missing `agent_id` or `prompt` → tool result with validation error

### Unit tests (`internal/agent/depth_test.go`)

6. `TestMaxAgentDepth_Default` — empty env → 3
7. `TestMaxAgentDepth_EnvOverride` — env "5" → 5
8. `TestMaxAgentDepth_InvalidFallsBack` — env "garbage" or "0" → 3

### Unit tests (`internal/config/config_test.go`)

9. `TestConfig_SubagentRequiresDescription` — agent with `Subagent: true` and empty `Description` → validation error at load
10. `TestConfig_SubagentEligibilityHelper` — `eligibleSubagents(cfg)` returns only agents with `Subagent: true`

### Integration tests (`internal/agent/agent_test.go`)

11. `TestRun_SubagentDelegationProducesFinalText` — parent agent emits a `task` tool call; subagent runs (in-memory); parent sees `EventToolResult` with the subagent's final text. Subagent's session is gone after the call (in-memory only).

12. `TestRun_SubagentEventsForwardedToParent` — assert intermediate events from the subagent appear in the parent's events channel with `AgentID` populated.

13. `TestRun_SubagentDepthCapEnforced` — chain A→B→C with cap=3 succeeds; A→B→C→D returns depth-limit error in the tool result (D never constructed).

14. `TestRun_SubagentAbortPropagatesFromParent` — cancel parent ctx mid-subagent execution; subagent terminates; both sessions end with paired entries (Phase A invariant); parent's session has the task tool call paired with an aborted result.

15. `TestRun_SubagentNotInEligibleListReturnsError` — try to invoke an agent_id whose config has `Subagent: false` → tool result error.

### Race-detector run

All 15 new tests must pass under `go test -race ./...`.

## Migration

No data migration. Existing felix.json5 configs without `Subagent` / `Description` fields parse cleanly (zero-values default to false / empty). No JSONL changes (subagent sessions are in-memory, never persisted). New `Parent` and `Depth` fields on Runtime default to nil/0 — backwards-compatible.

## Risks

- **Event forwarding non-blocking drop**: if the parent's events channel is genuinely overwhelmed, subagent intermediate events are silently dropped (only the final result via TaskTool survives). Acceptable — the channel is buffered at 100, and the tool result is the only API-relevant signal.
- **Recursion via task → tool calls task**: a depth-3 chain wastes turns even though the cap eventually fires. If FELIX_MAX_AGENT_DEPTH is set high (e.g., 10) and the model is uncareful, you can spend a lot of tokens on nested delegation. Default 3 is the safety net.
- **Subagent inheriting parent tools** is NOT what happens — each subagent's tools come from its own AgentConfig. A subagent that needs the `task` tool itself must have it registered via the same conditional logic. (Implication: if researcher.Subagent=true and another agent wants to invoke researcher → researcher's runtime ALSO gets task tool registered if other subagents exist. Recursion is opt-in via the cap.)
- **Backward compatibility of AgentEvent.AgentID**: if any existing UI code does shallow struct comparisons on AgentEvent (e.g., `assert.Equal(t, AgentEvent{Type:EventDone}, got)`), the new field changes the comparison result. Search confirmed no such callers in the felix codebase.
- **`BuildRuntimeForAgent` extraction touches all 6 Runtime construction sites**. This is a refactor of code Phase A and B both touched. Risk: introducing a subtle change in how cron vs heartbeat vs websocket runtimes are configured. Mitigation: the new helper preserves the exact field-by-field assignment of the existing sites; integration tests for all three paths exist.

## Out of scope (deferred)

- Phase D: streaming tool execution (kicks off tools as `tool_use` blocks arrive).
- Phase B-polish: ctx-aware semaphore acquire (I-2 from Phase B's final review).
- Persistent subagent sessions / sidechain JSONL.
- Per-parent allow-lists for which subagents can be invoked.
- Subagent timeout limits separate from the parent's MaxTurns.
- Subagent token budget tracking separate from the parent's.
- Hook system for PreSubagentRun / PostSubagentRun.
