package runs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/sausheong/felix/internal/config"
)

// IndexFile is the on-disk shape of <key>.runs/index.json.
type IndexFile struct {
	Runs []RunSummary `json:"runs"`
}

// Upsert inserts or replaces a run by ID.
func (i *IndexFile) Upsert(s RunSummary) {
	for j := range i.Runs {
		if i.Runs[j].ID == s.ID {
			i.Runs[j] = s
			return
		}
	}
	i.Runs = append(i.Runs, s)
}

// loadIndex reads path. Missing file returns an empty index with nil
// error. A corrupt file is also treated as empty (the registry will
// reconstruct from log-file directory listings during recovery).
func loadIndex(path string) (*IndexFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &IndexFile{}, nil
		}
		// Unreadable (permissions, is-a-directory, etc.) — return an empty
		// index alongside the error so callers that ignore the error don't
		// panic on a nil pointer. saveIndex will surface the real failure
		// when it tries to write.
		return &IndexFile{}, fmt.Errorf("read index %s: %w", path, err)
	}
	var idx IndexFile
	if err := json.Unmarshal(data, &idx); err != nil {
		// Corrupt file — caller (recovery) rebuilds from directory.
		return &IndexFile{}, nil
	}
	return &idx, nil
}

// saveIndex atomically writes idx to path via WriteFileAtomic.
func saveIndex(path string, idx *IndexFile) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}
	if err := os.MkdirAll(parentDir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir index parent: %w", err)
	}
	return config.WriteFileAtomic(path, data, 0o600)
}

func parentDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}
