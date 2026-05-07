// Package agent wraps github.com/sausheong/harness/runtime with the
// Felix-specific shape that pre-extraction call sites expect:
//
//   - BuildRuntimeForAgent takes *config.AgentConfig (not AgentSpec)
//   - RuntimeDeps holds Felix's typed Skills (*skill.Loader),
//     Memory (*memory.Manager), CortexFn (returning *cortex.Cortex)
//     and Config (*config.Config), plus the AgentLoop block.
//   - MakeSubagentFactory takes *config.Config and reads
//     EligibleSubagents / GetAgent off it, wrapping into the harness
//     SubagentResolver interface internally.
//
// All other types (Runtime, AgentEvent, EventType, Trace) and the spill
// directory janitor (CleanupOrphanedSpills) are direct re-exports. The
// adapter glue lives entirely in this file.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/sausheong/cortex"
	conv "github.com/sausheong/cortex/connector/conversation"
	hrt "github.com/sausheong/harness/runtime"
	"github.com/sausheong/harness/tool"

	"github.com/sausheong/felix/internal/config"
	cortexadapter "github.com/sausheong/felix/internal/cortex"
	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/skill"
	"github.com/sausheong/felix/internal/tokens"
)

// --- Type aliases ---

type (
	Runtime        = hrt.Runtime
	AgentEvent     = hrt.AgentEvent
	EventType      = hrt.EventType
	Trace          = hrt.Trace
	RuntimeInputs  = hrt.RuntimeInputs
	LiveSessionKeysFn = hrt.LiveSessionKeysFn
)

const (
	EventTextDelta         = hrt.EventTextDelta
	EventToolCallStart     = hrt.EventToolCallStart
	EventToolResult        = hrt.EventToolResult
	EventDone              = hrt.EventDone
	EventError             = hrt.EventError
	EventAborted           = hrt.EventAborted
	EventCompactionStart   = hrt.EventCompactionStart
	EventCompactionDone    = hrt.EventCompactionDone
	EventCompactionSkipped = hrt.EventCompactionSkipped
)

var (
	NewTrace               = hrt.NewTrace
	WithTrace              = hrt.WithTrace
	TraceFrom              = hrt.TraceFrom
	SetOTelTracer          = hrt.SetOTelTracer
	NewSubagentSession     = hrt.NewSubagentSession
	BuildStaticSystemPrompt = hrt.BuildStaticSystemPrompt
	BuildDynamicSystemPromptSuffix = func(dateLine, kgContext string) string {
		// Re-export of unexported helper isn't needed by call sites today;
		// kept here so a future export from harness can be wired without
		// changing the public Felix API.
		_ = dateLine
		_ = kgContext
		return ""
	}
	FormatDateLine    = hrt.FormatDateLine
	SpillDirForSession   = hrt.SpillDirForSession
	RemoveSessionSpill   = hrt.RemoveSessionSpill
	SpillRoot            = hrt.SpillRoot
	CleanupOrphanedSpills = hrt.CleanupOrphanedSpills

	MaxAgentMemoryBytes      = 40 * 1024 // historical re-export — used by Felix tests/UIs
	PostCompactRestoreFiles  = hrt.PostCompactRestoreFiles
	PostCompactRestoreBytesPerFile = hrt.PostCompactRestoreBytesPerFile
)

// --- Felix-shape RuntimeDeps ---

// RuntimeDeps holds Felix's per-process long-lived dependencies for
// agent runtimes. Same shape as pre-extraction; converted to
// hrt.RuntimeDeps inside BuildRuntimeForAgent.
type RuntimeDeps struct {
	Skills          *skill.Loader
	Memory          *memory.Manager
	Permission      tool.PermissionChecker
	CortexFn        func(model string) *cortex.Cortex
	AgentLoop       config.AgentLoopConfig
	Config          *config.Config
	CalibratorStore *tokens.CalibratorStore
}

// SubagentBuildFn is Felix's per-subagent inputs builder. Same signature
// as pre-extraction (takes *config.AgentConfig, returns RuntimeInputs).
type SubagentBuildFn func(a *config.AgentConfig) (RuntimeInputs, error)

// --- BuildRuntimeForAgent: bridges Felix shapes → harness AgentSpec ---

// BuildRuntimeForAgent constructs a Runtime for the given Felix AgentConfig.
// Wraps the deps into harness-shaped RuntimeDeps (interface adapters for
// Skills/Memory, KGFn closure for Cortex) and converts the AgentConfig
// into an AgentSpec, then delegates to harness.BuildRuntime.
func BuildRuntimeForAgent(deps RuntimeDeps, inputs RuntimeInputs, a *config.AgentConfig) (*Runtime, error) {
	hdeps := hrt.RuntimeDeps{
		Permission:      deps.Permission,
		AgentLoop: hrt.LoopConfig{
			MaxToolConcurrency: deps.AgentLoop.MaxToolConcurrency,
			MaxAgentDepth:      deps.AgentLoop.MaxAgentDepth,
			StreamingTools:     deps.AgentLoop.StreamingTools,
		},
		CalibratorStore: deps.CalibratorStore,
		ConfigSummary:   buildConfigSummary(deps.Config),
		MemoryFiles:     loadAgentMemoryFiles(a.Workspace),
	}
	if deps.Skills != nil {
		hdeps.Skills = skillProviderAdapter{l: deps.Skills}
	}
	if deps.Memory != nil {
		hdeps.Memory = memoryProviderAdapter{m: deps.Memory}
	}
	if deps.CortexFn != nil {
		hdeps.KGFn = func(model string) hrt.KnowledgeGraph {
			cx := deps.CortexFn(model)
			if cx == nil {
				return nil
			}
			return &cortexKGAdapter{cx: cx}
		}
	}

	spec := hrt.AgentSpec{
		ID:            a.ID,
		Name:          a.Name,
		Model:         a.Model,
		FallbackModel: a.FallbackModel,
		Workspace:     a.Workspace,
		SystemPrompt:  a.SystemPrompt,
		MaxTurns:      a.MaxTurns,
		ContextWindow: a.ContextWindow,
		Reasoning:     a.Reasoning,
	}

	return hrt.BuildRuntime(hdeps, inputs, spec)
}

// MakeSubagentFactory returns a tool.SubagentFactory that resolves
// subagents through Felix's *config.Config (cfg.GetAgent + Subagent flag
// + InheritContext flag) and builds them via the supplied
// SubagentBuildFn.
func MakeSubagentFactory(cfg *config.Config, deps RuntimeDeps, buildInputs SubagentBuildFn, parent *Runtime) tool.SubagentFactory {
	resolve := func(id string) (hrt.SubagentSpec, bool) {
		a, ok := cfg.GetAgent(id)
		if !ok {
			return hrt.SubagentSpec{}, false
		}
		return hrt.SubagentSpec{
			Spec: hrt.AgentSpec{
				ID:            a.ID,
				Name:          a.Name,
				Model:         a.Model,
				FallbackModel: a.FallbackModel,
				Workspace:     a.Workspace,
				SystemPrompt:  a.SystemPrompt,
				MaxTurns:      a.MaxTurns,
				ContextWindow: a.ContextWindow,
				Reasoning:     a.Reasoning,
			},
			Registered:     a.Subagent,
			InheritContext: a.InheritContext,
		}, true
	}

	hbuild := func(spec hrt.AgentSpec) (hrt.RuntimeInputs, error) {
		// We need the *config.AgentConfig the original SubagentBuildFn
		// expects. Resolve it once more via cfg — same lookup path the
		// resolver above used.
		a, ok := cfg.GetAgent(spec.ID)
		if !ok {
			return hrt.RuntimeInputs{}, fmt.Errorf("subagent %q not found", spec.ID)
		}
		return buildInputs(a)
	}

	hdeps := hrt.RuntimeDeps{
		Permission:      deps.Permission,
		AgentLoop: hrt.LoopConfig{
			MaxToolConcurrency: deps.AgentLoop.MaxToolConcurrency,
			MaxAgentDepth:      deps.AgentLoop.MaxAgentDepth,
			StreamingTools:     deps.AgentLoop.StreamingTools,
		},
		CalibratorStore: deps.CalibratorStore,
		ConfigSummary:   buildConfigSummary(deps.Config),
	}
	if deps.Skills != nil {
		hdeps.Skills = skillProviderAdapter{l: deps.Skills}
	}
	if deps.Memory != nil {
		hdeps.Memory = memoryProviderAdapter{m: deps.Memory}
	}
	if deps.CortexFn != nil {
		hdeps.KGFn = func(model string) hrt.KnowledgeGraph {
			cx := deps.CortexFn(model)
			if cx == nil {
				return nil
			}
			return &cortexKGAdapter{cx: cx}
		}
	}

	return hrt.MakeSubagentFactory(resolve, hdeps, hbuild, parent)
}

// --- Adapters: Felix concrete types → harness interfaces ---

type skillProviderAdapter struct{ l *skill.Loader }

func (s skillProviderAdapter) FormatIndex() string { return s.l.FormatIndex() }
func (s skillProviderAdapter) Get(name string) (string, bool) {
	for _, sk := range s.l.Skills() {
		if sk.Name == name {
			return sk.Body, true
		}
	}
	return "", false
}

type memoryProviderAdapter struct{ m *memory.Manager }

func (a memoryProviderAdapter) FormatIndex() string { return a.m.FormatIndex() }
func (a memoryProviderAdapter) Get(id string) (string, bool) {
	e, ok := a.m.Get(id)
	if !ok {
		return "", false
	}
	return e.Content, true
}

// cortexKGAdapter implements harness/runtime.KnowledgeGraph by delegating
// to Felix's cortex helpers (cortexadapter.ShouldRecall, FormatResults,
// IngestThreadAsync).
//
// Concurrency: a per-Run mutex serializes the cortex thread that the
// runtime accumulates and hands to Ingest at end of run; this adapter
// itself is stateless aside from the *cortex.Cortex pointer.
type cortexKGAdapter struct {
	cx *cortex.Cortex
	mu sync.Mutex
}

func (k *cortexKGAdapter) ShouldRecall(query string) bool {
	return cortexadapter.ShouldRecall(query)
}

func (k *cortexKGAdapter) Recall(ctx context.Context, query string) string {
	k.mu.Lock()
	cx := k.cx
	k.mu.Unlock()
	results, err := cx.Recall(ctx, query, cortex.WithLimit(5))
	out := cortexadapter.CortexHint
	if err != nil {
		slog.Debug("cortex recall error", "error", err)
		return out
	}
	if extra := cortexadapter.FormatResults(results); extra != "" {
		out += extra
	}
	return out
}

func (k *cortexKGAdapter) Ingest(ctx context.Context, thread []hrt.Message) {
	if len(thread) <= 1 {
		return
	}
	convThread := make([]conv.Message, 0, len(thread))
	for _, m := range thread {
		convThread = append(convThread, conv.Message{Role: m.Role, Content: m.Content})
	}
	cortexadapter.IngestThreadAsync(ctx, k.cx, convThread)
}

// --- Felix-side prompt composition (was BuildConfigSummary, LoadAgentMemoryFiles) ---

// buildConfigSummary mirrors the pre-extraction BuildConfigSummary in
// internal/agent/context.go: a brief inventory of configured agents
// and channels for the static system prompt.
func buildConfigSummary(cfg *config.Config) string {
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
			fmt.Fprintf(&sb, "\n- %s (id: %s, model: %s%s)", a.Name, a.ID, a.Model, tools)
		}
	}
	if cfg.Channels.CLI.Enabled {
		sb.WriteString("\n\nConfigured channels: cli")
	}
	if sb.Len() > 0 {
		sb.WriteString(fmt.Sprintf("\n\nYour configuration file is at %s and your data directory is %s.",
			config.DefaultConfigPath(), config.DefaultDataDir()))
	}
	return sb.String()
}

// loadAgentMemoryFiles is the Felix-side FELIX.md / AGENTS.md walker
// (pre-extraction internal/agent/context.go LoadAgentMemoryFiles). The
// harness no longer ships this — Felix now passes the result via
// RuntimeDeps.MemoryFiles.
func loadAgentMemoryFiles(workspace string) string {
	return loadAgentMemoryFilesImpl(workspace)
}
