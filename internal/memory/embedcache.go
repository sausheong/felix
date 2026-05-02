package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// embedCache persists per-entry embeddings to disk so Manager.Load doesn't
// re-call the embedder on every startup. Cache layout: a single JSON file
// at <baseDir>/.embeddings-cache.json mapping entry id → record.
//
// The cache is keyed by (id, ModTime). When an entry's on-disk ModTime
// changes (the user edited the file) the cached embedding is treated as
// stale and the entry is re-embedded on next Load.
//
// The cache also records the embedder fingerprint (model + dims). When
// the user switches embedding model (e.g., nomic-embed-text →
// text-embedding-3-small) the existing cache is silently invalidated:
// embedding spaces don't transfer between models, so stale vectors would
// poison search results.
//
// Failure modes are non-fatal — a corrupt cache file is logged and
// ignored; the Load path falls back to full re-embed.
type embedCache struct {
	path string
	mu   sync.Mutex
}

type embedCacheFile struct {
	Version    int                       `json:"v"`
	Embedder   string                    `json:"embedder"`            // fingerprint: "model" or "model@dims"
	Embeddings map[string]embedCacheItem `json:"embeddings"`
}

type embedCacheItem struct {
	ModTime   time.Time `json:"mtime"`
	Vector    []float32 `json:"vec"`
}

const embedCacheVersion = 1

func newEmbedCache(baseDir string) *embedCache {
	return &embedCache{path: filepath.Join(baseDir, ".embeddings-cache.json")}
}

// load reads the cache file. Returns (nil, nil) for missing file (cold
// start) and (nil, nil) for any other error after logging — callers
// always treat a nil map as "rebuild everything." The fingerprint is
// returned separately so the caller can detect embedder swaps.
func (c *embedCache) load() (cache map[string]embedCacheItem, fingerprint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := os.ReadFile(c.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Debug("embedding cache read error; ignoring", "path", c.path, "error", err)
		}
		return nil, ""
	}
	var f embedCacheFile
	if jerr := json.Unmarshal(data, &f); jerr != nil {
		slog.Warn("embedding cache parse error; ignoring", "path", c.path, "error", jerr)
		return nil, ""
	}
	if f.Version != embedCacheVersion {
		return nil, ""
	}
	if f.Embeddings == nil {
		f.Embeddings = map[string]embedCacheItem{}
	}
	return f.Embeddings, f.Embedder
}

// save writes the cache atomically via write-rename. No-op on any I/O
// error — cache misses cost an embed call, not correctness.
func (c *embedCache) save(cache map[string]embedCacheItem, fingerprint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := json.Marshal(embedCacheFile{
		Version:    embedCacheVersion,
		Embedder:   fingerprint,
		Embeddings: cache,
	})
	if err != nil {
		slog.Debug("embedding cache marshal failed", "error", err)
		return
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Debug("embedding cache write failed", "path", tmp, "error", err)
		return
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		slog.Debug("embedding cache rename failed", "path", c.path, "error", err)
	}
}

// embedderFingerprint returns a stable identifier for the embedder so
// model swaps invalidate the cache. We accept any fmt.Stringer-ish or
// just stringifies the model name; called by Manager which knows its
// configured model.
func embedderFingerprint(model string) string {
	if model == "" {
		return "default"
	}
	return fmt.Sprintf("model=%s", model)
}
