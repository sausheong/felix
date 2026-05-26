package runs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestIntegration_RunSurvivesSubscriberDisconnect verifies the regression
// guarantee from Tasks 11-13: a run, once created, continues to execute
// even if the WebSocket connection that started it disconnects. This is
// the load-bearing property that decouples run lifetime from connection
// lifetime.
func TestIntegration_RunSurvivesSubscriberDisconnect(t *testing.T) {
	base := t.TempDir()
	reg := NewRegistry(base)

	_, runCancel := context.WithCancel(context.Background())
	scope := SessionScope{AgentID: "a", SessionKey: "k"}
	run, err := reg.Create(scope, "r1", runCancel)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a connection subscribing to the run.
	conn := &websocket.Conn{} // sentinel
	_, ch, _, err := run.Subscribe(conn, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Background: drain a few events into the run.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3; i++ {
			time.Sleep(10 * time.Millisecond)
			_, _ = run.Append(EventTypeTextDelta, []byte(`{"text":"x"}`))
		}
		_ = run.Finish(StatusCompleted, "", "")
	}()

	// Read the first event, then "disconnect" by calling UnsubscribeAll
	// (what cleanupConnection does in production).
	<-ch
	reg.UnsubscribeAll(conn)

	// Wait for the run to complete on its own.
	select {
	case <-done:
		// Good — the producer finished even though the subscriber was gone.
	case <-time.After(2 * time.Second):
		t.Fatal("run did not complete after subscriber disconnect")
	}

	// Verify the on-disk log has all events including the terminal one.
	summaries, err := reg.Snapshot(scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Status != StatusCompleted {
		t.Fatalf("expected one completed run, got: %+v", summaries)
	}
	events, err := ReadLog(filepath.Join(base, "a", "k.runs", "r1.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	// 3 deltas + 1 terminal done
	if len(events) != 4 {
		t.Fatalf("expected 4 events on disk, got %d: %+v", len(events), events)
	}
	if events[3].Type != EventTypeDone || events[3].Status != StatusCompleted {
		t.Fatalf("last event should be Done/Completed: %+v", events[3])
	}
}

// TestIntegration_SupersedeFullLifecycle exercises the supersede flow
// end-to-end: a second run replaces a first, the first transitions to
// cancelled with superseded_by metadata, and the second completes cleanly.
func TestIntegration_SupersedeFullLifecycle(t *testing.T) {
	base := t.TempDir()
	reg := NewRegistry(base)
	scope := SessionScope{AgentID: "a", SessionKey: "k"}

	// Run 1
	_, c1 := context.WithCancel(context.Background())
	old, run1, err := reg.SupersedeAndCreate(scope, "r1", c1)
	if err != nil {
		t.Fatal(err)
	}
	if old != nil {
		t.Fatal("no prior run, want nil old")
	}
	_, _ = run1.Append(EventTypeTextDelta, []byte(`{"text":"first"}`))

	// Run 2 — supersedes run 1
	_, c2 := context.WithCancel(context.Background())
	old, run2, err := reg.SupersedeAndCreate(scope, "r2", c2)
	if err != nil {
		t.Fatal(err)
	}
	if old != run1 {
		t.Fatal("expected SupersedeAndCreate to return run1 as old")
	}

	// Caller's job (per spec): Finish old with superseded_by + CancelFn.
	_ = old.Finish(StatusCancelled, ReasonSuperseded, "r2")
	c1() // run1's context cancel

	_, _ = run2.Append(EventTypeTextDelta, []byte(`{"text":"second"}`))
	_ = run2.Finish(StatusCompleted, "", "")

	summaries, err := reg.Snapshot(scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 run summaries, got %d", len(summaries))
	}

	var s1, s2 *RunSummary
	for i := range summaries {
		switch summaries[i].ID {
		case "r1":
			s1 = &summaries[i]
		case "r2":
			s2 = &summaries[i]
		}
	}
	if s1 == nil || s2 == nil {
		t.Fatalf("missing summaries: r1=%v r2=%v", s1, s2)
	}
	if s1.Status != StatusCancelled || s1.SupersededBy != "r2" {
		t.Fatalf("r1 wrong terminal state: %+v", s1)
	}
	if s2.Status != StatusCompleted {
		t.Fatalf("r2 should be completed: %+v", s2)
	}
}

// TestIntegration_LateSubscriberGetsHistoryViaGapFill verifies that a
// subscriber attaching mid-run with fromSeq=0 receives the full event
// history (via gap-fill from the on-disk log) plus any subsequent live
// events, in monotonic order.
func TestIntegration_LateSubscriberGetsHistoryViaGapFill(t *testing.T) {
	base := t.TempDir()
	reg := NewRegistry(base)

	_, cancel := context.WithCancel(context.Background())
	scope := SessionScope{AgentID: "a", SessionKey: "k"}
	run, _ := reg.Create(scope, "r1", cancel)

	// First subscriber drains continuously (simulates the chat.send caller).
	firstConn := &websocket.Conn{}
	_, firstCh, _, _ := run.Subscribe(firstConn, 0)
	go func() {
		for range firstCh {
		}
	}()

	// Producer runs concurrently with the late subscriber.
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for i := 0; i < 5; i++ {
			_, _ = run.Append(EventTypeTextDelta, []byte(`{}`))
			time.Sleep(5 * time.Millisecond)
		}
		_ = run.Finish(StatusCompleted, "", "")
	}()

	// Late subscriber attaches mid-stream.
	time.Sleep(15 * time.Millisecond)
	lateConn := &websocket.Conn{}
	latePast, lateCh, _, err := run.Subscribe(lateConn, 0)
	if err != nil {
		t.Fatalf("late Subscribe failed: %v", err)
	}

	// Past (gap-fill) events come back as a slice now — no buffer overflow
	// risk regardless of how many events have already accumulated on disk.
	var lateSeqs []int64
	for _, e := range latePast {
		lateSeqs = append(lateSeqs, e.Seq)
	}
	for e := range lateCh {
		lateSeqs = append(lateSeqs, e.Seq)
	}
	<-producerDone

	if len(lateSeqs) == 0 {
		t.Fatal("late subscriber got no events")
	}
	// Monotonic seq — no duplicates.
	for i := 1; i < len(lateSeqs); i++ {
		if lateSeqs[i] <= lateSeqs[i-1] {
			t.Fatalf("late subscriber duplicate/out-of-order at index %d: %v", i, lateSeqs)
		}
	}
	// First seq should be >=1 (no holes from fromSeq=0).
	if lateSeqs[0] < 1 {
		t.Fatalf("late subscriber first seq < 1: %v", lateSeqs)
	}
}

// TestIntegration_LateSubscribeNoDuplicateAtBoundary stress-tests the
// concurrent Subscribe / Append boundary. Without the lock-across-read
// fix, ~50% of iterations would see the boundary event delivered twice.
func TestIntegration_LateSubscribeNoDuplicateAtBoundary(t *testing.T) {
	for iter := 0; iter < 50; iter++ {
		base := t.TempDir()
		reg := NewRegistry(base)
		_, cancel := context.WithCancel(context.Background())
		run, _ := reg.Create(SessionScope{AgentID: "a", SessionKey: "k"}, "r1", cancel)

		// Producer: write events as fast as possible.
		producerDone := make(chan struct{})
		go func() {
			defer close(producerDone)
			for i := 0; i < 20; i++ {
				_, _ = run.Append(EventTypeTextDelta, []byte(`{}`))
			}
			_ = run.Finish(StatusCompleted, "", "")
		}()

		// Tiny sleep so some events have written before subscribe.
		time.Sleep(time.Microsecond * 100)

		conn := &websocket.Conn{}
		past, ch, _, err := run.Subscribe(conn, 0)
		if err != nil {
			t.Fatalf("iter %d: Subscribe failed: %v", iter, err)
		}

		// Drain past + live and assert strictly monotonic seqs across the
		// merged stream — the boundary event must not appear in both.
		var prev int64 = 0
		for _, e := range past {
			if e.Seq <= prev {
				t.Fatalf("iter %d: non-monotonic past seq — prev=%d cur=%d (duplicate or out-of-order)", iter, prev, e.Seq)
			}
			prev = e.Seq
		}
		for e := range ch {
			if e.Seq <= prev {
				t.Fatalf("iter %d: non-monotonic seq across past/live boundary — prev=%d cur=%d (duplicate or out-of-order)", iter, prev, e.Seq)
			}
			prev = e.Seq
		}

		<-producerDone
	}
}
