package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Trace records phase timings across a single chat request so we can spot
// where latency comes from. Designed to be cheap: each Mark allocates a small
// struct and emits one slog.Info record. A typical request produces ~10 lines.
//
// Usage:
//
//	tr := agent.NewTrace(agentID, model)
//	ctx = agent.WithTrace(ctx, tr)
//	tr.Mark("ws.received")
//	... runtime calls tr.Mark("llm.first_token", "turn", n) ...
//	tr.Summary()
type Trace struct {
	ID      string
	AgentID string
	Model   string
	Started time.Time

	mu     sync.Mutex
	last   time.Time
	phases []phaseRecord

	// onMark, when set, is invoked synchronously on every Mark call so
	// the gateway WebSocket can forward live phase events to subscribed
	// clients (the chat-UI trace panel). Nil means no live forwarding,
	// which preserves the original "slog only" behavior for tests and
	// non-gateway call paths.
	onMark func(phase string, durMs, atMs int64, attrs []any)
}

// SetOnMark registers a callback fired on every Mark. Safe to call once
// before the trace is shared with a goroutine; later calls overwrite the
// previous callback. The callback is invoked under the trace's lock so
// receivers must be quick (e.g., non-blocking channel send).
func (t *Trace) SetOnMark(fn func(phase string, durMs, atMs int64, attrs []any)) {
	if t == nil {
		return
	}
	t.onMark = fn
}

type phaseRecord struct {
	Name  string
	DurMs int64 // since previous Mark
	AtMs  int64 // since trace start
}

// NewTrace creates a Trace seeded with the start time. Returns nil-safe value;
// all methods are no-ops on a nil receiver so callers don't need nil checks.
func NewTrace(agentID, model string) *Trace {
	now := time.Now()
	return &Trace{
		ID:      newTraceID(),
		AgentID: agentID,
		Model:   model,
		Started: now,
		last:    now,
	}
}

// Mark records a phase boundary and emits a slog.Info entry tagged "perf".
// extraAttrs are key/value pairs appended to the log record (e.g. "turn", 3).
func (t *Trace) Mark(phase string, extraAttrs ...any) {
	if t == nil {
		return
	}
	t.mu.Lock()
	now := time.Now()
	dur := now.Sub(t.last).Milliseconds()
	at := now.Sub(t.Started).Milliseconds()
	t.phases = append(t.phases, phaseRecord{Name: phase, DurMs: dur, AtMs: at})
	t.last = now
	cb := t.onMark
	t.mu.Unlock()

	attrs := []any{
		"trace_id", t.ID,
		"agent", t.AgentID,
		"phase", phase,
		"dur_ms", dur,
		"at_ms", at,
	}
	attrs = append(attrs, extraAttrs...)
	slog.Info("perf", attrs...)

	if cb != nil {
		cb(phase, dur, at, extraAttrs)
	}
}

// Summary emits one final slog.Info "perf summary" line with the total
// elapsed time and the top three slowest phases. Useful for at-a-glance
// triage without grep.
func (t *Trace) Summary() {
	if t == nil {
		return
	}
	t.mu.Lock()
	total := time.Since(t.Started).Milliseconds()
	// Aggregate dur by phase name (some phases recur per turn).
	agg := map[string]int64{}
	for _, p := range t.phases {
		agg[p.Name] += p.DurMs
	}
	type kv struct {
		Name string
		Dur  int64
	}
	flat := make([]kv, 0, len(agg))
	for k, v := range agg {
		flat = append(flat, kv{k, v})
	}
	t.mu.Unlock()
	sort.Slice(flat, func(i, j int) bool { return flat[i].Dur > flat[j].Dur })
	top := flat
	if len(top) > 3 {
		top = top[:3]
	}
	attrs := []any{
		"trace_id", t.ID,
		"agent", t.AgentID,
		"model", t.Model,
		"total_ms", total,
		"phase_count", len(t.phases),
	}
	for i, kv := range top {
		attrs = append(attrs,
			"top"+itoa(i+1)+"_phase", kv.Name,
			"top"+itoa(i+1)+"_ms", kv.Dur,
		)
	}
	slog.Info("perf summary", attrs...)
}

type traceKey struct{}

// WithTrace stashes the Trace in ctx so deeper layers can call Mark.
func WithTrace(ctx context.Context, t *Trace) context.Context {
	return context.WithValue(ctx, traceKey{}, t)
}

// TraceFrom retrieves the Trace from ctx, or nil if none. nil is safe — all
// Trace methods tolerate a nil receiver.
func TraceFrom(ctx context.Context) *Trace {
	if v := ctx.Value(traceKey{}); v != nil {
		if t, ok := v.(*Trace); ok {
			return t
		}
	}
	return nil
}

func newTraceID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
