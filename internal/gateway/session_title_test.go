package gateway

import (
	"testing"

	"github.com/sausheong/felix/internal/gateway/runs"
	"github.com/sausheong/felix/internal/session"
)

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

func TestMaybeGenerateSessionTitle_SkipsOnEmptyModelOutput(t *testing.T) {
	// No scripted replies -> the title model call yields "" -> sanitizeTitle
	// returns "" -> the best-effort path must NOT write a sidecar.
	h, _, _ := testHandler(t) // no replies
	scope := runs.SessionScope{AgentID: "default", SessionKey: "ws_default"}
	seedFirstTurn(t, h, scope.AgentID, scope.SessionKey, "how do I deploy?", "Run goreleaser.")

	h.maybeGenerateSessionTitle(scope)

	if got := readSessionMeta(h.sessionsBaseDir, scope.AgentID, scope.SessionKey); got != "" {
		t.Errorf("title = %q, want empty (model produced no usable output)", got)
	}
}
