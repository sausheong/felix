# Runs-log fsync coalescing Design

**Date:** 2026-06-21
**Status:** Approved
**Repo:** `felix` only (`internal/gateway/runs`).

## Problem

`runs.logWriter.Append` (`internal/gateway/runs/log.go:42`) calls `f.Sync()` on
**every** event written to a run's replay log. The agent emits one event per
streamed `text_delta`, so a single chat turn produces hundreds of fsyncs.

Measured on the target machine's APFS volume (200-event turn, `~/.felix`):

```
fsync cost: ~4.0 ms per event (rock-steady)

 50 events:  fsync-per-event 211 ms  →  flush-once 4.5 ms   (~207 ms wasted)
200 events:  fsync-per-event 809 ms  →  flush-once 4.7 ms   (~804 ms wasted)
500 events:  fsync-per-event 2005 ms →  flush-once 4.7 ms   (~2000 ms wasted)
```

The fsync overhead is linear in event count and independent of model speed. It
is the reason even small `write_file` turns feel sluggish: the slowness is not
the write, it is the hundreds of 4 ms disk barriers sprinkled through the turn
that surrounds it.

## Key architecture facts (verified against source)

- The fsync-per-event lives **only** in `internal/gateway/runs/log.go:42`. This
  is the durable run-replay log, one `<runID>.jsonl` per run.
- The harness **session JSONL** (`session/store.go:130`) — the actual durable
  conversation — does **not** fsync and writes once per entry (not per delta).
  It is already fast and is **out of scope**.
- Two distinct operations are currently fused in `Append`:
  - `bufio.Flush()` — pushes bytes to the OS page cache. Cheap (~µs). Makes the
    bytes visible to other readers of the file.
  - `f.Sync()` — forces a physical-disk barrier. Expensive (~4 ms). Guarantees
    durability across power loss.
- **Mid-run reconnect/replay reads past events from the log file on disk.**
  `Run.Subscribe` calls `ReadLog(r.logPath, fromSeq)` (`registry.go:386`) to
  gap-fill a reconnecting client. Therefore every event must remain *readable*
  from the file (i.e. flushed to the OS), even if not physically sync'd.
- **Recovery only inspects the log tail.** `recoverOne` (`recovery.go:56-116`)
  reconstructs a crashed run's status from the index plus the last event; it
  checks whether the tail is a `done` event (`recovery.go:78`). It never
  consumes `text_delta` events.
- **`ReadLog` already tolerates a truncated final line** (`log.go:71-73`): a
  torn/partial last line is silently dropped and the readable prefix returned.
- The live WebSocket client receives every event via in-memory fan-out
  (`fanoutLocked`, `registry.go:333`) independently of disk durability, so a
  `text_delta` lost to an unclean crash was already delivered to the user.

## Decisions (resolved with user)

1. **Sync strategy:** sync on meaningful events **plus** a time cap. fsync only
   on resume-relevant event types, or when more than `syncInterval` (250 ms) has
   elapsed since the last sync. A long pure-text generation still reaches disk
   periodically.
2. **Flush always, Sync selectively.** Every event calls `bufio.Flush()` to stay
   readable on mid-run reconnect; only the physical `f.Sync()` is restricted.
   (Confirmed: dropping `Flush()` for deltas would break reconnect gap-fill and
   was rejected.)
3. **`Close()` performs a final `f.Sync()`** so a clean close is fully durable
   regardless of the last event's type.

## Design

### Single file changed: `internal/gateway/runs/log.go`

Add a package constant and one field to `logWriter`:

```go
// syncInterval bounds how long buffered (un-synced) text_delta events may
// sit in the OS page cache before we force a physical-disk barrier, so a
// long pure-text generation still reaches disk periodically.
const syncInterval = 250 * time.Millisecond

type logWriter struct {
	f        *os.File
	w        *bufio.Writer
	lastSync time.Time
}
```

Extract the sync decision into a **pure, unit-testable helper**:

```go
// shouldSync reports whether an event of type t, written sinceLastSync after
// the previous fsync, warrants a physical-disk barrier. Every non-text_delta
// (resume-relevant) event syncs; a text_delta syncs only once the interval has
// elapsed. This keeps the hot streaming path cheap while bounding worst-case
// loss of cosmetic trailing deltas on an unclean crash.
func shouldSync(t EventType, sinceLastSync, interval time.Duration) bool {
	if t != EventTypeTextDelta {
		return true
	}
	return sinceLastSync > interval
}
```

`Append` becomes:

```go
func (l *logWriter) Append(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := l.w.Write(b); err != nil {
		return err
	}
	if err := l.w.WriteByte('\n'); err != nil {
		return err
	}
	if err := l.w.Flush(); err != nil { // always: keeps reconnect/replay correct
		return err
	}
	now := time.Now()
	if shouldSync(e.Type, now.Sub(l.lastSync), syncInterval) {
		if err := l.f.Sync(); err != nil {
			return err
		}
		l.lastSync = now
	}
	return nil
}
```

Note: `lastSync` is the zero `time.Time` on a freshly opened writer, so
`now.Sub(l.lastSync)` is enormous and the first event always syncs — a safe
default.

`Close` gains a final sync (best-effort, mirroring the existing best-effort
`Flush`):

```go
func (l *logWriter) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = l.w.Flush()
	_ = l.f.Sync()
	return l.f.Close()
}
```

`time` is added to the import block.

### What does NOT change

- `Run.Append` (`registry.go:248`) — unchanged; it still calls `l.log.Append(e)`
  then fans out. The fan-out (live delivery) is unaffected.
- `ReadLog`, `Subscribe`, `recoverOne`, the index — all unchanged.
- `EventType` constants — reused as-is. `EventTypeTextDelta` is the only type
  given special treatment; everything else (`tool_call_start`, `tool_result`,
  `done`, …) falls into the "always sync" branch by default, which is the safe
  direction (a future new event type syncs unless explicitly exempted).

## Durability & recovery semantics

- **Clean shutdown / abort:** `Finish` writes a `done` event (non-delta →
  synced); `Close()` also syncs. Zero loss of resume-relevant data.
- **Hard crash mid-turn:** may lose trailing `text_delta`s from the last
  <250 ms that were flushed-but-not-synced. These are cosmetic — already
  delivered live to the client and never read by recovery.
- **Recovery unaffected:** status is reconstructed from index + tail (`done` or
  synthetic interrupted); missing trailing deltas cannot change the outcome.
- **Checkpoints are monotonic:** every `tool_result`/`done` forces a barrier, so
  the on-disk log is always durable up to the last tool boundary — the
  granularity resume/replay cares about.
- **Truncation-safe:** we only ever flush whole lines, so an unclean crash
  leaves a clean line-prefix; `ReadLog`'s existing torn-line handling covers the
  rest.

## Testing

All tests in `internal/gateway/runs/` (package `runs`).

1. **`shouldSync` truth table** (pure function — fast, deterministic):
   - `EventTypeTextDelta`, `sinceLastSync < interval` → `false`
   - `EventTypeTextDelta`, `sinceLastSync > interval` → `true`
   - `EventTypeToolCallStart`, `< interval` → `true`
   - `EventTypeToolResult`, `< interval` → `true`
   - `EventTypeDone`, `< interval` → `true`
2. **`text_delta` is readable immediately after `Append`** (proves `Flush`
   happens even when no sync does): open a writer, `Append` a single
   `text_delta`, then `ReadLog(path, 0)` and assert the event is returned.
3. **Meaningful event readable after `Append`**: `Append` a `tool_result`,
   `ReadLog`, assert present.
4. **`Close()` flushes the buffered tail**: `Append` a `text_delta` (within the
   interval, so unsynced), `Close()`, reopen via `ReadLog`, assert the delta is
   present. (Validates the `Close` flush path; a real power-loss sync cannot be
   observed in a unit test.)
5. **Recovery regression**: existing `recovery` tests must stay green — a log
   whose tail is a `done` recovers as completed; a log with only deltas and no
   `done` recovers as `interrupted`. Extend only if coverage is missing.
6. **`BenchmarkAppendDeltas`** (non-gating, for the record): appends N
   `text_delta`s and reports ns/op, documenting the win. Timing is
   machine-dependent so it is not asserted.

Final gates: `go build ./...`, `go vet ./...`, `go test ./...`,
`go test -race ./internal/gateway/...`.

## Out of scope

- The harness session JSONL (already unsynced, per-entry, fast).
- `WriteFileAtomic`'s single fsync — that one stays; it is a genuine atomic-write
  durability guarantee and costs one barrier, not hundreds.
- A config knob to tune `syncInterval` (YAGNI; 250 ms is fixed, can be added
  later if ever needed).
- Token-generation latency (model throughput — a separate concern from disk).
