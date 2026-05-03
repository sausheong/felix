// Gateway-mode REPL: when `felix chat` detects that a Felix gateway is
// already running, this file's runChatViaGateway takes over instead of
// the in-process Runtime path in main.go's runChat.
//
// We talk to the gateway over its existing JSON-RPC 2.0 WebSocket
// (`/ws`) using the same chat.send / chat.abort / chat.compact and
// session.* methods the web chat uses. State (sessions, memory, cortex,
// MCP connections, bundled Ollama) lives entirely in the gateway
// process, so two CLI sessions and a browser tab can talk to the same
// agent without stepping on each other.
//
// The REPL output (tool-call rendering, glamour markdown for assistant
// text, color codes for errors and compaction notices) is intentionally
// identical to the in-process REPL in main.go so users can switch
// between the two modes without surprise.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/gorilla/websocket"
	"github.com/sausheong/felix/internal/config"
)

// gatewayBaseURL returns the http://host:port URL for the configured
// gateway. Defaults to 127.0.0.1:18789 when host/port are zero, matching
// the gateway's own startup defaults in config.go.
func gatewayBaseURL(cfg *config.Config) string {
	host := cfg.Gateway.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Gateway.Port
	if port == 0 {
		port = 18789
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

// probeGateway returns true if a Felix gateway is responding to GET /health
// at the given base URL within the timeout. Network errors and non-200
// responses both return false; the probe is best-effort and we always
// have the in-process fallback.
//
// Callers that have an auth token configured pass it via authToken so
// gateways with bearer auth enabled don't appear "down" to us — /health
// itself is unauthenticated, but using the token here is harmless and
// future-proofs against a tightening of the bypass.
func probeGateway(baseURL, authToken string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		return false
	}
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// jsonrpcRequest is the outbound envelope. Method names match the
// gateway's WebSocket dispatch table in internal/gateway/websocket.go.
type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
	ID      int64  `json:"id"`
}

// jsonrpcResponse is the inbound envelope. The gateway emits multiple
// responses for chat.send (one per agent event, all sharing the request's
// id); single responses for everything else.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	ID      int64           `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// gatewayClient owns the WebSocket connection and demultiplexes inbound
// responses by JSON-RPC id. Each in-flight request registers a channel
// before sending and drains it until it sees the terminal event (the
// only response for one-shot calls; the "done"/"error"/"aborted" event
// for chat.send streams).
type gatewayClient struct {
	conn *websocket.Conn

	idCounter atomic.Int64

	mu       sync.Mutex
	pending  map[int64]chan jsonrpcResponse
	closed   bool
	closeErr error

	// writeMu serializes WriteJSON; gorilla/websocket forbids concurrent
	// writes on the same connection.
	writeMu sync.Mutex
}

func dialGateway(ctx context.Context, baseURL, authToken string) (*gatewayClient, error) {
	wsURL, err := httpToWS(baseURL)
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	if authToken != "" {
		headers.Set("Authorization", "Bearer "+authToken)
	}

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 5 * time.Second
	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", wsURL, err)
	}

	gc := &gatewayClient{
		conn:    conn,
		pending: make(map[int64]chan jsonrpcResponse),
	}
	go gc.readLoop()
	return gc, nil
}

// httpToWS rewrites an http(s):// base URL into a ws(s):// URL with the
// `/ws` path appended. The gateway accepts the same Authorization header
// at /ws as the rest of the API, so no extra param munging is needed.
func httpToWS(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported gateway scheme %q", u.Scheme)
	}
	u.Path = "/ws"
	return u.String(), nil
}

func (gc *gatewayClient) readLoop() {
	for {
		_, raw, err := gc.conn.ReadMessage()
		if err != nil {
			gc.shutdown(err)
			return
		}
		var resp jsonrpcResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			// Skip malformed frames rather than killing the loop —
			// this is what the web chat does too, and it keeps the
			// connection usable if a future server adds an
			// out-of-protocol notification we don't recognise.
			continue
		}
		gc.mu.Lock()
		ch, ok := gc.pending[resp.ID]
		gc.mu.Unlock()
		if !ok {
			// No registered listener — either the call already
			// terminated (race between shutdown/cleanup and a
			// trailing event) or the server is replying to an id we
			// didn't issue. Drop silently.
			continue
		}
		select {
		case ch <- resp:
		default:
			// Channel buffered enough that this should never block;
			// if it does, drop the event rather than stall the
			// reader. The streaming path uses a generous buffer.
		}
	}
}

func (gc *gatewayClient) shutdown(err error) {
	gc.mu.Lock()
	if gc.closed {
		gc.mu.Unlock()
		return
	}
	gc.closed = true
	gc.closeErr = err
	pending := gc.pending
	gc.pending = nil
	gc.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
}

// Close terminates the connection. Safe to call multiple times.
func (gc *gatewayClient) Close() error {
	gc.shutdown(nil)
	return gc.conn.Close()
}

// register reserves an id and inbound channel for a soon-to-be-sent
// request. The buffer is sized to hold a typical chat.send burst
// without blocking the read loop.
func (gc *gatewayClient) register() (int64, chan jsonrpcResponse, error) {
	id := gc.idCounter.Add(1)
	ch := make(chan jsonrpcResponse, 64)
	gc.mu.Lock()
	if gc.closed {
		gc.mu.Unlock()
		return 0, nil, errors.New("gateway connection closed")
	}
	gc.pending[id] = ch
	gc.mu.Unlock()
	return id, ch, nil
}

// release deregisters an id; pending events for it are dropped.
func (gc *gatewayClient) release(id int64) {
	gc.mu.Lock()
	if !gc.closed {
		delete(gc.pending, id)
	}
	gc.mu.Unlock()
}

// send writes a JSON-RPC request to the connection. Concurrent senders
// are serialized through writeMu (gorilla/websocket doesn't allow
// concurrent writes on a single conn).
func (gc *gatewayClient) send(req jsonrpcRequest) error {
	gc.writeMu.Lock()
	defer gc.writeMu.Unlock()
	return gc.conn.WriteJSON(req)
}

// call performs a one-shot JSON-RPC request and waits for the single
// matching response. Returns the result payload or any envelope error.
func (gc *gatewayClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, ch, err := gc.register()
	if err != nil {
		return nil, err
	}
	defer gc.release(id)

	if err := gc.send(jsonrpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: id}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, errors.New("gateway connection closed before response")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// runChatViaGateway is the gateway-mode entry point invoked by runChat
// when the /health probe succeeds. It runs the same REPL prompt as the
// in-process path but routes every chat turn and slash command through
// the gateway's WebSocket API.
//
// Session key: we deliberately don't pass a sessionKey on chat.send so
// the gateway falls back to "ws_default" — the same key the web chat
// uses. That makes "talk to it from a terminal" continue the
// conversation you have open in the browser, which is the whole point
// of unifying state on the gateway.
func runChatViaGateway(ctx context.Context, agentID, modelStr, baseURL, authToken string) error {
	gc, err := dialGateway(ctx, baseURL, authToken)
	if err != nil {
		return err
	}
	defer gc.Close()

	fmt.Printf("Felix chat — agent %q via gateway %s (model: %s)\n", agentID, baseURL, modelStr)
	fmt.Println("Connected to running gateway; sessions are shared with the web chat at /chat.")
	fmt.Println("Type /quit to exit, /sessions to list sessions, /new to create a new session.")
	fmt.Println()

	// Active session key for this REPL — empty means "let the gateway
	// pick its default", which is "ws_default". /new and /switch update
	// this so subsequent chat.send calls target the right session.
	currentSessionKey := ""

	for {
		fmt.Print("> ")
		input, err := readLine(os.Stdin)
		if err != nil {
			return nil // EOF / Ctrl+D
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		switch {
		case input == "/quit", input == "/exit":
			fmt.Println("Goodbye!")
			return nil

		case input == "/sessions":
			handleSessionsList(ctx, gc, agentID, currentSessionKey)
			continue

		case strings.HasPrefix(input, "/new"):
			name := strings.TrimSpace(strings.TrimPrefix(input, "/new"))
			newKey, err := handleSessionNew(ctx, gc, agentID, name)
			if err != nil {
				fmt.Printf("\033[31mError creating session: %v\033[0m\n", err)
				continue
			}
			currentSessionKey = newKey
			fmt.Printf("Switched to new session %q\n", newKey)
			continue

		case strings.HasPrefix(input, "/switch "):
			name := strings.TrimSpace(strings.TrimPrefix(input, "/switch "))
			if name == "" {
				fmt.Println("Usage: /switch <session-key>")
				continue
			}
			if err := handleSessionSwitch(ctx, gc, agentID, name); err != nil {
				fmt.Printf("\033[31mError switching session: %v\033[0m\n", err)
				continue
			}
			currentSessionKey = name
			fmt.Printf("Switched to session %q\n", name)
			continue

		case strings.HasPrefix(input, "/rename "):
			fmt.Println("\033[33m/rename is not available in gateway mode (no session.rename WS method). Stop the gateway and rerun for in-process mode.\033[0m")
			continue

		case strings.HasPrefix(input, "/delete "):
			fmt.Println("\033[33m/delete is not available in gateway mode (no session.delete WS method). Stop the gateway and rerun for in-process mode.\033[0m")
			continue

		case strings.HasPrefix(input, "/compact"):
			instructions := strings.TrimSpace(strings.TrimPrefix(input, "/compact"))
			handleCompact(ctx, gc, agentID, currentSessionKey, instructions)
			continue

		case strings.HasPrefix(input, "/screenshot"):
			fmt.Println("\033[33m/screenshot is not available in gateway mode (gateway WebSocket protocol doesn't accept inbound user images yet). Restart with --no-gateway for in-process mode.\033[0m")
			continue
		}

		// Image-path detection: warn and strip rather than silently dropping.
		text, images := extractImagesFromInput(input)
		if len(images) > 0 {
			fmt.Printf("\033[33m[ignored %d image attachment(s)] gateway mode doesn't accept inbound images yet; sending text only.\033[0m\n", len(images))
		}

		if err := streamChatTurn(ctx, gc, agentID, currentSessionKey, text); err != nil {
			fmt.Printf("\033[31mError: %v\033[0m\n", err)
		}
	}
}

// readLine reads a single line from stdin without depending on bufio
// buffering across calls (so Ctrl+D / EOF on the previous turn doesn't
// strand bytes). Mirrors the byte-at-a-time reader the in-process REPL
// uses in main.go.
func readLine(stdin *os.File) (string, error) {
	var out []byte
	buf := make([]byte, 1)
	for {
		n, err := stdin.Read(buf)
		if err != nil || n == 0 {
			if len(out) > 0 {
				return string(out), nil
			}
			return "", err
		}
		if buf[0] == '\n' {
			return string(out), nil
		}
		out = append(out, buf[0])
	}
}

// streamChatTurn sends a chat.send and consumes the streaming response
// events until the terminal "done"/"error"/"aborted" event. Ctrl+C
// while a turn is in flight sends a chat.abort using a separate id,
// which causes the gateway to return an "aborted" event on this id.
func streamChatTurn(ctx context.Context, gc *gatewayClient, agentID, sessionKey, text string) error {
	id, ch, err := gc.register()
	if err != nil {
		return err
	}
	defer gc.release(id)

	params := map[string]any{"agentId": agentID, "text": text}
	if sessionKey != "" {
		params["sessionKey"] = sessionKey
	}
	if err := gc.send(jsonrpcRequest{JSONRPC: "2.0", Method: "chat.send", Params: params, ID: id}); err != nil {
		return err
	}

	// Per-turn cancellation: Ctrl+C aborts the in-flight turn but keeps
	// the REPL alive. We don't reuse the parent ctx because that's the
	// REPL's lifetime context — cancelling it would also tear down the
	// connection.
	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	go func() {
		select {
		case <-sigCh:
			// Best-effort abort. Errors are non-fatal — the gateway
			// will eventually emit an event for the original turn,
			// and worst case the user sees a stale completion.
			_, _ = gc.call(context.Background(), "chat.abort", nil)
			turnCancel()
		case <-turnCtx.Done():
		}
	}()

	var responseText strings.Builder
	for {
		select {
		case <-turnCtx.Done():
			return turnCtx.Err()
		case resp, ok := <-ch:
			if !ok {
				return errors.New("gateway connection closed mid-turn")
			}
			if resp.Error != nil {
				return resp.Error
			}
			done, err := renderTurnEvent(resp.Result, &responseText)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
	}
}

// renderTurnEvent prints one chat.send streaming event in the same
// format as the in-process REPL. Returns done=true on the terminal
// event so the caller knows to stop reading.
func renderTurnEvent(raw json.RawMessage, responseText *strings.Builder) (bool, error) {
	var ev struct {
		Type           string          `json:"type"`
		Text           string          `json:"text"`
		Tool           string          `json:"tool"`
		Input          json.RawMessage `json:"input"`
		Output         string          `json:"output"`
		Error          string          `json:"error"`
		Message        string          `json:"message"`
		Reason         string          `json:"reason"`
		Skipped        string          `json:"skipped"`
		TurnsCompacted int             `json:"turnsCompacted"`
		DurationMs     int             `json:"durationMs"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return false, fmt.Errorf("parse event: %w", err)
	}

	switch ev.Type {
	case "text_delta":
		responseText.WriteString(ev.Text)
	case "tool_call_start":
		fmt.Printf("\n\033[36m[tool: %s]\033[0m\n", ev.Tool)
	case "tool_result":
		header := formatToolCallHeader(ev.Tool, ev.Input)
		if header != "" {
			fmt.Printf("\033[90m  %s\033[0m\n", header)
		}
		if ev.Error != "" {
			fmt.Printf("\033[31m  error: %s\033[0m\n", ev.Error)
		} else if out := formatToolOutput(ev.Output); out != "" {
			fmt.Printf("\033[90m  %s\033[0m\n", strings.ReplaceAll(out, "\n", "\n  "))
		}
	case "compaction.start":
		fmt.Print("\033[90m🧹 Compacting…\033[0m\n")
	case "compaction.done":
		fmt.Printf("\033[90m🧹 Compacted %d turns in %dms\033[0m\n", ev.TurnsCompacted, ev.DurationMs)
	case "compaction.skipped":
		// Match in-process behavior: only surface reactive skips —
		// preventive ones (too_short, etc.) are routine and noisy.
		if ev.Reason == "reactive" {
			fmt.Printf("\033[33m⚠ Compaction skipped during reactive retry: %s\033[0m\n", ev.Skipped)
		}
	case "error":
		fmt.Printf("\n\033[31mError: %s\033[0m\n", ev.Message)
		// "error" is terminal for a turn.
		flushMarkdown(responseText)
		return true, nil
	case "aborted":
		fmt.Printf("\n\033[33m[aborted]\033[0m\n")
		flushMarkdown(responseText)
		return true, nil
	case "done":
		flushMarkdown(responseText)
		return true, nil
	}
	return false, nil
}

func flushMarkdown(buf *strings.Builder) {
	if buf.Len() == 0 {
		return
	}
	rendered, err := glamour.Render(buf.String(), "dark")
	if err != nil {
		fmt.Print(buf.String())
	} else {
		fmt.Print(rendered)
	}
	buf.Reset()
}

// handleSessionsList prints the same table the in-process /sessions does.
func handleSessionsList(ctx context.Context, gc *gatewayClient, agentID, currentSessionKey string) {
	raw, err := gc.call(ctx, "session.list", map[string]any{"agentId": agentID})
	if err != nil {
		fmt.Printf("\033[31mError listing sessions: %v\033[0m\n", err)
		return
	}
	var payload struct {
		Sessions []struct {
			Key          string `json:"key"`
			EntryCount   int    `json:"entryCount"`
			LastActivity int64  `json:"lastActivity"`
			Active       bool   `json:"active"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		fmt.Printf("\033[31mMalformed session.list response: %v\033[0m\n", err)
		return
	}
	if len(payload.Sessions) == 0 {
		fmt.Println("No sessions found.")
		return
	}
	fmt.Println("Sessions:")
	for _, s := range payload.Sessions {
		marker := "  "
		// Honor either the gateway's per-connection "active" flag OR
		// our locally-tracked currentSessionKey — they may diverge if
		// the user just /switch'd and the gateway hasn't echoed yet.
		if s.Active || s.Key == currentSessionKey {
			marker = "* "
		}
		lastAct := "-"
		if s.LastActivity > 0 {
			lastAct = time.Unix(s.LastActivity, 0).Format("2006-01-02 15:04")
		}
		fmt.Printf("  %s%-20s  %d entries  %s\n", marker, s.Key, s.EntryCount, lastAct)
	}
}

func handleSessionNew(ctx context.Context, gc *gatewayClient, agentID, name string) (string, error) {
	params := map[string]any{"agentId": agentID}
	if name != "" {
		params["name"] = name
	}
	raw, err := gc.call(ctx, "session.new", params)
	if err != nil {
		return "", err
	}
	var payload struct {
		SessionKey string `json:"sessionKey"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	return payload.SessionKey, nil
}

func handleSessionSwitch(ctx context.Context, gc *gatewayClient, agentID, sessionKey string) error {
	_, err := gc.call(ctx, "session.switch", map[string]any{
		"agentId":    agentID,
		"sessionKey": sessionKey,
	})
	return err
}

func handleCompact(ctx context.Context, gc *gatewayClient, agentID, sessionKey, instructions string) {
	params := map[string]any{"agentId": agentID}
	if sessionKey != "" {
		params["sessionKey"] = sessionKey
	}
	if instructions != "" {
		params["instructions"] = instructions
	}
	fmt.Println("\033[90m🧹 Compacting…\033[0m")
	raw, err := gc.call(ctx, "chat.compact", params)
	if err != nil {
		fmt.Printf("\033[31mCompaction failed: %v\033[0m\n", err)
		return
	}
	var payload struct {
		Compacted      bool   `json:"compacted"`
		Skipped        string `json:"skipped"`
		TurnsCompacted int    `json:"turnsCompacted"`
		DurationMs     int    `json:"durationMs"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		fmt.Printf("\033[31mMalformed chat.compact response: %v\033[0m\n", err)
		return
	}
	if !payload.Compacted {
		switch payload.Skipped {
		case "too_short":
			fmt.Println("\033[90mSession too short to compact.\033[0m")
		case "ollama_down", "summarizer_error":
			fmt.Println("\033[33mCompaction skipped: bundled Ollama not reachable. Start it in Settings → Models.\033[0m")
		case "empty_summary":
			fmt.Println("\033[33mCompaction skipped: model returned no summary.\033[0m")
		case "timeout":
			fmt.Println("\033[33mCompaction skipped: timed out.\033[0m")
		case "cancelled":
			fmt.Println("\033[33mCompaction cancelled.\033[0m")
		default:
			if payload.Skipped != "" {
				fmt.Printf("\033[33mCompaction skipped: %s\033[0m\n", payload.Skipped)
			}
		}
		return
	}
	fmt.Printf("\033[90m🧹 Compacted %d turns in %dms\033[0m\n", payload.TurnsCompacted, payload.DurationMs)
}
