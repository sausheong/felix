package agent

import (
	"log/slog"

	"github.com/sausheong/cortex"
	"github.com/sausheong/felix/internal/compaction"
	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/skill"
	"github.com/sausheong/felix/internal/tools"
)

// RuntimeDeps holds the long-lived dependencies that every Runtime in this
// process shares. Built once at startup and reused for every Runtime
// construction (including subagent runtimes built by the task tool factory).
type RuntimeDeps struct {
	Skills     *skill.Loader
	Memory     *memory.Manager
	Permission tools.PermissionChecker
	// CortexFn returns the per-agent Cortex instance for the given model
	// string. Nil-safe at call site (returns nil if cortex is disabled).
	CortexFn func(model string) *cortex.Cortex
	// AgentLoop carries the agentLoop config block (concurrency cap, depth
	// cap, streaming-tools toggle). Copied verbatim into every Runtime built
	// by BuildRuntimeForAgent. Zero value → readers fall back to env vars
	// then compiled-in defaults.
	AgentLoop config.AgentLoopConfig
	// Config is the live *config.Config. Used during BuildRuntimeForAgent
	// to pre-compute the configuration summary that goes into the static
	// system prompt — replaces the per-turn config.Load("") that the old
	// configSummary() did.
	Config *config.Config
}

// RuntimeInputs holds the per-Runtime-instance inputs that genuinely vary
// per call site: the resolved LLM provider for this agent's model, the tool
// executor (different per heartbeat/cron/chat/subagent path), the session,
// the per-agent compaction manager, and the IngestSource flag (controls
// whether this run writes to Cortex).
type RuntimeInputs struct {
	Provider     llm.LLMProvider
	Tools        tools.Executor
	Session      *session.Session
	Compaction   *compaction.Manager
	IngestSource string // "" | "chat" | "cron" | "heartbeat"
}

// BuildRuntimeForAgent constructs a Runtime for the given AgentConfig using
// the supplied deps + inputs. It centralises three patterns that are currently
// duplicated across 6 call sites:
//  1. Parsing the model identifier (provider/model) for Runtime.Model
//  2. Parsing the reasoning mode (with default-to-off + warning on invalid)
//  3. Resolving the per-agent Cortex via deps.CortexFn (nil-safe)
//
// Returns (*Runtime, nil) — the error return is reserved for future
// validation (e.g., "agent config requires X feature this build doesn't have")
// but is currently always nil.
func BuildRuntimeForAgent(deps RuntimeDeps, inputs RuntimeInputs, a *config.AgentConfig) (*Runtime, error) {
	provider, modelName := llm.ParseProviderModel(a.Model)
	reasoning, err := llm.ParseReasoningMode(a.Reasoning)
	if err != nil {
		slog.Error("invalid reasoning mode in agent config; defaulting to off",
			"agent", a.ID, "value", a.Reasoning, "err", err)
		reasoning = llm.ReasoningOff
	}
	var cx *cortex.Cortex
	if deps.CortexFn != nil {
		cx = deps.CortexFn(a.Model)
	}

	// Pre-compute the static portion of the system prompt so the per-turn
	// hot loop never reads config or rebuilds the skills index.
	configSummary := BuildConfigSummary(deps.Config)
	skillsIndex := ""
	if deps.Skills != nil {
		skillsIndex = deps.Skills.FormatIndex()
	}
	var toolNames []string
	if inputs.Tools != nil {
		toolNames = inputs.Tools.Names()
	}
	memoryFiles := LoadAgentMemoryFiles(a.Workspace)
	staticPrompt := BuildStaticSystemPrompt(
		a.Workspace, a.SystemPrompt, a.ID, a.Name,
		toolNames, configSummary, skillsIndex,
		memoryFiles,
	)

	return &Runtime{
		LLM:                inputs.Provider,
		Tools:              inputs.Tools,
		Session:            inputs.Session,
		AgentID:            a.ID,
		AgentName:          a.Name,
		Model:              modelName,
		Provider:           provider,
		Reasoning:          reasoning,
		Workspace:          a.Workspace,
		MaxTurns:           a.MaxTurns,
		SystemPrompt:       a.SystemPrompt,
		Skills:             deps.Skills,
		Memory:             deps.Memory,
		Cortex:             cx,
		Permission:         deps.Permission,
		Compaction:         inputs.Compaction,
		IngestSource:       inputs.IngestSource,
		AgentLoop:          deps.AgentLoop,
		StaticSystemPrompt: staticPrompt,
	}, nil
}
