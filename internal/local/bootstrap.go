package local

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Puller is the subset of *Installer used by the bootstrap goroutine.
// Defined as an interface so tests can inject a fake.
type Puller interface {
	List(ctx context.Context) ([]Model, error)
	Pull(ctx context.Context, name string, onEvent func(ProgressEvent)) error
}

// BootstrapEventType identifies a bootstrap-progress event.
type BootstrapEventType int

const (
	BootstrapStart BootstrapEventType = iota
	BootstrapProgress
	BootstrapModelDone // one model finished pulling (more may follow)
	BootstrapDone      // all models finished
	BootstrapFailed
)

// BootstrapEvent is delivered to the callback passed to EnsureFirstRunModels.
type BootstrapEvent struct {
	Type        BootstrapEventType
	Models      []string // populated for Start, Done
	Model       string   // populated for Progress, Failed
	Percent     float32  // populated for Progress (0-100)
	Completed   int64    // populated for Progress (bytes pulled so far)
	Total       int64    // populated for Progress (expected total bytes)
	DurationSec int      // populated for Done
	Error       string   // populated for Failed
}

// firstRunModels are the two defaults pulled on the first ever Felix launch.
// Order matters: embedding model first (small, brings semantic search online
// quickly) then the LLM (slow).
var firstRunModels = []string{"nomic-embed-text", "gemma4:latest"}

// FirstRunModels returns a copy of the first-run model list so other packages
// (e.g. the gateway) can flag them in UI without hardcoding the names.
func FirstRunModels() []string {
	out := make([]string, len(firstRunModels))
	copy(out, firstRunModels)
	return out
}

// ModelStatus is one model's current bootstrap state, suitable for JSON output.
type ModelStatus struct {
	Status    string  `json:"status"`              // queued | downloading | done | failed
	Completed int64   `json:"completed,omitempty"` // bytes pulled so far
	Total     int64   `json:"total,omitempty"`     // expected total bytes
	Pct       float32 `json:"pct,omitempty"`       // 0–100
	Error     string  `json:"error,omitempty"`     // populated on failed
}

// BootstrapSnapshot is the public view returned by Tracker.Snapshot().
type BootstrapSnapshot struct {
	Active bool                   `json:"active"` // true while a pull is in flight
	Done   bool                   `json:"done"`   // true once Done event observed
	Models map[string]ModelStatus `json:"models"`
}

// Tracker records the most recent BootstrapEvent for each first-run model so
// the gateway can surface progress to the Settings → Models tab.
type Tracker struct {
	mu     sync.RWMutex
	models map[string]ModelStatus
	active bool
	done   bool
}

// NewTracker returns a Tracker pre-seeded with each first-run model in the
// "queued" state. Use OnEvent as the callback to EnsureFirstRunModels.
func NewTracker() *Tracker {
	t := &Tracker{models: make(map[string]ModelStatus)}
	for _, m := range firstRunModels {
		t.models[m] = ModelStatus{Status: "queued"}
	}
	return t
}

// OnEvent matches the signature expected by EnsureFirstRunModels.
func (t *Tracker) OnEvent(ev BootstrapEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch ev.Type {
	case BootstrapStart:
		t.active = true
	case BootstrapProgress:
		st := t.models[ev.Model]
		st.Status = "downloading"
		st.Pct = ev.Percent
		st.Completed = ev.Completed
		st.Total = ev.Total
		t.models[ev.Model] = st
	case BootstrapModelDone:
		st := t.models[ev.Model]
		st.Status = "done"
		st.Pct = 100
		t.models[ev.Model] = st
	case BootstrapDone:
		t.active = false
		t.done = true
		for _, m := range ev.Models {
			st := t.models[m]
			if st.Status != "failed" {
				st.Status = "done"
				st.Pct = 100
			}
			t.models[m] = st
		}
	case BootstrapFailed:
		t.active = false
		st := t.models[ev.Model]
		st.Status = "failed"
		st.Error = ev.Error
		t.models[ev.Model] = st
	}
}

// Snapshot returns a deep copy of the current tracker state.
func (t *Tracker) Snapshot() BootstrapSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := BootstrapSnapshot{
		Active: t.active,
		Done:   t.done,
		Models: make(map[string]ModelStatus, len(t.models)),
	}
	for k, v := range t.models {
		out.Models[k] = v
	}
	return out
}

// EnsureFirstRunModels kicks off background pulls of the default LLM and
// embedding model on the first ever Felix run. If `dataDir/.first-run-done`
// exists the function returns immediately. Otherwise it spawns a goroutine
// that pulls each missing default sequentially via puller. The sentinel is
// written only on full success — partial failures retry on the next launch.
//
// onEvent is called on the goroutine; pass nil to discard events.
func EnsureFirstRunModels(ctx context.Context, dataDir string, puller Puller, onEvent func(BootstrapEvent)) {
	sentinel := filepath.Join(dataDir, ".first-run-done")
	if _, err := os.Stat(sentinel); err == nil {
		return // already bootstrapped
	}

	emit := func(ev BootstrapEvent) {
		if onEvent != nil {
			onEvent(ev)
		}
	}

	go func() {
		emit(BootstrapEvent{Type: BootstrapStart, Models: firstRunModels})
		slog.Info("first-run bootstrap start", "models", firstRunModels)
		start := time.Now()

		// Find which models are already on disk so we don't re-pull.
		have := map[string]bool{}
		if list, err := puller.List(ctx); err == nil {
			for _, m := range list {
				have[m.Name] = true
			}
		}

		for _, m := range firstRunModels {
			if have[m] {
				slog.Info("first-run model already present", "model", m)
				emit(BootstrapEvent{Type: BootstrapModelDone, Model: m})
				continue
			}
			mStart := time.Now()
			err := puller.Pull(ctx, m, func(ev ProgressEvent) {
				if ev.Total > 0 {
					pct := float32(ev.Completed) / float32(ev.Total) * 100
					emit(BootstrapEvent{
						Type:      BootstrapProgress,
						Model:     m,
						Percent:   pct,
						Completed: ev.Completed,
						Total:     ev.Total,
					})
				}
			})
			if err != nil {
				slog.Warn("first-run model pull failed", "model", m, "error", err)
				emit(BootstrapEvent{Type: BootstrapFailed, Model: m, Error: err.Error()})
				return // sentinel NOT written → retry on next launch
			}
			slog.Info("first-run model pulled", "model", m,
				"duration_ms", time.Since(mStart).Milliseconds())
			emit(BootstrapEvent{Type: BootstrapModelDone, Model: m})
		}

		dur := time.Since(start)
		slog.Info("first-run bootstrap complete", "duration_ms", dur.Milliseconds())

		if err := os.WriteFile(sentinel, []byte(time.Now().Format(time.RFC3339)), 0o644); err != nil {
			slog.Warn("first-run sentinel write failed", "error", err)
		}

		emit(BootstrapEvent{Type: BootstrapDone, Models: firstRunModels, DurationSec: int(dur.Seconds())})
	}()
}
