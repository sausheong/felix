# Auto-generated session titles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After the first completed turn of an untitled session, generate a short (< 10 words) title from the first Q&A using the agent's own provider/model, store it as the session's display title, and refresh the sidebar.

**Architecture:** A new gateway-side, best-effort, async step (`maybeGenerateSessionTitle`) runs inside the existing `handleChatSend` goroutine after `RunTurn` succeeds. It reads the persisted first user+assistant messages, calls the provider once (mirroring `compaction.Summarizer.callOnce`), sanitises/validates the result, writes the `<key>.meta.json` title sidecar (key/JSONL untouched), and broadcasts a `session_titled` notification. The client refreshes its session list on that notification.

**Tech Stack:** Go 1.25 (`internal/gateway`), `internal/llm` (harness shim — `llm.ChatRequest`, `ChatStream`, `ParseProviderModel`), inline JS in `chat.go`, stdlib `testing` + existing `testHandler`/`wsPair` harness.

**Repo:** `/Users/sausheong/projects/felix`. Gates: `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./internal/gateway/`. Commits omit the `Co-Authored-By` trailer.

---

## File Structure

- **Create:** `internal/gateway/session_title.go` — `sanitizeTitle`, `maybeGenerateSessionTitle`, and a small broadcast helper.
- **Create:** `internal/gateway/session_title_test.go` — unit tests for `sanitizeTitle` + behavioural tests for `maybeGenerateSessionTitle`.
- **Modify:** `internal/gateway/websocket.go` — call `maybeGenerateSessionTitle` in the `handleChatSend` goroutine after a successful `RunTurn`.
- **Modify:** `internal/gateway/chat.go` — handle the `session_titled` server notification by refreshing the session list.

---

## Pre-flight facts (verified; implementer re-confirms by reading)

- Session **title** is a sidecar: `writeSessionMeta(sessionsBase, agentID, key, title)` / `readSessionMeta(...)` / `validateSessionTitle(title)` in `session_meta.go`. Title cap `sessionMetaMaxTitleLen = 100` runes; rejects control chars and `/ \`.
- `session.list` returns `"title": readSessionMeta(...)`; client renders `s.title || s.key` (`chat.go:2770`).
- `WebSocketHandler` fields: `sessionStore *session.Store`, `sessionsBaseDir string`, `providers map[string]llm.LLMProvider`, `config *config.Config`, `serverCtx context.Context`, `mu sync.RWMutex`, `activeSessionKeys map[*websocket.Conn]map[string]string`. (Confirm exact names at `websocket.go:46-90`.)
- `h.config.GetAgent(agentID) (AgentConfig, bool)`; `AgentConfig.Model` is `"provider/model"`.
- `llm.ParseProviderModel(s) (provider, model string)`. The provider map is keyed by **provider name**; a direct `ChatStream` call must set `req.Model` to the **model** part (confirmed: `compaction/builder.go:75,105` sets `Model: model` from `ParseProviderModel`).
- One-shot call shape (mirror `harness/compaction/summarizer.go:96-139`): `llm.ChatRequest{Model, Messages:[]llm.Message{{Role:"user",Content:...}}, SystemPromptParts:[]llm.SystemPromptPart{{Text:...}}, MaxTokens:...}` → `provider.ChatStream(ctx,req)` → accumulate `llm.EventTextDelta`, bail on `llm.EventError`.
- `Session.History() []session.SessionEntry`; message entries are `Type == session.EntryTypeMessage` with `Role` "user"/"assistant" and `Data` unmarshalling to `session.MessageData{Text string}`.
- Broadcast pattern: `BroadcastNewRun` (`websocket.go:210-232`) snapshots conns whose `activeSessionKeys[conn][agentID] == sessionKey` under RLock, then `writeJSON`s a `{jsonrpc,method,params}` notification to each.
- Scripted test provider (`internal/llm/llmtest/scripted.go`): `NewScriptedProvider(replies...)` returns one canned `EventTextDelta` (the reply) + `EventDone` per `ChatStream` call, FIFO; records `Calls`. `testHandler(t, scripted...)` wires it as provider `"local"` with agent model `"local/test-model"`.

---

## Task 1: `sanitizeTitle` pure helper + unit tests

**Files:**
- Create: `internal/gateway/session_title.go`
- Test: `internal/gateway/session_title_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/gateway/session_title_test.go`:
```go
package gateway

import "testing"

func TestSanitizeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Deploying the release", "Deploying the release"},
		{"  spaced  out \n title \t here ", "spaced out title here"},
		{"\"Quoted title\"", "Quoted title"},
		{"'single quoted'", "single quoted"},
		{"Ends with a period.", "Ends with a period"},
		{"one two three four five six seven eight nine ten eleven",
			"one two three four five six seven eight nine"}, // capped at 9 words
		{"", ""},
		{"   ", ""},
		{"line1\nline2", "line1 line2"},
	}
	for _, c := range cases {
		if got := sanitizeTitle(c.in); got != c.want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeTitle_ClampsLength(t *testing.T) {
	// 9 very long words still get clamped to the rune cap.
	long := ""
	for i := 0; i < 9; i++ {
		for j := 0; j < 30; j++ {
			long += "x"
		}
		long += " "
	}
	got := sanitizeTitle(long)
	if len([]rune(got)) > sessionMetaMaxTitleLen {
		t.Errorf("sanitizeTitle did not clamp: %d runes", len([]rune(got)))
	}
}
```

- [ ] **Step 2: Run, verify it fails (compile error: sanitizeTitle undefined)**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run TestSanitizeTitle`
Expected: FAIL — `undefined: sanitizeTitle`.

- [ ] **Step 3: Implement `sanitizeTitle` (and the file skeleton)**

Create `internal/gateway/session_title.go`:
```go
package gateway

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/sausheong/felix/internal/gateway/runs"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/session"
)

// maxTitleWords caps the generated title to keep it glanceable ("< 10 words").
const maxTitleWords = 9

// sanitizeTitle cleans a model-generated title into a single short line:
// collapse all whitespace (incl. newlines) to single spaces, strip a single
// pair of surrounding quotes, drop a trailing period, cap to maxTitleWords
// words, then clamp to sessionMetaMaxTitleLen runes. Returns "" when nothing
// usable remains.
func sanitizeTitle(raw string) string {
	// Collapse whitespace.
	s := strings.Join(strings.Fields(raw), " ")
	if s == "" {
		return ""
	}
	// Strip one pair of surrounding quotes (straight single or double).
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	// Drop a single trailing period (titles shouldn't end in punctuation).
	s = strings.TrimRight(s, " ")
	if strings.HasSuffix(s, ".") {
		s = strings.TrimSuffix(s, ".")
		s = strings.TrimRight(s, " ")
	}
	if s == "" {
		return ""
	}
	// Cap word count.
	words := strings.Fields(s)
	if len(words) > maxTitleWords {
		words = words[:maxTitleWords]
	}
	s = strings.Join(words, " ")
	// Clamp rune length to the meta cap.
	if utf8.RuneCountInString(s) > sessionMetaMaxTitleLen {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:sessionMetaMaxTitleLen]))
	}
	return s
}
```
(The other imports — context, slog, time, websocket, runs, llm, session — are
used by Task 2's functions added to this same file. If Go complains about
unused imports at THIS step because Task 2 isn't written yet, add only the
imports `sanitizeTitle` needs now — `strings`, `unicode/utf8` — and re-add the
rest in Task 2. Prefer to implement Task 2's functions in the same edit cycle
if you're confident; otherwise keep imports minimal here.)

- [ ] **Step 4: Run, verify it passes**

Run: `go test ./internal/gateway/ -run TestSanitizeTitle`
Expected: PASS (both tests).

- [ ] **Step 5: vet**

Run: `go vet ./internal/gateway/`
Expected: clean (no unused imports — keep the import set matching what's
actually referenced so far).

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/session_title.go internal/gateway/session_title_test.go
git commit -m "feat(gateway): add sanitizeTitle helper for generated session titles"
```

---

## Task 2: `maybeGenerateSessionTitle` + broadcast, with behavioural tests

**Files:**
- Modify: `internal/gateway/session_title.go` (add the functions + any imports)
- Test: `internal/gateway/session_title_test.go` (add behavioural tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/gateway/session_title_test.go`:
```go
import (
	// add to the existing import block:
	"testing"
	// new:
	"github.com/sausheong/felix/internal/gateway/runs"
	"github.com/sausheong/felix/internal/session"
)

func seedFirstTurn(t *testing.T, h *WebSocketHandler, agentID, key, q, a string) {
	t.Helper()
	sess, err := h.sessionStore.Load(agentID, key)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sess.Append(session.UserMessageEntry(q))
	sess.Append(session.AssistantMessageEntry(a))
}

func TestMaybeGenerateSessionTitle_WritesTitle(t *testing.T) {
	// Scripted provider returns the title text on the title call.
	h, _, _ := testHandler(t, "Deploying the release")
	scope := runs.SessionScope{AgentID: "default", SessionKey: "ws_default"}
	seedFirstTurn(t, h, scope.AgentID, scope.SessionKey, "how do I deploy?", "Run goreleaser.")

	h.maybeGenerateSessionTitle(scope)

	got := readSessionMeta(h.sessionsBaseDir, scope.AgentID, scope.SessionKey)
	if got != "Deploying the release" {
		t.Errorf("title = %q, want %q", got, "Deploying the release")
	}
}

func TestMaybeGenerateSessionTitle_SkipsWhenTitled(t *testing.T) {
	h, _, _ := testHandler(t, "Should not be used")
	scope := runs.SessionScope{AgentID: "default", SessionKey: "ws_default"}
	seedFirstTurn(t, h, scope.AgentID, scope.SessionKey, "q", "a")
	if err := writeSessionMeta(h.sessionsBaseDir, scope.AgentID, scope.SessionKey, "Manual title"); err != nil {
		t.Fatalf("writeSessionMeta: %v", err)
	}

	h.maybeGenerateSessionTitle(scope)

	if got := readSessionMeta(h.sessionsBaseDir, scope.AgentID, scope.SessionKey); got != "Manual title" {
		t.Errorf("title = %q, want unchanged %q", got, "Manual title")
	}
}

func TestMaybeGenerateSessionTitle_SkipsWithoutAssistantReply(t *testing.T) {
	h, _, _ := testHandler(t, "unused")
	scope := runs.SessionScope{AgentID: "default", SessionKey: "ws_default"}
	// Only a user message; no assistant reply.
	sess, err := h.sessionStore.Load(scope.AgentID, scope.SessionKey)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sess.Append(session.UserMessageEntry("just a question"))

	h.maybeGenerateSessionTitle(scope)

	if got := readSessionMeta(h.sessionsBaseDir, scope.AgentID, scope.SessionKey); got != "" {
		t.Errorf("title = %q, want empty (no reply yet)", got)
	}
}
```
NOTE: the file already has `import "testing"`. Merge the new imports into the
existing block rather than adding a second `import (...)`. Confirm
`runs.SessionScope` and `session.UserMessageEntry`/`AssistantMessageEntry`
exist (they're used in `websocket_chat_test.go`/`websocket_history_test.go`).

- [ ] **Step 2: Run, verify it fails**

Run: `go test ./internal/gateway/ -run TestMaybeGenerateSessionTitle`
Expected: FAIL — `h.maybeGenerateSessionTitle undefined`.

- [ ] **Step 3: Implement `maybeGenerateSessionTitle` + broadcast**

Add to `internal/gateway/session_title.go` (ensure the import block now includes
`context`, `log/slog`, `time`, `github.com/gorilla/websocket`, `runs`, `llm`,
`session`):
```go
// titleGenTimeout bounds the one-shot title model call.
const titleGenTimeout = 20 * time.Second

// firstQAndA walks history and returns the first user message text and the
// first assistant message text. ok is false if either is missing.
func firstQAndA(hist []session.SessionEntry) (q, a string, ok bool) {
	for _, e := range hist {
		if e.Type != session.EntryTypeMessage {
			continue
		}
		var md session.MessageData
		if jsonUnmarshal(e.Data, &md) != nil {
			continue
		}
		if e.Role == "user" && q == "" {
			q = md.Text
		} else if e.Role == "assistant" && a == "" {
			a = md.Text
		}
		if q != "" && a != "" {
			return q, a, true
		}
	}
	return q, a, q != "" && a != ""
}

// maybeGenerateSessionTitle best-effort-generates a display title for a
// session from its first Q&A, using the agent's own provider/model. It is a
// no-op when the session already has a title or has no assistant reply yet.
// All failures are logged and swallowed; the chat turn is never affected.
// Intended to run on the detached handleChatSend goroutine AFTER RunTurn
// returns successfully.
func (h *WebSocketHandler) maybeGenerateSessionTitle(scope runs.SessionScope) {
	// Already titled (manual rename or a prior generation) -> skip.
	if readSessionMeta(h.sessionsBaseDir, scope.AgentID, scope.SessionKey) != "" {
		return
	}

	sess, err := h.sessionStore.Load(scope.AgentID, scope.SessionKey)
	if err != nil {
		slog.Debug("title-gen: session load failed", "agent", scope.AgentID, "session", scope.SessionKey, "error", err)
		return
	}
	q, a, ok := firstQAndA(sess.History())
	if !ok {
		return // no complete first turn yet
	}

	// Snapshot provider/model under the lock (hot-reload swaps these).
	h.mu.RLock()
	cfg := h.config
	providers := h.providers
	serverCtx := h.serverCtx
	h.mu.RUnlock()
	if cfg == nil || providers == nil {
		return
	}
	agentCfg, found := cfg.GetAgent(scope.AgentID)
	if !found {
		return
	}
	providerName, modelID := llm.ParseProviderModel(agentCfg.Model)
	provider, found := providers[providerName]
	if !found {
		return
	}

	base := serverCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, titleGenTimeout)
	defer cancel()

	title, err := generateTitle(ctx, provider, modelID, q, a)
	if err != nil {
		slog.Debug("title-gen: model call failed", "agent", scope.AgentID, "session", scope.SessionKey, "error", err)
		return
	}
	title = sanitizeTitle(title)
	if title == "" {
		return
	}
	if err := validateSessionTitle(title); err != nil {
		slog.Debug("title-gen: invalid title", "title", title, "error", err)
		return
	}
	if err := writeSessionMeta(h.sessionsBaseDir, scope.AgentID, scope.SessionKey, title); err != nil {
		slog.Warn("title-gen: write meta failed", "agent", scope.AgentID, "session", scope.SessionKey, "error", err)
		return
	}
	h.broadcastSessionTitled(scope, title)
}

// titlePromptBudget caps how much of each side of the first turn we feed the
// titler so a huge first message doesn't blow context/cost.
const titlePromptBudget = 2000

// generateTitle makes the one-shot model call (mirrors compaction.Summarizer).
func generateTitle(ctx context.Context, provider llm.LLMProvider, modelID, q, a string) (string, error) {
	system := "You write a concise title for a chat session. " +
		"Given the user's first message and the assistant's reply, respond " +
		"with a title of at most 8 words. Output only the title: no quotes, " +
		"no trailing punctuation, no preamble."
	user := "First user message:\n" + truncateForTitle(q, titlePromptBudget) +
		"\n\nAssistant reply:\n" + truncateForTitle(a, titlePromptBudget)

	req := llm.ChatRequest{
		Model:             modelID,
		Messages:          []llm.Message{{Role: "user", Content: user}},
		SystemPromptParts: []llm.SystemPromptPart{{Text: system}},
		MaxTokens:         32,
	}
	stream, err := provider.ChatStream(ctx, req)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for ev := range stream {
		switch ev.Type {
		case llm.EventTextDelta:
			sb.WriteString(ev.Text)
		case llm.EventError:
			return "", ev.Error
		}
	}
	return sb.String(), nil
}

func truncateForTitle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// broadcastSessionTitled notifies every conn currently viewing this scope that
// the session got a new title, so they can refresh their sidebar. Mirrors
// BroadcastNewRun's conn-snapshot-then-send pattern.
func (h *WebSocketHandler) broadcastSessionTitled(scope runs.SessionScope, title string) {
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
		"method":  "session_titled",
		"params": map[string]any{
			"agentId":    scope.AgentID,
			"sessionKey": scope.SessionKey,
			"title":      title,
		},
	}
	for _, c := range conns {
		writeJSON(c, notif)
	}
}
```
IMPORTANT details to verify while implementing:
- Replace `jsonUnmarshal` with the actual JSON unmarshal call used elsewhere in
  the package — it's `encoding/json`'s `json.Unmarshal`. Add `"encoding/json"`
  to imports and call `json.Unmarshal(e.Data, &md)`. (The placeholder name
  `jsonUnmarshal` above must NOT survive; use `json.Unmarshal`.)
- Confirm `writeJSON(conn, v)` is the package's existing conn-writer (used by
  `BroadcastNewRun`) — reuse it, don't reimplement.
- Confirm field names `h.sessionsBaseDir`, `h.sessionStore`, `h.providers`,
  `h.config`, `h.serverCtx`, `h.activeSessionKeys`, `h.mu` against
  `websocket.go`. Fix to match if any differ.

- [ ] **Step 4: Run, verify it passes**

Run: `go test ./internal/gateway/ -run TestMaybeGenerateSessionTitle`
Expected: PASS (all three behavioural tests + the Task 1 sanitize tests).

- [ ] **Step 5: vet + race + full package**

Run: `go vet ./internal/gateway/ && go test ./internal/gateway/ && go test -race ./internal/gateway/ -run 'Title'`
Expected: clean / PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/session_title.go internal/gateway/session_title_test.go
git commit -m "feat(gateway): generate session title from first Q&A via agent model"
```

---

## Task 3: Wire the call into handleChatSend

**Files:**
- Modify: `internal/gateway/websocket.go` (`handleChatSend` goroutine, ~lines 453-463)

- [ ] **Step 1: Read the current goroutine**

Confirm the goroutine body:
```go
	go func() {
		defer h.releaseRun()
		_, err := chatexec.RunTurn(context.Background(), deps, scope, params.Text, sub)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("chatexec.RunTurn", ...)
			writeRPCError(conn, metrics, rpcID, -32603, err.Error())
		}
	}()
```

- [ ] **Step 2: Add the title-gen call on the happy path**

Change the goroutine to attempt title generation when the turn completed
without error:
```go
	go func() {
		defer h.releaseRun()
		_, err := chatexec.RunTurn(context.Background(), deps, scope, params.Text, sub)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Error("chatexec.RunTurn", "agent", scope.AgentID, "session", scope.SessionKey, "error", err)
				writeRPCError(conn, metrics, rpcID, -32603, err.Error())
			}
			return
		}
		// Best-effort: name an untitled session from its first Q&A. No-op
		// when already titled or when there's no complete first turn. Never
		// affects the turn the user just saw (done already delivered).
		h.maybeGenerateSessionTitle(scope)
	}()
```
(Preserve the existing error logging/branch semantics — only add the
post-success call and the early `return` in the error branch. Match the exact
slog fields already present.)

- [ ] **Step 3: Build + vet + tests**

Run: `go build ./... && go vet ./internal/gateway/ && go test ./internal/gateway/`
Expected: clean / PASS. (Existing chat-send tests still green — title-gen is a
no-op for sessions that error or are already titled; for a successful scripted
turn it may now also consume the scripted provider's NEXT reply. CHECK: if any
existing `handleChatSend` test asserts on `Scripted.Calls` count or exhausts
replies exactly, the extra title call could change behaviour. If so, the test
session likely ends up titled — assert/adjust as needed, or confirm those tests
don't count calls. Report any test that needed adjustment.)

- [ ] **Step 4: Commit**

```bash
git add internal/gateway/websocket.go
git commit -m "feat(gateway): trigger session-title generation after first successful turn"
```

---

## Task 4: Client handles the `session_titled` notification

**Files:**
- Modify: `internal/gateway/chat.go` (WebSocket `onmessage` dispatcher)

- [ ] **Step 1: Locate the dispatcher's notification handling**

In `chat.go`, find the `ws.onmessage`/`onMessage` handler. Determine whether it
already branches on server-initiated notifications (a parsed message with a
`method` field and no request `id`). Search for how `run_started` (sent by
`BroadcastNewRun`) is handled — if `run_started` is currently ignored, the
dispatcher likely only handles `resp.id`-keyed responses, and you must add a
`method` branch.

- [ ] **Step 2: Add the `session_titled` branch**

Near the top of the message handler, after `JSON.parse`, add (adapt variable
names to the existing code — the parsed object may be `resp` or `msg`):
```js
				// Server-initiated notifications carry a method and no
				// response id. session_titled means a session got an
				// auto-generated display name; refresh the sidebar so it
				// shows. Only refresh when it concerns the selected agent.
				if (resp.method === 'session_titled') {
					var p = resp.params || {};
					if (!agentSelect || !agentSelect.value || p.agentId === agentSelect.value) {
						loadSessions();
					}
					return;
				}
```
Place this BEFORE the `resp.id`-based response branches (a notification has no
`id`, so it must be intercepted first). If the handler already has a
`if (resp.method) { ... }` notification section, add the case there instead.

- [ ] **Step 3: Build + vet**

Run: `go build ./... && go vet ./internal/gateway/`
Expected: clean. (No `%` introduced into `chatHTML`; the added JS has none.)

- [ ] **Step 4: Full tests**

Run: `go test ./internal/gateway/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/chat.go
git commit -m "feat(gateway): refresh session list on session_titled notification"
```

---

## Task 5: Verification — full gates

- [ ] **Step 1: Whole-repo build/vet/test/race**

```bash
cd /Users/sausheong/projects/felix
go build ./... && go vet ./... && go test ./... && go test -race ./internal/gateway/
```
Expected: all green.

- [ ] **Step 2: Manual reasoning (no commit)**

Confirm by reading the diff:
- New session → first turn completes → title appears in sidebar without reload.
- Already-titled / manually-renamed session → never overwritten.
- Errored/cancelled turn → no title attempt.
- Model failure/timeout → session keeps timestamp; turn unaffected.
- Filesystem key and JSONL are never renamed.

---

## Final controller checklist (after all tasks)

- [ ] `sanitizeTitle` covers quotes/newlines/word-cap/length-clamp (unit tests green).
- [ ] `maybeGenerateSessionTitle`: writes on first untitled Q&A; skips when titled; skips without reply (tests green).
- [ ] One-shot call uses the agent's provider + **model part**, bounded timeout, best-effort.
- [ ] Title stored as the sidecar title; key/JSONL untouched.
- [ ] `session_titled` broadcast + client refresh wired; no `%` added to chatHTML.
- [ ] `go build/vet/test` + `-race` on gateway all green.
- [ ] No `Co-Authored-By` trailer.
- [ ] Adversarial review: race on concurrent first turns, provider-nil paths, scripted-call-count interactions with existing tests, prompt-injection-into-title (sanitised + validated + escHtml on render), empty/whitespace/oversized model output.
