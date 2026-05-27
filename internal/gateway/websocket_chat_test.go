package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/gateway/runs"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/llm/llmtest"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/tools"
)

// testHandler builds a WebSocketHandler wired with: a tempdir for
// sessions, a scripted LLM provider, a real runs.Registry, an empty
// tool registry. Returns (handler, registry, sessionsDir).
//
// The variadic `scripted` is forwarded to llmtest.NewScriptedProvider —
// each string is one canned assistant response.
func testHandler(t *testing.T, scripted ...string) (*WebSocketHandler, *runs.Registry, string) {
	t.Helper()
	base := t.TempDir()
	sessionsDir := filepath.Join(base, "sessions")
	sessionStore := session.NewStore(sessionsDir)

	provider := llmtest.NewScriptedProvider(scripted...)
	providers := map[string]llm.LLMProvider{"local": provider}

	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        "default",
			Name:      "Test",
			Workspace: filepath.Join(base, "workspace-default"),
			Model:     "local/test-model",
			Sandbox:   "none",
		},
	}
	// Disable compaction so we don't need a provider entry for the
	// summarizer model (mirrors chatexec test setup).
	cfg.Agents.Defaults.Compaction.Enabled = false

	toolReg := tools.NewRegistry()
	h := NewWebSocketHandler(providers, toolReg, sessionStore, cfg, sessionsDir)

	reg := runs.NewRegistry(sessionsDir)
	h.SetRunsRegistry(reg)
	h.SetServerCtx(context.Background())
	// Metrics is required: chatexec.RunTurn nil-checks the MetricsLike
	// interface but h.metrics is a typed *Metrics — if nil, the
	// interface still satisfies !=nil and IncChatTurns panics.
	h.SetMetrics(NewMetrics())
	reg.OnNewRun = h.BroadcastNewRun

	return h, reg, sessionsDir
}

// wsPair returns a (clientConn, serverConn) websocket pair backed by a
// real httptest server. The handler-under-test is invoked with
// serverConn (writes flow to clientConn); the test reads from
// clientConn to assert what the handler wrote.
func wsPair(t *testing.T, h *WebSocketHandler) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	var serverConn *websocket.Conn
	var mu sync.Mutex
	ready := make(chan struct{})

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		mu.Lock()
		serverConn = c
		mu.Unlock()
		close(ready)
		// Hold the conn open until the request ctx ends (test cleanup).
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	<-ready
	mu.Lock()
	sc := serverConn
	mu.Unlock()
	t.Cleanup(func() { _ = sc.Close() })

	return clientConn, sc
}

// readJSON reads one JSON-RPC envelope from conn with a short timeout.
func readJSON(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal %q: %v", data, err)
	}
	return m
}

// makeReq builds a JSONRPCRequest with the given method, params, id.
func makeReq(t *testing.T, method string, params any, id any) JSONRPCRequest {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return JSONRPCRequest{JSONRPC: "2.0", Method: method, Params: raw, ID: id}
}

func TestHandleChatAbort_NoActiveRun(t *testing.T) {
	h, _, _ := testHandler(t)
	clientConn, serverConn := wsPair(t, h)

	h.handleChatAbort(serverConn, makeReq(t, "chat.abort",
		map[string]string{"agentId": "default", "sessionKey": "ws_default"}, "1"))

	resp := readJSON(t, clientConn)
	result, _ := resp["result"].(map[string]any)
	if got, _ := result["aborted"].(bool); got {
		t.Errorf("aborted=true, want false (no active run)")
	}
}

func TestHandleChatAbort_FallbackResolvesViaActiveSessionKeys(t *testing.T) {
	h, reg, _ := testHandler(t)
	clientConn, serverConn := wsPair(t, h)

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, err := reg.Create(runs.SessionScope{AgentID: "Other", SessionKey: "ws_default"}, "run-1", cancel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer run.Finish(runs.StatusCompleted, "", "")

	h.mu.Lock()
	if h.activeSessionKeys[serverConn] == nil {
		h.activeSessionKeys[serverConn] = map[string]string{}
	}
	h.activeSessionKeys[serverConn]["Other"] = "ws_default"
	h.mu.Unlock()

	h.handleChatAbort(serverConn, makeReq(t, "chat.abort", map[string]string{}, "abort"))

	resp := readJSON(t, clientConn)
	result, _ := resp["result"].(map[string]any)
	if got, _ := result["aborted"].(bool); !got {
		t.Errorf("aborted=false, want true (fallback should find the Other-agent run); resp=%v", resp)
	}
}

func TestHandleChatSubscribe_NoActiveRun(t *testing.T) {
	h, _, _ := testHandler(t)
	clientConn, serverConn := wsPair(t, h)

	h.handleChatSubscribe(serverConn, makeReq(t, "chat.subscribe",
		map[string]any{"agentId": "default", "sessionKey": "ws_default", "fromSeq": 0}, "sub"))

	resp := readJSON(t, clientConn)
	result, _ := resp["result"].(map[string]any)
	if active, _ := result["active"].(bool); active {
		t.Errorf("active=true, want false (no in-flight run); resp=%v", resp)
	}
}

func TestHandleChatReplay_ReturnsPastEvents(t *testing.T) {
	h, reg, _ := testHandler(t)
	clientConn, serverConn := wsPair(t, h)
	scope := runs.SessionScope{AgentID: "default", SessionKey: "ws_default"}

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, _ := reg.Create(scope, "replay-me", cancel)
	_, _ = run.Append(runs.EventTypeTextDelta, []byte(`{"text":"hello"}`))
	_ = run.Finish(runs.StatusCompleted, "", "")

	h.handleChatReplay(serverConn, makeReq(t, "chat.replay",
		map[string]any{"agentId": "default", "sessionKey": "ws_default", "runId": "replay-me", "fromSeq": 0}, "rp"))

	resp := readJSON(t, clientConn)
	result, _ := resp["result"].(map[string]any)
	past, ok := result["past"].([]any)
	if !ok {
		t.Fatalf("past not an array: %v", resp)
	}
	if len(past) < 1 {
		t.Errorf("want at least 1 past event, got %d", len(past))
	}
}

func TestHandleChatReplay_UnknownRunReturnsEmpty(t *testing.T) {
	h, _, _ := testHandler(t)
	clientConn, serverConn := wsPair(t, h)

	h.handleChatReplay(serverConn, makeReq(t, "chat.replay",
		map[string]any{"agentId": "default", "sessionKey": "ws_default", "runId": "no-such-run", "fromSeq": 0}, "rp"))

	resp := readJSON(t, clientConn)
	if _, isErr := resp["error"]; isErr {
		t.Errorf("expected no error for unknown runId, got: %v", resp)
	}
	result, _ := resp["result"].(map[string]any)
	past, _ := result["past"].([]any)
	if len(past) != 0 {
		t.Errorf("want empty past for unknown run, got %d events", len(past))
	}
}

func TestHandleChatRuns_ListsNewestFirst(t *testing.T) {
	h, reg, _ := testHandler(t)
	clientConn, serverConn := wsPair(t, h)
	scope := runs.SessionScope{AgentID: "default", SessionKey: "ws_default"}

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []string{"r-old", "r-mid", "r-new"} {
		run, err := reg.Create(scope, id, cancel)
		if err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
		_ = run.Finish(runs.StatusCompleted, "", "")
		// Finish persists the terminal index entry but does NOT clear
		// bySession/runs maps — Remove does. Without this, the next
		// Create on the same scope fails with "already in flight".
		reg.Remove(id)
		// Sleep so the next run's StartedAt is strictly later — the
		// handler sorts by StartedAt string descending, ties would
		// scramble the assertion.
		time.Sleep(5 * time.Millisecond)
	}

	h.handleChatRuns(serverConn, makeReq(t, "chat.runs",
		map[string]string{"agentId": "default", "sessionKey": "ws_default"}, "lr"))

	resp := readJSON(t, clientConn)
	result, _ := resp["result"].(map[string]any)
	arr, ok := result["runs"].([]any)
	if !ok {
		t.Fatalf("runs not an array: %v", resp)
	}
	if len(arr) != 3 {
		t.Fatalf("want 3 runs, got %d", len(arr))
	}
	firstID, _ := arr[0].(map[string]any)["id"].(string)
	if firstID != "r-new" {
		t.Errorf("first run id=%q, want r-new (newest first sort)", firstID)
	}
}

func TestHandleChatDeleteRun_HappyPath(t *testing.T) {
	h, reg, _ := testHandler(t)
	clientConn, serverConn := wsPair(t, h)
	scope := runs.SessionScope{AgentID: "default", SessionKey: "ws_default"}

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, _ := reg.Create(scope, "delme", cancel)
	_ = run.Finish(runs.StatusCompleted, "", "")
	// Remove from in-memory maps so DeleteRun (which refuses in-flight
	// runs) sees this as a completed/cleared run that can be wiped.
	reg.Remove("delme")

	h.handleChatDeleteRun(serverConn, makeReq(t, "chat.deleteRun",
		map[string]string{"agentId": "default", "sessionKey": "ws_default", "runId": "delme"}, "dr"))

	resp := readJSON(t, clientConn)
	result, _ := resp["result"].(map[string]any)
	if del, _ := result["deleted"].(bool); !del {
		t.Errorf("deleted=false, want true; resp=%v", resp)
	}

	snap, _ := reg.Snapshot(scope)
	if len(snap) != 0 {
		t.Errorf("snapshot still has %d run(s) after delete", len(snap))
	}
}

func TestHandleChatDeleteRun_RequiresRunID(t *testing.T) {
	h, _, _ := testHandler(t)
	clientConn, serverConn := wsPair(t, h)

	h.handleChatDeleteRun(serverConn, makeReq(t, "chat.deleteRun",
		map[string]string{"agentId": "default", "sessionKey": "ws_default"}, "dr"))

	resp := readJSON(t, clientConn)
	e, _ := resp["error"].(map[string]any)
	if e == nil {
		t.Fatalf("want error response, got %v", resp)
	}
	msg, _ := e["message"].(string)
	if !strings.Contains(msg, "runId") {
		t.Errorf("error message should mention runId, got: %s", msg)
	}
}

func TestHandleChatSend_RunAttachedThenDone(t *testing.T) {
	h, _, _ := testHandler(t, "ok")
	clientConn, serverConn := wsPair(t, h)

	h.handleChatSend(serverConn, makeReq(t, "chat.send",
		map[string]string{"agentId": "default", "sessionKey": "ws_default", "text": "hello"},
		1))

	resp := readJSON(t, clientConn)
	if got, _ := resp["id"].(float64); int(got) != 1 {
		t.Errorf("first response id=%v, want 1", resp["id"])
	}
	result, _ := resp["result"].(map[string]any)
	if typ, _ := result["type"].(string); typ != "run_attached" {
		t.Errorf("first response type=%q, want 'run_attached'; resp=%v", typ, resp)
	}
	if runID, _ := result["runID"].(string); runID == "" {
		t.Errorf("first response missing runID; resp=%v", resp)
	}

	// Drain until we see the terminal event. eventToResult renders the
	// Finish-written EventTypeDone as type="run_terminal", so accept
	// either "done" (legacy agent EventDone passthrough) or
	// "run_terminal" as the end marker.
	for i := 0; i < 60; i++ {
		msg := readJSON(t, clientConn)
		r, _ := msg["result"].(map[string]any)
		typ, _ := r["type"].(string)
		if typ == "done" || typ == "run_terminal" {
			// Brief settle wait: chatexec.RunTurn's deferred cleanup
			// (compactionMgr.ForgetSession, runCancel, session index
			// saveIndex) runs AFTER the done event is fanned out to
			// subscribers but BEFORE the RunTurn goroutine returns.
			// Without this, the deferred writes race with t.TempDir()'s
			// os.RemoveAll, producing "directory not empty" errors.
			time.Sleep(100 * time.Millisecond)
			return
		}
	}
	t.Fatal("did not see done/run_terminal event within 60 frames")
}

func TestChatHandlers_RejectNilRegistry(t *testing.T) {
	h, _, _ := testHandler(t)
	clientConn, serverConn := wsPair(t, h)

	h.mu.Lock()
	h.runs = nil
	h.mu.Unlock()

	handlers := []struct {
		name string
		fn   func(*websocket.Conn, JSONRPCRequest)
		req  JSONRPCRequest
	}{
		{"chat.abort", h.handleChatAbort, makeReq(t, "chat.abort", map[string]string{}, "a")},
		{"chat.subscribe", h.handleChatSubscribe, makeReq(t, "chat.subscribe", map[string]any{"fromSeq": 0}, "b")},
		{"chat.runs", h.handleChatRuns, makeReq(t, "chat.runs", map[string]string{}, "c")},
		{"chat.deleteRun", h.handleChatDeleteRun, makeReq(t, "chat.deleteRun", map[string]string{"runId": "x"}, "d")},
	}
	for _, tc := range handlers {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(serverConn, tc.req)
			resp := readJSON(t, clientConn)
			if _, isErr := resp["error"]; !isErr {
				t.Errorf("%s with nil registry should error, got: %v", tc.name, resp)
			}
		})
	}
}
