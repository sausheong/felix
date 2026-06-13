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
	// through the same store the handler reads from. Session.Append (not
	// Store.AppendEntry) is the production persistence path: it assigns
	// entry IDs, links the parentId chain, and stamps Timestamp, so the
	// handler's subsequent Load reconstructs a walkable DAG.
	sess, err := h.sessionStore.Load("default", "ws_default")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sess.Append(session.UserMessageEntry("hello there"))
	sess.Append(session.AssistantMessageEntry("hi back"))

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
