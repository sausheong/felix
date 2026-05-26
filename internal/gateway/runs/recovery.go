package runs

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RecoverInterruptedRuns walks sessionsDir for *.runs/index.json,
// reconciles each run's index status with its log tail, and writes
// synthetic interrupted events for runs that were left in 'running'
// state by a crashed process. Returns the number of runs whose index
// it modified. Missing sessionsDir → (0, nil).
func RecoverInterruptedRuns(sessionsDir string) (int, error) {
	info, err := os.Stat(sessionsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat sessions dir: %w", err)
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("sessions dir is not a directory: %s", sessionsDir)
	}

	recovered := 0
	walkErr := filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("recovery walk", "path", path, "error", err)
			return nil
		}
		if !d.IsDir() || !strings.HasSuffix(d.Name(), ".runs") {
			return nil
		}
		indexPath := filepath.Join(path, "index.json")
		n, rerr := recoverOne(path, indexPath)
		if rerr != nil {
			slog.Warn("recovery: failed", "indexPath", indexPath, "error", rerr)
			return nil
		}
		recovered += n
		return nil
	})
	if walkErr != nil {
		return recovered, walkErr
	}
	slog.Info("runs.recovery: complete", "recovered", recovered)
	return recovered, nil
}

func recoverOne(runsDir, indexPath string) (int, error) {
	idx, err := loadIndex(indexPath)
	if err != nil {
		return 0, fmt.Errorf("load index: %w", err)
	}
	changed := 0
	for i := range idx.Runs {
		s := &idx.Runs[i]
		if s.Status != StatusRunning {
			continue
		}
		logPath := filepath.Join(runsDir, s.ID+".jsonl")
		events, err := ReadLog(logPath, 0)
		if err != nil {
			slog.Warn("recovery: read log", "logPath", logPath, "error", err)
			// Treat as interrupted with no events readable.
			s.Status = StatusInterrupted
			s.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
			changed++
			continue
		}
		// Trust log tail if it's a Done event.
		if n := len(events); n > 0 && events[n-1].Type == EventTypeDone {
			tail := events[n-1]
			s.Status = tail.Status
			s.EndedAt = tail.Ts
			s.LastSeq = tail.Seq
			if tail.SupersededBy != "" {
				s.SupersededBy = tail.SupersededBy
			}
			changed++
			continue
		}
		// Otherwise: append synthetic interrupted event.
		var lastSeq int64
		if n := len(events); n > 0 {
			lastSeq = events[n-1].Seq
		}
		synthetic := Event{
			Seq:    lastSeq + 1,
			Ts:     time.Now().UTC().Format(time.RFC3339Nano),
			Type:   EventTypeDone,
			Status: StatusInterrupted,
		}
		lw, err := openLogWriter(logPath)
		if err == nil {
			_ = lw.Append(synthetic)
			_ = lw.Close()
		}
		s.Status = StatusInterrupted
		s.EndedAt = synthetic.Ts
		s.LastSeq = synthetic.Seq
		changed++
	}
	if changed > 0 {
		if err := saveIndex(indexPath, idx); err != nil {
			return changed, fmt.Errorf("save index: %w", err)
		}
	}
	return changed, nil
}
