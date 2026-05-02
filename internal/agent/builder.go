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
	"github.com/sausheong/felix/internal/tokens"
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
	// CalibratorStore is the per-(agentID, sessionKey) persistence layer
	// for the token Calibrator. nil disables persistence; in-memory
	// learning still happens. When non-nil, BuildRuntimeForAgent loads
	// the prior (ratio, count) and seeds the new Runtime's calibrator
	// so the first turn after a chat.send rebuild already has accurate
	// token estimates instead of starting at ratio=1.0.
	CalibratorStore *tokens.CalibratorStore
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
// Returns the constructed Runtime and a nil error today, but callers MUST
// check the error: the return is reserved for future validation (e.g.,
// "agent config requires X feature this build doesn't have"). Discarding
// it and dereferencing the *Runtime would nil-panic the moment any
// validation lands.
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

	// Register the on-demand load tools (sub-project 5) onto the agent's
	// tool registry. Doing this here means every BuildRuntimeForAgent call
	// site (chat, gateway, cron, subagent factory) gets the tools without
	// having to remember to register them — and skipping registration
	// when the corresponding manager is nil keeps test runtimes lean.
	//
	// Type-assert to *tools.Registry rather than widening tools.Executor
	// with Register: the Executor interface is consumed by partition,
	// dispatch, and gateway code paths plus several test fakes; broadening
	// it would force every fake to add a method none of them need.
	// Production callers always pass *tools.Registry; test paths that
	// don't won't get load tools registered (which is fine for them).
	if reg, ok := inputs.Tools.(*tools.Registry); ok {
		if deps.Skills != nil {
			reg.Register(&tools.LoadSkillTool{
				Lookup: func(name string) (string, bool) {
					for _, s := range deps.Skills.Skills() {
						if s.Name == name {
							return s.Body, true
						}
					}
					return "", false
				},
			})
		}
		if deps.Memory != nil {
			reg.Register(&tools.LoadMemoryTool{
				Lookup: func(id string) (string, bool) {
					e, ok := deps.Memory.Get(id)
					if !ok {
						return "", false
					}
					return e.Content, true
				},
			})
		}
	}

	// Pre-compute the static portion of the system prompt so the per-turn
	// hot loop never reads config or rebuilds the skills/memory indices.
	// The load tools are registered above so toolNames includes them in
	// the default-identity tool hints.
	configSummary := BuildConfigSummary(deps.Config)
	skillsIndex := ""
	if deps.Skills != nil {
		skillsIndex = deps.Skills.FormatIndex()
	}
	memoryIndex := ""
	if deps.Memory != nil {
		memoryIndex = deps.Memory.FormatIndex()
	}
	var toolNames []string
	if inputs.Tools != nil {
		toolNames = inputs.Tools.Names()
	}
	memoryFiles := LoadAgentMemoryFiles(a.Workspace)
	staticPrompt := BuildStaticSystemPrompt(
		a.Workspace, a.SystemPrompt, a.ID, a.Name,
		toolNames, configSummary, skillsIndex,
		memoryIndex, memoryFiles,
	)

	// Strip the provider prefix off FallbackModel so the runtime hands
	// the same provider client a bare model id on retry. Cross-provider
	// fallback isn't supported here — Runtime.LLM is one client; if the
	// configured fallback names a different provider it's a config bug,
	// so we log and discard.
	fallbackModel := ""
	if a.FallbackModel != "" {
		fbProvider, fbModel := llm.ParseProviderModel(a.FallbackModel)
		if fbProvider != "" && fbProvider != provider {
			slog.Warn("fallbackModel ignored: cross-provider fallback not supported",
				"agent", a.ID,
				"primary_provider", provider,
				"fallback", a.FallbackModel)
		} else {
			fallbackModel = fbModel
		}
	}

	rt := &Runtime{
		LLM:                inputs.Provider,
		Tools:              inputs.Tools,
		Session:            inputs.Session,
		AgentID:            a.ID,
		AgentName:          a.Name,
		Model:              modelName,
		FallbackModel:      fallbackModel,
		ContextWindow:      a.ContextWindow,
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
		CalibratorStore:    deps.CalibratorStore,
	}

	// Seed the calibrator from prior (ratio, count) for this session so a
	// long session that's been split across many chat.send calls retains
	// its learned chars→tokens ratio. Skipped for subagent sessions
	// (Session.Key == "subagent") and when no store is configured.
	if deps.CalibratorStore != nil && inputs.Session != nil && inputs.Session.Key != "" && inputs.Session.Key != "subagent" {
		ratio, count := deps.CalibratorStore.Load(a.ID, inputs.Session.Key)
		if count > 0 {
			rt.calibrator = tokens.NewCalibrator()
			rt.calibrator.Restore(ratio, count)
		}
	}

	return rt, nil
}
