package local

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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
	BootstrapDone
	BootstrapFailed
)

// BootstrapEvent is delivered to the callback passed to EnsureFirstRunModels.
type BootstrapEvent struct {
	Type        BootstrapEventType
	Models      []string // populated for Start, Done
	Model       string   // populated for Progress, Failed
	Percent     float32  // populated for Progress (0-100)
	DurationSec int      // populated for Done
	Error       string   // populated for Failed
}

// firstRunModels are the two defaults pulled on the first ever Felix launch.
// Order matters: embedding model first (small, brings semantic search online
// quickly) then the LLM (slow).
var firstRunModels = []string{"nomic-embed-text", "gemma4:latest"}

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
				continue
			}
			mStart := time.Now()
			err := puller.Pull(ctx, m, func(ev ProgressEvent) {
				if ev.Total > 0 {
					pct := float32(ev.Completed) / float32(ev.Total) * 100
					emit(BootstrapEvent{Type: BootstrapProgress, Model: m, Percent: pct})
				}
			})
			if err != nil {
				slog.Warn("first-run model pull failed", "model", m, "error", err)
				emit(BootstrapEvent{Type: BootstrapFailed, Model: m, Error: err.Error()})
				return // sentinel NOT written → retry on next launch
			}
			slog.Info("first-run model pulled", "model", m,
				"duration_ms", time.Since(mStart).Milliseconds())
		}

		dur := time.Since(start)
		slog.Info("first-run bootstrap complete", "duration_ms", dur.Milliseconds())

		if err := os.WriteFile(sentinel, []byte(time.Now().Format(time.RFC3339)), 0o644); err != nil {
			slog.Warn("first-run sentinel write failed", "error", err)
		}

		emit(BootstrapEvent{Type: BootstrapDone, Models: firstRunModels, DurationSec: int(dur.Seconds())})
	}()
}
