package runs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Registry is the process-wide map of in-flight runs.
type Registry struct {
	mu          sync.Mutex
	runs        map[string]*Run       // runID → Run
	bySession   map[SessionScope]*Run // (agent,session) → active run (at most one)
	sessionsDir string

	// OnNewRun is invoked (outside the registry lock) after Create /
	// SupersedeAndCreate has registered a new run. Lets the gateway push
	// a "run_started" notification so open WS conns viewing the same
	// scope can attach via chat.replay — without this, background runs
	// (inbox wake-loop, cron) generate events to disk that no live
	// client ever sees. Nil-safe.
	OnNewRun func(scope SessionScope, run *Run)
}

// Run is one in-flight chat turn whose lifetime is decoupled from
// any WebSocket connection.
type Run struct {
	ID        string
	Scope     SessionScope
	StartedAt time.Time
	CancelFn  context.CancelFunc
	LastSeq   atomic.Int64
	Completed atomic.Bool

	mu          sync.Mutex
	log         *logWriter
	closed      bool // true after Finish closes log; guarded by mu
	subscribers map[*websocket.Conn]*subscriber
	indexPath   string
	logPath     string
	done        chan struct{} // closed by Finish under Completed CAS
}

type subscriber struct {
	ch chan Event // bounded; drain goroutine drops on full
}

// NewRegistry returns a registry rooted at sessionsDir (typically
// ~/.felix/sessions). All on-disk paths are derived from this.
func NewRegistry(sessionsDir string) *Registry {
	return &Registry{
		runs:        map[string]*Run{},
		bySession:   map[SessionScope]*Run{},
		sessionsDir: sessionsDir,
	}
}

// runsDir returns <sessionsDir>/<agent>/<key>.runs
func (r *Registry) runsDir(scope SessionScope) string {
	return filepath.Join(r.sessionsDir, scope.AgentID, scope.SessionKey+".runs")
}

// Create allocates a new Run in state running, opens its log file,
// updates the on-disk index, and inserts into both maps. Returns an
// error if a run already exists for the given scope or runID — use
// SupersedeAndCreate (Task 5) when you need to replace an in-flight run.
func (r *Registry) Create(scope SessionScope, runID string, cancel context.CancelFunc) (*Run, error) {
	// Pre-check (cheap, lock held briefly): fail loud if scope is occupied.
	r.mu.Lock()
	if _, exists := r.bySession[scope]; exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("run already in flight for scope %+v", scope)
	}
	if _, exists := r.runs[runID]; exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("run %q already exists", runID)
	}
	r.mu.Unlock()

	// I/O outside the lock.
	dir := r.runsDir(scope)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir runs: %w", err)
	}
	logPath := filepath.Join(dir, runID+".jsonl")
	indexPath := filepath.Join(dir, "index.json")

	lw, err := openLogWriter(logPath)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}

	run := &Run{
		ID:          runID,
		Scope:       scope,
		StartedAt:   time.Now().UTC(),
		CancelFn:    cancel,
		log:         lw,
		subscribers: map[*websocket.Conn]*subscriber{},
		indexPath:   indexPath,
		logPath:     logPath,
		done:        make(chan struct{}),
	}

	idx, _ := loadIndex(indexPath)
	idx.Upsert(RunSummary{
		ID:        runID,
		StartedAt: run.StartedAt.Format(time.RFC3339Nano),
		Status:    StatusRunning,
	})
	if err := saveIndex(indexPath, idx); err != nil {
		_ = lw.Close()
		return nil, fmt.Errorf("save index: %w", err)
	}

	// Final commit to maps — re-check under lock in case of a race
	// between pre-check and now.
	r.mu.Lock()
	if _, exists := r.bySession[scope]; exists {
		r.mu.Unlock()
		_ = lw.Close()
		return nil, fmt.Errorf("run already in flight for scope %+v (race)", scope)
	}
	if _, exists := r.runs[runID]; exists {
		r.mu.Unlock()
		_ = lw.Close()
		return nil, fmt.Errorf("run %q already exists (race)", runID)
	}
	r.runs[runID] = run
	r.bySession[scope] = run
	cb := r.OnNewRun
	r.mu.Unlock()
	if cb != nil {
		cb(scope, run)
	}
	return run, nil
}

// Get returns a Run by ID, ok=false if not found OR if the run has
// completed. Callers that want a Run regardless of completion state
// should not exist — chat.replay falls through to disk-log replay when
// Get returns false, which is the correct behavior for a finished run.
// Run.Finish does not remove from the maps (see comment on bySession),
// so this filter is what keeps "active" honest.
func (r *Registry) Get(runID string) (*Run, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok || run.Completed.Load() {
		return nil, false
	}
	return run, true
}

// GetBySession returns the in-flight run for scope, or nil if none is
// active OR the latest one has completed. See Get's comment for why this
// filter on Completed exists rather than removing from the map in Finish.
func (r *Registry) GetBySession(scope SessionScope) *Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.bySession[scope]
	if run == nil || run.Completed.Load() {
		return nil
	}
	return run
}

// SupersedeAndCreate atomically evicts any in-flight run for scope and
// inserts a new one with runID. Returns (oldRun, newRun, err). oldRun is
// nil if there was no prior run. The caller is responsible for writing
// the "superseded" terminal event on oldRun and calling oldRun.CancelFn.
func (r *Registry) SupersedeAndCreate(scope SessionScope, runID string, cancel context.CancelFunc) (*Run, *Run, error) {
	// All I/O first. If any of this fails, the registry maps are unchanged.
	dir := r.runsDir(scope)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("mkdir runs: %w", err)
	}
	logPath := filepath.Join(dir, runID+".jsonl")
	indexPath := filepath.Join(dir, "index.json")

	lw, err := openLogWriter(logPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open log: %w", err)
	}

	newRun := &Run{
		ID:          runID,
		Scope:       scope,
		StartedAt:   time.Now().UTC(),
		CancelFn:    cancel,
		log:         lw,
		subscribers: map[*websocket.Conn]*subscriber{},
		indexPath:   indexPath,
		logPath:     logPath,
		done:        make(chan struct{}),
	}

	idx, _ := loadIndex(indexPath)
	idx.Upsert(RunSummary{
		ID:        runID,
		StartedAt: newRun.StartedAt.Format(time.RFC3339Nano),
		Status:    StatusRunning,
	})
	if err := saveIndex(indexPath, idx); err != nil {
		_ = lw.Close()
		return nil, nil, fmt.Errorf("save index: %w", err)
	}

	// I/O succeeded — now do the atomic map swap.
	r.mu.Lock()
	old := r.bySession[scope]
	if old != nil {
		delete(r.runs, old.ID)
	}
	r.runs[runID] = newRun
	r.bySession[scope] = newRun
	cb := r.OnNewRun
	r.mu.Unlock()
	if cb != nil {
		cb(scope, newRun)
	}

	return old, newRun, nil
}

// Append writes a non-terminal event to the run's log and fans out to
// every subscriber via non-blocking send. MUST be called only from the
// run's drain goroutine (single-writer invariant on logWriter).
// Returns the assigned sequence number.
//
// IMPLEMENTATION NOTE: Append holds r.mu across LastSeq.Add, log.Append,
// and fanout so that r.LastSeq accurately reflects "the seq of the last
// event that has been written to disk AND delivered via fanout". This
// is what makes Subscribe's gap-fill safe: under r.mu, Subscribe can
// read lastSeq, then read the disk log, knowing that any event with
// seq <= lastSeq has already been fanned out to existing subscribers
// (and won't be re-fanned to this new subscriber), and any event with
// seq > lastSeq is not yet on disk (so gap-fill can't see it).
func (r *Run) Append(t EventType, payload []byte) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Completed.Load() || r.closed {
		return 0, fmt.Errorf("run %s already completed", r.ID)
	}
	seq := r.LastSeq.Add(1)
	e := Event{
		Seq:     seq,
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Type:    t,
		Payload: payload,
	}
	if err := r.log.Append(e); err != nil {
		return seq, err
	}
	r.fanoutLocked(e)
	return seq, nil
}

// Finish writes a terminal "done" event, updates the index, and closes
// the log. fanout still happens so subscribers see the terminal event.
// Safe to call once; subsequent calls are no-ops.
func (r *Run) Finish(status Status, reason CancelReason, supersededBy string) error {
	if !r.Completed.CompareAndSwap(false, true) {
		return nil
	}
	close(r.done) // signal Done() listeners; safe — CAS ran exactly once

	r.mu.Lock()
	seq := r.LastSeq.Add(1)
	e := Event{
		Seq:          seq,
		Ts:           time.Now().UTC().Format(time.RFC3339Nano),
		Type:         EventTypeDone,
		Status:       status,
		Reason:       reason,
		SupersededBy: supersededBy,
	}
	logErr := r.log.Append(e)
	_ = r.log.Close()
	r.closed = true
	r.mu.Unlock()

	// Persist terminal index BEFORE notifying subscribers, so the
	// "done" signal is the last thing that happens. A subscriber that
	// sees the closed channel can then read a consistent terminal
	// status from disk without racing the writer.
	idx, _ := loadIndex(r.indexPath)
	idx.Upsert(RunSummary{
		ID:           r.ID,
		StartedAt:    r.StartedAt.Format(time.RFC3339Nano),
		EndedAt:      e.Ts,
		Status:       status,
		LastSeq:      seq,
		SupersededBy: supersededBy,
	})
	saveErr := saveIndex(r.indexPath, idx)

	// Now signal subscribers — they will read the terminal index above.
	r.fanout(e)
	r.closeAllSubscribers()

	if saveErr != nil {
		return fmt.Errorf("save terminal index: %w", saveErr)
	}
	return logErr
}

// Done returns a channel that is closed when Finish runs. Callers can
// wait for run completion without polling. The channel is also returned
// already-closed if Finish ran before Done was called.
func (r *Run) Done() <-chan struct{} {
	return r.done
}

// fanout sends e to every subscriber's channel non-blocking. A full
// channel drops the subscriber (slow client; will reconnect + replay).
func (r *Run) fanout(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fanoutLocked(e)
}

// fanoutLocked is fanout's body; r.mu must already be held.
func (r *Run) fanoutLocked(e Event) {
	for conn, sub := range r.subscribers {
		select {
		case sub.ch <- e:
		default:
			close(sub.ch)
			delete(r.subscribers, conn)
		}
	}
}

func (r *Run) closeAllSubscribers() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for conn, sub := range r.subscribers {
		close(sub.ch)
		delete(r.subscribers, conn)
	}
}

// subscriberBuffer sizes the live event channel handed out by Subscribe.
// 256 gives a single LLM response with tool use (dozens of text-delta
// events arriving in <100ms while the forwarder slowly writes each over
// the WebSocket) plenty of breathing room without unbounded memory.
// Subscribers that still fall behind this much are likely stuck and get
// dropped by fanout, which is the right behaviour — they can reconnect
// and replay.
const subscriberBuffer = 256

// Subscribe attaches conn to the run's live event stream. fromSeq is the
// caller's high-water mark. Past events with seq in (fromSeq, lastSeq]
// are returned as a slice; live events arrive on the returned channel.
// The caller is responsible for streaming `past` to its consumer before
// starting to drain `live` (otherwise live events would arrive before
// past ones).
//
// Returns (past, live, lastSeqAtAttach, error). The live channel is
// closed by Unsubscribe, by Finish, or by fan-out drop when the
// subscriber is too slow.
//
// Holds r.mu across the disk read of the log file so that no concurrent
// Append can fan out the boundary event (seq == lastSeq) to this
// subscriber both via the past slice AND via live delivery.
func (r *Run) Subscribe(conn *websocket.Conn, fromSeq int64) ([]Event, <-chan Event, int64, error) {
	sub := &subscriber{ch: make(chan Event, subscriberBuffer)}

	r.mu.Lock()
	defer r.mu.Unlock()

	lastSeq := r.LastSeq.Load()

	var past []Event
	if lastSeq > fromSeq {
		all, err := ReadLog(r.logPath, fromSeq)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("gap-fill: %w", err)
		}
		// Filter out events with seq > lastSeq (those are or will be
		// delivered live to other subscribers and will be delivered
		// live to us once we register below).
		past = make([]Event, 0, len(all))
		for _, e := range all {
			if e.Seq > lastSeq {
				continue
			}
			past = append(past, e)
		}
	}

	// Terminal short-circuit: if the run has already finished (Finish set
	// r.closed under this same r.mu after writing the terminal event to
	// disk), then `past` already contains every event including the
	// terminal one. Return an already-closed live channel and do NOT
	// register a subscriber — otherwise Finish's fanout (which runs in a
	// later, separate lock acquisition) would deliver the terminal event a
	// second time, duplicating it across the past/live boundary.
	if r.closed {
		closed := make(chan Event)
		close(closed)
		return past, closed, lastSeq, nil
	}

	// Double-subscribe guard: if conn already has a subscriber on this run,
	// close its channel so the orphaned forwardEvents goroutine exits.
	// Otherwise the old goroutine blocks on a now-unreachable channel until
	// Finish.
	if old, ok := r.subscribers[conn]; ok {
		close(old.ch)
	}
	r.subscribers[conn] = sub
	return past, sub.ch, lastSeq, nil
}

// Unsubscribe removes conn from the subscriber list and closes its channel.
func (r *Run) Unsubscribe(conn *websocket.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sub, ok := r.subscribers[conn]
	if !ok {
		return
	}
	close(sub.ch)
	delete(r.subscribers, conn)
}

// UnsubscribeAll is called by the WS handler on connection cleanup.
func (reg *Registry) UnsubscribeAll(conn *websocket.Conn) {
	reg.mu.Lock()
	runs := make([]*Run, 0, len(reg.runs))
	for _, run := range reg.runs {
		runs = append(runs, run)
	}
	reg.mu.Unlock()
	for _, run := range runs {
		run.Unsubscribe(conn)
	}
}

// Snapshot returns the current run summaries for scope from disk.
// Missing index file → empty slice.
func (reg *Registry) Snapshot(scope SessionScope) ([]RunSummary, error) {
	indexPath := filepath.Join(reg.runsDir(scope), "index.json")
	idx, err := loadIndex(indexPath)
	if err != nil {
		return nil, err
	}
	return idx.Runs, nil
}

// Remove drops the run from both maps. Caller is expected to have
// already closed the log and written the terminal index entry.
func (r *Registry) Remove(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return
	}
	delete(r.runs, runID)
	if r.bySession[run.Scope] == run {
		delete(r.bySession, run.Scope)
	}
}

// DeleteRun removes a completed run from disk: deletes the per-run
// <runID>.jsonl log file (best-effort; failures are logged) and rewrites
// index.json without the row (atomic via WriteFileAtomic). Returns an
// error if the run is currently in-flight — callers must wait for or
// cancel an active run before deleting.
//
// File-delete failures are non-fatal: a missing log after this returns
// nil leaves the index entry gone, so ReadLog on the path returns
// (nil, nil) on the next access. This is the safer order than
// "rewrite index first, then delete file" — a crash between the two
// would leave the index referencing a present log.
func (reg *Registry) DeleteRun(scope SessionScope, runID string) error {
	reg.mu.Lock()
	if run, ok := reg.runs[runID]; ok && !run.Completed.Load() {
		reg.mu.Unlock()
		return fmt.Errorf("cannot delete in-flight run %s", runID)
	}
	reg.mu.Unlock()

	dir := reg.runsDir(scope)
	logPath := filepath.Join(dir, runID+".jsonl")
	if err := os.Remove(logPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("DeleteRun: log file remove failed", "logPath", logPath, "error", err)
	}

	indexPath := filepath.Join(dir, "index.json")
	idx, err := loadIndex(indexPath)
	if err != nil {
		return nil
	}
	out := make([]RunSummary, 0, len(idx.Runs))
	for _, r := range idx.Runs {
		if r.ID == runID {
			continue
		}
		out = append(out, r)
	}
	if len(out) == len(idx.Runs) {
		return nil
	}
	idx.Runs = out
	return saveIndex(indexPath, idx)
}
