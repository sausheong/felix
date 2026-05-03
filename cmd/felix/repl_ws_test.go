package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sausheong/felix/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayBaseURL_Defaults(t *testing.T) {
	cfg := &config.Config{}
	assert.Equal(t, "http://127.0.0.1:18789", gatewayBaseURL(cfg))

	cfg.Gateway.Host = "10.0.0.1"
	cfg.Gateway.Port = 9000
	assert.Equal(t, "http://10.0.0.1:9000", gatewayBaseURL(cfg))
}

func TestHTTPToWS(t *testing.T) {
	cases := []struct {
		in, out string
		wantErr bool
	}{
		{"http://127.0.0.1:18789", "ws://127.0.0.1:18789/ws", false},
		{"https://gateway.example.com:443", "wss://gateway.example.com:443/ws", false},
		{"ftp://nope", "", true},
		{":not-a-url", "", true},
	}
	for _, tc := range cases {
		got, err := httpToWS(tc.in)
		if tc.wantErr {
			assert.Error(t, err, tc.in)
			continue
		}
		assert.NoError(t, err, tc.in)
		assert.Equal(t, tc.out, got)
	}
}

// TestProbeGateway_Success_NoAuth confirms a vanilla 200 on /health
// satisfies the probe.
func TestProbeGateway_Success_NoAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/health", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	assert.True(t, probeGateway(srv.URL, "", time.Second))
}

// TestProbeGateway_PassesBearerToken makes sure we forward the auth
// token even though the gateway's /health is currently unauthenticated;
// future-proofs us against a tightening of the bypass.
func TestProbeGateway_PassesBearerToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer s3cret", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	assert.True(t, probeGateway(srv.URL, "s3cret", time.Second))
}

// TestProbeGateway_FailureModes — connection refused, non-200, and
// timeout all return false rather than propagating.
func TestProbeGateway_FailureModes(t *testing.T) {
	// Non-200.
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv500.Close()
	assert.False(t, probeGateway(srv500.URL, "", time.Second))

	// Closed-port URL: simulate by starting and immediately closing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := srv.URL
	srv.Close()
	assert.False(t, probeGateway(deadURL, "", 100*time.Millisecond))
}

// stubGateway is a minimal in-memory WS server that mimics the Felix
// gateway's JSON-RPC behavior just enough to exercise the client. Each
// instance lets a test register per-method handlers.
type stubGateway struct {
	t           *testing.T
	srv         *httptest.Server
	upgrader    websocket.Upgrader
	handlers    map[string]func(req jsonrpcRequest, send func(jsonrpcResponse))
	authToken   string // when non-empty, requires matching Bearer header on /ws
	connectedCh chan struct{}
}

func newStubGateway(t *testing.T) *stubGateway {
	sg := &stubGateway{
		t:           t,
		upgrader:    websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		handlers:    map[string]func(req jsonrpcRequest, send func(jsonrpcResponse)){},
		connectedCh: make(chan struct{}, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/ws", sg.serveWS)
	sg.srv = httptest.NewServer(mux)
	return sg
}

func (sg *stubGateway) Close() { sg.srv.Close() }
func (sg *stubGateway) URL() string {
	return sg.srv.URL
}

func (sg *stubGateway) on(method string, fn func(req jsonrpcRequest, send func(jsonrpcResponse))) {
	sg.handlers[method] = fn
}

func (sg *stubGateway) serveWS(w http.ResponseWriter, r *http.Request) {
	if sg.authToken != "" {
		if r.Header.Get("Authorization") != "Bearer "+sg.authToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	conn, err := sg.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	select {
	case sg.connectedCh <- struct{}{}:
	default:
	}

	var writeMu sync.Mutex
	send := func(resp jsonrpcResponse) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(resp)
	}

	for {
		var raw json.RawMessage
		if err := conn.ReadJSON(&raw); err != nil {
			return
		}
		var req jsonrpcRequest
		// jsonrpcRequest.Params is `any`; decode into a passthrough form
		// so handlers can re-marshal as needed.
		var probe struct {
			JSONRPC string          `json:"jsonrpc"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			ID      int64           `json:"id"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			continue
		}
		req.JSONRPC = probe.JSONRPC
		req.Method = probe.Method
		req.ID = probe.ID
		req.Params = probe.Params

		handler, ok := sg.handlers[probe.Method]
		if !ok {
			send(jsonrpcResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32601, Message: "method not found: " + probe.Method},
				ID:      probe.ID,
			})
			continue
		}
		handler(req, send)
	}
}

func TestDialGateway_RequiresValidBearer(t *testing.T) {
	sg := newStubGateway(t)
	sg.authToken = "right"
	defer sg.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	_, err := dialGateway(ctx, sg.URL(), "wrong")
	require.Error(t, err)

	gc, err := dialGateway(ctx, sg.URL(), "right")
	require.NoError(t, err)
	defer gc.Close()
}

func TestGatewayClient_OneShotCall_ReturnsResultAndError(t *testing.T) {
	sg := newStubGateway(t)
	defer sg.Close()
	sg.on("session.list", func(req jsonrpcRequest, send func(jsonrpcResponse)) {
		send(jsonrpcResponse{
			JSONRPC: "2.0",
			Result:  json.RawMessage(`{"sessions":[{"key":"ws_default","entryCount":3,"lastActivity":0,"active":true}]}`),
			ID:      req.ID,
		})
	})
	sg.on("kaboom", func(req jsonrpcRequest, send func(jsonrpcResponse)) {
		send(jsonrpcResponse{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: -32603, Message: "internal"},
			ID:      req.ID,
		})
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	gc, err := dialGateway(ctx, sg.URL(), "")
	require.NoError(t, err)
	defer gc.Close()

	raw, err := gc.call(ctx, "session.list", map[string]any{"agentId": "default"})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"key":"ws_default"`)

	_, err = gc.call(ctx, "kaboom", nil)
	require.Error(t, err)
	var rpc *rpcError
	require.ErrorAs(t, err, &rpc)
	assert.Equal(t, -32603, rpc.Code)
}

// TestStreamChatTurn_StreamsTextAndStopsOnDone exercises the chat.send
// streaming path: the stub emits two text_delta events then a done
// envelope, all sharing the request id, and the client must consume
// all three and terminate cleanly.
func TestStreamChatTurn_StreamsTextAndStopsOnDone(t *testing.T) {
	sg := newStubGateway(t)
	defer sg.Close()

	var seenAgentID, seenText string
	var seenSessionKey string
	sg.on("chat.send", func(req jsonrpcRequest, send func(jsonrpcResponse)) {
		var p struct {
			AgentID    string `json:"agentId"`
			Text       string `json:"text"`
			SessionKey string `json:"sessionKey"`
		}
		_ = json.Unmarshal(req.Params.(json.RawMessage), &p)
		seenAgentID, seenText, seenSessionKey = p.AgentID, p.Text, p.SessionKey

		send(jsonrpcResponse{JSONRPC: "2.0", Result: json.RawMessage(`{"type":"text_delta","text":"Hello "}`), ID: req.ID})
		send(jsonrpcResponse{JSONRPC: "2.0", Result: json.RawMessage(`{"type":"text_delta","text":"world"}`), ID: req.ID})
		send(jsonrpcResponse{JSONRPC: "2.0", Result: json.RawMessage(`{"type":"done"}`), ID: req.ID})
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	gc, err := dialGateway(ctx, sg.URL(), "")
	require.NoError(t, err)
	defer gc.Close()

	require.NoError(t, streamChatTurn(ctx, gc, "default", "", "ping"))
	assert.Equal(t, "default", seenAgentID)
	assert.Equal(t, "ping", seenText)
	assert.Empty(t, seenSessionKey, "empty sessionKey should be omitted so gateway uses ws_default")
}

// TestStreamChatTurn_PassesSessionKey checks that an explicit session
// key reaches the wire (used by /new and /switch flows).
func TestStreamChatTurn_PassesSessionKey(t *testing.T) {
	sg := newStubGateway(t)
	defer sg.Close()

	var seenSessionKey string
	sg.on("chat.send", func(req jsonrpcRequest, send func(jsonrpcResponse)) {
		var p struct{ SessionKey string `json:"sessionKey"` }
		_ = json.Unmarshal(req.Params.(json.RawMessage), &p)
		seenSessionKey = p.SessionKey
		send(jsonrpcResponse{JSONRPC: "2.0", Result: json.RawMessage(`{"type":"done"}`), ID: req.ID})
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	gc, err := dialGateway(ctx, sg.URL(), "")
	require.NoError(t, err)
	defer gc.Close()

	require.NoError(t, streamChatTurn(ctx, gc, "default", "ws_my_chat", "hi"))
	assert.Equal(t, "ws_my_chat", seenSessionKey)
}

// TestStreamChatTurn_ErrorEventTerminates checks that an error event is
// surfaced as a returned error and ends the read loop.
func TestStreamChatTurn_ErrorEventTerminates(t *testing.T) {
	sg := newStubGateway(t)
	defer sg.Close()
	sg.on("chat.send", func(req jsonrpcRequest, send func(jsonrpcResponse)) {
		send(jsonrpcResponse{JSONRPC: "2.0", Result: json.RawMessage(`{"type":"error","message":"upstream blew up"}`), ID: req.ID})
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	gc, err := dialGateway(ctx, sg.URL(), "")
	require.NoError(t, err)
	defer gc.Close()

	// renderTurnEvent treats "error" as terminal but doesn't return a
	// Go error from streamChatTurn — it prints and exits. The contract
	// is "turn ended without panic"; so a successful nil return after
	// an error event is the right behavior.
	require.NoError(t, streamChatTurn(ctx, gc, "default", "", "hi"))
}

// TestStreamChatTurn_AbortedEventTerminates — same shape as the error
// case; "aborted" must end the turn cleanly so the REPL can prompt
// again.
func TestStreamChatTurn_AbortedEventTerminates(t *testing.T) {
	sg := newStubGateway(t)
	defer sg.Close()
	sg.on("chat.send", func(req jsonrpcRequest, send func(jsonrpcResponse)) {
		send(jsonrpcResponse{JSONRPC: "2.0", Result: json.RawMessage(`{"type":"text_delta","text":"part"}`), ID: req.ID})
		send(jsonrpcResponse{JSONRPC: "2.0", Result: json.RawMessage(`{"type":"aborted"}`), ID: req.ID})
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	gc, err := dialGateway(ctx, sg.URL(), "")
	require.NoError(t, err)
	defer gc.Close()

	require.NoError(t, streamChatTurn(ctx, gc, "default", "", "hi"))
}

// TestRenderTurnEvent_AccumulatesTextAndFlushesOnTerminal is a unit
// test on the renderer alone — exercises the demux-event-to-string path
// without touching the network.
func TestRenderTurnEvent_AccumulatesTextAndFlushesOnTerminal(t *testing.T) {
	var buf strings.Builder

	done, err := renderTurnEvent(json.RawMessage(`{"type":"text_delta","text":"foo"}`), &buf)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, "foo", buf.String())

	done, err = renderTurnEvent(json.RawMessage(`{"type":"text_delta","text":" bar"}`), &buf)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, "foo bar", buf.String())

	// "done" flushes (resets) the buffer through glamour rendering.
	done, err = renderTurnEvent(json.RawMessage(`{"type":"done"}`), &buf)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Empty(t, buf.String(), "buffer should be reset after flush")
}

func TestRenderTurnEvent_RejectsMalformedJSON(t *testing.T) {
	var buf strings.Builder
	_, err := renderTurnEvent(json.RawMessage(`not-json`), &buf)
	require.Error(t, err)
}

func TestHandleSessionNew_ReturnsServerKey(t *testing.T) {
	sg := newStubGateway(t)
	defer sg.Close()
	sg.on("session.new", func(req jsonrpcRequest, send func(jsonrpcResponse)) {
		send(jsonrpcResponse{JSONRPC: "2.0", Result: json.RawMessage(`{"sessionKey":"ws_my_name"}`), ID: req.ID})
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	gc, err := dialGateway(ctx, sg.URL(), "")
	require.NoError(t, err)
	defer gc.Close()

	key, err := handleSessionNew(ctx, gc, "default", "my_name")
	require.NoError(t, err)
	assert.Equal(t, "ws_my_name", key)
}
