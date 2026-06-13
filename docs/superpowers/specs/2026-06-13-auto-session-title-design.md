# Auto-generated session titles Design

**Date:** 2026-06-13
**Status:** Approved
**Repo:** `felix` only (`internal/gateway`).

## Problem

A new chat session's name defaults to a timestamp (`20060102-150405`). Users
want a human-readable name instead: a short (< 10 words) summary of the first
question and its answer, produced by the chatting agent's own provider/model.

## Key architecture facts (verified against source)

- A session has TWO identifiers:
  - **key** — a filesystem path segment under `<sessionsBase>/<agentID>/`,
    holding the JSONL. Defaults to the timestamp (`handleSessionNew`,
    `websocket.go:1098-1099`). Renaming it after creation would move/orphan the
    JSONL — out of scope.
  - **title** — a display name in a `<key>.meta.json` sidecar
    (`session_meta.go`), written atomically by `writeSessionMeta`, read by
    `readSessionMeta`, surfaced in `session.list` as the `"title"` field, and
    rendered client-side as `s.title || s.key` (`chat.go:2770`). Editable via
    `session.rename`.
- Therefore the generated summary is written as the **title**; the key (and
  JSONL path) is untouched. A user's manual rename remains authoritative.
- The chat turn runs through `chatexec.RunTurn` (called from `handleChatSend`'s
  goroutine, `websocket.go:453-463`). After it returns, the full first Q and A
  are persisted in the session JSONL and readable via `Session.History()`.
- The gateway already has a per-scope client broadcast pattern
  (`BroadcastNewRun`, `websocket.go:210-232`) that sends a JSON-RPC
  notification to every conn currently viewing `(agentID, sessionKey)`.
- A one-shot model call is exactly what `compaction.Summarizer.callOnce`
  (harness `compaction/summarizer.go:96`) does: build a `llm.ChatRequest`,
  `provider.ChatStream(ctx, req)`, accumulate `EventTextDelta`, bounded by a
  context timeout. We mirror this.

## Decisions (resolved with user)

1. **Target:** set the display **title** sidecar; keep the timestamp key.
2. **Trigger:** after the first completed turn, only when the session has **no
   title yet** (don't clobber a manual rename, don't regenerate every turn).
3. **Fallback:** best-effort and fully async — any failure (model error,
   timeout, empty/invalid output) is logged and dropped; the session keeps its
   timestamp name. Title generation never affects the chat turn.

## Design

### New file: `internal/gateway/session_title.go`

```
func (h *WebSocketHandler) maybeGenerateSessionTitle(scope runs.SessionScope)
```

Runs synchronously *inside the existing chat goroutine* AFTER `RunTurn` returns
successfully (so it never blocks the turn's event stream; the goroutine is
already detached from the request). Steps:

1. **Skip if already titled.** `if readSessionMeta(h.sessionsBaseDir,
   scope.AgentID, scope.SessionKey) != "" { return }`.
2. **Load history**, extract the first user message text and the first
   assistant message text by walking `sess.History()` and unmarshalling
   `MessageData` for `EntryTypeMessage` entries by role. If either is missing
   (e.g. the turn errored before an assistant reply), return without titling.
   ALSO skip when this is not actually the first turn: count user message
   entries — if there is more than one user message, a title would have been
   attempted on the first turn already; combined with the "already titled"
   check this is belt-and-suspenders, but we still gate on "exactly the first
   user message present" to avoid re-summarising an old untitled session on its
   second message. (Concretely: only proceed when the FIRST user message and
   the FIRST assistant message exist; we summarise those two regardless of how
   many later turns exist — but the empty-title gate already prevents redoing
   it.)
3. **Resolve provider/model** from the agent config, same as chatexec:
   `agentCfg, ok := h.config.GetAgent(scope.AgentID)`;
   `providerName, _ := llm.ParseProviderModel(agentCfg.Model)`;
   `provider, ok := h.providers[providerName]`. Snapshot these under
   `h.mu.RLock()` (config/providers can be swapped by hot-reload). If unresolved,
   return.
4. **Call the model once** (mirrors summarizer): build a `llm.ChatRequest` with
   `Model: agentCfg.Model`'s model part? — NO: the provider map is keyed by
   provider name and each provider is constructed for its own models; chatexec
   passes the FULL `agentCfg.Model` is NOT what ChatStream wants. Check: the
   summarizer sets `req.Model = s.Model` where `s.Model` is the model string the
   provider expects. chatexec builds the runtime with the agent model via the
   harness; for a direct ChatStream call we must pass the **model** part
   (post-`ParseProviderModel`) because the provider is already the
   provider-specific client. Use `_, modelID := llm.ParseProviderModel(agentCfg.Model)`
   and set `req.Model = modelID`. (Verify against how compaction.Provider.For
   constructs its summarizer Model — it stores the model id, not provider/model.
   The plan's Task 1 includes a check step to confirm the exact field.)
   - System prompt: instruct a terse titler. Something like:
     "You write a concise title for a chat session. Given the user's first
     message and the assistant's reply, respond with a title of at most 8
     words. No quotes, no trailing punctuation, no preamble — only the title."
   - User message: the first Q and A, clearly delimited and each truncated to a
     sane budget (e.g. 2000 chars each) so a huge first turn doesn't blow the
     context or cost.
   - `MaxTokens: 32` (a title is tiny), bounded `context.WithTimeout` (e.g.
     20s) derived from `h.serverCtx` if set, else `context.Background()`.
   - Accumulate `EventTextDelta`; on `EventError` return.
5. **Sanitize**: trim; collapse internal whitespace/newlines to single spaces;
   strip surrounding quotes; drop a trailing period; enforce the <10-word
   intent by trimming to at most 9 words; then clamp to
   `sessionMetaMaxTitleLen` runes. If empty after sanitising, return.
6. **Validate** with `validateSessionTitle`; on error, return (log at debug).
7. **Persist** via `writeSessionMeta(h.sessionsBaseDir, scope.AgentID,
   scope.SessionKey, title)`; on error, log warn and return.
8. **Broadcast** a `session_titled` JSON-RPC notification (new method name) to
   conns viewing the scope, carrying `{agentId, sessionKey, title}` — mirrors
   `BroadcastNewRun`. Factor the "conns viewing this scope" lookup so both can
   use it, or just inline the same RLock loop.

### Call site: `handleChatSend`

In the existing goroutine (`websocket.go:453`), after `RunTurn` returns with no
error (and not a context-cancel), call `h.maybeGenerateSessionTitle(scope)`.
It must run only on the happy path — a cancelled/failed turn shouldn't title.
Because it's already on the detached goroutine, it does not delay anything the
user sees (the turn's `done` event was already delivered by RunTurn before it
returned).

### Client: handle `session_titled`

In the WebSocket `onmessage` dispatcher (where `run_started` and other
server-initiated notifications are handled — search for the notification branch;
notifications have a `method` and no matching request `id`), add a case for
`method === 'session_titled'`: call `loadSessions()` to refresh the sidebar so
the new title appears. (Simplers than patching the single row; loadSessions is
already debounced by being a cheap list call.) Guard: only refresh if the
notification's `agentId` matches the currently selected agent.

If the dispatcher does not currently handle server-initiated notifications
(only request responses keyed by `id`), add minimal handling: when a parsed
message has a `method` field, branch on it before the `id`-based response
handling.

## Testing

- **`maybeGenerateSessionTitle` happy path** (`session_title_test.go`, package
  gateway): build a handler via the existing `testHandler` with a scripted
  provider whose canned response is a title (e.g. "Deploying the release").
  Seed a session with a first user message + assistant message via
  `sess.Append`. Call `h.maybeGenerateSessionTitle(scope)`. Assert
  `readSessionMeta(...)` now returns a sanitised title.
  - NOTE: `testHandler`'s `llmtest.NewScriptedProvider` returns canned
    assistant text. Confirm it implements `llm.LLMProvider.ChatStream` so the
    title call works against it. If the scripted provider replies with the
    SAME canned string for every call, seed it with the title text and assert
    that (sanitised) becomes the title.
- **Skip when already titled:** pre-write a title via `writeSessionMeta`, call
  the function, assert the title is unchanged (the model is not consulted — can
  assert via a provider that would return a different string).
- **Skip when no assistant reply:** seed only a user message; assert no title
  written.
- **Sanitisation unit test:** a pure helper `sanitizeTitle(raw string) string`
  is unit-tested directly: quotes stripped, newlines collapsed, >9 words
  trimmed, trailing period removed, length clamped, control chars handled.

Final gates: `go build ./...`, `go vet ./...`, `go test ./...`, plus
`go test -race ./internal/gateway/`.

## Out of scope

- Renaming the filesystem key / moving the JSONL.
- Regenerating titles for existing untitled sessions retroactively (only
  newly-chatted sessions get one, on their next first-turn-without-title).
- A user-facing setting to toggle the feature (could be added later; default-on
  matches the request).
- Streaming the title token-by-token to the UI — it's written once and the
  sidebar refreshes.
