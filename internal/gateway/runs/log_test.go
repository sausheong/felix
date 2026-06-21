package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLogWriter_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abc.jsonl")
	w, err := openLogWriter(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	events := []Event{
		{Seq: 1, Ts: "t1", Type: EventTypeTextDelta, Payload: json.RawMessage(`{"text":"a"}`)},
		{Seq: 2, Ts: "t2", Type: EventTypeTextDelta, Payload: json.RawMessage(`{"text":"b"}`)},
		{Seq: 3, Ts: "t3", Type: EventTypeDone, Status: StatusCompleted},
	}
	for _, e := range events {
		if err := w.Append(e); err != nil {
			t.Fatalf("append %d: %v", e.Seq, err)
		}
	}

	got, err := ReadLog(path, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d", len(got))
	}
	if got[2].Status != StatusCompleted {
		t.Fatalf("want last status completed, got %q", got[2].Status)
	}
}

func TestReadLog_FromSeqFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abc.jsonl")
	w, _ := openLogWriter(path)
	for i := 1; i <= 5; i++ {
		_ = w.Append(Event{Seq: int64(i), Type: EventTypeTextDelta})
	}
	_ = w.Close()

	got, err := ReadLog(path, 3)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 || got[0].Seq != 4 || got[1].Seq != 5 {
		t.Fatalf("fromSeq filter wrong: %+v", got)
	}
}

func TestReadLog_TruncatedLastLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abc.jsonl")
	content := `{"seq":1,"ts":"t1","type":"text_delta"}` + "\n" +
		`{"seq":2,"ts":"t2","type":"text_delta"}` + "\n" +
		`{"seq":3,"ts":"t3","type"` // truncated, no newline
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLog(path, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 valid events, got %d", len(got))
	}
}

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

	// Pin the production syncInterval const to the truth table so the two
	// cannot silently drift before Task 2 wires syncInterval into Append.
	if !shouldSync(EventTypeTextDelta, syncInterval+time.Millisecond, syncInterval) {
		t.Errorf("delta just past package syncInterval should sync")
	}
	if shouldSync(EventTypeTextDelta, syncInterval, syncInterval) {
		t.Errorf("delta exactly at package syncInterval should not sync")
	}
}

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
