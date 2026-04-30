// Package agent — subagent factory + adapter.
//
// This file wires the per-Runtime task tool: it constructs a tools.SubagentFactory
// that builds a fresh agent.Runtime for the named subagent, sets its Parent
// pointer (for event forwarding) and Depth (for the recursion cap), and adapts
// agent.Runtime.Run to tools.SubagentRunner so the task tool can drain it.
//
// The factory enforces the depth cap (maxAgentDepth()) before constructing
// anything — a subagent that would exceed the limit returns an error that
// TaskTool surfaces as a tool result error, which the parent LLM then sees.
package agent

import (
	"context"
	"fmt"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/tools"
)

// SubagentBuildFn constructs the per-subagent RuntimeInputs given the resolved
// AgentConfig. Each call site provides this closure because tool-registry
// construction (RegisterCoreTools / MCP / send_message) is environment-specific:
// startup.go has the fully-wired MCP manager and policy, cmd/felix/main.go has
// the chat-mode tool registry, etc.
//
// Implementations MUST:
//   - Build a fresh tools.Executor for the subagent (workspace = a.Workspace)
//   - Resolve the LLM provider from a.Model
//   - Create a fresh in-memory Session via NewSubagentSession
//   - Set IngestSource to "" (subagents are short-lived; no Cortex ingest)
//   - Set Compaction to whatever the call site's chat path uses for this agent
type SubagentBuildFn func(a *config.AgentConfig) (RuntimeInputs, error)

// MakeSubagentFactory returns a tools.SubagentFactory that builds a Runtime
// for the named subagent and adapts it to tools.SubagentRunner. Enforces the
// recursion depth cap before constructing anything.
//
// parent is the invoking Runtime — its Depth is captured so the cap check
// (parent.Depth + 1 <= maxAgentDepth()) fires before BuildRuntimeForAgent.
//
// cfg is the live config so EligibleSubagents() / GetAgent() reflect the
// current registered subagents (config hot-reload safe by reading through
// the *Config pointer at factory-call time, not at registration time).
func MakeSubagentFactory(
	cfg *config.Config,
	deps RuntimeDeps,
	buildInputs SubagentBuildFn,
	parent *Runtime,
) tools.SubagentFactory {
	return func(ctx context.Context, agentID string, parentDepth int) (tools.SubagentRunner, error) {
		// Depth-cap enforcement. Currently defense-in-depth: production subagent
		// tool registries do not register the task tool itself, so a subagent
		// cannot invoke another subagent — depth > 1 is unreachable from
		// production paths today. The cap is here so that adding `task` to
		// subagent registries in the future (e.g., for explicit recursive
		// delegation patterns) doesn't open the door to runaway delegation.
		if parentDepth+1 > parent.maxAgentDepth() {
			return nil, fmt.Errorf("subagent depth limit %d reached", parent.maxAgentDepth())
		}
		a, ok := cfg.GetAgent(agentID)
		if !ok {
			return nil, fmt.Errorf("subagent %q not found in config", agentID)
		}
		if !a.Subagent {
			return nil, fmt.Errorf("agent %q is not registered as a subagent", agentID)
		}
		inputs, err := buildInputs(a)
		if err != nil {
			return nil, fmt.Errorf("subagent %q: build inputs: %w", agentID, err)
		}
		rt, err := BuildRuntimeForAgent(deps, inputs, a)
		if err != nil {
			return nil, fmt.Errorf("subagent %q: build runtime: %w", agentID, err)
		}
		rt.Parent = parent
		rt.Depth = parentDepth + 1
		return &subagentRunnerAdapter{rt: rt}, nil
	}
}

// subagentRunnerAdapter satisfies tools.SubagentRunner by adapting agent.Runtime.Run.
// Each event from the subagent is converted to tools.AgentEventLike and pushed
// to the channel TaskTool drains. Forwarding to the parent's events channel
// happens INSIDE the subagent Runtime's emit() (set up via rt.Parent), not here —
// the adapter only carries text deltas / done / aborted / error to TaskTool so
// the parent sees the subagent's final assistant text as the tool output.
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
			out <- adaptEvent(ev)
		}
	}()
	return out, nil
}

// adaptEvent translates an agent.AgentEvent into the tools.AgentEventLike
// shape that TaskTool understands. Only the fields TaskTool actually inspects
// are filled — intermediate tool_call / tool_result events are forwarded to
// the parent stream by Runtime.emit, not by TaskTool's drain loop.
func adaptEvent(ev AgentEvent) tools.AgentEventLike {
	return tools.AgentEventLike{
		Type:    int(ev.Type),
		Text:    ev.Text,
		Done:    ev.Type == EventDone,
		Aborted: ev.Type == EventAborted,
		Err:     ev.Error,
	}
}

// NewSubagentSession returns a fresh in-memory Session for a subagent run.
// Centralised here so all 6 call sites build subagent sessions the same way.
// Crucially: SetStore is NOT called — subagent sessions are ephemeral and do
// NOT write JSONL to disk. The parent's session is the durable record; the
// subagent's transcript lives only in memory and is lost on Run completion.
func NewSubagentSession(agentID string) *session.Session {
	return session.NewSession(agentID, "subagent")
}
