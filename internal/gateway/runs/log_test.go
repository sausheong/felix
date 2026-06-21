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
}
