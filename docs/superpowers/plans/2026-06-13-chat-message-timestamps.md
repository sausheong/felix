# Chat message timestamps Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show a datetime caption on every user and assistant chat bubble (time-only for today, "Mon D, h:mm" for older), driven by persisted session timestamps for history and the client clock for live messages.

**Architecture:** Server already stores `session.SessionEntry.Timestamp` (Unix seconds). Forward it on `message` entries from `handleSessionHistory`. On the client, add a `formatMsgTime` helper and a `.msg-time` caption inside each bubble; history uses the persisted timestamp, live uses `Date.now()`, run-replay omits the caption.

**Tech Stack:** Go 1.25 (`internal/gateway`), inline HTML/CSS/JS in `chat.go`, stdlib `testing` + the existing `testHandler`/`wsPair` websocket test harness.

**Repo:** `/Users/sausheong/projects/felix`. Gates: `go build ./...`, `go vet ./...`, `go test ./...`. Commits omit the `Co-Authored-By` trailer.

---

## File Structure

- **Modify:** `internal/gateway/websocket.go` — `handleSessionHistory` message-entry maps (~lines 1310, 1328 region): add `"timestamp"`.
- **Modify:** `internal/gateway/chat.go` — CSS (`.msg-time`), `formatMsgTime` helper, `addUserMsg`/`addAssistantMsg` signatures + caption, history handler call sites, replay-mode call sites.
- **Test:** `internal/gateway/websocket_history_test.go` (new) — assert the history response carries timestamps on message entries.

No new production files. No API breakage (purely additive field).

---

## Task 1: Server forwards entry timestamp on message-history entries

**Files:**
- Modify: `internal/gateway/websocket.go` (`handleSessionHistory`, the `EntryTypeMessage` case ~line 1305-1314)
- Test: `internal/gateway/websocket_history_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `internal/gateway/websocket_history_test.go`:
```go
package gateway

import (
	"testing"

	"github.com/sausheong/felix/internal/session"
)

// TestSessionHistoryIncludesTimestamp drives handleSessionHistory over the
// real websocket test harness and asserts that message entries carry a
// non-zero "timestamp" field (Unix seconds) sourced from the persisted
// session entry.
func TestSessionHistoryIncludesTimestamp(t *testing.T) {
	h, _, _ := testHandler(t)
	clientConn, serverConn := wsPair(t, h)

	// Seed a session with one user + one assistant message, persisted
	// through the same store the handler reads from.
	sess, err := h.sessionStore.Load("default", "ws_default")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	u := session.UserMessageEntry("hello there")
	a := session.AssistantMessageEntry("hi back")
	h.sessionStore.AppendEntry(sess, u)
	h.sessionStore.AppendEntry(sess, a)

	h.handleSessionHistory(serverConn, makeReq(t, "session.history",
		map[string]any{"agentId": "default", "sessionKey": "ws_default"}, "history"))

	resp := readJSON(t, clientConn)
	result, _ := resp["result"].(map[string]any)
	entries, ok := result["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("want 2 entries, got %v (resp=%v)", entries, resp)
	}
	for i, raw := range entries {
		e, _ := raw.(map[string]any)
		if e["type"] != "message" {
			t.Fatalf("entry %d type=%v, want message", i, e["type"])
		}
		ts, ok := e["timestamp"].(float64) // JSON numbers decode to float64
		if !ok || ts <= 0 {
			t.Errorf("entry %d missing/zero timestamp: %v", i, e["timestamp"])
		}
	}
}
```

NOTE: `AppendEntry` sets `Timestamp = time.Now().Unix()` when zero (verified
`harness/session/session.go:112-113` is invoked via the store append path).
`makeReq`, `readJSON`, `testHandler`, `wsPair` already exist in
`websocket_chat_test.go` (same package).

- [ ] **Step 2: Run, verify it fails**

Run: `go test ./internal/gateway/ -run TestSessionHistoryIncludesTimestamp`
Expected: FAIL — `entry N missing/zero timestamp` (the handler does not emit
the field yet).

- [ ] **Step 3: Implement — add timestamp to the message entry map**

In `internal/gateway/websocket.go`, in `handleSessionHistory`, the
`case session.EntryTypeMessage:` block currently appends:
```go
			entries = append(entries, map[string]any{
				"type": "message",
				"role": entry.Role,
				"text": msg.Text,
			})
```
Change it to include the timestamp:
```go
			entries = append(entries, map[string]any{
				"type":      "message",
				"role":      entry.Role,
				"text":      msg.Text,
				"timestamp": entry.Timestamp,
			})
```
(Leave the `tool_call` and `tool_result` cases unchanged.)

- [ ] **Step 4: Run, verify it passes**

Run: `go test ./internal/gateway/ -run TestSessionHistoryIncludesTimestamp`
Expected: PASS.

- [ ] **Step 5: Package vet + full gateway tests**

Run: `go vet ./internal/gateway/ && go test ./internal/gateway/`
Expected: clean; no existing test regresses.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/websocket.go internal/gateway/websocket_history_test.go
git commit -m "feat(gateway): include entry timestamp in session.history message entries"
```

---

## Task 2: Client renders the timestamp caption (history + live), omits in replay

**Files:**
- Modify: `internal/gateway/chat.go` (CSS block; `formatMsgTime`; `addUserMsg`; `addAssistantMsg`; history handler; replay-mode handler)

This task is JS/CSS inside the Go string literal `chatHTML`. There is no JS test
harness; correctness is by construction + `go build`/`go vet`. Keep edits
surgical and exact.

- [ ] **Step 1: Add `.msg-time` CSS**

In `chat.go`, immediately AFTER the `.system-marker { ... }` rule (it ends at
the line `	align-self: center;\n}` around line 903), insert:
```css
.msg-time {
	font-size: var(--fs-xs);
	color: var(--text-muted);
	margin-top: 0.35rem;
	font-variant-numeric: tabular-nums;
}
.msg.user .msg-time { text-align: right; }
.msg.assistant .msg-time { text-align: left; }
```
(Place it before the `.msg.user {` rule. Any position inside the `<style>` block
is fine; this keeps it adjacent to the bubble rules.)

- [ ] **Step 2: Add the `formatMsgTime` helper**

In the `<script>` IIFE, add this helper next to the other small formatting
helpers — place it immediately BEFORE `function addUserMsg(text) {`
(around line 3938):
```js
	// formatMsgTime renders a bubble caption. tsSeconds is Unix seconds.
	// Falsy/0 -> '' (caption omitted). Same calendar day as now -> time
	// only ("2:32 PM"); older -> "Jun 12, 2:32 PM". Locale/zone aware.
	function formatMsgTime(tsSeconds) {
		if (!tsSeconds) return '';
		var d = new Date(tsSeconds * 1000);
		if (isNaN(d.getTime())) return '';
		var now = new Date();
		var sameDay = d.getFullYear() === now.getFullYear() &&
			d.getMonth() === now.getMonth() &&
			d.getDate() === now.getDate();
		var time = d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
		if (sameDay) return time;
		var date = d.toLocaleDateString([], { month: 'short', day: 'numeric' });
		return date + ', ' + time;
	}
	// appendMsgTime adds a .msg-time caption to a bubble element when ts
	// is renderable. Shared by user + assistant bubbles.
	function appendMsgTime(bubbleEl, tsSeconds) {
		var label = formatMsgTime(tsSeconds);
		if (!label) return;
		var t = document.createElement('div');
		t.className = 'msg-time';
		t.textContent = label;
		bubbleEl.appendChild(t);
	}
```

- [ ] **Step 3: Update `addUserMsg` to accept + render a timestamp**

Replace the existing `addUserMsg` (around lines 3938-3944):
```js
	function addUserMsg(text) {
		var div = document.createElement('div');
		div.className = 'msg user';
		div.textContent = text;
		messagesEl.appendChild(div);
		scrollToBottom();
	}
```
with:
```js
	function addUserMsg(text, tsSeconds) {
		var div = document.createElement('div');
		div.className = 'msg user';
		div.textContent = text;
		// Live sends pass no ts -> stamp now. History passes the
		// persisted Unix-seconds value. An explicit 0 suppresses the
		// caption (used by run-replay where no message time exists).
		var ts = (tsSeconds === undefined) ? Math.floor(Date.now() / 1000) : tsSeconds;
		appendMsgTime(div, ts);
		messagesEl.appendChild(div);
		scrollToBottom();
	}
```

- [ ] **Step 4: Update `addAssistantMsg` to accept + render a timestamp**

Replace the existing `addAssistantMsg` (around lines 3946-3955):
```js
	function addAssistantMsg() {
		var div = document.createElement('div');
		div.className = 'msg assistant';
		var content = document.createElement('div');
		content.className = 'content';
		div.appendChild(content);
		messagesEl.appendChild(div);
		scrollToBottom();
		return { el: div, content: content, raw: '' };
	}
```
with:
```js
	function addAssistantMsg(tsSeconds) {
		var div = document.createElement('div');
		div.className = 'msg assistant';
		var content = document.createElement('div');
		content.className = 'content';
		div.appendChild(content);
		// Caption order: content first, then time, so the time sits
		// below the rendered markdown. The caption is appended now (at
		// bubble creation); for a streamed reply this is ~first-token
		// time, which is within seconds of completion. An explicit 0
		// suppresses it (run-replay).
		var ts = (tsSeconds === undefined) ? Math.floor(Date.now() / 1000) : tsSeconds;
		appendMsgTime(div, ts);
		messagesEl.appendChild(div);
		scrollToBottom();
		return { el: div, content: content, raw: '' };
	}
```
IMPORTANT: `appendToAssistant`/`finalizeAssistant` set
`currentAssistant.content.innerHTML = renderMd(...)`. Because the caption is a
SEPARATE child (`.msg-time`, a sibling of `.content`), re-rendering `.content`
does NOT clobber the caption. Verify this invariant holds (it does: they target
`.content`, not the bubble `div`).

- [ ] **Step 5: Pass the persisted timestamp from the history handler**

In the `history` response handler (around lines 3726-3731), update the two
message branches to forward `entry.timestamp`:
```js
						if (entry.type === 'message' && entry.role === 'user') {
							addUserMsg(entry.text, entry.timestamp);
						} else if (entry.type === 'message' && entry.role === 'assistant') {
							var bubble = addAssistantMsg(entry.timestamp);
							bubble.raw = entry.text;
							bubble.content.innerHTML = renderMd(entry.text);
						} else if (entry.type === 'tool_call') {
```
(Only the first two branches change — add the `entry.timestamp` argument.)

- [ ] **Step 6: Suppress captions in run-replay mode**

Run-replay repaints a single run's event log (assistant side, no message
timestamps). Pass an explicit `0` so no caption renders. In `renderReplayMode`
(around lines 2989-3011) there are TWO `addAssistantMsg()` calls — change both
to `addAssistantMsg(0)`:
```js
				case 'text_delta':
					if (!localAssistant) {
						localAssistant = addAssistantMsg(0);
						currentAssistant = localAssistant;
					}
```
and:
```js
				case 'compaction.start':
					if (!localAssistant) {
						localAssistant = addAssistantMsg(0);
						currentAssistant = localAssistant;
					}
```
Leave the live-stream `addAssistantMsg('')` call sites (lines ~3805, ~3851)
UNCHANGED — `''` is not `undefined`, so guard against that: the live path passes
`''` (empty string) historically. **Fix:** those live call sites should pass NO
argument so the `=== undefined` default (stamp-now) applies. Change
`addAssistantMsg('')` at the live `text_delta` (~3805) and live
`compaction.start` (~3851) to `addAssistantMsg()`. (The empty-string argument
was only ever a placeholder for the old zero-arg signature and is unused inside
the function.)

CONFIRM during implementation: grep `addAssistantMsg(` and ensure every call
site is intentional —
  - history: `addAssistantMsg(entry.timestamp)`
  - live (text_delta, compaction.start): `addAssistantMsg()`
  - replay (text_delta, compaction.start): `addAssistantMsg(0)`
  - replay top `addAssistantMsg()` at line ~2991 inside renderReplayMode's
    text_delta is the SAME one being changed to `(0)`.

- [ ] **Step 7: Build + vet**

Run: `go build ./... && go vet ./internal/gateway/`
Expected: clean (the change is inside a Go string literal, so a stray `%`
would break `fmt.Fprintf(w, chatHTML, port)` — there are none introduced here;
the CSS added uses no `%`).

- [ ] **Step 8: Full test run**

Run: `go test ./internal/gateway/`
Expected: PASS (Task 1 test still green; nothing else touched server-side).

- [ ] **Step 9: Commit**

```bash
git add internal/gateway/chat.go
git commit -m "feat(gateway): render datetime caption on chat message bubbles"
```

---

## Task 3: Verification — whole build, vet, tests

- [ ] **Step 1: Full gates**

```bash
cd /Users/sausheong/projects/felix
go build ./... && go vet ./... && go test ./...
```
Expected: all green.

- [ ] **Step 2: Manual sanity reasoning (no commit)**

Confirm by reading the diff:
- History bubbles get the persisted time; older-than-today show the date prefix.
- Live bubbles get the send/first-token time.
- Replay bubbles have NO caption.
- The `.msg-time` caption survives assistant streaming re-renders (separate
  child from `.content`).

No commit (verification only).

---

## Final controller checklist (after all tasks)

- [ ] Server emits `timestamp` on message history entries (test green red→green).
- [ ] User + assistant bubbles show a caption; tool rows unchanged.
- [ ] Today → time only; older → "Mon D, h:mm".
- [ ] Live path stamps now; history path uses persisted ts; replay shows none.
- [ ] `go build ./... && go vet ./... && go test ./...` all green.
- [ ] No `Co-Authored-By` trailer.
- [ ] Adversarial review: confirm caption isn't clobbered by markdown re-render,
      no `%` introduced into the `chatHTML` printf template, and `formatMsgTime`
      handles 0/NaN/negative gracefully.
