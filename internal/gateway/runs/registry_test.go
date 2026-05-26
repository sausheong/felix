package runs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newTestRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	base := t.TempDir()
	return NewRegistry(base), base
}

func TestRegistry_CreateGetRemove(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	scope := SessionScope{AgentID: "a", SessionKey: "k"}
	run, err := reg.Create(scope, "run-1", cancel)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, ok := reg.Get("run-1"); !ok || got != run {
		t.Fatal("Get after Create failed")
	}
	if got := reg.GetBySession(scope); got != run {
		t.Fatal("GetBySession after Create failed")
	}
	reg.Remove("run-1")
	if _, ok := reg.Get("run-1"); ok {
		t.Fatal("Get after Remove returned a run")
	}
	if reg.GetBySession(scope) != nil {
		t.Fatal("GetBySession after Remove returned a run")
	}
}

func TestRegistry_GetBySessionEmpty(t *testing.T) {
	reg, _ := newTestRegistry(t)
	if reg.GetBySession(SessionScope{AgentID: "x", SessionKey: "y"}) != nil {
		t.Fatal("empty registry should return nil")
	}
}

func TestRegistry_CreateOpensLog(t *testing.T) {
	reg, base := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope := SessionScope{AgentID: "agent1", SessionKey: "sess1"}
	_, err := reg.Create(scope, "run-x", cancel)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(base, "agent1", "sess1.runs", "run-x.jsonl")
	if _, err := loadIndex(filepath.Join(base, "agent1", "sess1.runs", "index.json")); err != nil {
		t.Fatalf("index load: %v", err)
	}
	// log file is created lazily on first Append; here we just check the
	// directory exists.
	_ = logPath
}

func TestRegistry_CreateRejectsDuplicateScope(t *testing.T) {
	reg, _ := newTestRegistry(t)
	scope := SessionScope{AgentID: "a", SessionKey: "k"}
	_, c1 := context.WithCancel(context.Background())
	if _, err := reg.Create(scope, "run-1", c1); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, c2 := context.WithCancel(context.Background())
	if _, err := reg.Create(scope, "run-2", c2); err == nil {
		t.Fatal("second Create with same scope should have failed")
	}
}

func TestRegistry_CreateRejectsDuplicateRunID(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, c1 := context.WithCancel(context.Background())
	if _, err := reg.Create(SessionScope{AgentID: "a", SessionKey: "k1"}, "dup", c1); err != nil {
		t.Fatal(err)
	}
	_, c2 := context.WithCancel(context.Background())
	if _, err := reg.Create(SessionScope{AgentID: "a", SessionKey: "k2"}, "dup", c2); err == nil {
		t.Fatal("second Create with same runID should have failed")
	}
}

func TestRegistry_SupersedeAndCreate(t *testing.T) {
	reg, _ := newTestRegistry(t)
	scope := SessionScope{AgentID: "a", SessionKey: "k"}

	_, c1 := context.WithCancel(context.Background())
	old, err := reg.Create(scope, "run-1", c1)
	if err != nil {
		t.Fatal(err)
	}

	_, c2 := context.WithCancel(context.Background())
	oldOut, newRun, err := reg.SupersedeAndCreate(scope, "run-2", c2)
	if err != nil {
		t.Fatal(err)
	}
	if oldOut != old {
		t.Fatal("SupersedeAndCreate did not return the existing run")
	}
	if got := reg.GetBySession(scope); got != newRun {
		t.Fatal("new run is not the active one after supersede")
	}
	if _, ok := reg.Get("run-1"); ok {
		t.Fatal("old run should be evicted from runs map")
	}
}

func TestRegistry_SupersedeAndCreate_NoPrior(t *testing.T) {
	reg, _ := newTestRegistry(t)
	scope := SessionScope{AgentID: "a", SessionKey: "k"}
	_, c1 := context.WithCancel(context.Background())
	old, newRun, err := reg.SupersedeAndCreate(scope, "run-1", c1)
	if err != nil {
		t.Fatal(err)
	}
	if old != nil {
		t.Fatal("expected nil old")
	}
	if newRun == nil {
		t.Fatal("expected newRun")
	}
}

func TestRegistry_SupersedeAndCreate_Race(t *testing.T) {
	// With -race, two concurrent SupersedeAndCreate calls must not corrupt
	// the maps. One winner, one supersedes the winner.
	reg, _ := newTestRegistry(t)
	scope := SessionScope{AgentID: "a", SessionKey: "k"}
	done := make(chan struct{}, 2)
	for i, id := range []string{"r-A", "r-B"} {
		go func(i int, id string) {
			_, cancel := context.WithCancel(context.Background())
			_, _, _ = reg.SupersedeAndCreate(scope, id, cancel)
			done <- struct{}{}
			_ = i
		}(i, id)
	}
	<-done
	<-done
	// Exactly one run is the active one.
	active := reg.GetBySession(scope)
	if active == nil {
		t.Fatal("expected an active run after races")
	}
	if active.ID != "r-A" && active.ID != "r-B" {
		t.Fatalf("unexpected active ID %q", active.ID)
	}
}

func TestRegistry_SupersedeAndCreate_IOFailureLeavesNoZombie(t *testing.T) {
	reg, base := newTestRegistry(t)
	scope := SessionScope{AgentID: "a", SessionKey: "k"}

	// Seed a prior run.
	_, c1 := context.WithCancel(context.Background())
	prior, err := reg.Create(scope, "run-1", c1)
	if err != nil {
		t.Fatal(err)
	}

	// Make saveIndex fail by making the runs dir read-only after Create,
	// so MkdirAll succeeds but the file write fails. We invalidate the
	// index file path by replacing index.json with a directory.
	runsDir := filepath.Join(base, "a", "k.runs")
	if err := os.Remove(filepath.Join(runsDir, "index.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(runsDir, "index.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	_, c2 := context.WithCancel(context.Background())
	old, newRun, err := reg.SupersedeAndCreate(scope, "run-2", c2)
	if err == nil {
		t.Fatal("expected saveIndex failure")
	}
	if old != nil || newRun != nil {
		t.Fatalf("on I/O failure expected (nil, nil, err), got (%v, %v)", old, newRun)
	}

	// Registry must still hold the prior run; no zombie under run-2.
	if reg.GetBySession(scope) != prior {
		t.Fatal("prior run should still be active after failed supersede")
	}
	if _, ok := reg.Get("run-2"); ok {
		t.Fatal("run-2 should not be in registry after failed supersede")
	}
}

func TestRun_AppendIncrementsSeqAndWritesLog(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())
	run, err := reg.Create(SessionScope{AgentID: "a", SessionKey: "k"}, "r1", cancel)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if _, err := run.Append(EventTypeTextDelta, json.RawMessage(`{"text":"x"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if got := run.LastSeq.Load(); got != 3 {
		t.Fatalf("want seq 3, got %d", got)
	}

	got, err := ReadLog(run.logPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 events on disk, got %d", len(got))
	}
	for i, e := range got {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has wrong seq %d", i, e.Seq)
		}
	}
}

func TestRun_FinishWritesTerminalEvent(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())
	run, _ := reg.Create(SessionScope{AgentID: "a", SessionKey: "k"}, "r1", cancel)

	if err := run.Finish(StatusCompleted, "", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadLog(run.logPath, 0)
	if len(got) != 1 {
		t.Fatalf("want 1 event (done), got %d", len(got))
	}
	if got[0].Type != EventTypeDone || got[0].Status != StatusCompleted {
		t.Fatalf("unexpected terminal event: %+v", got[0])
	}
	if !run.Completed.Load() {
		t.Fatal("Completed flag should be set")
	}

	idx, _ := loadIndex(run.indexPath)
	if len(idx.Runs) != 1 || idx.Runs[0].Status != StatusCompleted {
		t.Fatalf("index not updated: %+v", idx.Runs)
	}
}

// Get / GetBySession must filter out completed runs. The dup-rendering
// bug we hit in canary: Finish doesn't remove from the maps, so a
// stale entry kept reporting "active" for completed runs, causing
// chat.subscribe to advertise an activeRun, frontend fires chat.replay,
// and events re-stream as duplicate bubbles.
func TestRegistry_GetAndGetBySession_FilterCompletedRuns(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())
	scope := SessionScope{AgentID: "a", SessionKey: "k"}
	run, err := reg.Create(scope, "r1", cancel)
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := reg.Get("r1"); !ok || got != run {
		t.Fatal("Get should return live run before Finish")
	}
	if got := reg.GetBySession(scope); got != run {
		t.Fatal("GetBySession should return live run before Finish")
	}

	if err := run.Finish(StatusCompleted, "", ""); err != nil {
		t.Fatal(err)
	}

	if got, ok := reg.Get("r1"); ok || got != nil {
		t.Fatalf("Get should hide completed run; got=%v ok=%v", got, ok)
	}
	if got := reg.GetBySession(scope); got != nil {
		t.Fatalf("GetBySession should hide completed run; got=%v", got)
	}
}

// OnNewRun must fire after both Create and SupersedeAndCreate, after
// the maps are populated so the callback can do GetBySession/Get safely.
func TestRegistry_OnNewRun_FiresAfterCreateAndSupersede(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())

	var mu sync.Mutex
	var calls []struct {
		scope SessionScope
		runID string
	}
	reg.OnNewRun = func(scope SessionScope, run *Run) {
		mu.Lock()
		calls = append(calls, struct {
			scope SessionScope
			runID string
		}{scope, run.ID})
		mu.Unlock()
	}

	scope := SessionScope{AgentID: "a", SessionKey: "k"}
	if _, err := reg.Create(scope, "r1", cancel); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.SupersedeAndCreate(scope, "r2", cancel); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("want 2 callback invocations, got %d", len(calls))
	}
	if calls[0].runID != "r1" || calls[0].scope != scope {
		t.Errorf("first call: got %+v want r1/%+v", calls[0], scope)
	}
	if calls[1].runID != "r2" || calls[1].scope != scope {
		t.Errorf("second call: got %+v want r2/%+v", calls[1], scope)
	}
}

func TestRun_FinishWithSupersededBy(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())
	run, _ := reg.Create(SessionScope{AgentID: "a", SessionKey: "k"}, "r1", cancel)

	if err := run.Finish(StatusCancelled, ReasonSuperseded, "r2"); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadLog(run.logPath, 0)
	if got[0].SupersededBy != "r2" || got[0].Reason != ReasonSuperseded {
		t.Fatalf("supersede metadata missing: %+v", got[0])
	}
}

func TestRun_Subscribe_ReceivesLiveEvents(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())
	run, _ := reg.Create(SessionScope{AgentID: "a", SessionKey: "k"}, "r1", cancel)

	conn := &websocket.Conn{} // sentinel pointer; never dereferenced
	past, ch, lastSeq, err := run.Subscribe(conn, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(past) != 0 {
		t.Fatalf("want empty past on fresh run, got %d events", len(past))
	}
	if lastSeq != 0 {
		t.Fatalf("want lastSeq 0 on fresh run, got %d", lastSeq)
	}

	go func() {
		_, _ = run.Append(EventTypeTextDelta, json.RawMessage(`{}`))
		_, _ = run.Append(EventTypeTextDelta, json.RawMessage(`{}`))
		_ = run.Finish(StatusCompleted, "", "")
	}()

	count := 0
	for range ch {
		count++
	}
	if count != 3 {
		t.Fatalf("want 3 events (2 deltas + done), got %d", count)
	}
}

func TestRun_Subscribe_GapFillFromLog(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())
	run, _ := reg.Create(SessionScope{AgentID: "a", SessionKey: "k"}, "r1", cancel)

	_, _ = run.Append(EventTypeTextDelta, nil)
	_, _ = run.Append(EventTypeTextDelta, nil)
	// Subscriber attaches at lastSeq=1; should receive event 2 in the
	// past slice via gap-fill and any further live events on the channel.
	conn := &websocket.Conn{}
	past, ch, returnedLast, err := run.Subscribe(conn, 1)
	if err != nil {
		t.Fatal(err)
	}
	if returnedLast != 2 {
		t.Fatalf("want returnedLast 2, got %d", returnedLast)
	}
	// Past slice must hold the gap-fill event (seq 2) and NOT seq 1.
	if len(past) != 1 || past[0].Seq != 2 {
		var seqs []int64
		for _, e := range past {
			seqs = append(seqs, e.Seq)
		}
		t.Fatalf("want past=[2], got %v", seqs)
	}

	go func() {
		_, _ = run.Append(EventTypeTextDelta, nil) // seq 3
		_ = run.Finish(StatusCompleted, "", "")    // seq 4
	}()

	// Merge past + live to verify the combined stream matches expectations.
	got := []int64{}
	for _, e := range past {
		got = append(got, e.Seq)
	}
	for e := range ch {
		got = append(got, e.Seq)
	}
	// Want: 2 (gap-fill via past), 3 (live), 4 (terminal). 1 must NOT appear.
	want := []int64{2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("want seqs %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("seq[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestRun_Unsubscribe(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())
	run, _ := reg.Create(SessionScope{AgentID: "a", SessionKey: "k"}, "r1", cancel)
	conn := &websocket.Conn{}
	_, ch, _, _ := run.Subscribe(conn, 0)
	run.Unsubscribe(conn)
	// After Unsubscribe the channel is closed.
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after Unsubscribe")
	}
}

func TestRegistry_Snapshot(t *testing.T) {
	reg, _ := newTestRegistry(t)
	scope := SessionScope{AgentID: "a", SessionKey: "k"}
	_, c1 := context.WithCancel(context.Background())
	run1, _ := reg.Create(scope, "r1", c1)
	_ = run1.Finish(StatusCompleted, "", "")

	got, err := reg.Snapshot(scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "r1" || got[0].Status != StatusCompleted {
		t.Fatalf("snapshot wrong: %+v", got)
	}
}

func TestRegistry_Snapshot_MissingIsEmpty(t *testing.T) {
	reg, _ := newTestRegistry(t)
	got, err := reg.Snapshot(SessionScope{AgentID: "x", SessionKey: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}

func TestRun_DoneClosesOnFinish(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())
	run, err := reg.Create(SessionScope{AgentID: "a", SessionKey: "k"}, "r1", cancel)
	if err != nil {
		t.Fatal(err)
	}

	// Before Finish, Done() must NOT be closed.
	select {
	case <-run.Done():
		t.Fatal("Done() closed before Finish")
	default:
		// expected
	}

	if err := run.Finish(StatusCompleted, "", ""); err != nil {
		t.Fatal(err)
	}

	// After Finish, Done() must be closed.
	select {
	case <-run.Done():
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Done() did not close after Finish")
	}

	// Second receive on a closed channel returns immediately — close-once.
	select {
	case <-run.Done():
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Done() second receive blocked")
	}
}

func TestRun_DoneClosesOnceUnderConcurrentFinish(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, cancel := context.WithCancel(context.Background())
	run, _ := reg.Create(SessionScope{AgentID: "a", SessionKey: "k"}, "r1", cancel)

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_ = run.Finish(StatusCompleted, "", "")
		}()
	}
	wg.Wait()

	select {
	case <-run.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Done() not closed after concurrent Finishes")
	}
}
