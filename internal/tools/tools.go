// Package tools wraps github.com/sausheong/harness/tool (interfaces) and
// the per-tool subpackages in github.com/sausheong/harness/tools (file,
// bash, web, browser, todo). Felix consumers continue to import
// "internal/tools" for Registry, Tool, Executor, ExecPolicy, the
// concrete *Tool types, and the RegisterCoreTools / RegisterCron
// helpers.
//
// Felix-specific tools (Telegram outbound via SendMessageTool) live in
// this package directly — they are not part of the harness framework.
package tools

import (
	harnesstool "github.com/sausheong/harness/tool"
	harnessmem "github.com/sausheong/harness/tool/memory"
	bashtool "github.com/sausheong/harness/tools/bash"
	browsertool "github.com/sausheong/harness/tools/browser"
	filetool "github.com/sausheong/harness/tools/file"
	todotool "github.com/sausheong/harness/tools/todo"
	webtool "github.com/sausheong/harness/tools/web"
)

// --- Core interface + helper aliases ---

type (
	Tool              = harnesstool.Tool
	ToolResult        = harnesstool.ToolResult
	Executor          = harnesstool.Executor
	Registry          = harnesstool.Registry
	PermissionChecker = harnesstool.PermissionChecker
	Policy            = harnesstool.Policy
	Decision          = harnesstool.Decision

	SubagentFactory = harnesstool.SubagentFactory
	SubagentRunner  = harnesstool.SubagentRunner
	AgentEventLike  = harnesstool.AgentEventLike

	JobScheduler = harnesstool.JobScheduler
	JobInfo      = harnesstool.JobInfo

	LoadSkillTool  = harnesstool.LoadSkillTool
	LoadMemoryTool = harnesstool.LoadMemoryTool
	TaskTool       = harnesstool.TaskTool
	CronTool       = harnesstool.CronTool
)

// Decision behaviour constants.
const (
	DecisionAllow = harnesstool.DecisionAllow
	DecisionDeny  = harnesstool.DecisionDeny
)

var (
	NewRegistry      = harnesstool.NewRegistry
	NewStaticChecker = harnesstool.NewStaticChecker
	NewTaskTool      = harnesstool.NewTaskTool
)

// --- Concrete tool aliases ---

type (
	ReadFileTool     = filetool.ReadFileTool
	WriteFileTool    = filetool.WriteFileTool
	EditFileTool     = filetool.EditFileTool
	BashTool         = bashtool.BashTool
	ExecPolicy       = bashtool.ExecPolicy
	WebFetchTool     = webtool.WebFetchTool
	WebSearchTool    = webtool.WebSearchTool
	WebSearchBackend = webtool.WebSearchBackend
	WebSearchConfig  = webtool.WebSearchConfig
	BrowserTool      = browsertool.BrowserTool
	TodoWriteTool    = todotool.TodoWriteTool
)

var (
	NewWebSearchBackend = webtool.NewWebSearchBackend
	ShutdownBrowsers    = browsertool.ShutdownBrowsers
)

// --- Felix-side wiring helpers (kept from pre-extraction) ---

// RegisterCoreTools registers the standard set of in-process tools.
// execPolicy is optional; pass nil for unrestricted bash execution.
// The web_search tool is registered with the default backend; call
// SetBackend on the returned tool (or use RegisterCoreToolsWithSearch)
// to swap in a configured backend.
func RegisterCoreTools(reg *Registry, workDir string, execPolicy *ExecPolicy) {
	reg.Register(&filetool.ReadFileTool{WorkDir: workDir})
	reg.Register(&filetool.WriteFileTool{WorkDir: workDir})
	reg.Register(&filetool.EditFileTool{WorkDir: workDir})
	reg.Register(&bashtool.BashTool{WorkDir: workDir, ExecPolicy: execPolicy})
	reg.Register(&webtool.WebFetchTool{})
	reg.Register(&webtool.WebSearchTool{})
	reg.Register(browsertool.NewBrowserTool())
	reg.Register(&todotool.TodoWriteTool{WorkDir: workDir})
}

// RegisterCoreToolsWithSearch is a variant of RegisterCoreTools that
// pre-configures the web_search tool with a specific backend so the
// configured backend (Brave, Tavily, SearXNG) takes effect on the first
// agent turn rather than only after a hot-reload.
func RegisterCoreToolsWithSearch(reg *Registry, workDir string, execPolicy *ExecPolicy, search WebSearchBackend) {
	reg.Register(&filetool.ReadFileTool{WorkDir: workDir})
	reg.Register(&filetool.WriteFileTool{WorkDir: workDir})
	reg.Register(&filetool.EditFileTool{WorkDir: workDir})
	reg.Register(&bashtool.BashTool{WorkDir: workDir, ExecPolicy: execPolicy})
	reg.Register(&webtool.WebFetchTool{})
	reg.Register(&webtool.WebSearchTool{Backend: search})
	reg.Register(browsertool.NewBrowserTool())
	reg.Register(&todotool.TodoWriteTool{WorkDir: workDir})
}

// RegisterCron registers the cron tool with the given scheduler. agentID
// is baked into the tool so jobs the agent schedules later run as the
// same agent that scheduled them.
func RegisterCron(reg *Registry, agentID string, scheduler JobScheduler) {
	reg.Register(&harnesstool.CronTool{AgentID: agentID, Scheduler: scheduler})
}

// RegisterMemoryTool registers the harness "memory" tool backed by the
// given store. Pass an adapter from internal/memory.NewHarnessAdapter
// to wire it onto Felix's existing markdown memory. Skip the call when
// memory is disabled — the read-only "load_memory" tool still works
// regardless.
func RegisterMemoryTool(reg *Registry, store harnessmem.MemoryStore) {
	reg.Register(&harnessmem.MemoryTool{Store: store})
}
