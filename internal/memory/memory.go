package memory

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
	baseDir  string
	entries  map[string]Entry
	index    *BM25Index
	embedder Embedder         // nil → BM25 only
	vecDB    *chromem.DB      // nil → BM25 only
	vecColl  *chromem.Collection
	mu       sync.RWMutex
}

// NewManager creates a new memory manager rooted at the given directory.
func NewManager(baseDir string) *Manager {
	return &Manager{
		baseDir: baseDir,
		entries: make(map[string]Entry),
		index:   NewBM25Index(),
	}
}

// SetEmbedder attaches an embedder to enable vector search.
// Must be called before Load() so that existing entries are indexed.
func (m *Manager) SetEmbedder(e Embedder) {
	m.embedder = e
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

// initVectorCollection creates the chromem collection and embeds all entries.
// Must be called with m.mu held.
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

	docs := make([]chromem.Document, 0, len(m.entries))
	for _, e := range m.entries {
		docs = append(docs, chromem.Document{
			ID:      e.ID,
			Content: e.Content,
		})
	}

	if err := coll.AddDocuments(ctx, docs, 1); err != nil {
		return fmt.Errorf("embed existing entries: %w", err)
	}

	m.vecDB = db
	m.vecColl = coll
	slog.Info("vector memory index built", "entries", len(docs))
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
