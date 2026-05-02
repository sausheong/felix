package tokens

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// CalibratorStore persists Calibrator state to disk so a long-running
// session retains its learned chars→tokens ratio across Runtime
// reconstructions (every chat.send rebuilds the Runtime, which would
// otherwise drop the calibration).
//
// Layout: one JSON file per (agentID, sessionKey) under baseDir, named
// <agentID>__<sessionKey>.json. The double-underscore separator avoids
// collisions because session keys are validated to be filesystem-safe at
// the gateway boundary.
//
// Concurrency: the file mutex is held only around read/write. The store
// is shared process-wide so calls from different chats don't race on the
// same JSON file (very unlikely in practice — one session per chat — but
// cheap to guarantee).
type CalibratorStore struct {
	baseDir string
	mu      sync.Mutex
}

// NewCalibratorStore creates a store rooted at baseDir. The directory is
// created lazily on the first Save call.
func NewCalibratorStore(baseDir string) *CalibratorStore {
	return &CalibratorStore{baseDir: baseDir}
}

// persisted is the on-disk shape. Versioned so a future schema change can
// be detected and ignored gracefully.
type persisted struct {
	Version int     `json:"v"`
	Ratio   float64 `json:"ratio"`
	Count   int     `json:"count"`
}

const persistedVersion = 1

func (s *CalibratorStore) path(agentID, sessionKey string) string {
	if s == nil || s.baseDir == "" || agentID == "" || sessionKey == "" {
		return ""
	}
	return filepath.Join(s.baseDir, agentID+"__"+sessionKey+".json")
}

// Load returns the persisted (ratio, count) for the given session, or
// (1.0, 0) if no record exists. Never returns an error — corrupt or
// missing files just yield the default, since calibration is advisory.
func (s *CalibratorStore) Load(agentID, sessionKey string) (ratio float64, count int) {
	p := s.path(agentID, sessionKey)
	if p == "" {
		return 1.0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(p)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Debug("calibrator load error", "path", p, "error", err)
		}
		return 1.0, 0
	}
	var rec persisted
	if jerr := json.Unmarshal(data, &rec); jerr != nil {
		slog.Debug("calibrator parse error; ignoring", "path", p, "error", jerr)
		return 1.0, 0
	}
	if rec.Version != persistedVersion || rec.Ratio <= 0 || rec.Count < 0 {
		return 1.0, 0
	}
	return rec.Ratio, rec.Count
}

// Save writes the (ratio, count) for the given session via a write-rename
// dance so a crash mid-write can't corrupt the file. No-op when the store
// is nil or the path resolution fails.
func (s *CalibratorStore) Save(agentID, sessionKey string, ratio float64, count int) {
	if s == nil {
		return
	}
	p := s.path(agentID, sessionKey)
	if p == "" {
		return
	}
	if ratio <= 0 || count <= 0 {
		// No useful information to persist yet.
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
		slog.Warn("calibrator dir create failed", "dir", s.baseDir, "error", err)
		return
	}
	data, err := json.Marshal(persisted{Version: persistedVersion, Ratio: ratio, Count: count})
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Debug("calibrator write failed", "path", tmp, "error", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		slog.Debug("calibrator rename failed", "path", p, "error", err)
	}
}

// Forget removes the persisted record for a session — call from the same
// site that calls session.Store.Delete so deleted sessions don't leak
// calibrator files into the data dir.
func (s *CalibratorStore) Forget(agentID, sessionKey string) {
	if s == nil {
		return
	}
	p := s.path(agentID, sessionKey)
	if p == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = os.Remove(p)
}
