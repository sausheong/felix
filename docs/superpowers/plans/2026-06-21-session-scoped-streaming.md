# Session-scoped streaming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop a chat turn's streamed output from rendering into the wrong session when the user switches sessions mid-generation; let in-flight turns keep running in the background with a sidebar indicator, and resume their live view on switch-back.

**Architecture:** Stamp `agentId`/`sessionKey` onto every streamed event on the server (both the chat.send response envelope and the chat.subscribe notification envelope). On the client, hold per-scope render state keyed by `runsKey(agent, session)` and render an event into the DOM only when its scope matches the displayed session; switch-back re-attaches via `chat.subscribe`; a sidebar dot marks sessions with a live run.

**Tech Stack:** Go 1.25 (`internal/gateway`), the embedded HTML/JS chat client in `internal/gateway/chat.go`, existing `runs` registry + `chat.subscribe`/`forwardEvents` subsystem. Tests use the `websocket_chat_test.go` ws-pair + scripted-provider harness.

**Spec:** `docs/superpowers/specs/2026-06-21-session-scoped-streaming-design.md`

---

## File Structure

- **Modify:** `internal/gateway/websocket.go` — add `scope` to `wsSubscriber`; stamp scope in `OnEvent`, `OnAttached`, `forwardEvents`, the `chat.subscribe` response, and the trace-mark forwarder. (Server, Go-testable.)
- **Modify:** `internal/gateway/websocket_chat_test.go` — assert streamed frames carry scope; assert concurrent-scope isolation.
- **Modify:** `internal/gateway/chat.go` — per-scope render state + scoped routing in the `onmessage` dispatcher; `chat.event` notification branch; `chat.subscribe` re-attach on switch-back; sidebar running badge; per-scope composer lock. (Client JS inside a Go string; not unit-tested — manual verification.)

The server tasks (1) come first because they are independently testable and the client depends on the scope fields existing on the wire.

---

### Task 1: Server — stamp scope on every streamed event

**Files:**
- Modify: `internal/gateway/websocket.go` (`wsSubscriber` struct ~706; `OnAttached` ~714; `OnEvent` ~725; `handleChatSend` subscriber construction ~460; `handleChatSubscribe` response ~656 + `forwardEvents` ~675; `makeTraceMarkForwarder` ~491)
- Test: `internal/gateway/websocket_chat_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/gateway/websocket_chat_test.go`:

```go
// Every streamed frame from a chat.send must carry the originating scope so
// the client can route it to the right session and never spill over.
func TestHandleChatSend_FramesCarryScope(t *testing.T) {
	h, _, _ := testHandler(t, "hello world")
	clientConn, serverConn := wsPair(t, h)

	h.handleChatSend(serverConn, makeReq(t, "chat.send",
		map[string]string{"agentId": "default", "sessionKey": "ws_alpha", "text": "hi"},
		1))

	// Drain frames until the terminal one; assert each result frame that has
	// a "type" also carries agentId/sessionKey == the send scope.
	sawScoped := false
	for i := 0; i < 60; i++ {
		msg := readJSON(t, clientConn)
		r, _ := msg["result"].(map[string]any)
		if r == nil {
			continue
		}
		typ, _ := r["type"].(string)
		if typ == "" {
			continue
		}
		aid, _ := r["agentId"].(string)
		sk, _ := r["sessionKey"].(string)
		if aid != "default" || sk != "ws_alpha" {
			t.Fatalf("frame type=%q has scope (%q,%q), want (default,ws_alpha)", typ, aid, sk)
		}
		sawScoped = true
		if typ == "done" || typ == "run_terminal" {
			time.Sleep(100 * time.Millisecond) // let RunTurn deferred cleanup settle
			break
		}
	}
	if !sawScoped {
		t.Fatal("no scoped result frames observed")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gateway/ -run TestHandleChatSend_FramesCarryScope -v`
Expected: FAIL — frames have empty `agentId`/`sessionKey` (scope not stamped yet).

- [ ] **Step 3: Add scope to `wsSubscriber` and stamp it**

In `internal/gateway/websocket.go`, change the struct (~706):

```go
type wsSubscriber struct {
	conn  *websocket.Conn
	rpcID any
	scope runs.SessionScope
}
```

Set it in `handleChatSend` (~460) — replace `sub := &wsSubscriber{conn: conn, rpcID: rpcID}` with:

```go
	sub := &wsSubscriber{conn: conn, rpcID: rpcID, scope: scope}
```

Stamp in `OnEvent` (~725) — after the `if res == nil { return }` guard, before `writeJSON`:

```go
func (s *wsSubscriber) OnEvent(e runs.Event) {
	res := eventToResult(e)
	if res == nil {
		return
	}
	res["agentId"] = s.scope.AgentID
	res["sessionKey"] = s.scope.SessionKey
	writeJSON(s.conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  res,
		ID:      s.rpcID,
	})
}
```

Stamp in `OnAttached` (~714):

```go
func (s *wsSubscriber) OnAttached(runID string) {
	writeJSON(s.conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result: map[string]any{
			"type":       "run_attached",
			"runID":      runID,
			"agentId":    s.scope.AgentID,
			"sessionKey": s.scope.SessionKey,
		},
		ID: s.rpcID,
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gateway/ -run TestHandleChatSend_FramesCarryScope -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/websocket.go internal/gateway/websocket_chat_test.go
git commit -m "feat(gateway): stamp agentId/sessionKey on chat.send stream frames"
```

(No Co-Authored-By trailer in any commit in this plan.)

---

### Task 2: Server — stamp scope on the chat.subscribe re-attach path

**Files:**
- Modify: `internal/gateway/websocket.go` (`forwardEvents` ~675; `handleChatSubscribe` response ~656; `makeTraceMarkForwarder` ~491)
- Test: `internal/gateway/websocket_chat_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/gateway/websocket_chat_test.go`:

```go
// The chat.subscribe response must identify the scope of the replayed run so
// the client can confirm which session the past events belong to.
func TestHandleChatSubscribe_ResponseCarriesScope(t *testing.T) {
	h, reg, _ := testHandler(t)
	clientConn, serverConn := wsPair(t, h)
	scope := runs.SessionScope{AgentID: "default", SessionKey: "ws_beta"}

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, _ := reg.Create(scope, "live-run", cancel)
	_, _ = run.Append(runs.EventTypeTextDelta, []byte(`{"text":"partial"}`))
	defer run.Finish(runs.StatusCompleted, "", "")

	h.mu.Lock()
	if h.activeSessionKeys[serverConn] == nil {
		h.activeSessionKeys[serverConn] = map[string]string{}
	}
	h.activeSessionKeys[serverConn]["default"] = "ws_beta"
	h.mu.Unlock()

	h.handleChatSubscribe(serverConn, makeReq(t, "chat.subscribe",
		map[string]any{"agentId": "default", "sessionKey": "ws_beta", "fromSeq": 0}, "subscribe-x"))

	resp := readJSON(t, clientConn)
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		t.Fatalf("no result in subscribe response: %v", resp)
	}
	if aid, _ := result["agentId"].(string); aid != "default" {
		t.Errorf("subscribe response agentId=%q, want default; resp=%v", aid, resp)
	}
	if sk, _ := result["sessionKey"].(string); sk != "ws_beta" {
		t.Errorf("subscribe response sessionKey=%q, want ws_beta; resp=%v", sk, resp)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gateway/ -run TestHandleChatSubscribe_ResponseCarriesScope -v`
Expected: FAIL — response has no `agentId`/`sessionKey`.

- [ ] **Step 3: Add scope to the subscribe response and forwardEvents**

In `handleChatSubscribe` (~656), add the two fields to the response result map:

```go
	writeJSON(conn, JSONRPCResponse{
		JSONRPC: "2.0",
		Result: map[string]any{
			"active":     true,
			"runId":      run.ID,
			"lastSeq":    lastSeq,
			"past":       pastJSON,
			"agentId":    params.AgentID,
			"sessionKey": params.SessionKey,
		},
		ID: req.ID,
	})

	go forwardEvents(conn, live, runs.SessionScope{AgentID: params.AgentID, SessionKey: params.SessionKey})
}
```

Change `forwardEvents` (~675) to take a scope and stamp each `chat.event`:

```go
func forwardEvents(conn *websocket.Conn, ch <-chan runs.Event, scope runs.SessionScope) {
	for e := range ch {
		res := eventToResult(e)
		if res == nil {
			continue
		}
		res["agentId"] = scope.AgentID
		res["sessionKey"] = scope.SessionKey
		writeJSON(conn, map[string]any{
			"jsonrpc": "2.0",
			"method":  "chat.event",
			"params":  res,
		})
	}
}
```

(Note: the previous `forwardEvents` passed `eventToResult(e)` inline as `params`; this preserves that but adds the nil-skip and the scope fields. Check for any OTHER caller of `forwardEvents` with `grep -n "forwardEvents" internal/gateway/*.go` and update its call site to pass the scope. As of this plan the only call is in `handleChatSubscribe`.)

Stamp the trace-mark forwarder (`makeTraceMarkForwarder` ~491) — it currently takes `(conn, rpcID)`. Change its signature to also take the scope and add the fields to its result map. Update the call site in `handleChatSend` (`OnTraceMark: h.makeTraceMarkForwarder(conn, rpcID)` ~444) to `h.makeTraceMarkForwarder(conn, rpcID, scope)`:

```go
func (h *WebSocketHandler) makeTraceMarkForwarder(conn *websocket.Conn, rpcID any, scope runs.SessionScope) func(phase string, durMs, atMs int64, attrs []any) {
	return func(phase string, durMs, atMs int64, attrs []any) {
		writeJSON(conn, JSONRPCResponse{
			JSONRPC: "2.0",
			Result: map[string]any{
				"type":       "trace",
				"phase":      phase,
				"dur_ms":     durMs,
				"at_ms":      atMs,
				"attrs":      flattenAttrs(attrs),
				"agentId":    scope.AgentID,
				"sessionKey": scope.SessionKey,
			},
			ID: rpcID,
		})
	}
}
```

- [ ] **Step 4: Run test + full package to verify**

Run: `go test ./internal/gateway/ -run 'TestHandleChatSubscribe_ResponseCarriesScope|TestHandleChatSend_FramesCarryScope' -v`
Expected: PASS.
Run: `go build ./... && go test ./internal/gateway/ -count=1`
Expected: PASS (no other caller of the changed signatures left unbuilt).

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/websocket.go internal/gateway/websocket_chat_test.go
git commit -m "feat(gateway): stamp scope on chat.subscribe + trace frames"
```

---

### Task 3: Server — concurrent-scope isolation regression test

**Files:**
- Test: `internal/gateway/websocket_chat_test.go`

This task adds the core regression guard for the bug (two runs on different sessions over one conn stay correctly labeled). No production change.

- [ ] **Step 1: Write the test**

Append to `internal/gateway/websocket_chat_test.go`:

```go
// Two concurrent chat.send turns on different sessions over one connection
// must produce frames that are each labeled with their own scope — never
// cross-labeled. This is the regression guard for the cross-session
// spill-over bug.
func TestHandleChatSend_ConcurrentScopesNotCrossLabeled(t *testing.T) {
	h, _, _ := testHandler(t, "alpha-reply", "beta-reply")
	clientConn, serverConn := wsPair(t, h)

	h.handleChatSend(serverConn, makeReq(t, "chat.send",
		map[string]string{"agentId": "default", "sessionKey": "ws_alpha", "text": "a"}, 1))
	h.handleChatSend(serverConn, makeReq(t, "chat.send",
		map[string]string{"agentId": "default", "sessionKey": "ws_beta", "text": "b"}, 2))

	// Collect frames from both runs; every scoped frame must have a sessionKey
	// of either ws_alpha or ws_beta, and must match its rpc id's run. We map
	// rpc id 1 → ws_alpha, 2 → ws_beta (the send order above).
	wantByID := map[int]string{1: "ws_alpha", 2: "ws_beta"}
	terminals := map[int]bool{}
	deadline := time.Now().Add(5 * time.Second)
	for len(terminals) < 2 && time.Now().Before(deadline) {
		_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := clientConn.ReadMessage()
		if err != nil {
			break
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		idf, _ := msg["id"].(float64)
		id := int(idf)
		r, _ := msg["result"].(map[string]any)
		if r == nil {
			continue
		}
		typ, _ := r["type"].(string)
		if typ == "" || typ == "run_attached" {
			// run_attached carries scope too, but assert only typed stream frames.
			if typ != "run_attached" {
				continue
			}
		}
		if sk, ok := r["sessionKey"].(string); ok && sk != "" {
			if want := wantByID[id]; want != "" && sk != want {
				t.Fatalf("frame for rpc id %d labeled session %q, want %q (cross-labeled!)", id, sk, want)
			}
		}
		if typ == "done" || typ == "run_terminal" {
			terminals[id] = true
		}
	}
	time.Sleep(100 * time.Millisecond) // settle deferred cleanup
	if len(terminals) < 2 {
		t.Fatalf("did not see both runs terminate; saw %v", terminals)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/gateway/ -run TestHandleChatSend_ConcurrentScopesNotCrossLabeled -v -count=3`
Expected: PASS all 3 (scope is on the wire from Tasks 1–2, so frames are correctly labeled).

- [ ] **Step 3: Commit**

```bash
git add internal/gateway/websocket_chat_test.go
git commit -m "test(gateway): concurrent-scope isolation guard for stream frames"
```

---

### Task 4: Client — per-scope render state + scoped DOM routing

> **DESIGN CORRECTION (applied during execution).** The per-scope `turnState`
> map below (making `assistant`/`toolEls` per-scope and deleting the globals)
> was found to be unworkable: `appendToAssistant`/`finalizeAssistant` and the
> read-only replay path both render *through* the global `currentAssistant`, so
> it cannot be removed. The spec only requires `sending` to be per-session (the
> visible pane is inherently single-session). The implemented design instead:
> **keeps `currentAssistant`/`toolEls` global**, **scope-gates** every live
> streaming case (`if (eventScope(r) === displayedScope())`), and makes **only
> `sending` per-scope** via a `sendingByScope` Map with `isSending`/`setSending`
> helpers (plus `displayedScope()`/`eventScope(r)`). Terminal cases
> (`done`/`aborted`/`error`) clear the event scope's `sending` even when not
> displayed. Task 5 below is likewise adjusted: it uses the global
> `currentAssistant` and `liveRunIdBySession`, not `st.assistant`/`turnState`.
> The original per-scope-`turnState` text is retained below for history; follow
> the correction.


**Files:**
- Modify: `internal/gateway/chat.go` (state vars ~3390; streaming cases ~3814-3917; `text_delta`/`tool_*`/`done`/etc.; `updateSendBtn`/`sendMessage`; switch handlers ~3327/3347)

This is client JS embedded in a Go string. There is no JS unit harness; verification is `go build` (the string must stay valid Go) plus the manual steps in Task 6. Make minimal, surgical edits.

- [ ] **Step 1: Add per-scope state helpers**

Find the global declarations near `chat.go:3390` (`var currentAssistant = null;` and `var sending = false;`). Add ABOVE them (keep `currentAssistant`/`sending` for now to avoid breaking other references in the same edit; they will be removed where replaced):

```javascript
		// Per-scope turn state keyed by runsKey(agent, session). Each entry:
		// { assistant: <DOM bubble or null>, sending: bool, toolEls: {} }.
		var turnState = new Map();
		function stateFor(scopeKey) {
			var s = turnState.get(scopeKey);
			if (!s) { s = { assistant: null, sending: false, toolEls: {} }; turnState.set(scopeKey, s); }
			return s;
		}
		function displayedScope() { return runsKey(agentSelect.value, sessionSelect.value); }
		function eventScope(r) { return runsKey(r.agentId, r.sessionKey); }
```

- [ ] **Step 2: Route the streaming cases by scope**

Replace the streaming `switch (r.type)` cases (`chat.go:3814-3883`, the `text_delta` through `compaction.skipped` cases — NOT `run_attached`/`run_terminal`, handled in Task 5) so each resolves its scope and only touches the DOM when it matches the displayed scope. Replace the block starting at `case 'text_delta':` through `case 'compaction.skipped':` with:

```javascript
				case 'text_delta': {
					var sk = eventScope(r), st = stateFor(sk);
					if (sk === displayedScope()) {
						if (!st.assistant) { st.assistant = addAssistantMsg(); }
						appendToAssistant(r.text);
					}
					break;
				}
				case 'tool_call_start': {
					var sk2 = eventScope(r), st2 = stateFor(sk2);
					if (sk2 === displayedScope()) {
						if (st2.assistant) { finalizeAssistant(); st2.assistant = null; }
						addToolCall(r.tool, r.id, r.input);
					}
					break;
				}
				case 'tool_result': {
					if (eventScope(r) === displayedScope()) {
						updateToolResult(r.tool, r.id, r.input, r.output, r.error, r.images, r.auth_required);
					}
					break;
				}
				case 'done': {
					var skD = eventScope(r), stD = stateFor(skD);
					stD.sending = false;
					if (skD === displayedScope()) {
						if (stD.assistant) { finalizeAssistant(); }
						stD.assistant = null;
						if (r.context_window && agentSelect && agentSelect.value) {
							agentWindows[agentSelect.value] = r.context_window;
						}
						updateTokenChip(r.usage);
						updateSendBtn();
					}
					break;
				}
				case 'aborted': {
					var skA = eventScope(r), stA = stateFor(skA);
					stA.sending = false;
					if (skA === displayedScope()) {
						if (stA.assistant) { finalizeAssistant(); }
						stA.assistant = null;
						updateSendBtn();
					}
					break;
				}
				case 'error': {
					var skE = eventScope(r), stE = stateFor(skE);
					stE.sending = false;
					if (skE === displayedScope()) {
						addError(r.message);
						stE.assistant = null;
						updateSendBtn();
					}
					break;
				}
				case 'compaction.start': {
					var skC = eventScope(r), stC = stateFor(skC);
					if (skC === displayedScope()) {
						if (!stC.assistant) { stC.assistant = addAssistantMsg(); }
						appendToAssistant('\n*[Compacting context…]*\n');
					}
					break;
				}
				case 'compaction.done': {
					var skC2 = eventScope(r), stC2 = stateFor(skC2);
					if (skC2 === displayedScope() && stC2.assistant) {
						appendToAssistant('\n*[Context compacted.]*\n');
					}
					break;
				}
				case 'compaction.skipped':
					break;
				case 'trace':
					if (eventScope(r) === displayedScope()) { addTraceRow(r); }
					break;
```

Note: the old code also had a `case 'trace':` later (`chat.go:3881`). Remove that now-duplicated later `case 'trace':` block so there is exactly one. Confirm with `grep -n "case 'trace'" internal/gateway/chat.go` after editing — expect ONE match.

- [ ] **Step 3: Make the composer lock per-scope**

Find `updateSendBtn` (search `function updateSendBtn`). Wherever it reads the global `sending`, change it to read `stateFor(displayedScope()).sending`. Find `sendMessage` (~`chat.go:4500-4540`). Replace its `sending = true;` with `stateFor(displayedScope()).sending = true;` and keep `updateSendBtn()` after. Replace the catch-block `sending = false;` (~4546) with `stateFor(displayedScope()).sending = false;`. Also the early error-return in `onmessage` (`chat.go:3633`, `sending = false;`) becomes `stateFor(displayedScope()).sending = false;`.

After this step, search for any remaining bare `sending` references: `grep -n "[^.]sending\b" internal/gateway/chat.go`. Each must be either the new per-scope form or removed. Remove the now-unused `var sending = false;` global declaration. Leave `var currentAssistant = null;` removal for whichever references remain — replace remaining `currentAssistant` uses in non-streaming handlers (clear-session, switch handlers) per Step 4.

- [ ] **Step 4: Update switch + clear handlers to use per-scope state**

In `agentSelect` change (`chat.go:3327`) and `sessionSelect` change (`chat.go:3347`) handlers, the lines `currentAssistant = null;` and `toolEls = {};` (global) should be removed — per-scope state is keyed by scope, so switching does not null another scope's in-flight bubble. After clearing the pane (`messagesEl.innerHTML=''`), call `updateSendBtn()` so the composer reflects the target scope's lock. (The re-attach wiring is Task 5; for now the sessionSelect handler keeps its existing `session.history` send.) In `doClearSession` (`chat.go:3311`) and any other place that sets the globals, replace `currentAssistant = null; toolEls = {};` with `stateFor(displayedScope()).assistant = null; stateFor(displayedScope()).toolEls = {};`.

Search `grep -n "currentAssistant\|toolEls" internal/gateway/chat.go` and reconcile every reference. Confirmed current shape: `toolEls` is a global map (`chat.go:4026`); `addToolCall` writes `toolEls[id] = div` (`chat.go:4094`); `updateToolResult` reads `toolEls[toolId] || toolEls[toolName]` (`chat.go:4099`). Migrate both to source the map from the displayed scope — inside each function, replace the bare `toolEls` with `stateFor(displayedScope()).toolEls`. This is correct because both functions are only ever called from the displayed-scope branch of the streaming cases (DOM ops are gated on `sk === displayedScope()`), so "the displayed scope's tool map" is always the right one. Remove the global `var toolEls = {};` (`chat.go:4026`) and `var currentAssistant = null;` once no references remain. Note `updateSendBtn` (`chat.go:4315-4316`) currently reads the global `sending` — that is the read changed in Step 3.

- [ ] **Step 5: Verify the Go string still compiles + manual smoke**

Run: `go build ./...`
Expected: exit 0 (the embedded JS is a Go string literal; a syntax slip won't fail Go build, but an unterminated backtick will — so this catches gross breakage).
Run: `go vet ./internal/gateway/`
Expected: clean.
Then a quick manual smoke (server run): `go run ./cmd/felix start` is NOT required for this task's commit, but confirm the file parses. Defer functional manual checks to Task 6.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/chat.go
git commit -m "feat(gateway): per-scope client render state; events render only into their session"
```

---

### Task 5: Client — chat.event branch, re-attach on switch-back, sidebar badge

**Files:**
- Modify: `internal/gateway/chat.go` (`onmessage` notification branch ~3641; `run_attached`/`run_terminal` cases ~3815/3894; `sessionSelect` change ~3347; `renderSessions` ~2743; CSS block; new helpers)

- [ ] **Step 1: Add the `chat.event` notification branch**

This step refactors the streaming `switch (r.type)` that Task 4 edited in place (the `text_delta`…`trace` cases on `resp.result`) into a reusable function so the re-attach notification path can share it. Move the entire `switch (r.type) { ... }` block (all the Task-4 cases PLUS `run_attached` and `run_terminal`, which Step 2 below rewrites) into a new function declared in the same closure scope:

```javascript
		function handleStreamFrame(r) {
			switch (r.type) {
			// ... all the streaming cases (text_delta, tool_call_start,
			// tool_result, done, aborted, error, compaction.*, trace,
			// run_attached, run_terminal) ...
			}
		}
```

At the original dispatch site (`var r = resp.result;` followed by the switch), replace the inline switch with a single call: `handleStreamFrame(r);`.

Then, next to the `session_titled` branch (`chat.go:3641`), add the notification branch that feeds re-attach events through the same function:

```javascript
				if (resp.method === 'chat.event') {
					if (resp.params) { handleStreamFrame(resp.params); }
					return;
				}
```

Refactor: wrap the `switch (r.type) { ... }` body (Task 4's cases plus `run_attached`/`run_terminal`) inside `function handleStreamFrame(r)`, and at the original site replace it with `handleStreamFrame(r);`. Keep `var r = resp.result;` feeding `handleStreamFrame(r)`.

- [ ] **Step 2: Scope-correct `run_attached` / `run_terminal` + badge updates**

Inside `handleStreamFrame`, change `run_attached` (`chat.go:3815`) to key off the event's own scope, not the displayed scope:

```javascript
				case 'run_attached':
					if (r.runID && r.agentId && r.sessionKey) {
						liveRunIdBySession.set(runsKey(r.agentId, r.sessionKey), r.runID);
						updateSessionBadges();
					}
					break;
```

Change `run_terminal` (`chat.go:3894`) similarly — clear the live run for the event's scope and update the per-scope sending lock + badge:

```javascript
				case 'run_terminal': {
					var skT = eventScope(r), stT = stateFor(skT);
					stT.sending = false;
					if (r.agentId && r.sessionKey) {
						liveRunIdBySession.delete(runsKey(r.agentId, r.sessionKey));
						updateSessionBadges();
					}
					if (skT === displayedScope()) {
						if (stT.assistant) { finalizeAssistant(); stT.assistant = null; }
						updateSendBtn();
						if (r.status === 'completed') { break; }
						var marker = '';
						switch (r.status) {
						case 'cancelled':
							marker = r.reason === 'superseded' ? '↳ replaced by next turn' : '⏹ cancelled';
							break;
						case 'failed': marker = '⚠ run failed'; break;
						case 'interrupted': marker = '⏸ interrupted'; break;
						default: marker = '';
						}
						if (marker) { addError(marker); }
					}
					break;
				}
```

(Read the existing `run_terminal` block first, `chat.go:3894-3925`, and preserve its exact marker strings/branches — the snippet above mirrors the structure; match whatever the current statuses/markers are.)

- [ ] **Step 3: Re-attach via chat.subscribe on switch-back**

In the `sessionSelect` change handler (`chat.go:3347`), after wiping the pane and BEFORE the unconditional `session.history` send, branch on whether the target scope is live:

```javascript
			messagesEl.innerHTML = '';
			stateFor(displayedScope()).assistant = null;
			stateFor(displayedScope()).toolEls = {};
			resetTokenChip();
			refreshEmptyState();
			var sk = runsKey(agentSelect.value, sessionSelect.value);
			if (liveRunIdBySession.has(sk)) {
				// Re-attach to the in-flight run: replay events so far, then live.
				ws.send(JSON.stringify({
					jsonrpc: '2.0',
					method: 'chat.subscribe',
					params: { agentId: agentSelect.value, sessionKey: sessionSelect.value, fromSeq: 0 },
					id: 'subscribe-' + sk
				}));
			} else {
				ws.send(JSON.stringify({
					jsonrpc: '2.0',
					method: 'session.history',
					params: { agentId: agentSelect.value, sessionKey: sessionSelect.value },
					id: 'history'
				}));
			}
			updateSendBtn();
```

Add a dispatcher branch for the `subscribe-` response (next to the `history`/`runs-` branches, e.g. after `chat.go:3741`'s `history` branch):

```javascript
				if (typeof resp.id === 'string' && resp.id.indexOf('subscribe-') === 0) {
					var sres = resp.result || {};
					if (sres.active === false) {
						// Run finished while away — fall back to history.
						ws.send(JSON.stringify({
							jsonrpc: '2.0',
							method: 'session.history',
							params: { agentId: agentSelect.value, sessionKey: sessionSelect.value },
							id: 'history'
						}));
						return;
					}
					var pastEv = sres.past || [];
					for (var pi = 0; pi < pastEv.length; pi++) {
						// Past events lack per-frame scope; they belong to the
						// subscribed scope, which is the one we just switched to.
						var pe = pastEv[pi];
						pe.agentId = sres.agentId; pe.sessionKey = sres.sessionKey;
						handleStreamFrame(pe);
					}
					return;
				}
```

- [ ] **Step 4: Sidebar running badge**

In `renderSessions` (`chat.go:2743`), add a badge span to the row HTML (after `.ses-name`):

```javascript
					'<span class="ses-running" aria-label="Generating"></span>' +
```

After the row is built and appended, set its initial visibility. Simplest: at the end of `renderSessions`, call `updateSessionBadges()`. Add the helper near `renderSessions`:

```javascript
		// updateSessionBadges toggles the running dot on existing session rows
		// from liveRunIdBySession, without a full re-render.
		function updateSessionBadges() {
			var aid = agentSelect.value;
			var rows = sessionsList.querySelectorAll('.session-row[data-session-key]');
			for (var i = 0; i < rows.length; i++) {
				var key = rows[i].dataset.sessionKey;
				var live = liveRunIdBySession.has(runsKey(aid, key));
				rows[i].classList.toggle('has-run', live);
			}
		}
```

Add CSS (find the existing `.ses-name`/`.session-row` rules block and add nearby):

```css
.ses-running { display: none; width: 8px; height: 8px; border-radius: 50%; background: #16a34a; margin-left: 6px; flex: 0 0 auto; }
.session-row.has-run .ses-running { display: inline-block; animation: ses-pulse 1.2s ease-in-out infinite; }
@keyframes ses-pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.3; } }
```

- [ ] **Step 5: Verify build + vet**

Run: `go build ./... && go vet ./internal/gateway/`
Expected: exit 0, clean.
Run: `grep -n "case 'trace'" internal/gateway/chat.go`
Expected: exactly ONE match (no duplicate from the refactor).
Run: `grep -n "function handleStreamFrame" internal/gateway/chat.go`
Expected: exactly ONE match.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/chat.go
git commit -m "feat(gateway): re-attach on switch-back via chat.subscribe + sidebar running badge"
```

---

### Task 6: Full gates + manual verification

**Files:** none (verification only)

- [ ] **Step 1: Full project gates**

Run each, confirm pass:

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./internal/gateway/...
```

Expected: all pass. Report any failure with exact output. `internal/gateway` MUST be green.

- [ ] **Step 2: Manual functional verification**

Start the gateway (`go run ./cmd/felix start`), open the chat UI in a browser, configure at least two sessions on one agent, and verify:

1. Send a message in session A. While it is generating, switch to session B → B's pane stays clean (no spill-over); A's sidebar row shows the green running dot.
2. Switch back to A → the full response so far is present (replayed), continues streaming if still live, and finalizes normally; the dot clears on completion.
3. While A is still generating, send a message in idle B → B's turn runs concurrently; both rows show the dot; each session renders only its own output.
4. Switch away from A mid-turn and return after it finished → A shows the completed turn (history fallback via `active:false`).
5. The composer is locked only in a session with a live run; switching to an idle session frees it.

Record the outcomes. If any fails, capture which step + symptom and stop for triage (do not paper over).

- [ ] **Step 3: Commit (verification note, if any artifacts)**

No code change expected here. If gates required a fix, that fix is its own commit with an accurate message. Otherwise nothing to commit.

---

## Notes for the implementer

- **Read before edit.** The client JS in `chat.go` is large; read each region (the `onmessage` dispatcher, `renderSessions`, `updateSendBtn`, `sendMessage`, the switch handlers) before editing so you preserve surrounding behavior. Match the existing var-style (the file uses `var`, not `let`/`const`).
- **The streaming switch is shared by two callers after Task 5** (`resp.result` for fresh sends, `resp.params` for re-attach `chat.event`). Both must call the same `handleStreamFrame(r)`.
- **Past events from chat.subscribe lack per-frame scope** — the enclosing subscribe response carries it; stamp it onto each past event before calling `handleStreamFrame` (Task 5 Step 3).
- **Do not change** the `runs` package, the `chat.send` dual-envelope contract beyond adding scope fields, or any server file other than `websocket.go`.
- **Reconcile every `sending`, `currentAssistant`, and `toolEls` reference** — the per-scope migration is only correct if no global remnant still drives the DOM. Grep after each client task.
