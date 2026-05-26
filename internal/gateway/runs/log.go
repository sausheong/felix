package runs

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// logWriter is the single-writer append-only handle to a <runID>.jsonl.
// Only the drain goroutine of the owning Run may call Append.
type logWriter struct {
	f *os.File
	w *bufio.Writer
}

func openLogWriter(path string) (*logWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", path, err)
	}
	return &logWriter{f: f, w: bufio.NewWriter(f)}, nil
}

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
	if err := l.w.Flush(); err != nil {
		return err
	}
	return l.f.Sync()
}

func (l *logWriter) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = l.w.Flush()
	return l.f.Close()
}

// ReadLog reads all events with seq > fromSeq from path. Truncated final
// lines are silently dropped (per spec recovery §7). Returns an empty
// slice with nil error if the file does not exist.
func ReadLog(path string, fromSeq int64) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open log %s: %w", path, err)
	}
	defer f.Close()

	var out []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // 1MiB max line
	for scanner.Scan() {
		var e Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			// Truncated/corrupt line: stop here, return what we have.
			break
		}
		if e.Seq > fromSeq {
			out = append(out, e)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		// Partial-line scanner errors are non-fatal — return prefix.
		return out, nil
	}
	return out, nil
}
