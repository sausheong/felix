package compaction

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sausheong/felix/internal/session"
)

// MaxConsecutiveFailures is the per-session circuit-breaker threshold.
// After this many consecutive autocompact attempts that drop to the
// placeholder stage (stage 3), MaybeCompact stops attempting compaction
// for the session and returns Skipped="circuit_breaker".
//
// The breaker resets on any genuine summarizer success (stage 1 or 2
// returning real content). It exists to prevent a session whose context
// is irrecoverably over the limit from hammering the API on every turn.
//
// Pattern from Claude Code MAX_CONSECUTIVE_AUTOCOMPACT_FAILURES
// (src/services/compact/autoCompact.ts:67-70).
const MaxConsecutiveFailures = 3

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
	PreserveTurns int     // K; default 4 if zero
	Threshold     float64 // fraction of context window that triggers preventive compaction (e.g. 0.6); 0 means use caller default
	// MessageCap is a hard backstop on total message count before compaction
	// fires, regardless of token threshold. 0 disables the cap. See
	// CompactionConfig.MessageCap for the rationale.
	MessageCap int

	mu    sync.Mutex             // guards locks map
	locks map[string]*sync.Mutex // session.ID → mutex

	failMu   sync.Mutex     // guards failures map; separate from mu so the breaker check (called before lockFor) doesn't serialize on the per-session lock-map allocator
	failures map[string]int // session.ID → consecutive-failure count
}

// MaybeCompact runs a compaction pass on sess if the session has more than
// K user turns. It is safe to call concurrently from multiple goroutines on
// the same session; calls serialize per-session.
//
// Errors are returned only for true unexpected failures. Routine "skip"
// outcomes (too short, empty summary, provider error) come back via
// Result.Skipped with err == nil so callers can treat them uniformly.
//
// Note: MaybeCompact holds the per-session mutex for the entire summarizer
// call (default 60s timeout). A second concurrent call on the same session
// will block until the first completes. This is intentional — it prevents
// two compactions from racing on session.Append — but callers triggering
// manual compactions while a preventive one is in flight should expect a wait.
func (m *Manager) MaybeCompact(ctx context.Context, sess *session.Session, reason Reason, instructions string) (Result, error) {
	if m == nil || m.Summarizer == nil {
		return Result{Reason: reason, Skipped: "no_summarizer"}, nil
	}

	if fc := m.failureCount(sess.ID); fc >= MaxConsecutiveFailures {
		slog.Info("compaction skipped",
			"session_id", sess.ID,
			"reason", string(reason),
			"skipped", "circuit_breaker",
			"consecutive_failures", fc)
		return Result{Reason: reason, Skipped: "circuit_breaker"}, nil
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
		// A hard error from Summarize means even stage 3 didn't run.
		// Count it as a failure for breaker accounting.
		m.incrementFailure(sess.ID)
		return Result{Reason: reason, Skipped: skipReason}, nil
	}

	// summarizeWithFallback's stage 3 returns a placeholder summary
	// (no error) when both stage 1 and stage 2 failed. We detect
	// placeholders by their stable marker phrase and treat them as
	// failures for breaker accounting; real (stage-1 or stage-2)
	// summaries reset the counter.
	isPlaceholder := strings.Contains(summary, "compaction failed and the summary could not be generated")
	if isPlaceholder {
		m.incrementFailure(sess.ID)
	} else {
		m.resetFailures(sess.ID)
	}

	first := toCompact[0]
	last := toCompact[len(toCompact)-1]
	_, toPreserve, _ := Split(view, K)
	entry := session.CompactionEntry(summary, first.ID, last.ID, m.Summarizer.Model, 0, 0, len(toCompact))
	// Splice the compaction entry between the to-be-compacted range and the
	// preserved range so View()'s walk-back from leaf hits: leaf → ... →
	// preserved[0] → compaction → STOP. Without re-linking, Append would put
	// compaction at the leaf and View() would terminate on it immediately,
	// silently dropping every preserved turn.
	entry.ParentID = toPreserve[0].ParentID
	sess.Append(entry)
	for i, e := range toPreserve {
		if i == 0 {
			e.ParentID = entry.ID
		}
		// Re-append with same ID — Session.Append's entryMap overwrite and
		// the loader's last-write-wins behaviour both make this safe.
		sess.Append(e)
	}

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

// ForgetSession removes the per-session lock and per-session failure
// counter for the given session ID. Safe to call on a nil Manager and
// on unknown sessions.
//
// Wire it via `defer mgr.ForgetSession(sess.ID)` immediately after
// constructing or loading the *session.Session at the top of any
// per-call handler that runs the agent loop (chat.send, heartbeat
// agentFn, cron agentFn). Session.ID is freshly generated by every
// session.NewSession / Store.Load — it's an in-memory instance ID,
// not a persistent identifier — so without per-call cleanup the
// locks/failures maps grow by one entry per agent turn forever.
//
// (Keying these maps by a persistent agentID+sessionKey instead of
// the per-load Session.ID would be a cleaner long-term fix, but
// would change the lock-scope semantics across reloads of the same
// persistent session — out of scope for this iteration.)
func (m *Manager) ForgetSession(sessionID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.locks, sessionID)
	m.mu.Unlock()
	m.failMu.Lock()
	delete(m.failures, sessionID)
	m.failMu.Unlock()
}

// incrementFailure bumps the per-session consecutive-failure count and
// returns the new count.
func (m *Manager) incrementFailure(sessionID string) int {
	m.failMu.Lock()
	defer m.failMu.Unlock()
	if m.failures == nil {
		m.failures = make(map[string]int)
	}
	m.failures[sessionID]++
	return m.failures[sessionID]
}

// resetFailures clears the per-session counter on a genuine success.
func (m *Manager) resetFailures(sessionID string) {
	m.failMu.Lock()
	defer m.failMu.Unlock()
	if m.failures != nil {
		delete(m.failures, sessionID)
	}
}

// failureCount returns the current per-session consecutive-failure count.
func (m *Manager) failureCount(sessionID string) int {
	m.failMu.Lock()
	defer m.failMu.Unlock()
	return m.failures[sessionID]
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
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		// Network failure to localhost Ollama → "ollama_down" (best effort).
		// More specific classification can come later.
		return "summarizer_error"
	}
}
