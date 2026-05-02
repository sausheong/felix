package memory

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

// Entry represents a single memory entry stored as a Markdown file.
type Entry struct {
	ID       string // derived from filename
	Title    string
	Content  string
	FilePath string
	ModTime  time.Time
}

// Manager handles persistent memory stored as Markdown files with BM25 search
// and optional vector search via chromem-go when an Embedder is configured.
type Manager struct {
	baseDir       string
	entries       map[string]Entry
	index         *BM25Index
	embedder      Embedder // nil → BM25 only
	embedderModel string   // for cache fingerprint; "" before SetEmbedder
	vecDB         *chromem.DB
	vecColl       *chromem.Collection
	cache         *embedCache
	mu            sync.RWMutex
}

// NewManager creates a new memory manager rooted at the given directory.
func NewManager(baseDir string) *Manager {
	return &Manager{
		baseDir: baseDir,
		entries: make(map[string]Entry),
		index:   NewBM25Index(),
		cache:   newEmbedCache(filepath.Join(baseDir, "entries")),
	}
}

// SetEmbedder attaches an embedder to enable vector search.
// Must be called before Load() so that existing entries are indexed.
func (m *Manager) SetEmbedder(e Embedder) {
	m.embedder = e
}

// SetEmbedderModel records the configured embedding model name. Used as
// part of the on-disk cache fingerprint so a model swap silently
// invalidates the persisted embeddings (different models produce
// vectors in different spaces; mixing them poisons search results).
//
// Optional: AttachWithProbe is the canonical caller, but tests that
// construct an embedder directly can leave this unset and the cache
// will use a "default" fingerprint.
func (m *Manager) SetEmbedderModel(model string) {
	m.embedderModel = model
}

// Load scans the memory directory and indexes all Markdown files.
func (m *Manager) Load() error {
	entriesDir := filepath.Join(m.baseDir, "entries")
	if err := os.MkdirAll(entriesDir, 0o755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.entries = make(map[string]Entry)
	m.index = NewBM25Index()
	m.vecDB = nil
	m.vecColl = nil

	files, err := os.ReadDir(entriesDir)
	if err != nil {
		return fmt.Errorf("read memory dir: %w", err)
	}

	for _, de := range files {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".md") {
			continue
		}

		path := filepath.Join(entriesDir, de.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("failed to read memory entry", "path", path, "error", err)
			continue
		}

		info, _ := de.Info()
		modTime := time.Now()
		if info != nil {
			modTime = info.ModTime()
		}

		id := strings.TrimSuffix(de.Name(), ".md")
		content := string(data)

		entry := Entry{
			ID:       id,
			Title:    extractTitle(id, content),
			Content:  content,
			FilePath: path,
			ModTime:  modTime,
		}

		m.entries[id] = entry
		m.index.Add(id, content)
	}

	slog.Info("loaded memory entries", "count", len(m.entries))

	// If an embedder is configured, build the vector collection from loaded entries.
	if m.embedder != nil && len(m.entries) > 0 {
		if err := m.initVectorCollection(context.Background()); err != nil {
			slog.Warn("vector index init failed, falling back to BM25", "error", err)
		}
	}

	return nil
}

// initVectorCollection creates the chromem collection and embeds all
// entries. Must be called with m.mu held.
//
// Persistence: a side-cache at <baseDir>/entries/.embeddings-cache.json
// stores per-entry (mtime, embedding) pairs. Entries whose ModTime
// matches the cache are loaded with their persisted vector — no
// embedder call. Stale entries (ModTime mismatch or absent from cache)
// are embedded fresh and the cache is updated. The cache is invalidated
// wholesale when the embedder model fingerprint changes (different
// models produce incompatible vector spaces).
//
// Result: cold start with N entries goes from N embedder calls to 0
// when nothing changed since last run.
func (m *Manager) initVectorCollection(ctx context.Context) error {
	embedder := m.embedder
	embFn := func(ctx context.Context, text string) ([]float32, error) {
		vecs, err := embedder.Embed(ctx, []string{text})
		if err != nil {
			return nil, err
		}
		return vecs[0], nil
	}

	db := chromem.NewDB()
	coll, err := db.CreateCollection("memory", nil, embFn)
	if err != nil {
		return fmt.Errorf("create vector collection: %w", err)
	}

	// Load the on-disk cache. A model-fingerprint mismatch invalidates
	// the entire cache — embedding spaces don't transfer across models.
	wantFingerprint := embedderFingerprint(m.embedderModel)
	cached, gotFingerprint := map[string]embedCacheItem{}, ""
	if m.cache != nil {
		cached, gotFingerprint = m.cache.load()
		if gotFingerprint != wantFingerprint {
			cached = map[string]embedCacheItem{}
		}
	}

	docs := make([]chromem.Document, 0, len(m.entries))
	hits, misses := 0, 0
	freshCache := make(map[string]embedCacheItem, len(m.entries))
	missDocs := make([]chromem.Document, 0)
	for _, e := range m.entries {
		// Cache hit only when the entry's mtime matches what we cached.
		if c, ok := cached[e.ID]; ok && c.ModTime.Equal(e.ModTime) && len(c.Vector) > 0 {
			docs = append(docs, chromem.Document{
				ID:        e.ID,
				Content:   e.Content,
				Embedding: c.Vector, // skips embFn
			})
			freshCache[e.ID] = c
			hits++
		} else {
			missDocs = append(missDocs, chromem.Document{
				ID:      e.ID,
				Content: e.Content,
			})
			misses++
		}
	}

	// Add cache hits first (no network).
	if len(docs) > 0 {
		if err := coll.AddDocuments(ctx, docs, 1); err != nil {
			return fmt.Errorf("seed cached embeddings: %w", err)
		}
	}
	// Embed cache misses (network — slow path).
	if len(missDocs) > 0 {
		if err := coll.AddDocuments(ctx, missDocs, 1); err != nil {
			return fmt.Errorf("embed missing entries: %w", err)
		}
		// Pull the freshly-computed vectors back out of the collection
		// and persist them so the next start is a cache hit.
		for _, d := range missDocs {
			doc, gerr := coll.GetByID(ctx, d.ID)
			if gerr != nil || len(doc.Embedding) == 0 {
				continue
			}
			freshCache[d.ID] = embedCacheItem{
				ModTime: m.entries[d.ID].ModTime,
				Vector:  doc.Embedding,
			}
		}
	}

	// Persist whatever we have (cache hits + freshly-embedded misses).
	if m.cache != nil && len(freshCache) > 0 {
		m.cache.save(freshCache, wantFingerprint)
	}

	m.vecDB = db
	m.vecColl = coll
	slog.Info("vector memory index built",
		"entries", len(m.entries),
		"cache_hits", hits,
		"cache_misses", misses)
	return nil
}

// Save writes a memory entry to disk and updates both indexes.
func (m *Manager) Save(id, content string) error {
	entriesDir := filepath.Join(m.baseDir, "entries")
	if err := os.MkdirAll(entriesDir, 0o755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}

	path := filepath.Join(entriesDir, id+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write memory entry: %w", err)
	}

	entry := Entry{
		ID:       id,
		Title:    extractTitle(id, content),
		Content:  content,
		FilePath: path,
		ModTime:  time.Now(),
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.entries[id] = entry

	// Rebuild BM25 index.
	m.index = NewBM25Index()
	for _, e := range m.entries {
		m.index.Add(e.ID, e.Content)
	}

	// Add/update in vector collection if available.
	if m.vecColl != nil {
		go func() {
			ctx := context.Background()
			doc := chromem.Document{ID: id, Content: content}
			if err := m.vecColl.AddDocument(ctx, doc); err != nil {
				slog.Warn("vector index add failed", "id", id, "error", err)
			}
		}()
	}

	return nil
}

// Search queries the memory and returns relevant entries.
// Uses vector search when an embedder is configured, BM25 otherwise.
func (m *Manager) Search(query string, maxResults int) []Entry {
	if maxResults <= 0 {
		maxResults = 5
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.entries) == 0 {
		return nil
	}

	// Vector search when available.
	if m.vecColl != nil {
		results, err := m.vecColl.Query(context.Background(), query, maxResults, nil, nil)
		if err == nil {
			var entries []Entry
			for _, r := range results {
				if e, ok := m.entries[r.ID]; ok {
					entries = append(entries, e)
				}
			}
			if len(entries) > 0 {
				return entries
			}
		} else {
			slog.Debug("vector search failed, falling back to BM25", "error", err)
		}
	}

	// BM25 fallback.
	results := m.index.Search(query, maxResults)
	var entries []Entry
	for _, r := range results {
		if e, ok := m.entries[r.ID]; ok {
			entries = append(entries, e)
		}
	}
	return entries
}

// Entries returns all memory entries.
func (m *Manager) Entries() []Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entries := make([]Entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	return entries
}

// Get returns a specific memory entry by ID.
func (m *Manager) Get(id string) (Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	return e, ok
}

// Delete removes a memory entry from disk and both indexes.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.entries[id]
	if !ok {
		return fmt.Errorf("memory entry not found: %s", id)
	}

	if err := os.Remove(entry.FilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete memory file: %w", err)
	}

	delete(m.entries, id)

	// Rebuild BM25 index.
	m.index = NewBM25Index()
	for _, e := range m.entries {
		m.index.Add(e.ID, e.Content)
	}

	// Vector collection doesn't support deletion in chromem-go; rebuild it.
	if m.vecColl != nil && m.embedder != nil {
		if err := m.initVectorCollection(context.Background()); err != nil {
			slog.Warn("vector index rebuild after delete failed", "error", err)
		}
	}

	return nil
}

// FormatForPrompt formats relevant memory entries for injection into the system prompt.
func FormatForPrompt(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n## Relevant Memory\n\n")

	for _, e := range entries {
		b.WriteString("### ")
		b.WriteString(e.Title)
		b.WriteString("\n\n")
		content := e.Content
		if len(content) > 2000 {
			content = content[:2000] + "\n\n[truncated]"
		}
		b.WriteString(content)
		b.WriteString("\n\n")
	}

	return b.String()
}

// MaxMemoryIndexEntries caps how many entries FormatIndex includes. The
// index is injected into the cached static system prompt every turn, so
// it must stay bounded — a few hundred entries × ~80 chars/line is fine
// (~25 KB). Entries beyond the cap are silently elided.
const MaxMemoryIndexEntries = 200

// FormatIndex returns a markdown index of every loaded memory entry
// (id + title + 1-line description). Mirrors skill.Loader.FormatIndex.
// The agent loads full bodies on demand via the load_memory tool
// instead of having Top-N hits jammed into every prefill — saves
// 5–10 KB of system-prompt prefix and lets the model decide what's
// worth pulling. Returns "" for empty Manager.
//
// Entries are sorted by id for stable cache-prefix ordering across turns.
// One-line description is the first non-empty line of the body that is
// not the title heading; trimmed to ~120 chars.
func (m *Manager) FormatIndex() string {
	m.mu.RLock()
	entries := make([]Entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.mu.RUnlock()

	if len(entries) == 0 {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	if len(entries) > MaxMemoryIndexEntries {
		entries = entries[:MaxMemoryIndexEntries]
	}

	var b strings.Builder
	b.WriteString("\n\n## Memory Index\n\nThe following memory entries are available. Use the `load_memory` tool with an entry id to read its full body when relevant — entries are not injected automatically. Always check whether memory is relevant before answering domain or user-context questions.\n\n")
	for _, e := range entries {
		b.WriteString("- **")
		b.WriteString(e.ID)
		b.WriteString("** — ")
		b.WriteString(e.Title)
		if d := indexDescription(e); d != "" {
			b.WriteString(": ")
			b.WriteString(d)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// indexDescription returns a short one-line teaser for the index entry —
// the first non-empty body line that isn't the H1 title, trimmed to
// 120 chars. Returns "" when the body has nothing beyond the title.
func indexDescription(e Entry) string {
	for _, line := range strings.Split(e.Content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "# ") {
			continue
		}
		if len(line) > 120 {
			line = line[:120] + "…"
		}
		return line
	}
	return ""
}

// extractTitle pulls the first H1 heading from content, falling back to the id.
func extractTitle(id, content string) string {
	if idx := strings.Index(content, "# "); idx >= 0 {
		end := strings.Index(content[idx:], "\n")
		if end > 0 {
			return strings.TrimPrefix(content[idx:idx+end], "# ")
		}
	}
	return id
}
