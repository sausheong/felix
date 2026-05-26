package chatexec

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/felix/internal/compaction"
	"github.com/sausheong/felix/internal/config"
	"github.com/sausheong/felix/internal/gateway/runs"
	"github.com/sausheong/felix/internal/llm"
	"github.com/sausheong/felix/internal/llm/llmtest"
	"github.com/sausheong/felix/internal/session"
	"github.com/sausheong/felix/internal/tools"
)

func TestSubscriberInterface(t *testing.T) {
	var s Subscriber = &recordSubscriber{}
	s.OnAttached("r1")
	s.OnEvent(runs.Event{Seq: 1, Type: runs.EventTypeTextDelta, Payload: json.RawMessage(`{}`)})
	rec := s.(*recordSubscriber)
	if rec.attachedRunID != "r1" {
		t.Fatalf("OnAttached not recorded: %s", rec.attachedRunID)
	}
	if len(rec.events) != 1 {
		t.Fatalf("OnEvent not recorded: %d", len(rec.events))
	}
}

type recordSubscriber struct {
	attachedRunID string
	events        []runs.Event
}

func (r *recordSubscriber) OnAttached(runID string) { r.attachedRunID = runID }
func (r *recordSubscriber) OnEvent(e runs.Event)    { r.events = append(r.events, e) }

// newTestDeps builds a minimal TurnDeps backed by an in-memory fake
// LLM provider and a temp-dir session store. Caller defers nothing —
// t.TempDir handles cleanup.
func newTestDeps(t *testing.T, agentID, providerName string, replies ...string) (TurnDeps, runs.SessionScope) {
	t.Helper()
	base := t.TempDir()
	sessionsBase := filepath.Join(base, "sessions")

	store := session.NewStore(sessionsBase)
	reg := runs.NewRegistry(sessionsBase)

	fake := llmtest.NewScriptedProvider(replies...)
	providers := map[string]llm.LLMProvider{providerName: fake}

	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        agentID,
			Workspace: filepath.Join(base, "workspace-"+agentID),
			Model:     providerName + "/test-model",
			Sandbox:   "none",
		},
	}
	// Disable compaction in the test so we don't need a provider entry
	// for the test model — the chat model is provided directly via
	// deps.Providers above. With Enabled=false, NewProvider returns nil,
	// and RunTurn handles nil compactionProv gracefully.
	cfg.Agents.Defaults.Compaction.Enabled = false

	toolReg := tools.NewRegistry()

	deps := TurnDeps{
		Runs:           reg,
		Sessions:       store,
		SessionsBase:   sessionsBase,
		Providers:      providers,
		Tools:          toolReg,
		Config:         cfg,
		CompactionProv: compaction.NewProvider(cfg),
		ServerCtx:      context.Background(),
	}
	scope := runs.SessionScope{AgentID: agentID, SessionKey: "default"}
	return deps, scope
}

func TestRunTurn_HappyPath(t *testing.T) {
	deps, scope := newTestDeps(t, "B", "fake", "hello there")

	rec := &recordSubscriber{}
	runID, err := RunTurn(context.Background(), deps, scope, "say hi", rec)
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if runID == "" {
		t.Fatal("expected non-empty runID")
	}
	if rec.attachedRunID != runID {
		t.Fatalf("OnAttached runID = %q, want %q", rec.attachedRunID, runID)
	}
	if len(rec.events) == 0 {
		t.Fatal("expected at least one event delivered to subscriber")
	}

	summaries, err := deps.Runs.Snapshot(scope)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var found bool
	for _, s := range summaries {
		if s.ID == runID {
			found = true
			if s.Status != runs.StatusCompleted {
				t.Fatalf("status = %s, want Completed", s.Status)
			}
		}
	}
	if !found {
		t.Fatalf("runID %s not in snapshot: %+v", runID, summaries)
	}
}

func TestRunTurn_UnknownAgent(t *testing.T) {
	deps, _ := newTestDeps(t, "B", "fake", "ok")
	scope := runs.SessionScope{AgentID: "ghost", SessionKey: "default"}
	_, err := RunTurn(context.Background(), deps, scope, "hi", nil)
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !errors.Is(err, ErrAgentNotConfigured) {
		t.Fatalf("expected ErrAgentNotConfigured, got %v", err)
	}
}

func TestRunTurn_UnknownProvider(t *testing.T) {
	deps, scope := newTestDeps(t, "B", "fake", "ok")
	// Reconfigure agent to use a provider that's not registered.
	deps.Config.Agents.List[0].Model = "ghost/test-model"
	_, err := RunTurn(context.Background(), deps, scope, "hi", nil)
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatalf("expected ErrProviderNotConfigured, got %v", err)
	}
}

func TestRunTurn_NilSubscriberOK(t *testing.T) {
	deps, scope := newTestDeps(t, "B", "fake", "x")
	_, err := RunTurn(context.Background(), deps, scope, "hi", nil)
	if err != nil {
		t.Fatalf("RunTurn with nil subscriber: %v", err)
	}
}

func TestRunTurn_ContextCancellation(t *testing.T) {
	deps, scope := newTestDeps(t, "B", "fake", "long reply")
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	_, err := RunTurn(ctx, deps, scope, "go", nil)
	// Acceptable outcomes: nil (if the entire stream landed before
	// cancellation propagated), context.Canceled, or wrapped equivalent.
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = json.RawMessage(nil) // keep encoding/json imported
}

// TestRunTurn_TraceMarkForwarded verifies that RunTurn wires the trace
// callback such that phase marks emitted during the turn are forwarded
// to deps.OnTraceMark. At minimum the "chat.received" mark RunTurn
// emits itself should fire.
func TestRunTurn_TraceMarkForwarded(t *testing.T) {
	deps, scope := newTestDeps(t, "B", "fake", "ok")

	var marks []string
	deps.OnTraceMark = func(phase string, _, _ int64, _ []any) {
		marks = append(marks, phase)
	}

	_, err := RunTurn(context.Background(), deps, scope, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(marks) == 0 {
		t.Fatal("expected at least one trace mark")
	}
	found := false
	for _, m := range marks {
		if strings.HasPrefix(m, "chat.") || strings.HasPrefix(m, "ws.") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a chat./ws. trace mark, got: %v", marks)
	}
}
