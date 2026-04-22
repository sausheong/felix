package compaction

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/sausheong/felix/internal/session"
)

// Reason identifies why compaction was triggered.
type Reason string

const (
	ReasonPreventive Reason = "preventive"
	ReasonReactive   Reason = "reactive"
	ReasonManual     Reason = "manual"
)

// Result describes the outcome of a MaybeCompact call. When Compacted is
// false, Skipped names the reason ("too_short", "empty_summary",
// "ollama_down", "model_missing", "timeout", "summarizer_error").
type Result struct {
	Compacted      bool
	Reason         Reason
	Skipped        string
	TurnsCompacted int
	TokensBefore   int
	TokensAfter    int
	Summary        string
	DurationMs     int64
}

// Manager orchestrates compaction for sessions. One Manager is shared across
// the whole agent runtime; it tracks per-session mutexes internally.
type Manager struct {
	Summarizer    *Summarizer
	PreserveTurns int // K; default 4 if zero

	mu    sync.Mutex             // guards locks map
	locks map[string]*sync.Mutex // session.ID → mutex
}

// MaybeCompact runs a compaction pass on sess if the session has more than
// K user turns. It is safe to call concurrently from multiple goroutines on
// the same session; calls serialize per-session.
//
// Errors are returned only for true unexpected failures. Routine "skip"
// outcomes (too short, empty summary, provider error) come back via
// Result.Skipped with err == nil so callers can treat them uniformly.
func (m *Manager) MaybeCompact(ctx context.Context, sess *session.Session, reason Reason, instructions string) (Result, error) {
	if m == nil || m.Summarizer == nil {
		return Result{Reason: reason, Skipped: "no_summarizer"}, nil
	}

	K := m.PreserveTurns
	if K <= 0 {
		K = 4
	}

	mu := m.lockFor(sess.ID)
	mu.Lock()
	defer mu.Unlock()

	start := time.Now()
	view := sess.View()
	toCompact, _, ok := Split(view, K)
	if !ok {
		slog.Debug("compaction skipped", "session_id", sess.ID, "reason", string(reason), "skipped", "too_short")
		return Result{Reason: reason, Skipped: "too_short"}, nil
	}

	slog.Info("compaction triggered", "session_id", sess.ID, "reason", string(reason))

	summary, err := m.Summarizer.Summarize(ctx, toCompact, instructions)
	if err != nil {
		skipReason := classifySummarizerError(err)
		slog.Warn("compaction skipped", "session_id", sess.ID, "reason", string(reason), "skipped", skipReason, "detail", err.Error())
		return Result{Reason: reason, Skipped: skipReason}, nil
	}

	first := toCompact[0]
	last := toCompact[len(toCompact)-1]
	entry := session.CompactionEntry(summary, first.ID, last.ID, m.Summarizer.Model, 0, 0, len(toCompact))
	sess.Append(entry)

	dur := time.Since(start).Milliseconds()
	slog.Info("compaction complete", "session_id", sess.ID, "reason", string(reason), "turns_compacted", len(toCompact), "duration_ms", dur)

	return Result{
		Compacted:      true,
		Reason:         reason,
		TurnsCompacted: len(toCompact),
		Summary:        summary,
		DurationMs:     dur,
	}, nil
}

func (m *Manager) lockFor(sessionID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = make(map[string]*sync.Mutex)
	}
	if mu, ok := m.locks[sessionID]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	m.locks[sessionID] = mu
	return mu
}

func classifySummarizerError(err error) string {
	switch {
	case errors.Is(err, ErrEmptySummary):
		return "empty_summary"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		// Network failure to localhost Ollama → "ollama_down" (best effort).
		// More specific classification can come later.
		return "summarizer_error"
	}
}
