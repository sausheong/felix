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
	"unicode/utf8"

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
	vecSem        chan struct{} // bounds concurrent vector-add goroutines
	mu            sync.RWMutex
}

// NewManager creates a new memory manager rooted at the given directory.
func NewManager(baseDir string) *Manager {
	return &Manager{
		baseDir: baseDir,
		entries: make(map[string]Entry),
		index:   NewBM25Index(),
		cache:   newEmbedCache(filepath.Join(baseDir, "entries")),
		vecSem:  make(chan struct{}, 4),
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
	if err := writeFileAtomic(path, []byte(content), 0o600); err != nil {
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

	// Add/update in vector collection if available. Capture coll under the
	// lock so the goroutine never dereferences m.vecColl (which Load/Delete
	// reassign). vecSem bounds concurrent vector-add network calls.
	if coll := m.vecColl; coll != nil {
		go func() {
			m.vecSem <- struct{}{}
			defer func() { <-m.vecSem }()
			doc := chromem.Document{ID: id, Content: content}
			if err := coll.AddDocument(context.Background(), doc); err != nil {
				slog.Warn("vector index add failed", "id", id, "error", err)
			}
		}()
	}

	return nil
}

// Search queries the memory and returns relevant entries.
// Uses vector search when an embedder is configured, BM25 otherwise.
//
// The passed ctx bounds and cancels the vector query: chromem invokes the
// embedder (an HTTP round-trip) synchronously inside Query, and this call
// holds m.mu.RLock for its duration — so an unbounded call would block every
// memory write behind a hung endpoint. The vector query is additionally
// capped at 5s (matching the embedder probe timeout) regardless of ctx.
func (m *Manager) Search(ctx context.Context, query string, maxResults int) []Entry {
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
		// Bound the embedder round-trip (chromem calls the embedder inside
		// Query) so a hung endpoint can't hold the read lock — and writes —
		// indefinitely. 5s matches the embedder probe timeout.
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		results, err := m.vecColl.Query(qctx, query, maxResults, nil, nil)
		cancel()
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
		if utf8.RuneCountInString(content) > 2000 {
			content = truncateRunes(content, 2000) + "\n\n[truncated]"
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
// Entries are sorted by ModTime descending (tie-break id descending) so
// that when there are more than MaxMemoryIndexEntries the NEWEST entries
// survive the cap — agent ids are time-ascending, so id-sorting would
// silently drop the most recent memories off the only discovery surface.
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
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].ModTime.Equal(entries[j].ModTime) {
			return entries[i].ModTime.After(entries[j].ModTime)
		}
		return entries[i].ID > entries[j].ID
	})
	omitted := 0
	if len(entries) > MaxMemoryIndexEntries {
		omitted = len(entries) - MaxMemoryIndexEntries
		entries = entries[:MaxMemoryIndexEntries]
		slog.Warn("memory index truncated", "shown", MaxMemoryIndexEntries, "omitted", omitted)
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
	if omitted > 0 {
		fmt.Fprintf(&b, "\n…and %d more (use the memory list tool to see all).\n", omitted)
	}
	return b.String()
}

// truncateRunes returns s limited to n runes, never splitting a multibyte
// rune (byte-slicing UTF-8 can inject invalid bytes into the system prompt).
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
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
		if utf8.RuneCountInString(line) > 120 {
			line = truncateRunes(line, 120) + "…"
		}
		return line
	}
	return ""
}

// extractTitle pulls the first H1 heading from content, falling back to the id.
func extractTitle(id, content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return id
}
