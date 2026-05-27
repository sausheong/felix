package runs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIndexFile_LoadEmpty(t *testing.T) {
	dir := t.TempDir()
	idx, err := loadIndex(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Runs) != 0 {
		t.Fatalf("want empty, got %d", len(idx.Runs))
	}
}

func TestIndexFile_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	idx := &IndexFile{Runs: []RunSummary{
		{ID: "r1", StartedAt: "t1", Status: StatusRunning},
	}}
	if err := saveIndex(path, idx); err != nil {
		t.Fatal(err)
	}
	got, err := loadIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 1 || got.Runs[0].ID != "r1" {
		t.Fatalf("round trip failed: %+v", got)
	}
}

func TestIndexFile_Upsert(t *testing.T) {
	idx := &IndexFile{}
	idx.Upsert(RunSummary{ID: "r1", Status: StatusRunning})
	idx.Upsert(RunSummary{ID: "r2", Status: StatusRunning})
	idx.Upsert(RunSummary{ID: "r1", Status: StatusCompleted, EndedAt: "t2"})

	if len(idx.Runs) != 2 {
		t.Fatalf("want 2 runs, got %d", len(idx.Runs))
	}
	var r1 *RunSummary
	for i := range idx.Runs {
		if idx.Runs[i].ID == "r1" {
			r1 = &idx.Runs[i]
		}
	}
	if r1 == nil || r1.Status != StatusCompleted || r1.EndedAt != "t2" {
		t.Fatalf("upsert did not update r1: %+v", r1)
	}
}

func TestIndexFile_CorruptFallsBackToEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	_ = os.WriteFile(path, []byte("not json"), 0o600)
	idx, err := loadIndex(path)
	if err != nil {
		t.Fatalf("loadIndex should not error on corrupt file, got %v", err)
	}
	if idx == nil || len(idx.Runs) != 0 {
		t.Fatal("corrupt file should yield empty index")
	}
}
