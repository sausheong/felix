// Package startup provides shared gateway startup logic used by both the
// CLI (cmd/felix) and the menu bar app (cmd/felix-app).
package startup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/felix/internal/agent"
	"github.com/sausheong/felix/internal/compaction"
	"github.com/sausheong/felix/internal/config"
	cortexadapter "github.com/sausheong/felix/internal/cortex"
	"github.com/sausheong/felix/internal/cron"
	"github.com/sausheong/felix/internal/gateway"
	"github.com/sausheong/felix/internal/heartbeat"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/local"
	"github.com/sausheong/felix/internal/mcp"
	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/skill"
	"github.com/sausheong/felix/internal/tools"
)

// Result holds the running gateway components.
type Result struct {
	Server    *gateway.Server
	Config    *config.Config
	Cleanup   func() // call to gracefully shut down everything
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
type CronSchedulerAdapter struct {
	Scheduler    *cron.Scheduler
	Ctx          context.Context
	AgentFactory func(name string) func(context.Context, string) (string, error)
	OutputFn     cron.OutputFunc
	JobsFile     string
}

// persistedJob is the on-disk shape. We don't persist AgentFn/OutputFn — they
// are reconstructed from AgentFactory/OutputFn at Restore time.
type persistedJob struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
	Paused   bool   `json:"paused"`
}

func (a *CronSchedulerAdapter) addJobInternal(name, schedule, prompt string, persist bool) error {
	var agentFn func(context.Context, string) (string, error)
	if a.AgentFactory != nil {
		agentFn = a.AgentFactory(name)
	} else {
		agentFn = func(ctx context.Context, p string) (string, error) {
			slog.Info("dynamic cron job executed (no agent)", "name", name)
			return "OK", nil
		}
	}

	err := a.Scheduler.Add(cron.Job{
		Name:     name,
		Schedule: schedule,
		Prompt:   prompt,
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

func (a *CronSchedulerAdapter) AddJob(name, schedule, prompt string) error {
	return a.addJobInternal(name, schedule, prompt, true)
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
		out = append(out, persistedJob{
			Name:     j.Name,
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
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
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
		// file N times during startup; we'll flush once at the end.
		if err := a.addJobInternal(p.Name, p.Schedule, p.Prompt, false); err != nil {
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
	}
	return nil
}

// Options configures gateway startup behavior. Reserved for future use.
type Options struct{}

// StartGateway starts the full gateway server and returns the result.
// The caller is responsible for calling Result.Cleanup() on shutdown and
// starting the HTTP server via Result.Server.Start() in a goroutine.
func StartGateway(configPath, version string, opts ...Options) (*Result, error) {
	_ = opts
	// Install a tee log handler that captures entries for the /logs page
	// while forwarding to a stderr handler. We create a fresh TextHandler
	// rather than using slog.Default().Handler() because in Go 1.22+ the
	// default handler routes through log.Logger which routes back through
	// slog.Default(), creating a deadlock when LogBuffer replaces it.
	logBuf := gateway.NewLogBuffer(2000, slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(slog.New(logBuf))

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

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
			})
			startCtx, startCancel := context.WithTimeout(context.Background(), 70*time.Second)
			if err := localSup.Start(startCtx); err != nil {
				slog.Warn("failed to start bundled ollama; local provider disabled", "error", err)
				localSup = nil
			} else {
				if ierr := local.InjectLocalProvider(configPath, localSup.BoundPort()); ierr != nil {
					slog.Warn("failed to inject local provider config", "error", ierr)
				}
				// Re-load so InitProviders sees the local block.
				if reloaded, rerr := config.Load(configPath); rerr == nil {
					cfg.UpdateFrom(reloaded)
				}
				// First-run background pull of default local models (gemma4 + nomic-embed).
				if cfg.Local.Enabled {
					if pcfg := cfg.GetProvider("local"); pcfg.BaseURL != "" {
						ollamaURL := strings.TrimSuffix(pcfg.BaseURL, "/v1")
						puller := local.NewInstaller(ollamaURL)
						bootstrapTracker = local.NewTracker()
						local.EnsureFirstRunModels(context.Background(), config.DefaultDataDir(), puller, bootstrapTracker.OnEvent)

						// Pre-warm the default agent's local model so the first chat
						// turn doesn't pay the ~10s cold-load latency. Runs in the
						// background and silently logs failure (e.g. model still pulling).
						if len(cfg.Agents.List) > 0 {
							defaultModel := cfg.Agents.List[0].Model
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
						if cfg.Memory.Enabled && cfg.Memory.EmbeddingModel != "" {
							embedModel := cfg.Memory.EmbeddingModel
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
			}
			startCancel()
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
	sessionStore := session.NewStore(filepath.Join(dataDir, "sessions"))
	toolReg := tools.NewRegistry()
	execPolicy := &tools.ExecPolicy{
		Level:     cfg.Security.ExecApprovals.Level,
		Allowlist: cfg.Security.ExecApprovals.Allowlist,
	}
	tools.RegisterCoreTools(toolReg, "", execPolicy)
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
	mcpInitCtx, mcpInitCancel := context.WithTimeout(context.Background(), 30*time.Second)
	mcpMgr, err := mcp.NewManager(mcpInitCtx, mcpServerCfgs)
	mcpInitCancel()
	if err != nil {
		return nil, fmt.Errorf("init mcp manager: %w", err)
	}
	mcpNames, err := mcp.RegisterTools(toolReg, mcpMgr)
	if err != nil {
		mcpMgr.Close()
		return nil, fmt.Errorf("register mcp tools: %w", err)
	}
	cfg.ApplyMCPToolNamesToAllowlists(mcpNames)

	// Init skill loader
	skillLoader := skill.NewLoader()
	skillDirs := []string{filepath.Join(dataDir, "skills")}
	for _, a := range cfg.Agents.List {
		skillDirs = append(skillDirs, filepath.Join(a.Workspace, "skills"))
	}
	if err := skillLoader.LoadFrom(skillDirs...); err != nil {
		slog.Warn("failed to load skills", "error", err)
	}

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
	}

	// Init Cortex knowledge graph
	var cx *cortex.Cortex
	if cfg.Cortex.Enabled {
		var initErr error
		defaultAgentModel := ""
		if len(cfg.Agents.List) > 0 {
			defaultAgentModel = cfg.Agents.List[0].Model
		}
		cx, initErr = cortexadapter.Init(cfg.Cortex, cfg.Memory, defaultAgentModel, cfg.GetProvider)
		if initErr != nil {
			slog.Warn("failed to init cortex", "error", initErr)
		} else if cx != nil {
			// Pre-warm Cortex: a tiny background recall warms the embedder,
			// chromem index, and any per-process caches so the first user
			// request doesn't pay a ~4–11s cold-search penalty.
			go func() {
				warmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				start := time.Now()
				_, err := cx.Recall(warmCtx, "hello", cortex.WithLimit(1))
				if err != nil {
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

	// Init WebSocket handler
	wsHandler := gateway.NewWebSocketHandler(providers, toolReg, sessionStore, cfg)
	wsHandler.SetSkills(skillLoader)
	wsHandler.SetMemory(memMgr)
	wsHandler.SetCortex(cx)

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
			wsHandler.UpdateConfig(newCfg)
			wsHandler.UpdateProviders(newProviders)
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

	// Build the compaction Manager once and share across heartbeat, cron,
	// and the cron-tool agent factory below. The Manager's per-session mutex
	// map only serializes correctly when the same instance is reused.
	startupCompactionMgr := compaction.BuildManager(cfg)

	// Start heartbeat daemon for each agent if enabled
	var heartbeats []*heartbeat.Daemon
	if cfg.Heartbeat.Enabled {
		interval, err := time.ParseDuration(cfg.Heartbeat.Interval)
		if err != nil {
			interval = 30 * time.Minute
		}

		for _, agentCfg := range cfg.Agents.List {
			providerName, modelName := llm.ParseProviderModel(agentCfg.Model)
			provider, ok := providers[providerName]
			if !ok {
				continue
			}

			agentWorkspace := agentCfg.Workspace
			agentID := agentCfg.ID
			agentMaxTurns := agentCfg.MaxTurns

			agentSystemPrompt := agentCfg.SystemPrompt
			agentName := agentCfg.Name
			agentFn := func(ctx context.Context, prompt string) (string, error) {
				sess := session.NewSession(agentID, "heartbeat")
				hbToolReg := tools.NewRegistry()
				tools.RegisterCoreTools(hbToolReg, agentWorkspace, execPolicy)
				if _, err := mcp.RegisterTools(hbToolReg, mcpMgr); err != nil {
					slog.Warn("mcp: failed to register tools for sub-registry, continuing", "error", err)
				}
				tools.RegisterSendMessage(hbToolReg, sendMsgConfigFn)

				rt := &agent.Runtime{
					LLM:          provider,
					Tools:        hbToolReg,
					Session:      sess,
					AgentID:      agentID,
					AgentName:    agentName,
					Model:        modelName,
					Workspace:    agentWorkspace,
					MaxTurns:     agentMaxTurns,
					SystemPrompt: agentSystemPrompt,
					Skills:       skillLoader,
					Memory:       memMgr,
					Cortex:       cx,
					Compaction:   startupCompactionMgr,
					IngestSource: "heartbeat",
				}
				return rt.RunSync(ctx, prompt, nil)
			}

			daemon := heartbeat.NewDaemon(agentCfg.Workspace, interval, agentFn)
			daemon.Start(ctx)
			heartbeats = append(heartbeats, daemon)
		}
	}

	// Start cron scheduler for agents with cron jobs
	cronScheduler := cron.NewScheduler()
	for _, agentCfg := range cfg.Agents.List {
		for _, cronJob := range agentCfg.Cron {
			providerName, modelName := llm.ParseProviderModel(agentCfg.Model)
			provider, ok := providers[providerName]
			if !ok {
				continue
			}
			agentWorkspace := agentCfg.Workspace
			agentID := agentCfg.ID
			agentMaxTurns := agentCfg.MaxTurns
			jobPrompt := cronJob.Prompt

			agentSystemPrompt := agentCfg.SystemPrompt
			agentName := agentCfg.Name
			agentFn := func(ctx context.Context, prompt string) (string, error) {
				sess := session.NewSession(agentID, "cron_"+cronJob.Name)
				cronToolReg := tools.NewRegistry()
				tools.RegisterCoreTools(cronToolReg, agentWorkspace, execPolicy)
				if _, err := mcp.RegisterTools(cronToolReg, mcpMgr); err != nil {
					slog.Warn("mcp: failed to register tools for sub-registry, continuing", "error", err)
				}
				tools.RegisterSendMessage(cronToolReg, sendMsgConfigFn)
				rt := &agent.Runtime{
					LLM:          provider,
					Tools:        cronToolReg,
					Session:      sess,
					AgentID:      agentID,
					AgentName:    agentName,
					Model:        modelName,
					Workspace:    agentWorkspace,
					MaxTurns:     agentMaxTurns,
					SystemPrompt: agentSystemPrompt,
					Skills:       skillLoader,
					Memory:       memMgr,
					Cortex:       cx,
					Compaction:   startupCompactionMgr,
					IngestSource: "cron",
				}
				return rt.RunSync(ctx, prompt, nil)
			}

			cronScheduler.Add(cron.Job{
				Name:     cronJob.Name,
				Schedule: cronJob.Schedule,
				Prompt:   jobPrompt,
				AgentFn:  agentFn,
			})
		}
	}

	cronAdapter := &CronSchedulerAdapter{
		Scheduler: cronScheduler,
		Ctx:       ctx,
		JobsFile:  filepath.Join(dataDir, "cron-jobs.json"),
		AgentFactory: func(jobName string) func(context.Context, string) (string, error) {
			return func(ctx context.Context, prompt string) (string, error) {
				defaultCfg := cfg.Agents.List[0]
				pName, mName := llm.ParseProviderModel(defaultCfg.Model)
				p, ok := providers[pName]
				if !ok {
					return "", fmt.Errorf("provider %q not available", pName)
				}
				cronSess := session.NewSession(defaultCfg.ID, "cron_"+jobName)
				cronToolReg := tools.NewRegistry()
				tools.RegisterCoreTools(cronToolReg, defaultCfg.Workspace, execPolicy)
				if _, err := mcp.RegisterTools(cronToolReg, mcpMgr); err != nil {
					slog.Warn("mcp: failed to register tools for sub-registry, continuing", "error", err)
				}
				tools.RegisterSendMessage(cronToolReg, sendMsgConfigFn)
				rt := &agent.Runtime{
					LLM:          p,
					Tools:        cronToolReg,
					Session:      cronSess,
					AgentID:      defaultCfg.ID,
					AgentName:    defaultCfg.Name,
					Model:        mName,
					Workspace:    defaultCfg.Workspace,
					MaxTurns:     defaultCfg.MaxTurns,
					SystemPrompt: defaultCfg.SystemPrompt,
					Skills:       skillLoader,
					Memory:       memMgr,
					Cortex:       cx,
					Compaction:   startupCompactionMgr,
					IngestSource: "cron",
				}
				return rt.RunSync(ctx, prompt, nil)
			}
		},
	}
	tools.RegisterCron(toolReg, cronAdapter)
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

	// Init metrics
	metrics := gateway.NewMetrics()

	// Start gateway HTTP server
	port := cfg.Gateway.Port
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
		LogBuffer: logBuf,
	})

	cleanup := func() {
		tools.ShutdownBrowsers()
		// Bound MCP close so a slow/unreachable upstream can't hang the whole
		// shutdown chain (which would also leave the bundled Ollama supervisor
		// running, leaking processes across runs). 8s budget covers the SDK's
		// 5s stdin-close → SIGTERM grace period for stdio child processes,
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
		for _, hb := range heartbeats {
			hb.Stop()
		}
		if localSup != nil {
			_ = localSup.Stop()
		}
		if configWatcher != nil {
			configWatcher.Stop()
		}
		if cx != nil {
			cortexadapter.Drain()
			cx.Close()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
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
