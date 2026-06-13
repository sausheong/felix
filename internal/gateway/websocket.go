package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sausheong/felix/internal/agent"
	"github.com/sausheong/felix/internal/chatexec"
	"github.com/sausheong/felix/internal/compaction"
	"github.com/sausheong/felix/internal/config"
	cortexadapter "github.com/sausheong/felix/internal/cortex"
	"github.com/sausheong/felix/internal/gateway/runs"
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
	activeSessionKeys map[*websocket.Conn]map[string]string // conn → agentID → sessionKey
	sessionsBaseDir   string                                // root of session storage; passed to constructor; used by handleChatReplay to read on-disk run logs
	runs              *runs.Registry                        // durable in-flight run registry; nil until SetRunsRegistry; chat.send fails with RPC -32000 if still nil
	serverCtx         context.Context                       // process-wide ctx so runs unwind on shutdown; nil → falls back to context.Background; SetServerCtx wires it
	metrics           *Metrics                              // optional; chat-turn and tool-call counters; SetMetrics wires it
	upgrader          websocket.Upgrader
	mu                sync.RWMutex
	runSem            chan struct{} // bounds concurrent chat.send runs; nil until initLimits
	connCount         atomic.Int64  // current open WebSocket connections
}

// NewWebSocketHandler creates a new WebSocket handler.
func NewWebSocketHandler(
	providers map[string]llm.LLMProvider,
	toolReg *tools.Registry,
	sessionStore *session.Store,
	cfg *config.Config,
	sessionsBaseDir string,
) *WebSocketHandler {
	h := &WebSocketHandler{
		providers:         providers,
		tools:             toolReg,
		sessionStore:      sessionStore,
		config:            cfg,
		compactionProv:    compaction.NewProvider(cfg),
		activeSessionKeys: make(map[*websocket.Conn]map[string]string),
		sessionsBaseDir:   sessionsBaseDir,
		upgrader: websocket.Upgrader{
			CheckOrigin: AllowedOrigins(nil), // default: localhost-only; overridden by SetOriginChecker
		},
	}
	h.initLimits()
	return h
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

// SetRunsRegistry installs the durable in-flight run registry. chat.send
// fails with RPC -32000 until this is set.
func (h *WebSocketHandler) SetRunsRegistry(reg *runs.Registry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runs = reg
}

// SetServerCtx wires the process-wide server context so chat runs unwind
// when the gateway shuts down. nil → run handlers fall back to
// context.Background.
func (h *WebSocketHandler) SetServerCtx(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.serverCtx = ctx
}

// SetMetrics installs the optional metrics collector. chatexec.RunTurn and
// writeRPCError nil-guard before touching it.
func (h *WebSocketHandler) SetMetrics(m *Metrics) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.metrics = m
}

// BroadcastNewRun is the runs.Registry.OnNewRun callback. For every open conn
// currently viewing the same (agent, session) scope as the new run, push a
// JSON-RPC notification "run_started" so the frontend can attach via
// chat.replay/chat.subscribe.
func (h *WebSocketHandler) BroadcastNewRun(scope runs.SessionScope, run *runs.Run) {
	h.mu.RLock()
	conns := make([]*websocket.Conn, 0)
	for conn, viewMap := range h.activeSessionKeys {
		if viewMap[scope.AgentID] == scope.SessionKey {
			conns = append(conns, conn)
		}
	}
	h.mu.RUnlock()

	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "run_started",
		"params": map[string]any{
			"runId":      run.ID,
			"agentId":    scope.AgentID,
			"sessionKey": scope.SessionKey,
		},
	}
	for _, c := range conns {
		writeJSON(c, notif)
	}
}

// Handle upgrades an HTTP connection to WebSocket and processes messages.
func (h *WebSocketHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if !h.acquireConn() {
		http.Error(w, `{"error":"too many connections"}`, http.StatusServiceUnavailable)
		return
	}
	defer h.releaseConn()
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()
	defer releaseConnMutex(conn)

	conn.SetReadLimit(1 * 1024 * 1024) // 1MB max message size

	slog.Info("websocket client connected", "remote", r.RemoteAddr)
	defer func() {
		// chatexec/runs.Registry owns the run lifecycle now — runs survive
		// a conn disconnect. We drop this conn's session-key view and
		// unsubscribe it from any in-flight runs so the per-conn
		// forwardEvents goroutines exit instead of looping on writeJSON
		// against a dead conn.
		h.mu.RLock()
		reg := h.runs
		h.mu.RUnlock()
		if reg != nil {
			reg.UnsubscribeAll(conn)
		}
		h.mu.Lock()
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
	case "chat.subscribe":
		h.handleChatSubscribe(conn, req)
	case "chat.replay":
		h.handleChatReplay(conn, req)
	case "chat.runs":
		h.handleChatRuns(conn, req)
	case "chat.deleteRun":
		h.handleChatDeleteRun(conn, req)
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
	case "session.rename":
		h.handleSessionRename(conn, req)
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

// handleChatSend drives a chat turn through chatexec.RunTurn. The new
// flow: parse params → snapshot deps under RLock → kick off RunTurn on
// a goroutine. chatexec owns the run lifecycle: it registers the run in
// h.runs (which broadcasts run_started via BroadcastNewRun), drains
// events to disk and to the wsSubscriber, and calls Finish on every
// exit path. wsSubscriber.OnAttached fires the JSON-RPC response
// carrying {runID} for this chat.send; OnEvent fires each agent event
// as a chat.event-shaped JSON-RPC notification on this conn.
//
// The default session key remains "ws_default" to preserve on-disk
// session JSONL files from felix's pre-port era (cloudcat uses
// "default"; do NOT copy that part verbatim).
func (h *WebSocketHandler) handleChatSend(conn *websocket.Conn, req JSONRPCRequest) {
	var params chatSendParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(conn, h.metrics, req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}

	// Resolve session key (caller-provided, per-conn active, or
	// felix's pre-port default "ws_default" — NOT "default").
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
	scope := runs.SessionScope{AgentID: params.AgentID, SessionKey: sessionKey}

	rpcID := req.ID

	// Snapshot deps under RLock so a concurrent UpdateConfig /
	// UpdateProviders / SetPermission can't tear references mid-call.
	h.mu.RLock()
	deps := chatexec.TurnDeps{
		Runs:           h.runs,
		Sessions:       h.sessionStore,
		SessionsBase:   h.sessionsBaseDir,
		Providers:      h.providers,
		Tools:          h.tools,
		Permission:     h.permission,
		Skills:         h.skills,
		Memory:         h.memory,
		CompactionProv: h.compactionProv,
		Config:         h.config,
		SubagentBuild:  h.subagentBuild,
		JobScheduler:   h.jobScheduler,
		Metrics:        h.metrics,
		ServerCtx:      h.serverCtx,
		OnTraceMark:    h.makeTraceMarkForwarder(conn, rpcID),
	}
	if h.cortexProvider != nil {
		// Nil-guard so deps.CortexProvider stays nil-safe when cortex
		// is unconfigured (chatexec's nil-check is on the field, not
		// the underlying value).
		deps.CortexProvider = h.cortexProvider
	}
	metrics := h.metrics
	h.mu.RUnlock()

	if deps.Runs == nil {
		writeRPCError(conn, metrics, rpcID, -32000, "runs registry not configured")
		return
	}

	sub := &wsSubscriber{conn: conn, rpcID: rpcID}

	if !h.acquireRun() {
		writeRPCError(conn, metrics, rpcID, -32000, "server busy: too many concurrent runs, retry shortly")
		return
	}
	go func() {
		defer h.releaseRun()
		_, err := chatexec.RunTurn(context.Background(), deps, scope, params.Text, sub)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("chatexec.RunTurn", "agent", scope.AgentID, "session", scope.SessionKey, "error", err)
			// chatexec wrote a terminal failure event via Finish, which the
			// subscriber already saw. Surface an RPC error for completeness
			// so clients that wired chat.send → callback can fail cleanly.
			writeRPCError(conn, metrics, rpcID, -32603, err.Error())
		}
	}()
}

// makeTraceMarkForwarder returns a closure that mirrors agent trace
// phase marks to the conn as JSON-RPC notifications shaped as the
// existing felix wire format (matches the pre-refactor inline
// SetOnMark callback at lines 498-510 of the prior websocket.go).
func (h *WebSocketHandler) makeTraceMarkForwarder(conn *websocket.Conn, rpcID any) func(phase string, durMs, atMs int64, attrs []any) {
	return func(phase string, durMs, atMs int64, attrs []any) {
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
	}
}

// handleChatAbort cancels the in-flight run for (agentID, sessionKey)
// by looking it up in h.runs and invoking its CancelFn, then finishing
// it as cancelled with reason=user_abort. Subscribers see the terminal
// Done event. Replies {aborted: true, runId} on success, {aborted:
// false} when no run is active for the scope (not an error — pressing
// abort with nothing to abort is success).
func (h *WebSocketHandler) handleChatAbort(conn *websocket.Conn, req JSONRPCRequest) {
	var params struct {
		AgentID    string `json:"agentId"`
		SessionKey string `json:"sessionKey"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(conn, h.metrics, req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}

	// Resolve session key: explicit param → per-conn active → "ws_default".
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

	h.mu.RLock()
	reg := h.runs
	metrics := h.metrics
	h.mu.RUnlock()

	if reg == nil {
		writeRPCError(conn, metrics, req.ID, -32000, "runs registry not configured")
		return
	}

	run := reg.GetBySession(runs.SessionScope{AgentID: params.AgentID, SessionKey: sessionKey})
	resolvedAgent := params.AgentID
	resolvedSession := sessionKey
	if run == nil {
		// Fallback: client didn't supply scope (or supplied a mismatched one),
		// but this conn has views — try each.
		h.mu.RLock()
		viewMap := make(map[string]string, len(h.activeSessionKeys[conn]))
		for a, s := range h.activeSessionKeys[conn] {
			viewMap[a] = s
		}
		h.mu.RUnlock()
		for a, s := range viewMap {
			if a == params.AgentID && s == sessionKey {
				continue
			}
			if r := reg.GetBySession(runs.SessionScope{AgentID: a, SessionKey: s}); r != nil {
				run = r
				resolvedAgent = a
				resolvedSession = s
				slog.Debug("chat.abort: resolved via fallback per-conn lookup", "agentId", a, "sessionKey", s)
				break
			}
		}
	}
	if run == nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Result:  map[string]any{"aborted": false},
			ID:      req.ID,
		})
		return
	}

	if run.CancelFn != nil {
		run.CancelFn()
	}
	if err := run.Finish(runs.StatusCancelled, runs.ReasonUserAbort, ""); err != nil {
		slog.Warn("handleChatAbort: run.Finish", "runId", run.ID, "agent", resolvedAgent, "session", resolvedSession, "error", err)
	}

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"aborted": true, "runId": run.ID},
		ID:      req.ID,
	})
}

// handleChatSubscribe attaches conn to the in-flight run for scope.
// Returns past events (seq > fromSeq) in the RPC response; live events
// arrive as chat.event notifications until Finish closes the channel.
//
// Default session-key fallback matches handleChatSend / handleChatAbort:
// explicit → activeSessionKeys[conn][agentID] → "ws_default".
func (h *WebSocketHandler) handleChatSubscribe(conn *websocket.Conn, req JSONRPCRequest) {
	var params struct {
		AgentID    string `json:"agentId"`
		SessionKey string `json:"sessionKey"`
		FromSeq    int64  `json:"fromSeq"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(conn, h.metrics, req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}
	if params.SessionKey == "" {
		h.mu.RLock()
		if m, ok := h.activeSessionKeys[conn]; ok {
			params.SessionKey = m[params.AgentID]
		}
		h.mu.RUnlock()
		if params.SessionKey == "" {
			params.SessionKey = "ws_default"
		}
	}

	h.mu.RLock()
	reg := h.runs
	metrics := h.metrics
	h.mu.RUnlock()
	if reg == nil {
		writeRPCError(conn, metrics, req.ID, -32000, "runs registry not configured")
		return
	}

	run := reg.GetBySession(runs.SessionScope{AgentID: params.AgentID, SessionKey: params.SessionKey})
	if run == nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Result:  map[string]any{"active": false},
			ID:      req.ID,
		})
		return
	}

	past, live, lastSeq, err := run.Subscribe(conn, params.FromSeq)
	if err != nil {
		writeRPCError(conn, metrics, req.ID, -32000, "subscribe: "+err.Error())
		return
	}

	pastJSON := make([]map[string]any, 0, len(past))
	for _, e := range past {
		pastJSON = append(pastJSON, eventToResult(e))
	}
	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result: map[string]any{
			"active":  true,
			"runId":   run.ID,
			"lastSeq": lastSeq,
			"past":    pastJSON,
		},
		ID: req.ID,
	})

	go forwardEvents(conn, live)
}

// forwardEvents drains live events to conn until the channel closes
// (Unsubscribe, Finish, or fan-out drop). Each event is written as a
// chat.event JSON-RPC notification using eventToResult so the wire
// shape matches the past-event payloads emitted in the subscribe
// response.
func forwardEvents(conn *websocket.Conn, ch <-chan runs.Event) {
	for e := range ch {
		writeJSON(conn, map[string]any{
			"jsonrpc": "2.0",
			"method":  "chat.event",
			"params":  eventToResult(e),
		})
	}
}

// Wire-format note: this codebase has two paths for sending event
// payloads to a WebSocket conn, intentionally asymmetric.
//
//   1. chat.send → wsSubscriber.OnEvent — writes each event as a
//      JSONRPCResponse with Result set, ID = the original chat.send
//      request ID. The existing felix HTML chat client treats multiple
//      Results sharing one rpcID as a stream; do NOT change this without
//      updating the client.
//
//   2. chat.subscribe → forwardEvents — writes each event as a JSON-RPC
//      notification (method = "chat.event", no ID). Newer clients that
//      attach to existing runs (post-disconnect, multi-tab) consume this
//      shape.
//
// Same underlying runs.Event; two different envelopes. The asymmetry
// exists for backward-compatibility with the chat.send-as-stream pattern
// the felix HTML client was designed around before durable-runs landed.

// wsSubscriber adapts chatexec.Subscriber → WebSocket JSON-RPC
// notifications on a single conn. OnEvent runs eventToResult on each
// event so live and (future) replay paths produce identical wire shapes.
type wsSubscriber struct {
	conn  *websocket.Conn
	rpcID any
}

// OnAttached writes the JSON-RPC response carrying {runID} so the
// client can track which run it's already receiving live events for.
// This is the response to the originating chat.send.
func (s *wsSubscriber) OnAttached(runID string) {
	writeJSON(s.conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"type": "run_attached", "runID": runID},
		ID:      s.rpcID,
	})
}

// OnEvent renders a chatexec run event as a JSON-RPC message on the
// conn. Uses the shared eventToResult transformer so the wire shape
// matches what (future) chat.replay will emit for the same on-disk event.
func (s *wsSubscriber) OnEvent(e runs.Event) {
	res := eventToResult(e)
	if res == nil {
		return
	}
	writeJSON(s.conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  res,
		ID:      s.rpcID,
	})
}

// eventToResult turns a runs.Event into the JSON shape the chat client
// renders. For non-terminal events the agent's original map (with
// "type", "text", etc.) is reconstructed by unmarshalling Payload —
// that payload was produced by chatexec.buildAgentEventResult at write
// time. The terminal EventTypeDone written by Run.Finish is rendered
// as a synthesized "run_terminal" map so clients can distinguish
// "agent said done" (with usage) from "run closed for reason X".
//
// Returns nil when the payload fails to decode; the caller skips the
// event in that case rather than emitting a broken notification.
func eventToResult(e runs.Event) map[string]any {
	if e.Type == runs.EventTypeDone {
		return map[string]any{
			"type":          "run_terminal",
			"status":        string(e.Status),
			"reason":        string(e.Reason),
			"superseded_by": e.SupersededBy,
			"error":         e.Error,
		}
	}
	if len(e.Payload) == 0 {
		return map[string]any{"type": string(e.Type)}
	}
	var m map[string]any
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		slog.Warn("eventToResult: unmarshal payload", "type", e.Type, "error", err)
		return nil
	}
	return m
}

// handleChatReplay reads events with seq > fromSeq from the on-disk
// log file for the given run. Works for both in-flight and finished
// runs. Does not attach a live subscription — use chat.subscribe for
// that flow.
func (h *WebSocketHandler) handleChatReplay(conn *websocket.Conn, req JSONRPCRequest) {
	var params struct {
		AgentID    string `json:"agentId"`
		SessionKey string `json:"sessionKey"`
		RunID      string `json:"runId"`
		FromSeq    int64  `json:"fromSeq"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(conn, h.metrics, req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}
	if params.SessionKey == "" {
		h.mu.RLock()
		if m, ok := h.activeSessionKeys[conn]; ok {
			params.SessionKey = m[params.AgentID]
		}
		h.mu.RUnlock()
		if params.SessionKey == "" {
			params.SessionKey = "ws_default"
		}
	}
	if params.RunID == "" {
		writeRPCError(conn, h.metrics, req.ID, -32602, "runId is required")
		return
	}

	h.mu.RLock()
	sessionsBase := h.sessionsBaseDir
	metrics := h.metrics
	h.mu.RUnlock()

	logPath := filepath.Join(sessionsBase, params.AgentID, params.SessionKey+".runs", params.RunID+".jsonl")
	past, err := runs.ReadLog(logPath, params.FromSeq)
	if err != nil {
		writeRPCError(conn, metrics, req.ID, -32000, "replay: "+err.Error())
		return
	}

	pastJSON := make([]map[string]any, 0, len(past))
	for _, e := range past {
		pastJSON = append(pastJSON, eventToResult(e))
	}
	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result: map[string]any{
			"runId": params.RunID,
			"past":  pastJSON,
		},
		ID: req.ID,
	})
}

// handleChatRuns returns the past run summaries for a session, sorted
// newest-first. Reads from the on-disk index.json via Registry.Snapshot.
// No live subscription is attached — frontends typically follow this
// with chat.replay to view a specific run.
func (h *WebSocketHandler) handleChatRuns(conn *websocket.Conn, req JSONRPCRequest) {
	var params struct {
		AgentID    string `json:"agentId"`
		SessionKey string `json:"sessionKey"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(conn, h.metrics, req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}
	if params.SessionKey == "" {
		h.mu.RLock()
		if m, ok := h.activeSessionKeys[conn]; ok {
			params.SessionKey = m[params.AgentID]
		}
		h.mu.RUnlock()
		if params.SessionKey == "" {
			params.SessionKey = "ws_default"
		}
	}

	h.mu.RLock()
	reg := h.runs
	metrics := h.metrics
	h.mu.RUnlock()
	if reg == nil {
		writeRPCError(conn, metrics, req.ID, -32000, "runs registry not configured")
		return
	}

	summaries, err := reg.Snapshot(runs.SessionScope{AgentID: params.AgentID, SessionKey: params.SessionKey})
	if err != nil {
		writeRPCError(conn, metrics, req.ID, -32000, "runs snapshot: "+err.Error())
		return
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].StartedAt > summaries[j].StartedAt
	})

	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"runs": summaries},
		ID:      req.ID,
	})
}

// handleChatDeleteRun removes a completed run from disk. Refuses to
// delete an in-flight run (the registry enforces this).
func (h *WebSocketHandler) handleChatDeleteRun(conn *websocket.Conn, req JSONRPCRequest) {
	var params struct {
		AgentID    string `json:"agentId"`
		SessionKey string `json:"sessionKey"`
		RunID      string `json:"runId"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(conn, h.metrics, req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	if params.RunID == "" {
		writeRPCError(conn, h.metrics, req.ID, -32602, "runId is required")
		return
	}
	if params.AgentID == "" {
		params.AgentID = "default"
	}
	if params.SessionKey == "" {
		h.mu.RLock()
		if m, ok := h.activeSessionKeys[conn]; ok {
			params.SessionKey = m[params.AgentID]
		}
		h.mu.RUnlock()
		if params.SessionKey == "" {
			params.SessionKey = "ws_default"
		}
	}

	h.mu.RLock()
	reg := h.runs
	metrics := h.metrics
	h.mu.RUnlock()
	if reg == nil {
		writeRPCError(conn, metrics, req.ID, -32000, "runs registry not configured")
		return
	}

	if err := reg.DeleteRun(runs.SessionScope{AgentID: params.AgentID, SessionKey: params.SessionKey}, params.RunID); err != nil {
		writeRPCError(conn, metrics, req.ID, -32000, err.Error())
		return
	}
	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"deleted": true},
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
			"id":             a.ID,
			"name":           a.Name,
			"model":          a.Model,
			"workspace":      a.Workspace,
			"context_window": tokens.ContextWindowFor(a.Model, a.ContextWindow),
			"default":        a.Default,
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
			"title":        readSessionMeta(h.sessionsBaseDir, params.AgentID, s.Key),
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

	// Session key is the user-supplied name (or the timestamp default)
	// directly — no "ws_" prefix as of the rename-sessions change. Keys
	// are filesystem path segments under <sessionsBase>/<agentID>/, so
	// they must satisfy validateSessionPathSegment (no separators, no
	// '.' or '..', non-empty). Earlier sessions named "ws_*" stay
	// reachable by their existing keys.
	sessionKey := params.Name
	if err := validateSessionPathSegment(sessionKey); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "name: " + err.Error()},
			ID:      req.ID,
		})
		return
	}
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

type sessionRenameParams struct {
	AgentID    string `json:"agentId"`
	SessionKey string `json:"sessionKey"`
	Title      string `json:"title"`
}

// handleSessionRename writes a title sidecar for the given session. The
// underlying JSONL is untouched; only <sessionsBase>/<agent>/<key>.meta.json
// is created or overwritten atomically. Title is validated (length cap,
// no control chars, no path separators) before any disk write.
func (h *WebSocketHandler) handleSessionRename(conn *websocket.Conn, req JSONRPCRequest) {
	var params sessionRenameParams
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
	if params.SessionKey == "" {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "sessionKey required"},
			ID:      req.ID,
		})
		return
	}
	if err := validateSessionPathSegment(params.AgentID); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "agentId: " + err.Error()},
			ID:      req.ID,
		})
		return
	}
	if err := validateSessionPathSegment(params.SessionKey); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "sessionKey: " + err.Error()},
			ID:      req.ID,
		})
		return
	}
	if !h.sessionStore.Exists(params.AgentID, params.SessionKey) {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": "Session not found: " + params.SessionKey},
			ID:      req.ID,
		})
		return
	}
	if err := validateSessionTitle(params.Title); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32602, "message": err.Error()},
			ID:      req.ID,
		})
		return
	}
	if err := writeSessionMeta(h.sessionsBaseDir, params.AgentID, params.SessionKey, params.Title); err != nil {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   map[string]any{"code": -32603, "message": "Save title error: " + err.Error()},
			ID:      req.ID,
		})
		return
	}
	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  map[string]any{"sessionKey": params.SessionKey, "title": params.Title},
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
	AgentID  string `json:"agentId"`
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

	if err := js.AddJob(params.AgentID, params.Name, params.Schedule, params.Prompt); err != nil {
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

// connWriteMutexes serialises writes to each *websocket.Conn.
// gorilla/websocket explicitly forbids concurrent writes to a single
// connection (Conn.NextWriter / Conn.WriteJSON panic with
// "concurrent write to websocket connection" if violated). The chat
// path is multi-goroutine by construction:
//
//   - the main agent-event drain loop writes EventTextDelta /
//     EventToolCallStart / EventToolResult / EventDone events
//   - the trace.SetOnMark callback fires from any goroutine inside
//     Runtime.Run (cortex recall, parallel tool dispatch, streaming
//     tools), each one calling writeJSON on the same conn
//   - chat.compact / chat.abort responses can land mid-stream
//
// Without serialisation, two of those goroutines can race into
// Conn.NextWriter and crash the whole gateway process. The map is
// keyed by conn pointer; entries are reaped on disconnect via
// releaseConnMutex so long-running gateways don't accumulate them.
var connWriteMutexes sync.Map // *websocket.Conn → *sync.Mutex

func writeJSON(conn *websocket.Conn, v any) {
	muAny, _ := connWriteMutexes.LoadOrStore(conn, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if err := conn.WriteJSON(v); err != nil {
		slog.Error("websocket write error", "error", err)
	}
}

// writeRPCError writes a JSON-RPC 2.0 error response for the given request ID.
// Mirrors the inline error-construction pattern used elsewhere in this file.
// metrics may be nil; when non-nil, IncErrors is bumped.
func writeRPCError(conn *websocket.Conn, metrics *Metrics, rpcID any, code int, message string) {
	if metrics != nil {
		metrics.IncErrors()
	}
	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Error:   map[string]any{"code": code, "message": message},
		ID:      rpcID,
	})
}

// releaseConnMutex drops the per-connection write mutex from the
// global map. Call from the disconnect path so the map size tracks
// active connections rather than total connections seen.
func releaseConnMutex(conn *websocket.Conn) {
	connWriteMutexes.Delete(conn)
}

// chatToolOverlay was the per-chat wrapper around tools.Registry that
// injected the "task" and "cron" tools. chatexec/overlay.go now owns
// that responsibility — see chatexec.RunTurn for the call site.
//
// safeRawMessage returns the input json.RawMessage when it is
// non-empty and parses as valid JSON, and `nil` (which marshals to
// `null`) otherwise. The encoding/json marshaler refuses to emit a
// json.RawMessage whose bytes don't form valid JSON, so without this
// guard a single bad ToolCall.Input bubbles up as
// `json: error calling MarshalJSON for type json.RawMessage:
// unexpected end of JSON input` and aborts the WebSocket write —
// breaking the chat client's view of the in-flight turn.
//
// Bad inputs typically come from upstream model glitches: an empty
// `tool_use` arguments stream that never produced a valid JSON
// object, or a "thought" tool call from a reasoning model where the
// arguments slot wasn't populated. Either way, surfacing `null` to
// the chat client is more useful than dropping the whole event.
func safeRawMessage(m json.RawMessage) any {
	if len(m) == 0 || !json.Valid(m) {
		return nil
	}
	return m
}
