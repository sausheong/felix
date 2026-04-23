package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakePuller struct {
	mu       sync.Mutex
	have     map[string]bool // models already on disk
	pulled   []string        // pull calls in order
	failOn   string          // model name that should fail; "" = never
	failWith error
}

func (f *fakePuller) List(ctx context.Context) ([]Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Model
	for name := range f.have {
		out = append(out, Model{Name: name})
	}
	return out, nil
}

func (f *fakePuller) Pull(ctx context.Context, name string, onEvent func(ProgressEvent)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, name)
	if name == f.failOn {
		return f.failWith
	}
	if f.have == nil {
		f.have = map[string]bool{}
	}
	f.have[name] = true
	return nil
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestEnsureFirstRunModelsPullsBothInOrder(t *testing.T) {
	tmp := t.TempDir()
	puller := &fakePuller{}
	done := make(chan struct{})
	EnsureFirstRunModels(context.Background(), tmp, puller, func(ev BootstrapEvent) {
		if ev.Type == BootstrapDone || ev.Type == BootstrapFailed {
			close(done)
		}
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("bootstrap did not complete in time")
	}
	puller.mu.Lock()
	defer puller.mu.Unlock()
	if len(puller.pulled) != 2 {
		t.Fatalf("expected 2 pulls, got %d (%v)", len(puller.pulled), puller.pulled)
	}
	if puller.pulled[0] != "nomic-embed-text" {
		t.Errorf("first pull should be nomic-embed-text, got %q", puller.pulled[0])
	}
	if puller.pulled[1] != "gemma4:latest" {
		t.Errorf("second pull should be gemma4:latest, got %q", puller.pulled[1])
	}
	if _, err := os.Stat(filepath.Join(tmp, ".first-run-done")); err != nil {
		t.Errorf("sentinel not written: %v", err)
	}
}

func TestEnsureFirstRunModelsSkipsWhenSentinelExists(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".first-run-done"), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	puller := &fakePuller{}
	called := false
	EnsureFirstRunModels(context.Background(), tmp, puller, func(ev BootstrapEvent) {
		called = true
	})
	// Give the goroutine a chance to fire if it were going to.
	time.Sleep(50 * time.Millisecond)
	puller.mu.Lock()
	defer puller.mu.Unlock()
	if len(puller.pulled) != 0 {
		t.Errorf("expected 0 pulls when sentinel present, got %d", len(puller.pulled))
	}
	if called {
		t.Errorf("expected no events when sentinel present")
	}
}

func TestEnsureFirstRunModelsSkipsAlreadyPulledModels(t *testing.T) {
	tmp := t.TempDir()
	puller := &fakePuller{have: map[string]bool{"nomic-embed-text": true}}
	done := make(chan struct{})
	EnsureFirstRunModels(context.Background(), tmp, puller, func(ev BootstrapEvent) {
		if ev.Type == BootstrapDone || ev.Type == BootstrapFailed {
			close(done)
		}
	})
	if !waitFor(func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 2*time.Second) {
		t.Fatal("bootstrap did not complete in time")
	}
	puller.mu.Lock()
	defer puller.mu.Unlock()
	if len(puller.pulled) != 1 {
		t.Fatalf("expected 1 pull (skipping pre-existing nomic), got %d (%v)", len(puller.pulled), puller.pulled)
	}
	if puller.pulled[0] != "gemma4:latest" {
		t.Errorf("only missing model should be pulled, got %q", puller.pulled[0])
	}
}

func TestTrackerInitialSnapshotHasQueuedModels(t *testing.T) {
	tr := NewTracker()
	snap := tr.Snapshot()
	for _, m := range FirstRunModels() {
		st, ok := snap.Models[m]
		if !ok {
			t.Fatalf("model %q missing from initial snapshot", m)
		}
		if st.Status != "queued" {
			t.Errorf("model %q: initial status = %q, want %q", m, st.Status, "queued")
		}
	}
	if snap.Active || snap.Done {
		t.Errorf("initial snapshot active=%v done=%v, want both false", snap.Active, snap.Done)
	}
}

func TestTrackerRecordsProgressAndDone(t *testing.T) {
	tr := NewTracker()
	tr.OnEvent(BootstrapEvent{Type: BootstrapStart, Models: FirstRunModels()})
	if !tr.Snapshot().Active {
		t.Errorf("after Start, Active should be true")
	}
	tr.OnEvent(BootstrapEvent{
		Type: BootstrapProgress, Model: "nomic-embed-text",
		Percent: 42.5, Completed: 100, Total: 200,
	})
	st := tr.Snapshot().Models["nomic-embed-text"]
	if st.Status != "downloading" || st.Pct != 42.5 || st.Completed != 100 || st.Total != 200 {
		t.Errorf("progress not recorded: %+v", st)
	}
	tr.OnEvent(BootstrapEvent{Type: BootstrapDone, Models: FirstRunModels(), DurationSec: 10})
	snap := tr.Snapshot()
	if snap.Active || !snap.Done {
		t.Errorf("after Done snapshot active=%v done=%v, want active=false done=true", snap.Active, snap.Done)
	}
	for _, m := range FirstRunModels() {
		if got := snap.Models[m].Status; got != "done" {
			t.Errorf("model %q final status = %q, want %q", m, got, "done")
		}
	}
}

func TestTrackerRecordsFailure(t *testing.T) {
	tr := NewTracker()
	tr.OnEvent(BootstrapEvent{Type: BootstrapStart, Models: FirstRunModels()})
	tr.OnEvent(BootstrapEvent{Type: BootstrapFailed, Model: "gemma4:latest", Error: "boom"})
	snap := tr.Snapshot()
	if snap.Active {
		t.Errorf("after Failed, Active should be false")
	}
	st := snap.Models["gemma4:latest"]
	if st.Status != "failed" || st.Error != "boom" {
		t.Errorf("failure not recorded: %+v", st)
	}
}

func TestEnsureFirstRunModelsLeavesSentinelAbsentOnFailure(t *testing.T) {
	tmp := t.TempDir()
	puller := &fakePuller{failOn: "gemma4:latest", failWith: errors.New("network down")}
	done := make(chan struct{})
	EnsureFirstRunModels(context.Background(), tmp, puller, func(ev BootstrapEvent) {
		if ev.Type == BootstrapDone || ev.Type == BootstrapFailed {
			close(done)
		}
	})
	if !waitFor(func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 2*time.Second) {
		t.Fatal("bootstrap did not complete in time")
	}
	if _, err := os.Stat(filepath.Join(tmp, ".first-run-done")); !os.IsNotExist(err) {
		t.Errorf("sentinel must NOT be written on failure (err=%v)", err)
	}
}
