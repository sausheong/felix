package gateway

import (
	"context"
	"encoding/json"
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

// titleGenTimeout bounds the one-shot title model call.
const titleGenTimeout = 20 * time.Second

// titlePromptBudget caps how much of each side of the first turn we feed the
// titler so a huge first message doesn't blow context/cost.
const titlePromptBudget = 2000

// firstQAndA walks history and returns the first user message text and the
// first assistant message text. ok is false if either is missing.
func firstQAndA(hist []session.SessionEntry) (q, a string, ok bool) {
	for _, e := range hist {
		if e.Type != session.EntryTypeMessage {
			continue
		}
		var md session.MessageData
		if json.Unmarshal(e.Data, &md) != nil {
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
// session from its first Q&A, using the agent's own provider/model. No-op
// when the session already has a title or has no assistant reply yet. All
// failures are logged and swallowed; the chat turn is never affected.
func (h *WebSocketHandler) maybeGenerateSessionTitle(scope runs.SessionScope) {
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
		return
	}

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

	raw, err := generateTitle(ctx, provider, modelID, q, a)
	if err != nil {
		slog.Debug("title-gen: model call failed", "agent", scope.AgentID, "session", scope.SessionKey, "error", err)
		return
	}
	title := sanitizeTitle(raw)
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
