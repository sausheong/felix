# Runs-log fsync coalescing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop fsync-per-event in the run replay log so a chat turn spends ~5ms on disk barriers instead of ~4ms × hundreds of streamed events.

**Architecture:** Split the fused flush+sync in `runs.logWriter.Append` into "flush always" (cheap, keeps mid-run reconnect/replay correct) and "sync selectively" (physical barrier only on resume-relevant event types, or once per 250ms). A pure `shouldSync` helper holds the decision so it is exhaustively unit-testable.

**Tech Stack:** Go 1.25, stdlib only (`bufio`, `os`, `time`, `encoding/json`), `testing`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-06-21-runs-log-fsync-coalescing-design.md`

---

## File Structure

- **Modify:** `internal/gateway/runs/log.go` — add `syncInterval` const, `shouldSync` helper, `lastSync` field on `logWriter`; rewrite `Append` (flush always, sync conditionally) and `Close` (final sync). This is the only production file changed.
- **Modify:** `internal/gateway/runs/log_test.go` — add tests for `shouldSync` (truth table), delta-readable-after-Append, meaningful-event-readable, Close-flushes-tail, plus a non-gating benchmark. Existing tests in this file must keep passing unchanged.
- **Untouched but verified:** `internal/gateway/runs/recovery_test.go` (must stay green — recovery semantics are unchanged).

All event-type constants are defined in `internal/gateway/runs/types.go` and are reused verbatim: `EventTypeTextDelta = "text_delta"`, `EventTypeToolCallStart = "tool_call_start"`, `EventTypeToolResult = "tool_result"`, `EventTypeDone = "done"`.

---

### Task 1: Add the pure `shouldSync` decision helper

**Files:**
- Modify: `internal/gateway/runs/log.go` (add `time` import, `syncInterval` const, `shouldSync` func)
- Test: `internal/gateway/runs/log_test.go` (append truth-table test)

- [ ] **Step 1: Write the failing test**

Append this to `internal/gateway/runs/log_test.go`:

```go
func TestShouldSync(t *testing.T) {
	const interval = 250 * time.Millisecond
	cases := []struct {
		name          string
		typ           EventType
		sinceLastSync time.Duration
		want          bool
	}{
		{"delta within interval -> no sync", EventTypeTextDelta, 10 * time.Millisecond, false},
		{"delta past interval -> sync", EventTypeTextDelta, 300 * time.Millisecond, true},
		{"tool_call_start always syncs", EventTypeToolCallStart, 1 * time.Millisecond, true},
		{"tool_result always syncs", EventTypeToolResult, 1 * time.Millisecond, true},
		{"done always syncs", EventTypeDone, 1 * time.Millisecond, true},
		{"delta exactly at interval -> no sync", EventTypeTextDelta, interval, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSync(tc.typ, tc.sinceLastSync, interval); got != tc.want {
				t.Errorf("shouldSync(%q, %v, %v) = %v, want %v",
					tc.typ, tc.sinceLastSync, interval, got, tc.want)
			}
		})
	}
}
```

Also add `"time"` to the test file's import block if it is not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gateway/runs/ -run TestShouldSync -v`
Expected: FAIL — compile error `undefined: shouldSync`.

- [ ] **Step 3: Write minimal implementation**

In `internal/gateway/runs/log.go`, add `"time"` to the import block (alongside `bufio`, `encoding/json`, `errors`, `fmt`, `io`, `io/fs`, `os`). Then add the const and helper near the top of the file, after the imports and before `type logWriter`:

```go
// syncInterval bounds how long buffered (un-synced) text_delta events may sit
// in the OS page cache before we force a physical-disk barrier, so a long
// pure-text generation still reaches disk periodically.
const syncInterval = 250 * time.Millisecond

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

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gateway/runs/ -run TestShouldSync -v`
Expected: PASS — all six subtests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/runs/log.go internal/gateway/runs/log_test.go
git commit -m "feat(runs): add shouldSync helper for selective fsync"
```

---

### Task 2: Wire `shouldSync` into `Append` (flush always, sync selectively)

**Files:**
- Modify: `internal/gateway/runs/log.go:13-43` (`logWriter` struct + `Append`)
- Test: `internal/gateway/runs/log_test.go` (delta-readable + meaningful-readable tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/gateway/runs/log_test.go`:

```go
// A text_delta must be readable via ReadLog immediately after Append even
// though it is (within the interval) not fsync'd — proving bufio.Flush still
// runs on every event, which keeps mid-run reconnect/replay correct.
func TestLogWriter_DeltaReadableWithoutSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r1.jsonl")
	w, err := openLogWriter(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	if err := w.Append(Event{Seq: 1, Type: EventTypeTextDelta, Payload: json.RawMessage(`{"text":"hi"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := ReadLog(path, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("want 1 readable delta event, got %d (%+v)", len(got), got)
	}
}

// A meaningful (non-delta) event is readable after Append.
func TestLogWriter_MeaningfulReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r2.jsonl")
	w, err := openLogWriter(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	if err := w.Append(Event{Seq: 1, Type: EventTypeToolResult, Payload: json.RawMessage(`{"output":"ok"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := ReadLog(path, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].Type != EventTypeToolResult {
		t.Fatalf("want 1 tool_result event, got %d (%+v)", len(got), got)
	}
}
```

- [ ] **Step 2: Run tests to verify they pass against current code, then confirm they still pass after the change**

Run: `go test ./internal/gateway/runs/ -run 'TestLogWriter_DeltaReadableWithoutSync|TestLogWriter_MeaningfulReadable' -v`
Expected: PASS even before the `Append` change (the current code flushes+syncs every event, so the events are readable). These tests lock in the *readability* invariant so the Task-2 refactor cannot regress it. Proceed to change `Append`; the tests must remain green afterward.

- [ ] **Step 3: Apply the implementation**

In `internal/gateway/runs/log.go`, add the `lastSync` field to the struct:

```go
// logWriter is the single-writer append-only handle to a <runID>.jsonl.
// Only the drain goroutine of the owning Run may call Append.
type logWriter struct {
	f        *os.File
	w        *bufio.Writer
	lastSync time.Time
}
```

Replace the body of `Append` (currently `log.go:28-43`) with:

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
	// Flush on every event: bytes must be visible to ReadLog so a client
	// reconnecting mid-run (Run.Subscribe gap-fill) sees all past events.
	if err := l.w.Flush(); err != nil {
		return err
	}
	// Physical-disk barrier only on resume-relevant events, or once the
	// interval has elapsed for a long pure-text stream.
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

(`lastSync` is the zero `time.Time` on a fresh writer, so the first event's `now.Sub(l.lastSync)` is enormous and always syncs — a safe default.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/gateway/runs/ -run 'TestLogWriter|TestReadLog|TestShouldSync' -v`
Expected: PASS — the new readability tests plus the pre-existing `TestLogWriter_AppendAndRead` / `TestReadLog_FromSeqFilter` all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/runs/log.go internal/gateway/runs/log_test.go
git commit -m "perf(runs): flush every event, fsync only meaningful ones + 250ms cap"
```

---

### Task 3: Final sync on `Close`

**Files:**
- Modify: `internal/gateway/runs/log.go:45-51` (`Close`)
- Test: `internal/gateway/runs/log_test.go` (Close-flushes-tail test)

- [ ] **Step 1: Write the failing test**

Append to `internal/gateway/runs/log_test.go`:

```go
// A buffered (within-interval, unsynced) text_delta must survive Close():
// Close flushes and syncs the tail so a cleanly-closed run is fully durable.
func TestLogWriter_CloseFlushesTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r3.jsonl")
	w, err := openLogWriter(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Prime lastSync with a meaningful event, then append a delta that is
	// within the interval (so Append does NOT sync it) and immediately close.
	if err := w.Append(Event{Seq: 1, Type: EventTypeToolResult}); err != nil {
		t.Fatalf("append meaningful: %v", err)
	}
	if err := w.Append(Event{Seq: 2, Type: EventTypeTextDelta, Payload: json.RawMessage(`{"text":"tail"}`)}); err != nil {
		t.Fatalf("append delta: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, err := ReadLog(path, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 || got[1].Seq != 2 {
		t.Fatalf("want 2 events incl. buffered tail delta, got %d (%+v)", len(got), got)
	}
}
```

- [ ] **Step 2: Run test to verify behavior**

Run: `go test ./internal/gateway/runs/ -run TestLogWriter_CloseFlushesTail -v`
Expected: PASS — the existing `Close` already calls `l.w.Flush()`, so the tail is readable. This test guards the durability-on-close invariant before we add the explicit `Sync`.

- [ ] **Step 3: Add the final sync to `Close`**

In `internal/gateway/runs/log.go`, replace `Close` with:

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

- [ ] **Step 4: Run test to verify it still passes**

Run: `go test ./internal/gateway/runs/ -run TestLogWriter_CloseFlushesTail -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/runs/log.go internal/gateway/runs/log_test.go
git commit -m "fix(runs): fsync on Close so cleanly-closed runs are fully durable"
```

---

### Task 4: Recovery regression check + benchmark + full gates

**Files:**
- Test: `internal/gateway/runs/log_test.go` (benchmark)
- Verify only: `internal/gateway/runs/recovery_test.go` (no edit unless a gap is found)

- [ ] **Step 1: Add a non-gating benchmark**

Append to `internal/gateway/runs/log_test.go`:

```go
// BenchmarkAppendDeltas documents the fsync-coalescing win. Timing is
// machine-dependent so it asserts nothing; run with -bench to observe.
func BenchmarkAppendDeltas(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "bench.jsonl")
	w, err := openLogWriter(path)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer w.Close()
	payload := json.RawMessage(`{"text":"a chunk of streamed assistant text"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = w.Append(Event{Seq: int64(i), Type: EventTypeTextDelta, Payload: payload})
	}
}
```

- [ ] **Step 2: Run the recovery tests to confirm no regression**

Run: `go test ./internal/gateway/runs/ -run 'Recover|Recovery' -v`
Expected: PASS — recovery reconstructs status from the index + log tail; the fsync change does not alter which events are written, only when they are physically barriered, so recovery behavior is unchanged.

- [ ] **Step 3: Run the benchmark to confirm it builds and the win is real**

Run: `go test ./internal/gateway/runs/ -bench BenchmarkAppendDeltas -benchtime 200x -run '^$'`
Expected: completes without error; ns/op is small (deltas no longer fsync each iteration). Informational only — do not assert on the number.

- [ ] **Step 4: Full gates**

Run each and confirm PASS:

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./internal/gateway/...
```

Expected: all pass. `go test -race ./internal/gateway/...` is important because `Append` is called from the per-run drain goroutine while `ReadLog`/`Subscribe` run on connection goroutines; the race detector confirms the `lastSync` field (only ever touched by the single drain goroutine that owns the writer) introduces no data race.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/runs/log_test.go
git commit -m "test(runs): benchmark delta append + recovery regression coverage"
```

---

## Notes for the implementer

- **Single-writer invariant:** `logWriter` is documented as written only by the owning Run's drain goroutine (`log.go:13-14`). `lastSync` is therefore goroutine-confined — no mutex needed. Do not add locking; if the race detector flags anything, stop and report it (it would indicate a pre-existing invariant violation, not something this change should paper over).
- **Do not touch** `Run.Append` in `registry.go`, `ReadLog`, `Subscribe`, or `recovery.go`. The change is confined to `log.go`.
- **Existing tests are the contract.** `TestLogWriter_AppendAndRead` and `TestReadLog_FromSeqFilter` must pass unchanged throughout.
- The test file already imports `encoding/json`, `os`, `path/filepath`, `testing`. You are adding `time`. Keep the import block gofmt-clean.
