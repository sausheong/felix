package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sausheong/cortex"
	"github.com/sausheong/felix/internal/agent"
	"github.com/sausheong/felix/internal/compaction"
	"github.com/sausheong/felix/internal/config"
	cortexadapter "github.com/sausheong/felix/internal/cortex"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/skill"
	"github.com/sausheong/felix/internal/tokens"
	"github.com/sausheong/felix/internal/tools"
)

// JSONRPCRequest is a JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      any             `json:"id"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
	ID      any    `json:"id"`
}

// WebSocketHandler handles WebSocket connections and JSON-RPC dispatch.
type WebSocketHandler struct {
	providers         map[string]llm.LLMProvider
	tools             *tools.Registry
	sessionStore      *session.Store
	config            *config.Config
	compactionProv    *compaction.Provider // per-agent compaction manager factory; rebuilt in UpdateConfig
	jobScheduler      tools.JobScheduler
	skills            *skill.Loader
	memory            *memory.Manager
	cortexProvider    *cortexadapter.Provider // per-agent cortex client factory
	permission        tools.PermissionChecker // dispatch-time tool gate; nil → allow-all
	subagentBuild     agent.SubagentBuildFn   // builds RuntimeInputs for subagent dispatch via task tool
	calibratorStore   *tokens.CalibratorStore // per-session token-estimate calibration; cleared on session.clear
	activeRuns        map[*websocket.Conn]context.CancelFunc
	activeSessionKeys map[*websocket.Conn]map[string]string // conn → agentID → sessionKey
	upgrader          websocket.Upgrader
	mu                sync.RWMutex
}

// NewWebSocketHandler creates a new WebSocket handler.
func NewWebSocketHandler(
	providers map[string]llm.LLMProvider,
	toolReg *tools.Registry,
	sessionStore *session.Store,
	cfg *config.Config,
) *WebSocketHandler {
	return &WebSocketHandler{
		providers:         providers,
		tools:             toolReg,
		sessionStore:      sessionStore,
		config:            cfg,
		compactionProv:    compaction.NewProvider(cfg),
		activeRuns:        make(map[*websocket.Conn]context.CancelFunc),
		activeSessionKeys: make(map[*websocket.Conn]map[string]string),
		upgrader: websocket.Upgrader{
			CheckOrigin: AllowedOrigins(nil), // default: localhost-only; overridden by SetOriginChecker
		},
	}
}

// SetOriginChecker sets the WebSocket origin validation function.
func (h *WebSocketHandler) SetOriginChecker(check func(*http.Request) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.upgrader.CheckOrigin = check
}

// UpdateConfig hot-reloads the config.
func (h *WebSocketHandler) UpdateConfig(cfg *config.Config) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.config = cfg
	// Rebuild the per-agent compaction provider so config changes
	// (enable/disable, model swap, threshold tweak) take effect on the
	// next chat turn.
	h.compactionProv = compaction.NewProvider(cfg)
}

// UpdateProviders swaps the LLM provider map atomically. Called by the config
// watcher after the user edits provider credentials in the Settings UI so the
// next chat turn sees the new API key / base URL without a restart. Without
// this swap, the provider clients are frozen at startup time and any UI edit
// is silently ignored until the process is bounced.
func (h *WebSocketHandler) UpdateProviders(providers map[string]llm.LLMProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.providers = providers
}

// SetJobScheduler sets the job scheduler for jobs.* RPC methods.
func (h *WebSocketHandler) SetJobScheduler(js tools.JobScheduler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jobScheduler = js
}

// SetCortexProvider sets the per-agent Cortex factory. The handler resolves
// a *cortex.Cortex per chat turn via cxProvider.For(agentModel) so cortex's
// LLM extraction stays in lock-step with the chatting agent.
func (h *WebSocketHandler) SetCortexProvider(p *cortexadapter.Provider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cortexProvider = p
}

// SetPermission installs the dispatch-time tool permission gate. nil means
// allow-all (matches today's behavior when no policy is configured).
func (h *WebSocketHandler) SetPermission(p tools.PermissionChecker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.permission = p
}

// SetSkills sets the skill loader for the WebSocket handler.
func (h *WebSocketHandler) SetSkills(s *skill.Loader) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.skills = s
}

// SetMemory sets the memory manager for the WebSocket handler.
func (h *WebSocketHandler) SetMemory(m *memory.Manager) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.memory = m
}

// SetCalibratorStore wires the per-session token-estimate persistence layer.
// Called from startup wiring; nil disables the cleanup performed in
// handleSessionClear (the calibrator file would simply remain on disk).
func (h *WebSocketHandler) SetCalibratorStore(s *tokens.CalibratorStore) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calibratorStore = s
}

// SetSubagentBuilder installs the per-call SubagentBuildFn used by handleChatSend
// to construct task-tool subagent runtimes. nil disables subagent dispatch from
// the websocket path. Called once at startup wiring (the builder closes over
// the long-lived providers/MCP/policy state from the gateway scope).
func (h *WebSocketHandler) SetSubagentBuilder(fn agent.SubagentBuildFn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subagentBuild = fn
}

// Handle upgrades an HTTP connection to WebSocket and processes messages.
func (h *WebSocketHandler) Handle(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	conn.SetReadLimit(1 * 1024 * 1024) // 1MB max message size

	slog.Info("websocket client connected", "remote", r.RemoteAddr)
	defer func() {
		// Cancel any active run for this connection to prevent orphaned goroutines
		h.mu.Lock()
		if cancel, ok := h.activeRuns[conn]; ok {
			cancel()
			delete(h.activeRuns, conn)
		}
		delete(h.activeSessionKeys, conn)
		h.mu.Unlock()
	}()

	// Per-connection rate limiter: max 30 messages per second.
	// Uses a token bucket that refills at 30 tokens/sec with burst of 30.
	const rateLimit = 30
	tokens := rateLimit
	lastRefill := time.Now()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Error("websocket read error", "error", err)
			}
			return
		}

		// Refill tokens based on elapsed time
		now := time.Now()
		elapsed := now.Sub(lastRefill)
		tokens += int(elapsed.Seconds() * rateLimit)
		if tokens > rateLimit {
			tokens = rateLimit
		}
		lastRefill = now

		if tokens <= 0 {
			writeJSON(conn, JSONRPCResponse{
				JSONRPC: "2.0",
				Error:   map[string]any{"code": -32000, "message": "rate limit exceeded"},
				ID:      nil,
			})
			continue
		}
		tokens--

		var req JSONRPCRequest
		if err := json.Unmarshal(msg, &req); err != nil {
			writeJSON(conn, JSONRPCResponse{
				JSONRPC: "2.0",
				Error:   map[string]any{"code": -32700, "message": "Parse error"},
				ID:      nil,
			})
			continue
		}

		h.dispatch(conn, req)
	}
}

func (h *WebSocketHandler) dispatch(conn *websocket.Conn, req JSONRPCRequest) {
	switch req.Method {
	case "chat.send":
		h.handleChatSend(conn, req)
	case "chat.abort":
		h.handleChatAbort(conn, req)
	case "chat.compact":
		h.handleChatCompact(conn, req)
	case "agent.status":
		h.handleAgentStatus(conn, req)
	case "session.list":
		h.handleSessionList(conn, req)
	case "session.new":
		h.handleSessionNew(conn, req)
	case "session.switch":
		h.handleSessionSwitch(conn, req)
	case "session.history":
		h.handleSessionHistory(conn, req)
	case "session.clear":
		h.handleSessionClear(conn, req)
	case "jobs.list":
		h.handleJobsList(conn, req)
	case "jobs.pause":
		h.handleJobsPause(conn, req)
	case "jobs.resume":
		h.handleJobsResume(conn, req)
	case "jobs.remove":
		h.handleJobsRemove(conn, req)
	case "jobs.update":
		h.handleJobsUpdate(conn, req)
	case "jobs.add":
		h.handleJobsAdd(conn, req)
	default:
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32601, "message": "Method not found"},
			ID:      req.ID,
		})
	}
}

type chatSendParams struct {
	AgentID    string `json:"agentId"`
	Text       string `json:"text"`
	SessionKey string `json:"sessionKey,omitempty"`
}

func (h *WebSocketHandler) handleChatSend(conn *websocket.Conn, req JSONRPCRequest) {
	var params chatSendParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Invalid params"},
			ID:      req.ID,
		})
		return
	}

	if params.AgentID == "" {
		params.AgentID = "default"
	}

	h.mu.RLock()
	agentCfg, ok := h.config.GetAgent(params.AgentID)
	h.mu.RUnlock()

	if !ok {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Unknown agent"},
			ID:      req.ID,
		})
		return
	}

	// Resolve LLM provider — read under RLock so a concurrent UpdateProviders
	// (triggered by a Settings save / config hot-reload) can't tear the map.
	providerName, _ := llm.ParseProviderModel(agentCfg.Model)
	h.mu.RLock()
	provider, ok := h.providers[providerName]
	h.mu.RUnlock()
	if !ok {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "LLM provider not configured: " + providerName},
			ID:      req.ID,
		})
		return
	}

	// Load or create session — use explicit param, per-connection tracking, or default
	sessionKey := params.SessionKey
	if sessionKey == "" {
		h.mu.RLock()
		if m, ok := h.activeSessionKeys[conn]; ok {
			sessionKey = m[params.AgentID]
		}
		h.mu.RUnlock()
	}
	if sessionKey == "" {
		sessionKey = "ws_default"
	}
	sess, err := h.sessionStore.Load(params.AgentID, sessionKey)
	if err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Session error: " + err.Error()},
			ID:      req.ID,
		})
		return
	}

	// Tool policy enforcement is now handled by PermissionChecker:
	// FilterToolDefs hides denied tools from the model, and Check
	// short-circuits any deny attempts at dispatch time.
	//
	// We wrap the shared registry in a per-chat layer so the per-call task
	// tool (which captures THIS chat's parent runtime) doesn't leak into
	// other chats' tool definitions. The wrapper falls through to h.tools
	// for everything else.
	var executor tools.Executor = h.tools

	// Run agent. Resolve cortex + compaction per agent so both use the same
	// LLM as the chat itself (instead of mirroring whatever model the default
	// agent happens to use at startup).
	h.mu.RLock()
	cxProv := h.cortexProvider
	sk := h.skills
	mem := h.memory
	compProv := h.compactionProv
	perm := h.permission
	subagentBuild := h.subagentBuild
	cfg := h.config
	h.mu.RUnlock()
	var cx *cortex.Cortex
	if cxProv != nil {
		var err error
		if cx, err = cxProv.For(agentCfg.Model); err != nil {
			slog.Warn("cortex resolve failed", "agent", agentCfg.ID, "model", agentCfg.Model, "error", err)
		}
	}
	compactionMgr := compProv.For(agentCfg.Model)
	// Session.ID is freshly generated on every Load (in-memory instance
	// ID, not a persistent identifier). compactionMgr keys per-session
	// locks/failures by that ID, so without ForgetSession at end of
	// turn the locks map grows by one entry per chat.send forever.
	defer compactionMgr.ForgetSession(sess.ID)

	runtimeDeps := agent.RuntimeDeps{
		Skills:     sk,
		Memory:     mem,
		Permission: perm,
		CortexFn:   func(_ string) *cortex.Cortex { return cx },
		AgentLoop:  cfg.AgentLoop,
		Config:     cfg,
	}

	rt, err := agent.BuildRuntimeForAgent(runtimeDeps, agent.RuntimeInputs{
		Provider:   provider,
		Tools:      executor,
		Session:    sess,
		Compaction: compactionMgr,
	}, agentCfg)
	if err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Build runtime failed: " + err.Error()},
			ID:      req.ID,
		})
		return
	}

	// Wire the task tool for subagent dispatch. The shared h.tools registry
	// can't hold the per-call task tool (which captures THIS rt as Parent),
	// so we overlay it via a per-chat executor that adds "task" on top of
	// the shared registry. Activation requires both an installed builder
	// and at least one eligible subagent in the live config.
	if subagentBuild != nil && cfg != nil {
		if eligible := cfg.EligibleSubagents(); len(eligible) > 0 {
			factory := agent.MakeSubagentFactory(cfg, runtimeDeps, subagentBuild, rt)
			taskTool := tools.NewTaskTool(factory, rt.Depth, eligible)
			rt.Tools = &taskOverlayExecutor{base: h.tools, task: taskTool}
		}
	}

	runCtx, runCancel := context.WithCancel(context.Background())

	// Performance trace — emits one slog.Info "perf" line per phase boundary
	// (skills.match, llm.first_token, tool.exec, …) plus a final "perf summary".
	// Live-forward each phase mark to the WebSocket as a JSON-RPC notification
	// so the chat UI's trace panel can show phase timings as they happen.
	trace := agent.NewTrace(agentCfg.ID, agentCfg.Model)
	rpcID := req.ID
	trace.SetOnMark(func(phase string, durMs, atMs int64, attrs []any) {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Result: map[string]any{
				"type":   "trace",
				"phase":  phase,
				"dur_ms": durMs,
				"at_ms":  atMs,
				"attrs":  flattenAttrs(attrs),
			},
			ID: rpcID,
		})
	})
	trace.Mark("ws.received", "msg_chars", len(params.Text))
	runCtx = agent.WithTrace(runCtx, trace)

	// Track this run so chat.abort and disconnect can cancel it
	h.mu.Lock()
	h.activeRuns[conn] = runCancel
	h.mu.Unlock()

	events, err := rt.Run(runCtx, params.Text, nil)
	if err != nil {
		runCancel()
		h.mu.Lock()
		delete(h.activeRuns, conn)
		h.mu.Unlock()
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": err.Error()},
			ID:      req.ID,
		})
		return
	}

	// Stream events in a goroutine so the WebSocket read loop stays free
	// to process chat.abort messages.
	go func() {
		defer func() {
			runCancel()
			h.mu.Lock()
			delete(h.activeRuns, conn)
			h.mu.Unlock()
		}()

		for event := range events {
			var result any
			switch event.Type {
			case agent.EventTextDelta:
				result = map[string]any{"type": "text_delta", "text": event.Text}
			case agent.EventToolCallStart:
				result = map[string]any{"type": "tool_call_start", "tool": event.ToolCall.Name, "id": event.ToolCall.ID, "input": event.ToolCall.Input}
			case agent.EventToolResult:
				r := map[string]any{"type": "tool_result", "tool": event.ToolCall.Name, "id": event.ToolCall.ID, "input": event.ToolCall.Input, "output": event.Result.Output, "error": event.Result.Error}
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
				result = r
			case agent.EventCompactionStart:
				result = map[string]any{"type": "compaction.start"}
			case agent.EventCompactionDone:
				if event.Compaction != nil {
					result = map[string]any{
						"type":           "compaction.done",
						"turnsCompacted": event.Compaction.TurnsCompacted,
						"durationMs":     event.Compaction.DurationMs,
					}
				}
			case agent.EventCompactionSkipped:
				if event.Compaction != nil {
					result = map[string]any{
						"type":    "compaction.skipped",
						"reason":  string(event.Compaction.Reason),
						"skipped": event.Compaction.Skipped,
					}
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
					done["context_window"] = tokens.ContextWindow(agentCfg.Model)
					done["model"] = agentCfg.Model
				}
				result = done
			case agent.EventError:
				result = map[string]any{"type": "error", "message": event.Error.Error()}
			case agent.EventAborted:
				result = map[string]any{"type": "aborted"}
			}
			writeJSON(conn, JSONRPCResponse{
				JSONRPC: "2.0",
				Result:  result,
				ID:      req.ID,
			})
		}
	}()
}

func (h *WebSocketHandler) handleChatAbort(conn *websocket.Conn, req JSONRPCRequest) {
	h.mu.RLock()
	cancel, ok := h.activeRuns[conn]
	h.mu.RUnlock()

	if ok {
		cancel()
	}

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"ok": true},
		ID:      req.ID,
	})
}

type chatCompactParams struct {
	AgentID      string `json:"agentId"`
	SessionKey   string `json:"sessionKey,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

func (h *WebSocketHandler) handleChatCompact(conn *websocket.Conn, req JSONRPCRequest) {
	var params chatCompactParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Invalid params"},
			ID:      req.ID,
		})
		return
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}

	// Resolve compaction Manager for the agent's model so manual compaction
	// uses the same LLM the agent chats with.
	h.mu.RLock()
	compProv := h.compactionProv
	cfg := h.config
	h.mu.RUnlock()
	agentCfg, ok := cfg.GetAgent(params.AgentID)
	if !ok {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "agent not found: " + params.AgentID},
			ID:      req.ID,
		})
		return
	}
	mgr := compProv.For(agentCfg.Model)
	if mgr == nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32001, "message": "compaction not enabled"},
			ID:      req.ID,
		})
		return
	}

	// Resolve session key — explicit param, per-connection tracking, or default.
	sessionKey := params.SessionKey
	if sessionKey == "" {
		h.mu.RLock()
		if m, ok := h.activeSessionKeys[conn]; ok {
			sessionKey = m[params.AgentID]
		}
		h.mu.RUnlock()
	}
	if sessionKey == "" {
		sessionKey = "ws_default"
	}

	sess, err := h.sessionStore.Load(params.AgentID, sessionKey)
	if err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32004, "message": "session not found: " + err.Error()},
			ID:      req.ID,
		})
		return
	}

	res, err := mgr.MaybeCompact(context.Background(), sess, compaction.ReasonManual, params.Instructions)
	if err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32000, "message": err.Error()},
			ID:      req.ID,
		})
		return
	}

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result: map[string]any{
			"compacted":      res.Compacted,
			"reason":         string(res.Reason),
			"skipped":        res.Skipped,
			"turnsCompacted": res.TurnsCompacted,
			"tokensBefore":   res.TokensBefore,
			"tokensAfter":    res.TokensAfter,
			"durationMs":     res.DurationMs,
		},
		ID: req.ID,
	})
}

func (h *WebSocketHandler) handleAgentStatus(conn *websocket.Conn, req JSONRPCRequest) {
	h.mu.RLock()
	agents := h.config.Agents.List
	h.mu.RUnlock()

	var statuses []map[string]any
	for _, a := range agents {
		statuses = append(statuses, map[string]any{
			"id":        a.ID,
			"name":      a.Name,
			"model":     a.Model,
			"workspace": a.Workspace,
		})
	}

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"agents": statuses},
		ID:      req.ID,
	})
}

func (h *WebSocketHandler) handleSessionList(conn *websocket.Conn, req JSONRPCRequest) {
	var params sessionParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		params.AgentID = "default"
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}

	sessions, err := h.sessionStore.List(params.AgentID)
	if err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "List sessions error: " + err.Error()},
			ID:      req.ID,
		})
		return
	}

	// Determine active session key for this connection+agent
	h.mu.RLock()
	activeKey := "ws_default"
	if m, ok := h.activeSessionKeys[conn]; ok {
		if k, ok := m[params.AgentID]; ok {
			activeKey = k
		}
	}
	h.mu.RUnlock()

	var result []map[string]any
	for _, s := range sessions {
		result = append(result, map[string]any{
			"key":          s.Key,
			"entryCount":   s.EntryCount,
			"createdAt":    s.CreatedAt.Unix(),
			"lastActivity": s.LastActivity.Unix(),
			"active":       s.Key == activeKey,
		})
	}

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"sessions": result},
		ID:      req.ID,
	})
}

type sessionNewParams struct {
	AgentID string `json:"agentId"`
	Name    string `json:"name"`
}

func (h *WebSocketHandler) handleSessionNew(conn *websocket.Conn, req JSONRPCRequest) {
	var params sessionNewParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Invalid params"},
			ID:      req.ID,
		})
		return
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}
	if params.Name == "" {
		params.Name = time.Now().Format("20060102-150405")
	}

	sessionKey := "ws_" + params.Name
	if h.sessionStore.Exists(params.AgentID, sessionKey) {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Session already exists: " + sessionKey},
			ID:      req.ID,
		})
		return
	}

	// Create the session file on disk so it appears in List
	if err := h.sessionStore.Create(params.AgentID, sessionKey); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Create session error: " + err.Error()},
			ID:      req.ID,
		})
		return
	}

	// Set as active for this connection
	h.mu.Lock()
	if h.activeSessionKeys[conn] == nil {
		h.activeSessionKeys[conn] = make(map[string]string)
	}
	h.activeSessionKeys[conn][params.AgentID] = sessionKey
	h.mu.Unlock()

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"sessionKey": sessionKey},
		ID:      req.ID,
	})
}

type sessionSwitchParams struct {
	AgentID    string `json:"agentId"`
	SessionKey string `json:"sessionKey"`
}

func (h *WebSocketHandler) handleSessionSwitch(conn *websocket.Conn, req JSONRPCRequest) {
	var params sessionSwitchParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionKey == "" {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Invalid params: sessionKey required"},
			ID:      req.ID,
		})
		return
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}

	// Verify session exists (or it's a new one — Load creates if missing)
	h.mu.Lock()
	if h.activeSessionKeys[conn] == nil {
		h.activeSessionKeys[conn] = make(map[string]string)
	}
	h.activeSessionKeys[conn][params.AgentID] = params.SessionKey
	h.mu.Unlock()

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"sessionKey": params.SessionKey},
		ID:      req.ID,
	})
}

type sessionParams struct {
	AgentID    string `json:"agentId"`
	SessionKey string `json:"sessionKey,omitempty"`
}

func (h *WebSocketHandler) handleSessionHistory(conn *websocket.Conn, req JSONRPCRequest) {
	var params sessionParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		params.AgentID = "default"
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}

	// Resolve session key
	sessionKey := params.SessionKey
	if sessionKey == "" {
		h.mu.RLock()
		if m, ok := h.activeSessionKeys[conn]; ok {
			sessionKey = m[params.AgentID]
		}
		h.mu.RUnlock()
	}
	if sessionKey == "" {
		sessionKey = "ws_default"
	}

	sess, err := h.sessionStore.Load(params.AgentID, sessionKey)
	if err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Session load error: " + err.Error()},
			ID:      req.ID,
		})
		return
	}

	history := sess.History()
	var entries []map[string]any

	for _, entry := range history {
		switch entry.Type {
		case session.EntryTypeMessage:
			var msg session.MessageData
			if err := json.Unmarshal(entry.Data, &msg); err != nil {
				continue
			}
			entries = append(entries, map[string]any{
				"type": "message",
				"role": entry.Role,
				"text": msg.Text,
			})
		case session.EntryTypeToolCall:
			var tc session.ToolCallData
			if err := json.Unmarshal(entry.Data, &tc); err != nil {
				continue
			}
			entries = append(entries, map[string]any{
				"type":  "tool_call",
				"tool":  tc.Tool,
				"id":    tc.ID,
				"input": tc.Input,
			})
		case session.EntryTypeToolResult:
			var tr session.ToolResultData
			if err := json.Unmarshal(entry.Data, &tr); err != nil {
				continue
			}
			e := map[string]any{
				"type":         "tool_result",
				"tool_call_id": tr.ToolCallID,
				"output":       tr.Output,
				"error":        tr.Error,
			}
			if len(tr.Images) > 0 {
				var imgs []map[string]string
				for _, img := range tr.Images {
					imgs = append(imgs, map[string]string{
						"mimeType": img.MimeType,
						"data":     img.Data, // already base64
					})
				}
				e["images"] = imgs
			}
			entries = append(entries, e)
		case session.EntryTypeMeta:
			// Skip compaction summaries — internal
		}
	}

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"entries": entries},
		ID:      req.ID,
	})
}

func (h *WebSocketHandler) handleSessionClear(conn *websocket.Conn, req JSONRPCRequest) {
	var params sessionParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		params.AgentID = "default"
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}

	// Resolve session key
	sessionKey := params.SessionKey
	if sessionKey == "" {
		h.mu.RLock()
		if m, ok := h.activeSessionKeys[conn]; ok {
			sessionKey = m[params.AgentID]
		}
		h.mu.RUnlock()
	}
	if sessionKey == "" {
		sessionKey = "ws_default"
	}

	if err := h.sessionStore.Delete(params.AgentID, sessionKey); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Delete error: " + err.Error()},
			ID:      req.ID,
		})
		return
	}

	// Cascade-remove the per-session spill directory if the agent has a
	// workspace. Best-effort — RemoveSessionSpill never returns an error.
	h.mu.RLock()
	cfg := h.config
	calStore := h.calibratorStore
	h.mu.RUnlock()
	if cfg != nil {
		if a, ok := cfg.GetAgent(params.AgentID); ok {
			agent.RemoveSessionSpill(a.Workspace, sessionKey)
		}
	}
	// Forget the calibrator record for this session — without this the
	// calibrator JSON file leaks into ~/.felix/calibrators/ forever.
	calStore.Forget(params.AgentID, sessionKey)

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"ok": true},
		ID:      req.ID,
	})
}

// jobs.* handlers

type jobNameParams struct {
	Name string `json:"name"`
}

type jobUpdateParams struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
}

func (h *WebSocketHandler) handleJobsList(conn *websocket.Conn, req JSONRPCRequest) {
	h.mu.RLock()
	js := h.jobScheduler
	h.mu.RUnlock()

	if js == nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Job scheduler not available"},
			ID:      req.ID,
		})
		return
	}

	jobs := js.ListJobs()
	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"jobs": jobs},
		ID:      req.ID,
	})
}

func (h *WebSocketHandler) handleJobsPause(conn *websocket.Conn, req JSONRPCRequest) {
	h.mu.RLock()
	js := h.jobScheduler
	h.mu.RUnlock()

	if js == nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Job scheduler not available"},
			ID:      req.ID,
		})
		return
	}

	var params jobNameParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Invalid params: name required"},
			ID:      req.ID,
		})
		return
	}

	if err := js.PauseJob(params.Name); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": err.Error()},
			ID:      req.ID,
		})
		return
	}

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"ok": true},
		ID:      req.ID,
	})
}

func (h *WebSocketHandler) handleJobsResume(conn *websocket.Conn, req JSONRPCRequest) {
	h.mu.RLock()
	js := h.jobScheduler
	h.mu.RUnlock()

	if js == nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Job scheduler not available"},
			ID:      req.ID,
		})
		return
	}

	var params jobNameParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Invalid params: name required"},
			ID:      req.ID,
		})
		return
	}

	if err := js.ResumeJob(params.Name); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": err.Error()},
			ID:      req.ID,
		})
		return
	}

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"ok": true},
		ID:      req.ID,
	})
}

func (h *WebSocketHandler) handleJobsRemove(conn *websocket.Conn, req JSONRPCRequest) {
	h.mu.RLock()
	js := h.jobScheduler
	h.mu.RUnlock()

	if js == nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Job scheduler not available"},
			ID:      req.ID,
		})
		return
	}

	var params jobNameParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Invalid params: name required"},
			ID:      req.ID,
		})
		return
	}

	if err := js.RemoveJob(params.Name); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": err.Error()},
			ID:      req.ID,
		})
		return
	}

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"ok": true},
		ID:      req.ID,
	})
}

type jobAddParams struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
}

func (h *WebSocketHandler) handleJobsAdd(conn *websocket.Conn, req JSONRPCRequest) {
	h.mu.RLock()
	js := h.jobScheduler
	h.mu.RUnlock()

	if js == nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Job scheduler not available"},
			ID:      req.ID,
		})
		return
	}

	var params jobAddParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Invalid params: " + err.Error()},
			ID:      req.ID,
		})
		return
	}
	if params.Name == "" || params.Schedule == "" || params.Prompt == "" {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "name, schedule, and prompt are all required"},
			ID:      req.ID,
		})
		return
	}

	if err := js.AddJob(params.Name, params.Schedule, params.Prompt); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": err.Error()},
			ID:      req.ID,
		})
		return
	}

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"ok": true, "name": params.Name},
		ID:      req.ID,
	})
}

func (h *WebSocketHandler) handleJobsUpdate(conn *websocket.Conn, req JSONRPCRequest) {
	h.mu.RLock()
	js := h.jobScheduler
	h.mu.RUnlock()

	if js == nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Job scheduler not available"},
			ID:      req.ID,
		})
		return
	}

	var params jobUpdateParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" || params.Schedule == "" {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Invalid params: name and schedule required"},
			ID:      req.ID,
		})
		return
	}

	if err := js.UpdateJobSchedule(params.Name, params.Schedule); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": err.Error()},
			ID:      req.ID,
		})
		return
	}

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"ok": true},
		ID:      req.ID,
	})
}

// flattenAttrs converts the variadic key,value,key,value slice that
// agent.Trace.Mark emits into a string-keyed map suitable for JSON
// serialization. Non-string keys are stringified via fmt.Sprintf("%v")
// for safety; trailing odd-length tail is ignored. Returns nil for an
// empty input so the JSON shape stays compact.
func flattenAttrs(attrs []any) map[string]any {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]any, len(attrs)/2)
	for i := 0; i+1 < len(attrs); i += 2 {
		k, ok := attrs[i].(string)
		if !ok {
			continue
		}
		out[k] = attrs[i+1]
	}
	return out
}

func writeJSON(conn *websocket.Conn, v any) {
	if err := conn.WriteJSON(v); err != nil {
		slog.Error("websocket write error", "error", err)
	}
}

// taskOverlayExecutor wraps the shared tools.Registry and adds the per-chat
// "task" tool on top. The shared registry can't hold the task tool because
// each chat needs its own task tool with its own parent Runtime captured;
// registering on the shared registry would either race-clobber other chats
// or cross-wire parents. The overlay is read-through for everything except
// the "task" name.
type taskOverlayExecutor struct {
	base *tools.Registry
	task *tools.TaskTool
}

func (e *taskOverlayExecutor) Execute(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	if name == e.task.Name() {
		return e.task.Execute(ctx, input)
	}
	return e.base.Execute(ctx, name, input)
}

func (e *taskOverlayExecutor) ToolDefs() []llm.ToolDef {
	defs := e.base.ToolDefs()
	defs = append(defs, llm.ToolDef{
		Name:        e.task.Name(),
		Description: e.task.Description(),
		Parameters:  e.task.Parameters(),
	})
	// Re-sort to keep prompt-cache-stable ordering — see Registry.ToolDefs.
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

func (e *taskOverlayExecutor) Names() []string {
	names := e.base.Names()
	names = append(names, e.task.Name())
	sort.Strings(names)
	return names
}

func (e *taskOverlayExecutor) Get(name string) (tools.Tool, bool) {
	if name == e.task.Name() {
		return e.task, true
	}
	return e.base.Get(name)
}
