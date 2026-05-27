package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeIndex(t *testing.T, path string, idx *IndexFile) {
	t.Helper()
	if err := saveIndex(path, idx); err != nil {
		t.Fatal(err)
	}
}

func writeLog(t *testing.T, path string, events []Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range events {
		b, _ := json.Marshal(e)
		_, _ = f.Write(b)
		_, _ = f.Write([]byte("\n"))
	}
}

func TestRecover_FlipsRunningToInterrupted(t *testing.T) {
	base := t.TempDir()
	runsDir := filepath.Join(base, "agentA", "sessK.runs")
	writeIndex(t, filepath.Join(runsDir, "index.json"), &IndexFile{Runs: []RunSummary{
		{ID: "r1", StartedAt: "t0", Status: StatusRunning, LastSeq: 2},
	}})
	writeLog(t, filepath.Join(runsDir, "r1.jsonl"), []Event{
		{Seq: 1, Ts: "t1", Type: EventTypeTextDelta},
		{Seq: 2, Ts: "t2", Type: EventTypeTextDelta},
	})

	n, err := RecoverInterruptedRuns(base)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 recovered, got %d", n)
	}

	idx, _ := loadIndex(filepath.Join(runsDir, "index.json"))
	if idx.Runs[0].Status != StatusInterrupted {
		t.Fatalf("status not flipped: %+v", idx.Runs[0])
	}
	got, _ := ReadLog(filepath.Join(runsDir, "r1.jsonl"), 0)
	if len(got) != 3 || got[2].Type != EventTypeDone || got[2].Status != StatusInterrupted {
		t.Fatalf("synthetic interrupted event missing: %+v", got)
	}
}

func TestRecover_TrustsLogTailIfDone(t *testing.T) {
	base := t.TempDir()
	runsDir := filepath.Join(base, "a", "k.runs")
	// Index says running, but log has a done event => trust the log.
	writeIndex(t, filepath.Join(runsDir, "index.json"), &IndexFile{Runs: []RunSummary{
		{ID: "r1", Status: StatusRunning, LastSeq: 1},
	}})
	writeLog(t, filepath.Join(runsDir, "r1.jsonl"), []Event{
		{Seq: 1, Ts: "t1", Type: EventTypeDone, Status: StatusCompleted},
	})

	n, err := RecoverInterruptedRuns(base)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 recovered, got %d", n)
	}
	idx, _ := loadIndex(filepath.Join(runsDir, "index.json"))
	if idx.Runs[0].Status != StatusCompleted {
		t.Fatalf("should trust log tail: %+v", idx.Runs[0])
	}
}

func TestRecover_IgnoresAlreadyTerminal(t *testing.T) {
	base := t.TempDir()
	runsDir := filepath.Join(base, "a", "k.runs")
	writeIndex(t, filepath.Join(runsDir, "index.json"), &IndexFile{Runs: []RunSummary{
		{ID: "r1", Status: StatusCompleted, LastSeq: 1},
	}})
	writeLog(t, filepath.Join(runsDir, "r1.jsonl"), []Event{
		{Seq: 1, Type: EventTypeDone, Status: StatusCompleted},
	})

	n, err := RecoverInterruptedRuns(base)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("terminal runs should not be touched, recovered %d", n)
	}
}

func TestRecover_NoSessionsBase(t *testing.T) {
	n, err := RecoverInterruptedRuns(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("want 0, got %d", n)
	}
}
