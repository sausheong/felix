// Package startup provides shared gateway startup logic used by both the
// CLI (cmd/felix) and the menu bar app (cmd/felix-app).
package startup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/felix/internal/agent"
	"github.com/sausheong/felix/internal/compaction"
	"github.com/sausheong/felix/internal/config"
	cortexadapter "github.com/sausheong/felix/internal/cortex"
	"github.com/sausheong/felix/internal/cron"
	"github.com/sausheong/felix/internal/gateway"
	"github.com/sausheong/felix/internal/gateway/runs"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/local"
	"github.com/sausheong/felix/internal/mcp"
	"github.com/sausheong/felix/internal/memory"
	otelpkg "github.com/sausheong/felix/internal/otel"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/skill"
	"github.com/sausheong/felix/internal/tokens"
	"github.com/sausheong/felix/internal/tools"
	"golang.org/x/sync/errgroup"
)

// Result holds the running gateway components.
type Result struct {
	Server  *gateway.Server
	Config  *config.Config
	Cleanup func() // call to gracefully shut down everything
}

// cortexAutoAddToolNames are the cortex tool names auto-added to every
// agent's allowlist when cortex is enabled. Shared by the startup and
// hot-reload paths so they can't drift.
var cortexAutoAddToolNames = []string{"recall", "remember", "find_entities", "get_relationships"}

// applyAutoAddedAllowlists augments every agent's Tools.Allow with the tool
// names added at runtime: MCP-discovered tools, the task tool, and (when
// cortex is enabled) the cortex tools. This MUST run before
// BuildPermissionChecker on BOTH the startup path and the hot-reload path —
// otherwise a curated Tools.Allow silently loses these grants after the
// first config edit (finding R2).
func applyAutoAddedAllowlists(cfg *config.Config, mcpNames []string) {
	cfg.ApplyMCPToolNamesToAllowlists(mcpNames)
	cfg.ApplyTaskToolToAllowlists()
	if cfg.Cortex.Enabled {
		cfg.ApplyCortexToolNamesToAllowlists(cortexAutoAddToolNames)
	}
}

// ResolveProviderOpts builds ProviderOptions for a given provider name
// from the config file only.
func ResolveProviderOpts(name string, cfg *config.Config) llm.ProviderOptions {
	pcfg := cfg.GetProvider(name)
	return llm.ProviderOptions{
		APIKey:  pcfg.APIKey,
		BaseURL: pcfg.BaseURL,
		Kind:    pcfg.Kind,
	}
}

// providerHolder is an atomically-swappable provider map shared by the cron
// agent factory and the subagent factory, so a hot reload that rebuilds
// providers is visible to them at once (rotated/revoked keys take effect
// everywhere, not just on the WebSocket path). Finding R5.
type providerHolder struct {
	p atomic.Pointer[map[string]llm.LLMProvider]
}

func newProviderHolder(m map[string]llm.LLMProvider) *providerHolder {
	h := &providerHolder{}
	h.p.Store(&m)
	return h
}

func (h *providerHolder) get(name string) (llm.LLMProvider, bool) {
	m := *h.p.Load()
	p, ok := m[name]
	return p, ok
}

func (h *providerHolder) store(m map[string]llm.LLMProvider) { h.p.Store(&m) }

// InitProviders creates LLM providers from config.
func InitProviders(cfg *config.Config) map[string]llm.LLMProvider {
	providers := make(map[string]llm.LLMProvider)

	needed := make(map[string]bool)
	for _, a := range cfg.Agents.List {
		provName, _ := llm.ParseProviderModel(a.Model)
		if provName != "" {
			needed[provName] = true
		}
		for _, fb := range a.Fallbacks {
			provName, _ = llm.ParseProviderModel(fb)
			if provName != "" {
				needed[provName] = true
			}
		}
	}

	for name := range needed {
		opts := ResolveProviderOpts(name, cfg)

		// "local" is a no-key provider routed at the bundled Ollama supervisor;
		// don't gate it on APIKey like the cloud providers.
		if opts.APIKey == "" && name != "local" {
			slog.Warn("no API key for provider, skipping", "provider", name)
			continue
		}

		if opts.BaseURL != "" {
			slog.Info("using custom base URL for provider", "provider", name, "base_url", opts.BaseURL)
		}

		p, err := llm.NewProvider(name, opts)
		if err != nil {
			slog.Error("failed to create provider", "provider", name, "error", err)
			continue
		}
		providers[name] = p
	}

	return providers
}

// CronSchedulerAdapter adapts cron.Scheduler to the tools.JobScheduler interface.
//
// JobsFile (optional) enables on-disk persistence of dynamically-added jobs.
// Without it, tool-added jobs only live for the current process — a gateway
// restart wipes them. With it, every mutation is flushed to disk and the file
// is replayed at startup via Restore().
//
// AgentFactory receives both the agentID the job will run as and the job's
// name. When AddJob is called with an empty agentID (legacy persisted jobs
// or UI calls that omit the field), the adapter substitutes DefaultAgentID
// before invoking AgentFactory so existing wiring behaves identically.
type CronSchedulerAdapter struct {
	Scheduler      *cron.Scheduler
	Ctx            context.Context
	AgentFactory   func(agentID, name string) func(context.Context, string) (string, error)
	OutputFn       cron.OutputFunc
	JobsFile       string
	DefaultAgentID string // used when AddJob is called with an empty agentID
}

// persistedJob is the on-disk shape. We don't persist AgentFn/OutputFn — they
// are reconstructed from AgentFactory/OutputFn at Restore time.
type persistedJob struct {
	Name     string `json:"name"`
	AgentID  string `json:"agentId,omitempty"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
	Paused   bool   `json:"paused"`
}

func (a *CronSchedulerAdapter) addJobInternal(agentID, name, schedule, prompt string, persist bool) error {
	if agentID == "" {
		agentID = a.DefaultAgentID
	}
	var agentFn func(context.Context, string) (string, error)
	if a.AgentFactory != nil {
		agentFn = a.AgentFactory(agentID, name)
	} else {
		agentFn = func(ctx context.Context, p string) (string, error) {
			slog.Info("dynamic cron job executed (no agent)", "name", name)
			return "OK", nil
		}
	}

	err := a.Scheduler.Add(cron.Job{
		Name:     name,
		AgentID:  agentID,
		Schedule: schedule,
		Prompt:   prompt,
		Source:   "dynamic",
		AgentFn:  agentFn,
		OutputFn: a.OutputFn,
	})
	if err != nil {
		return err
	}
	a.Scheduler.Start(a.Ctx)
	if persist {
		a.persist()
	}
	return nil
}

func (a *CronSchedulerAdapter) AddJob(agentID, name, schedule, prompt string) error {
	return a.addJobInternal(agentID, name, schedule, prompt, true)
}

func (a *CronSchedulerAdapter) RemoveJob(name string) error {
	if err := a.Scheduler.Remove(name); err != nil {
		return err
	}
	a.persist()
	return nil
}

func (a *CronSchedulerAdapter) ListJobs() []tools.JobInfo {
	jobs := a.Scheduler.Jobs()
	infos := make([]tools.JobInfo, len(jobs))
	for i, j := range jobs {
		infos[i] = tools.JobInfo{
			Name:     j.Name,
			AgentID:  j.AgentID,
			Schedule: j.Schedule,
			Prompt:   j.Prompt,
			Paused:   j.Paused,
		}
	}
	return infos
}

func (a *CronSchedulerAdapter) PauseJob(name string) error {
	if err := a.Scheduler.Pause(name); err != nil {
		return err
	}
	a.persist()
	return nil
}

func (a *CronSchedulerAdapter) ResumeJob(name string) error {
	if err := a.Scheduler.Resume(name); err != nil {
		return err
	}
	a.persist()
	return nil
}

func (a *CronSchedulerAdapter) UpdateJobSchedule(name, schedule string) error {
	if err := a.Scheduler.UpdateSchedule(name, schedule); err != nil {
		return err
	}
	a.persist()
	return nil
}

// persist writes the current scheduler state to JobsFile via a write-rename
// dance so a crash mid-write can't corrupt the file. No-op when JobsFile is "".
func (a *CronSchedulerAdapter) persist() {
	if a.JobsFile == "" {
		return
	}
	jobs := a.Scheduler.Jobs()
	out := make([]persistedJob, 0, len(jobs))
	for _, j := range jobs {
		if j.Source == "static" {
			continue // static jobs live in felix.json5; don't persist them
		}
		out = append(out, persistedJob{
			Name:     j.Name,
			AgentID:  j.AgentID,
			Schedule: j.Schedule,
			Prompt:   j.Prompt,
			Paused:   j.Paused,
		})
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		slog.Warn("cron persist marshal failed", "error", err)
		return
	}
	tmp := a.JobsFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		slog.Warn("cron persist write failed", "path", tmp, "error", err)
		return
	}
	if err := os.Rename(tmp, a.JobsFile); err != nil {
		slog.Warn("cron persist rename failed", "path", a.JobsFile, "error", err)
	}
}

// Restore reloads jobs from JobsFile and re-adds them via the adapter so each
// gets a fresh AgentFn from AgentFactory. Missing file is not an error.
// Schedule-parse errors on a single entry are logged and skipped, not fatal.
func (a *CronSchedulerAdapter) Restore() error {
	if a.JobsFile == "" {
		return nil
	}
	data, err := os.ReadFile(a.JobsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var stored []persistedJob
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("parse %s: %w", a.JobsFile, err)
	}
	for _, p := range stored {
		// Skip persistence on each individual restore so we don't rewrite the
		// file N times during startup; we'll flush once at the end. Old
		// persisted jobs (pre-AgentID) have empty p.AgentID; addJobInternal
		// substitutes DefaultAgentID for them so behavior is unchanged.
		if err := a.addJobInternal(p.AgentID, p.Name, p.Schedule, p.Prompt, false); err != nil {
			slog.Warn("cron restore: skipped invalid job", "name", p.Name, "error", err)
			continue
		}
		if p.Paused {
			if err := a.Scheduler.Pause(p.Name); err != nil {
				slog.Warn("cron restore: failed to pause", "name", p.Name, "error", err)
			}
		}
	}
	if len(stored) > 0 {
		slog.Info("cron jobs restored", "count", len(stored), "path", a.JobsFile)
		// Flush once now that all restored jobs are in. This rewrites the
		// file canonically with only dynamic jobs (Source-filtered), dropping
		// any static jobs an older build may have persisted into it.
		a.persist()
	}
	return nil
}

// Options configures gateway startup behavior.
type Options struct {
	// LogWriter is where the underlying slog TextHandler writes. Defaults to
	// os.Stderr when nil. Set this to a file (e.g. ~/.felix/felix-app.log) so
	// macOS .app bundles and Windows GUI apps — which have no console — get
	// persistent diagnostic logs.
	LogWriter io.Writer
}

// StartGateway starts the full gateway server and returns the result.
// The caller is responsible for calling Result.Cleanup() on shutdown and
// starting the HTTP server via Result.Server.Start() in a goroutine.
func StartGateway(configPath, version string, opts ...Options) (*Result, error) {
	var opt Options
	if len(opts) > 0 {
		opt = opts[0]
	}
	logWriter := opt.LogWriter
	if logWriter == nil {
		logWriter = os.Stderr
	}
	// Install a tee log handler that captures entries for the /logs page
	// while forwarding to logWriter. We create a fresh TextHandler rather
	// than using slog.Default().Handler() because in Go 1.22+ the default
	// handler routes through log.Logger which routes back through
	// slog.Default(), creating a deadlock when LogBuffer replaces it.
	//
	// Initial install uses TextHandler only; if the loaded config enables
	// OTel logs, we re-install below with a fanout that also forwards to
	// the OTel slog bridge. The brief window of "TextHandler only" is the
	// price of loading config AFTER the LogBuffer install (which is
	// itself necessary because config.Load emits warnings we want to
	// capture).
	logBuf := gateway.NewLogBuffer(2000, slog.NewTextHandler(logWriter, nil))
	slog.SetDefault(slog.New(logBuf))

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	// OTel: build the SDK pipelines (traces / metrics / logs) before
	// anything else that might emit telemetry, so init-time errors land
	// in the user's collector. Disabled() when the user hasn't opted in.
	otelProv, err := otelpkg.Setup(context.Background(), otelpkg.Config{
		Enabled:     cfg.OTel.Enabled,
		Endpoint:    cfg.OTel.Endpoint,
		ServiceName: cfg.OTel.ServiceName,
		Version:     version,
		SampleRatio: cfg.OTel.SampleRatio,
		Headers:     cfg.OTel.Headers,
		Traces:      cfg.OTel.Signals.Traces,
		Metrics:     cfg.OTel.Signals.Metrics,
		Logs:        cfg.OTel.Signals.Logs,
	})
	if err != nil {
		// Setup never fails for the disabled path; here a non-nil err
		// means a configured-but-broken endpoint. Log and continue with
		// the no-op provider so Felix still serves chat.
		slog.Warn("otel: setup failed; running without OTel", "error", err)
		otelProv = otelpkg.Disabled()
	}
	if otelProv.LoggerHandler != nil {
		// Re-install slog.Default with the fanout so subsequent log
		// records reach both stderr/log-file AND the OTLP/logs collector.
		// The LogBuffer keeps capturing for the local /logs view.
		fan := newFanoutHandler(slog.NewTextHandler(logWriter, nil), otelProv.LoggerHandler)
		logBuf = gateway.NewLogBuffer(2000, fan)
		slog.SetDefault(slog.New(logBuf))
	}
	// Wire the agent package's tracer once. Safe to call with nil — the
	// trace.go code nil-checks before creating spans.
	agent.SetOTelTracer(otelProv.Tracer("felix.agent"))

	// Bundled-Ollama supervisor — start before InitProviders so the local
	// provider's BaseURL reflects the bound port.
	var localSup *local.Supervisor
	var bootstrapTracker *local.Tracker
	if cfg.Local.Enabled {
		exeDir, _ := os.Executable()
		exeDir = filepath.Dir(exeDir)
		bin, derr := local.Discover(exeDir)
		switch {
		case derr != nil:
			slog.Warn("bundled ollama not found; local provider disabled", "error", derr)
		default:
			modelsDir := cfg.Local.ModelsDir
			if modelsDir == "" {
				modelsDir = local.DefaultModelsDir()
			}
			_ = os.MkdirAll(modelsDir, 0o755)
			localSup = local.New(local.Options{
				BinPath:   bin,
				ModelsDir: modelsDir,
				KeepAlive: cfg.Local.KeepAlive,
				PIDFile:   filepath.Join(config.DefaultDataDir(), "ollama.pid"),
			})
			if err := localSup.Spawn(); err != nil {
				slog.Warn("failed to spawn bundled ollama; local provider disabled", "error", err)
				localSup = nil
			} else {
				if ierr := local.InjectLocalProvider(configPath, localSup.BoundPort()); ierr != nil {
					slog.Warn("failed to inject local provider config", "error", ierr)
				}
				// Re-load so InitProviders sees the local block.
				if reloaded, rerr := config.Load(configPath); rerr == nil {
					cfg.UpdateFrom(reloaded)
				}
				// Create the bootstrap tracker synchronously so the Settings
				// handler (wired below) sees a non-nil tracker regardless of
				// readiness timing. The actual model pulls/warmups are kicked
				// off from the background goroutine once ollama is ready.
				if cfg.Local.Enabled {
					if pcfg := cfg.GetProvider("local"); pcfg.BaseURL != "" {
						bootstrapTracker = local.NewTracker()
					}
				}
				// Defer readiness wait + first-run model pulls/warmups to a
				// background goroutine so they don't block the HTTP server.
				// The port is already bound after Spawn, so InitProviders and
				// the rest of wiring proceed against a valid local provider.
				sup := localSup
				tracker := bootstrapTracker
				// Snapshot cfg fields the goroutine reads into locals before
				// launching it. config.Config.UpdateFrom rewrites these same
				// fields under cfg.mu.Lock() on hot reload, so reading them
				// directly inside the goroutine would race. cfg.GetProvider is
				// mutex-guarded and stays as a live call below.
				localEnabled := cfg.Local.Enabled
				defaultModel := ""
				if len(cfg.Agents.List) > 0 {
					defaultModel = cfg.Agents.List[0].Model
				}
				memEnabled := cfg.Memory.Enabled
				embedModel := cfg.Memory.EmbeddingModel
				go func() {
					rctx, rcancel := context.WithTimeout(context.Background(), 70*time.Second)
					defer rcancel()
					if err := sup.WaitReady(rctx); err != nil {
						slog.Warn("bundled ollama did not become ready", "error", err)
						return
					}
					// First-run background pull of default local models (gemma4 + nomic-embed).
					if localEnabled {
						if pcfg := cfg.GetProvider("local"); pcfg.BaseURL != "" {
							ollamaURL := strings.TrimSuffix(pcfg.BaseURL, "/v1")
							puller := local.NewInstaller(ollamaURL)
							var onEvent func(local.BootstrapEvent)
							if tracker != nil {
								onEvent = tracker.OnEvent
							}
							local.EnsureFirstRunModels(context.Background(), config.DefaultDataDir(), puller, onEvent)

							// Pre-warm the default agent's local model so the first chat
							// turn doesn't pay the ~10s cold-load latency. Runs in the
							// background and silently logs failure (e.g. model still pulling).
							if defaultModel != "" {
								go func() {
									// Wait briefly so EnsureFirstRunModels can start; if the
									// model isn't on disk yet, /api/generate will fail and we
									// just log+move on.
									time.Sleep(2 * time.Second)
									warmCtx, warmCancel := context.WithTimeout(context.Background(), 60*time.Second)
									defer warmCancel()
									if err := local.WarmModel(warmCtx, ollamaURL, defaultModel); err != nil {
										slog.Debug("ollama warmup deferred", "model", defaultModel, "error", err)
									}
								}()
							}
							// Pre-warm the embedder model too. Cortex recall and
							// memory both hit it on the user's first chat turn —
							// without warmup that turn pays a ~5–10s cold-load on
							// top of the chat-model warmup. Runs concurrently with
							// the chat-model warmup since they're different models
							// and Ollama can hold two resident if RAM allows.
							if memEnabled && embedModel != "" {
								go func() {
									time.Sleep(2 * time.Second)
									warmCtx, warmCancel := context.WithTimeout(context.Background(), 60*time.Second)
									defer warmCancel()
									if err := local.WarmEmbedder(warmCtx, ollamaURL, embedModel); err != nil {
										slog.Debug("ollama embedder warmup deferred", "model", embedModel, "error", err)
									}
								}()
							}
						}
					}
				}()
			}
		}
	}

	// Ensure data directories exist
	dataDir := config.DefaultDataDir()
	os.MkdirAll(filepath.Join(dataDir, "sessions"), 0o755)
	os.MkdirAll(filepath.Join(dataDir, "memory"), 0o755)
	skillsDir := filepath.Join(dataDir, "skills")
	os.MkdirAll(skillsDir, 0o755)
	if _, err := skill.SeedBundledSkills(skillsDir); err != nil {
		slog.Warn("failed to seed bundled skills", "error", err)
	}

	// Init components
	providers := InitProviders(cfg)
	providerHldr := newProviderHolder(providers)
	sessionsDir := filepath.Join(dataDir, "sessions")
	sessionStore := session.NewStore(sessionsDir)

	// Reap orphan spill directories from previous crashed runs / deleted
	// sessions. Best-effort: errors are logged but do not block startup.
	// Runs once per agent workspace; cheap unless a workspace has accumulated
	// hundreds of orphans.
	for _, a := range cfg.Agents.List {
		ws := a.Workspace
		agentID := a.ID
		if _, err := agent.CleanupOrphanedSpills(ws, func() (map[string]bool, error) {
			infos, lerr := sessionStore.List(agentID)
			if lerr != nil {
				return nil, lerr
			}
			out := make(map[string]bool, len(infos))
			for _, s := range infos {
				out[s.Key] = true
			}
			return out, nil
		}); err != nil {
			slog.Warn("orphan spill cleanup failed", "agent", agentID, "workspace", ws, "error", err)
		}
	}

	toolReg := tools.NewRegistry()
	execPolicy := &tools.ExecPolicy{
		Level:     cfg.Security.ExecApprovals.Level,
		Allowlist: cfg.Security.ExecApprovals.Allowlist,
	}
	// Resolve the configured web-search backend so first-call performance
	// uses the user's chosen backend instead of falling back to DDG until
	// hot-reload swaps the registry.
	searchBackend := tools.NewWebSearchBackend(tools.WebSearchConfig{
		Backend: cfg.WebSearch.Backend,
		APIKey:  cfg.WebSearch.APIKey,
		BaseURL: cfg.WebSearch.BaseURL,
	})
	tools.RegisterCoreToolsWithSearch(toolReg, "", execPolicy, searchBackend)
	// send_message is registered unconditionally so it shows in the Settings →
	// Agents tool picker even before any messaging channel is configured. The
	// closure reads from cfg on every call, so config edits in Settings take
	// effect on the next invocation without a process restart.
	sendMsgConfigFn := func() tools.SendMessageRegistration {
		return tools.SendMessageRegistration{
			TelegramEnabled:       cfg.Telegram.Enabled,
			TelegramBotToken:      cfg.Telegram.BotToken,
			TelegramDefaultChatID: cfg.Telegram.DefaultChatID,
		}
	}
	tools.RegisterSendMessage(toolReg, sendMsgConfigFn)

	// Initialize MCP manager once and register tools into the main registry.
	mcpServerCfgs, err := cfg.ResolveMCPServers()
	if err != nil {
		return nil, fmt.Errorf("resolve mcp_servers: %w", err)
	}
	// MCP connect can block up to 30s on remote handshakes. Run it
	// concurrently with the skill/memory/cortex-tool-def init below, which do
	// not touch the MCP manager or register MCP tools. Join (eg.Wait) before
	// mcp.RegisterTools so the critical invariant holds:
	//   RegisterTools -> applyAutoAddedAllowlists -> BuildPermissionChecker -> server.
	// mcpMgr is written only in this goroutine and read after eg.Wait()
	// returns, so the happens-before edge from Wait makes the read race-free.
	var mcpMgr *mcp.Manager
	var eg errgroup.Group
	eg.Go(func() error {
		mctx, mcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer mcancel()
		m, mErr := mcp.NewManager(mctx, mcpServerCfgs)
		if mErr != nil {
			return fmt.Errorf("init mcp manager: %w", mErr)
		}
		mcpMgr = m
		return nil
	})

	// --- concurrent window: init that does NOT touch mcpMgr / MCP tools ---

	// Init skill loader
	skillLoader := skill.NewLoader()
	skillDirs := []string{filepath.Join(dataDir, "skills")}
	for _, a := range cfg.Agents.List {
		skillDirs = append(skillDirs, filepath.Join(a.Workspace, "skills"))
	}
	if err := skillLoader.LoadFrom(skillDirs...); err != nil {
		slog.Warn("failed to load skills", "error", err)
	}
	skillHandlers := gateway.NewSkillHandlers(skillLoader, filepath.Join(dataDir, "skills"), skillDirs)

	// Init memory manager
	var memMgr *memory.Manager
	if cfg.Memory.Enabled {
		memMgr = memory.NewManager(filepath.Join(dataDir, "memory"))
		if cfg.Memory.EmbeddingProvider != "" {
			pcfg := cfg.GetProvider(cfg.Memory.EmbeddingProvider)
			embedder := memory.NewOpenAIEmbedder(pcfg.APIKey, pcfg.BaseURL, cfg.Memory.EmbeddingModel)
			memory.AttachWithProbe(memMgr, embedder, cfg.Memory.EmbeddingModel)
		}
		if err := memMgr.Load(); err != nil {
			slog.Warn("failed to load memory", "error", err)
		}
		// Register the harness "memory" write tool backed by Felix's
		// existing markdown store. The pre-existing read-only
		// load_memory tool stays registered too — agents see both.
		tools.RegisterMemoryTool(toolReg, memory.NewHarnessAdapter(memMgr))
	}

	// Register cortex tool DEFINITIONS on the shared registry (without a
	// real *cortex.Cortex). They expose Name/Description/Parameters so
	// the Settings UI's tool picker lists them and the auto-add to
	// agent allowlists has a matching tool name to reference. Actual
	// per-chat execution always overlays a real cortex via
	// chatexec.ChatToolOverlay's Cortex slice, which intercepts these
	// names before the shared registry sees them.
	if cfg.Cortex.Enabled {
		tools.RegisterCortexTools(toolReg, nil)
	}

	// --- end concurrent window: join MCP before registering its tools ---
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	mcpNames, err := mcp.RegisterTools(toolReg, mcpMgr, cfg.IsServerParallelSafe)
	if err != nil {
		mcpMgr.Close()
		return nil, fmt.Errorf("register mcp tools: %w", err)
	}
	applyAutoAddedAllowlists(cfg, mcpNames)

	// Build a single PermissionChecker covering every agent in cfg. Same
	// checker, different agent IDs per Runtime — StaticChecker keys on
	// AgentID. An agent absent from the map is treated as allow-all, matching
	// today's behavior when no policy is configured.
	permission := cfg.BuildPermissionChecker()

	// Init Cortex knowledge graph as a per-agent factory. Each chatting agent
	// gets its own *cortex.Cortex (cached) wired with the same provider/model
	// it uses for chat, so cortex's LLM extraction stays consistent with the
	// conversation. All clients share the same SQLite DB.
	var cxProvider *cortexadapter.Provider
	if cfg.Cortex.Enabled {
		cxProvider = cortexadapter.NewProvider(cfg.Cortex, cfg.Memory, cfg.GetProvider)
		// Pre-warm the default agent's cortex so the first chat doesn't pay
		// a ~4–11s cold-search penalty. Other agents warm lazily on first use.
		if len(cfg.Agents.List) > 0 {
			defaultAgentModel := cfg.Agents.List[0].Model
			go func() {
				cx, err := cxProvider.For(defaultAgentModel)
				if err != nil {
					slog.Warn("failed to init cortex", "error", err)
					return
				}
				warmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				start := time.Now()
				if _, err := cx.Recall(warmCtx, "hello", cortex.WithLimit(1)); err != nil {
					slog.Debug("cortex warmup error", "error", err)
				} else {
					slog.Info("cortex warmed", "dur_ms", time.Since(start).Milliseconds())
				}
			}()
		}
	}

	// Pre-warm bash subprocess so the first tool.exec doesn't pay the macOS
	// Gatekeeper / TCC initialization cost (~2s on a fresh install).
	go func() {
		start := time.Now()
		c := exec.Command("bash", "-c", ":")
		if err := c.Run(); err == nil {
			slog.Debug("bash warmed", "dur_ms", time.Since(start).Milliseconds())
		}
	}()

	// resolveCortex returns the *cortex.Cortex for a given chat-agent model
	// (or nil if cortex is disabled / build fails). Used by the cron and
	// cron-tool agent factories so each agent's cortex extractor matches
	// its chat model.
	resolveCortex := func(agentModel string) *cortex.Cortex {
		if cxProvider == nil {
			return nil
		}
		cx, err := cxProvider.For(agentModel)
		if err != nil {
			slog.Warn("cortex resolve failed", "model", agentModel, "error", err)
			return nil
		}
		return cx
	}

	// Per-(agentID, sessionKey) calibrator persistence. Constructed before
	// the WebSocket handler so it can be injected immediately and used by
	// session-clear cleanup in handleSessionClear.
	calibratorStore := tokens.NewCalibratorStore(filepath.Join(dataDir, "calibrators"))

	// serverCtx is the process-wide ctx whose cancellation triggers
	// shutdown of every long-lived subsystem rooted in StartGateway.
	// cleanup() cancels it BEFORE srv.Shutdown so in-flight chat runs
	// observe cancellation rather than draining their full LLM stream.
	serverCtx, serverCancel := context.WithCancel(context.Background())

	// Init WebSocket handler
	wsHandler := gateway.NewWebSocketHandler(providers, toolReg, sessionStore, cfg, sessionsDir)
	wsHandler.SetSkills(skillLoader)
	wsHandler.SetMemory(memMgr)
	wsHandler.SetCortexProvider(cxProvider)
	wsHandler.SetPermission(permission)
	wsHandler.SetCalibratorStore(calibratorStore)
	wsHandler.SetServerCtx(serverCtx)

	// Build runs registry rooted at the sessions directory and recover any
	// runs that were left in 'running' state by a previous process. Recovery
	// writes synthetic terminal events for orphaned rows so replay/subscribe
	// from the next process sees clean terminal states. Missing dir → no-op.
	runsReg := runs.NewRegistry(sessionsDir)
	if n, err := runs.RecoverInterruptedRuns(sessionsDir); err != nil {
		slog.Warn("runs recovery failed", "error", err)
	} else if n > 0 {
		slog.Info("runs recovery complete", "recovered", n)
	}
	wsHandler.SetRunsRegistry(runsReg)
	// Background runs (cron, etc.) have no wsSubscriber of their own.
	// Without this hook, events stream to disk only and open WS conns
	// viewing the affected session never see them. BroadcastNewRun filters
	// by activeSessionKeys so only conns watching the scope get notified.
	runsReg.OnNewRun = wsHandler.BroadcastNewRun

	// Config hot-reload — rebuild LLM provider clients from the new config
	// and push them into the WebSocket handler. Without the provider rebuild,
	// edits to API keys / base URLs in the Settings UI would silently no-op
	// until the process is restarted (the cached clients still hold the stale
	// credentials they were instantiated with at startup).
	var configWatcher *config.Watcher
	if cfg.Path() != "" {
		watcher, err := config.NewWatcher(cfg.Path(), func(newCfg *config.Config) {
			cfg.UpdateFrom(newCfg)
			newProviders := InitProviders(newCfg)
			providerHldr.store(newProviders)
			// Rebuild the permission checker from the new config and re-inject.
			// Without this, edits to agent Tools.Allow/Deny in felix.json5
			// silently no-op the dispatch-time gate until restart.
			// Re-apply runtime auto-added grants first so curated Tools.Allow
			// lists don't lose their MCP/task/cortex tools after an edit (R2).
			applyAutoAddedAllowlists(newCfg, mcpNames)
			wsHandler.SetPermission(newCfg.BuildPermissionChecker())
			// Invalidate the cached invariant prompt inputs (config summary,
			// memory-files block) so the next agent run recomputes them from
			// the reloaded config (P4).
			agent.BumpConfigGeneration()
			wsHandler.UpdateConfig(newCfg)
			wsHandler.UpdateProviders(newProviders)
			// Swap the web_search backend on the live shared registry so
			// settings-page edits to web_search.backend / api_key take
			// effect on the next chat turn without a restart.
			if t, ok := toolReg.Get("web_search"); ok {
				if ws, ok := t.(*tools.WebSearchTool); ok {
					ws.SetBackend(tools.NewWebSearchBackend(tools.WebSearchConfig{
						Backend: newCfg.WebSearch.Backend,
						APIKey:  newCfg.WebSearch.APIKey,
						BaseURL: newCfg.WebSearch.BaseURL,
					}))
				}
			}
			slog.Info("config hot-reloaded", "providers", len(newProviders))
		})
		if err == nil {
			watcher.Start()
			configWatcher = watcher
		} else {
			slog.Warn("config watcher not started", "error", err)
		}
	}

	ctx := context.Background()

	// Build the compaction Manager once and share across cron and the
	// cron-tool agent factory below. The Manager's per-session mutex map
	// only serializes correctly when the same instance is reused.
	startupCompactionMgr := compaction.BuildManager(cfg)

	// Shared Runtime dependencies for every Runtime built in this process —
	// cron-static, cron-adapter, and the subagent factory. Per-call inputs
	// (provider, tools, session, ingest-source) are passed via
	// agent.RuntimeInputs at each call site below.
	runtimeDeps := agent.RuntimeDeps{
		Skills:          skillLoader,
		Memory:          memMgr,
		Permission:      permission,
		CortexFn:        resolveCortex,
		AgentLoop:       cfg.AgentLoop,
		Config:          cfg,
		CalibratorStore: calibratorStore,
	}

	// runtimeJobScheduler is late-bound below to the CronSchedulerAdapter
	// so cron-launched runs and subagents that themselves register the cron
	// tool route new jobs back through the same adapter (and thus the same
	// persistence file). nil-safe: tools.RegisterCron tolerates a nil
	// scheduler — the CronTool surfaces "not available" at execution time.
	var runtimeJobScheduler tools.JobScheduler

	// Subagent input builder shared by both call sites in this file
	// (cron-static, cron-adapter). Builds a fresh per-subagent tool
	// registry + provider lookup + in-memory session. Subagents do NOT
	// ingest into Cortex (IngestSource="") since they're short-lived and
	// would otherwise flood the graph with tool-use chatter.
	buildSubagentInputs := func(a *config.AgentConfig) (agent.RuntimeInputs, error) {
		pName, _ := llm.ParseProviderModel(a.Model)
		p, ok := providerHldr.get(pName)
		if !ok {
			return agent.RuntimeInputs{}, fmt.Errorf("provider %q not configured for subagent %q", pName, a.ID)
		}
		reg := tools.NewRegistry()
		if strings.TrimSpace(a.Workspace) == "" {
			return agent.RuntimeInputs{}, fmt.Errorf("agent %q has no workspace; refusing to build unclamped file tools", a.ID)
		}
		tools.RegisterCoreToolsWithSearch(reg, a.Workspace, execPolicy, searchBackend)
		if _, err := mcp.RegisterTools(reg, mcpMgr, cfg.IsServerParallelSafe); err != nil {
			slog.Warn("subagent mcp registration failed; continuing", "agent", a.ID, "error", err)
		}
		tools.RegisterSendMessage(reg, sendMsgConfigFn)
		// Subagents share the parent's memory store so a save here is
		// visible to the parent's next load_memory call.
		if memMgr != nil {
			tools.RegisterMemoryTool(reg, memory.NewHarnessAdapter(memMgr))
		}
		// Cortex tools: per-subagent client resolved via resolveCortex.
		// Passes through to a no-op when cortex is disabled (resolveCortex
		// returns nil and RegisterCortexTools tolerates a nil cx).
		if cfg.Cortex.Enabled {
			tools.RegisterCortexTools(reg, resolveCortex(a.Model))
		}
		// Subagents that schedule cron jobs schedule them as themselves.
		// runtimeJobScheduler is set below after the adapter exists; the
		// subagent build is invoked at task-dispatch time, by which point
		// it is non-nil.
		tools.RegisterCron(reg, a.ID, runtimeJobScheduler)
		return agent.RuntimeInputs{
			Provider:     p,
			Tools:        reg,
			Session:      agent.NewSubagentSession(a.ID),
			Compaction:   startupCompactionMgr,
			IngestSource: "",
		}, nil
	}

	// Wire the subagent builder into the websocket handler so chat sessions
	// can dispatch to subagents via the task tool. The same builder closure
	// is reused across all chat connections.
	wsHandler.SetSubagentBuilder(buildSubagentInputs)

	// resolveAgentCfg returns the agent config matching id, falling back to
	// the first agent if id is empty or unknown. Used by the cron adapter so
	// jobs always have *some* agent to run as even if the configured agent
	// has been removed since the job was persisted.
	resolveAgentCfg := func(id string) config.AgentConfig {
		if id != "" {
			for _, a := range cfg.Agents.List {
				if a.ID == id {
					return a
				}
			}
			slog.Warn("cron: agent not found, falling back to default",
				"requested", id, "default", cfg.Agents.List[0].ID)
		}
		return cfg.Agents.List[0]
	}

	// buildCronAgentFn builds the closure cron jobs use to invoke the LLM.
	// Shared by the static (config-defined) cron loop below and the dynamic
	// (tool-scheduled) AgentFactory used by CronSchedulerAdapter.
	buildCronAgentFn := func(agentCfg config.AgentConfig, jobName string) func(context.Context, string) (string, error) {
		return func(ctx context.Context, prompt string) (string, error) {
			pName, _ := llm.ParseProviderModel(agentCfg.Model)
			p, ok := providerHldr.get(pName)
			if !ok {
				return "", fmt.Errorf("provider %q not available for cron job %q on agent %q", pName, jobName, agentCfg.ID)
			}
			sess := session.NewSession(agentCfg.ID, "cron_"+jobName)
			defer startupCompactionMgr.ForgetSession(sess)
			cronToolReg := tools.NewRegistry()
			if strings.TrimSpace(agentCfg.Workspace) == "" {
				return "", fmt.Errorf("agent %q has no workspace; refusing to build unclamped file tools", agentCfg.ID)
			}
			tools.RegisterCoreToolsWithSearch(cronToolReg, agentCfg.Workspace, execPolicy, searchBackend)
			if _, err := mcp.RegisterTools(cronToolReg, mcpMgr, cfg.IsServerParallelSafe); err != nil {
				slog.Warn("mcp: failed to register tools for sub-registry, continuing", "error", err)
			}
			tools.RegisterSendMessage(cronToolReg, sendMsgConfigFn)
			if memMgr != nil {
				tools.RegisterMemoryTool(cronToolReg, memory.NewHarnessAdapter(memMgr))
			}
			// Cortex tools: same pattern as the subagent registry — per-agent
			// cortex client via resolveCortex, no-op when cortex is disabled.
			if cfg.Cortex.Enabled {
				tools.RegisterCortexTools(cronToolReg, resolveCortex(agentCfg.Model))
			}
			// Per-call cron tool with this agent's ID baked in so jobs that
			// schedule MORE jobs from inside the loop run them under the
			// same agent.
			tools.RegisterCron(cronToolReg, agentCfg.ID, runtimeJobScheduler)
			rt, err := agent.BuildRuntimeForAgent(runtimeDeps, agent.RuntimeInputs{
				Provider:     p,
				Tools:        cronToolReg,
				Session:      sess,
				Compaction:   startupCompactionMgr,
				IngestSource: "cron",
			}, &agentCfg)
			if err != nil {
				return "", fmt.Errorf("build runtime for cron job %q on agent %q: %w", jobName, agentCfg.ID, err)
			}
			if eligible := cfg.EligibleSubagents(); len(eligible) > 0 {
				factory := agent.MakeSubagentFactory(cfg, runtimeDeps, buildSubagentInputs, agent.ConfigSummaryFor(cfg), rt)
				cronToolReg.Register(tools.NewTaskTool(factory, rt.Depth, eligible))
			}
			return rt.RunSync(ctx, prompt, nil)
		}
	}

	// Start cron scheduler for agents with cron jobs
	cronScheduler := cron.NewScheduler()
	for _, agentCfg := range cfg.Agents.List {
		for _, cronJob := range agentCfg.Cron {
			providerName, _ := llm.ParseProviderModel(agentCfg.Model)
			if _, ok := providerHldr.get(providerName); !ok {
				continue
			}
			agentCfg := agentCfg
			cronJob := cronJob

			if err := cronScheduler.Add(cron.Job{
				Name:     cronJob.Name,
				AgentID:  agentCfg.ID,
				Schedule: cronJob.Schedule,
				Prompt:   cronJob.Prompt,
				Source:   "static",
				AgentFn:  buildCronAgentFn(agentCfg, cronJob.Name),
			}); err != nil {
				slog.Warn("skip duplicate static cron job", "name", cronJob.Name, "error", err)
			}
		}
	}

	cronAdapter := &CronSchedulerAdapter{
		Scheduler:      cronScheduler,
		Ctx:            ctx,
		JobsFile:       filepath.Join(dataDir, "cron-jobs.json"),
		DefaultAgentID: cfg.Agents.List[0].ID,
		AgentFactory: func(agentID, jobName string) func(context.Context, string) (string, error) {
			return buildCronAgentFn(resolveAgentCfg(agentID), jobName)
		},
	}
	// Late-bind the scheduler reference used by buildCronAgentFn above so
	// jobs scheduled from inside a cron run resolve to the same adapter.
	runtimeJobScheduler = cronAdapter
	wsHandler.SetJobScheduler(cronAdapter)

	// Restore dynamic jobs persisted from a prior run. Static jobs from
	// felix.json5 were already added above; Restore re-adds tool-created jobs
	// that would otherwise be lost on restart.
	if err := cronAdapter.Restore(); err != nil {
		slog.Warn("failed to restore cron jobs from disk", "error", err)
	}

	if len(cronScheduler.Jobs()) > 0 {
		cronScheduler.Start(ctx)
	}

	// Init metrics. When OTel is enabled the meter mirrors every counter
	// to the configured OTLP/metrics endpoint; when disabled the meter
	// is a no-op and only the local /metrics endpoint sees the data.
	metrics := gateway.NewMetricsWithMeter(otelProv.Meter("felix.gateway"))
	wsHandler.SetMetrics(metrics)

	// Start gateway HTTP server
	port := cfg.Gateway.Port
	filesHandlers := gateway.NewFilesHandlers(func() *config.Config { return cfg })
	srv := gateway.NewServer(cfg.Gateway.Host, port, wsHandler, gateway.ServerOptions{
		AuthToken:      cfg.Gateway.Auth.Token,
		MetricsHandler: metrics.Handler(),
		UIHandler:      gateway.NewUIHandler(cfg, version),
		ChatHandler:    gateway.NewChatHandler(port),
		JobsHandler:    gateway.NewJobsHandler(port),
		Settings: gateway.NewSettingsHandlers(cfg, toolReg, settingsBootstrap(bootstrapTracker), func(newCfg *config.Config) {
			wsHandler.UpdateConfig(newCfg)
			slog.Info("config updated via settings page")
		}),
		Skills: skillHandlers,
		Memory: gateway.NewMemoryHandlers(memMgr),
		MCP: gateway.NewMCPHandlers(mcpMgr, func() *config.Config {
			// Closure rather than a snapshot so config hot-reload (which
			// rewrites cfg in place) is observed by the re-auth handler.
			return cfg
		}),
		LogBuffer: logBuf,
		Files:     filesHandlers,
	})

	cleanup := func() {
		// Cancel serverCtx FIRST so in-flight chat runs (chatexec.RunTurn)
		// observe cancellation and unwind. If we didn't do this before
		// srv.Shutdown below, srv.Shutdown would block waiting for WS
		// handlers that are blocked waiting for chatexec which is blocked
		// on the LLM stream which has no other cancel signal.
		serverCancel()
		// Ollama supervisor goes FIRST. Bundled ollama lives in its own
		// process group (Setpgid in supervisor_unix.go) so it does NOT
		// receive a SIGTERM that the menubar app sends to the gateway's
		// process group. Killing it here, before the slower cleanup
		// steps below, guarantees ollama dies even if the gateway is
		// itself SIGKILLed shortly after for taking too long. Up to ~7s
		// worst case (5s SIGTERM grace + 2s SIGKILL fallback inside Stop).
		if localSup != nil {
			_ = localSup.Stop()
		}
		tools.ShutdownBrowsers()
		// Bound MCP close so a slow/unreachable upstream can't hang the
		// whole shutdown chain. 8s budget covers the SDK's 5s
		// stdin-close → SIGTERM grace period for stdio child processes,
		// plus ~3s headroom for the JSON-RPC shutdown handshake.
		mcpDone := make(chan error, 1)
		go func() { mcpDone <- mcpMgr.Close() }()
		select {
		case err := <-mcpDone:
			if err != nil {
				slog.Warn("mcp: manager close returned error", "error", err)
			}
		case <-time.After(8 * time.Second):
			slog.Warn("mcp: manager close timed out, continuing shutdown")
		}
		cronScheduler.Stop()
		if configWatcher != nil {
			configWatcher.Stop()
		}
		if cxProvider != nil {
			cortexadapter.Drain()
			cxProvider.Close()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
		// OTel last so trailing spans/metrics/logs from the cleanup steps
		// above get flushed before exporters close. 5s budget — exporters
		// shut down quickly when their queues are short.
		otelCtx, otelCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer otelCancel()
		_ = otelProv.Shutdown(otelCtx)
	}

	return &Result{
		Server:  srv,
		Config:  cfg,
		Cleanup: cleanup,
	}, nil
}

// settingsBootstrap returns t as a gateway.BootstrapSnapshotter, or nil when
// t is nil. Avoids passing a typed-nil interface that would panic on Snapshot.
func settingsBootstrap(t *local.Tracker) gateway.BootstrapSnapshotter {
	if t == nil {
		return nil
	}
	return t
}
