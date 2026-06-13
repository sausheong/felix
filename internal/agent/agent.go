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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sausheong/cortex"
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

// effectiveMaxToolResultLen resolves the per-tool-result context cap.
// Felix's default is 64K — the harness's own 4000-char fallback is sized
// for chatbots, not engineering agents; at 4K nearly every file read or
// test run spills/truncates, and the re-read tax exhausts the turn budget.
func effectiveMaxToolResultLen(configured int) int {
	if configured > 0 {
		return configured
	}
	return 65536
}

// --- Prompt-input cache: invariant inputs keyed by config generation ---

var configGeneration atomic.Int64

// BumpConfigGeneration invalidates the prompt cache. Called from the startup
// fsnotify reload callback.
func BumpConfigGeneration() { configGeneration.Add(1) }

type cachedPrompt struct {
	gen           int64
	configSummary string
	memoryFiles   string
}

var (
	promptCacheMu sync.Mutex
	promptCache   = map[string]cachedPrompt{}
)

func promptCacheGet(agentID string, gen int64) (cachedPrompt, bool) {
	promptCacheMu.Lock()
	defer promptCacheMu.Unlock()
	c, ok := promptCache[agentID]
	if ok && c.gen == gen {
		return c, true
	}
	return cachedPrompt{}, false
}

func promptCachePut(agentID string, gen int64, p cachedPrompt) {
	p.gen = gen
	promptCacheMu.Lock()
	promptCache[agentID] = p
	promptCacheMu.Unlock()
}

// ConfigSummaryFor returns the (cached) config summary for the current
// generation, shared by subagent-factory call sites and BuildRuntimeForAgent.
func ConfigSummaryFor(cfg *config.Config) string {
	const key = "\x00config_summary"
	gen := configGeneration.Load()
	if c, ok := promptCacheGet(key, gen); ok {
		return c.configSummary
	}
	cs := buildConfigSummary(cfg)
	promptCachePut(key, gen, cachedPrompt{configSummary: cs})
	return cs
}

// --- BuildRuntimeForAgent: bridges Felix shapes → harness AgentSpec ---

// BuildRuntimeForAgent constructs a Runtime for the given Felix AgentConfig.
// Wraps the deps into harness-shaped RuntimeDeps (interface adapters for
// Skills/Memory, KGFn closure for Cortex) and converts the AgentConfig
// into an AgentSpec, then delegates to harness.BuildRuntime.
func BuildRuntimeForAgent(deps RuntimeDeps, inputs RuntimeInputs, a *config.AgentConfig) (*Runtime, error) {
	gen := configGeneration.Load()
	cached, ok := promptCacheGet(a.ID, gen)
	if !ok {
		cached = cachedPrompt{
			configSummary: buildConfigSummary(deps.Config),
			memoryFiles:   loadAgentMemoryFiles(a.Workspace) + felixEnvHint() + cortexStaticHint(deps.Config),
		}
		promptCachePut(a.ID, gen, cached)
	}
	hdeps := hrt.RuntimeDeps{
		Permission:      deps.Permission,
		AgentLoop: hrt.LoopConfig{
			MaxToolConcurrency: deps.AgentLoop.MaxToolConcurrency,
			MaxAgentDepth:      deps.AgentLoop.MaxAgentDepth,
			StreamingTools:     deps.AgentLoop.StreamingTools,
			MaxToolResultLen:   effectiveMaxToolResultLen(deps.AgentLoop.MaxToolResultLen),
		},
		CalibratorStore: deps.CalibratorStore,
		ConfigSummary:   cached.configSummary,
		MemoryFiles:     cached.memoryFiles,
		KGFn: func(model string) hrt.KnowledgeGraph {
			if deps.CortexFn == nil {
				return nil
			}
			return cortexadapter.NewKnowledgeGraph(deps.CortexFn(model))
		},
	}
	if deps.Skills != nil {
		hdeps.Skills = skillProviderAdapter{l: deps.Skills}
	}
	if deps.Memory != nil {
		hdeps.Memory = memoryProviderAdapter{m: deps.Memory}
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
func MakeSubagentFactory(cfg *config.Config, deps RuntimeDeps, buildInputs SubagentBuildFn, configSummary string, parent *Runtime) tool.SubagentFactory {
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
			MaxToolResultLen:   effectiveMaxToolResultLen(deps.AgentLoop.MaxToolResultLen),
		},
		CalibratorStore: deps.CalibratorStore,
		ConfigSummary:   configSummary,
		// KGFn intentionally unset — subagents do not auto-recall or ingest.
	}
	if deps.Skills != nil {
		hdeps.Skills = skillProviderAdapter{l: deps.Skills}
	}
	if deps.Memory != nil {
		hdeps.Memory = memoryProviderAdapter{m: deps.Memory}
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

// cortexStaticHint returns the CortexHint text (prepended by a blank
// line) when Cortex is globally enabled, or "" otherwise. Concatenated
// into RuntimeDeps.MemoryFiles so the hint lives in the cached static
// system prompt instead of being re-shipped on every Recall(). Returns
// "" when cfg is nil or Cortex.Enabled is false — in those cases the
// per-turn Recall() path also stays empty, so the agent never sees a
// cortex reference it can't act on.
func cortexStaticHint(cfg *config.Config) string {
	if cfg == nil || !cfg.Cortex.Enabled {
		return ""
	}
	return cortexadapter.CortexHint
}

// felixEnvHint adds a "Felix execution environment" section to the
// static system prompt: the data-dir layout (so "where is brain.db?"
// type questions get answered without a filesystem search) and
// guardrails for bash (so the agent doesn't run unscoped find / commands
// that block for minutes against the default 120s bash timeout).
func felixEnvHint() string {
	return `

## Felix execution environment

Data directory: ~/.felix/
  ~/.felix/felix.json5             your config file
  ~/.felix/brain.db                cortex knowledge graph (SQLite)
  ~/.felix/memory/entries/*.md     per-id markdown memory entries
  ~/.felix/sessions/<agent>/       append-only session JSONL files
  ~/.felix/skills/*.md             user skills
  ~/.felix/calibrators/            per-session token-estimate calibration
  ~/.felix/mcp-tokens/             MCP OAuth refresh tokens

For questions about "where is X" inside the felix data dir, ANSWER FROM
THE LAYOUT ABOVE rather than calling bash to search the filesystem.

Bash guardrails: the bash tool has a default 120-second timeout. If a
command might take longer than a few seconds, pass an explicit shorter
timeout argument AND scope your search.
  - Never run "find /" or "find ~" with no scope — pick a specific dir.
  - On macOS, prefer "mdfind -name X" for filename lookups; it uses the
    Spotlight index and returns in milliseconds.
  - For text search, prefer "rg" (ripgrep) over "grep -r" with no scope.
  - When you DO need a slow command, pass timeout=30 (or less) so a
    miss fails fast instead of blocking the conversation.`
}
