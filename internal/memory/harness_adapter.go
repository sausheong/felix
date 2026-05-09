package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	harnessmem "github.com/sausheong/harness/tool/memory"
)

// HarnessAdapter exposes a *Manager as a harness/tool/memory.MemoryStore
// so the harness MemoryTool can write into Felix's existing markdown-
// backed memory. Read paths (FormatIndex, Search) continue to flow
// through the Manager directly — the BM25 / vector indexes are kept in
// sync inside Manager.Save and Manager.Delete.
//
// Felix's storage schema is plain markdown without metadata, so harness
// Entry.Tags and Entry.Origin are dropped on save and synthesized as
// empty on read. List with a non-empty tag returns an empty slice
// rather than the unfiltered set, so an agent that expected a tag
// filter doesn't silently get back everything.
//
// Update deviates from the harness fresh-id contract: Felix's per-entry
// .md files are mutable, so Update rewrites in place and returns the
// same id. The harness fresh-id semantic exists for append-only JSONL
// backends; for Felix, a stable id matches what users see in the
// Settings UI and what load_memory output references.
type HarnessAdapter struct {
	mgr *Manager
}

// NewHarnessAdapter wraps mgr as a harness MemoryStore.
func NewHarnessAdapter(mgr *Manager) *HarnessAdapter {
	return &HarnessAdapter{mgr: mgr}
}

// Save persists e. If e.ID is empty, an id of the form
// "agent-<unix-ms>-<8 hex>" is generated. Returns the persisted entry.
func (a *HarnessAdapter) Save(_ context.Context, e harnessmem.Entry) (harnessmem.Entry, error) {
	if e.Content == "" {
		return harnessmem.Entry{}, harnessmem.ErrInvalidContent
	}
	if e.ID == "" {
		e.ID = generateMemoryID()
	}
	if err := a.mgr.Save(e.ID, e.Content); err != nil {
		return harnessmem.Entry{}, fmt.Errorf("memory save: %w", err)
	}
	stored, ok := a.mgr.Get(e.ID)
	if !ok {
		return harnessmem.Entry{}, fmt.Errorf("memory save: entry %q not visible after write", e.ID)
	}
	return toHarnessEntry(stored), nil
}

// Update rewrites the entry in place. Returns ErrNotFound when id is
// unknown.
func (a *HarnessAdapter) Update(_ context.Context, id, content string) (harnessmem.Entry, error) {
	if content == "" {
		return harnessmem.Entry{}, harnessmem.ErrInvalidContent
	}
	if _, ok := a.mgr.Get(id); !ok {
		return harnessmem.Entry{}, harnessmem.ErrNotFound
	}
	if err := a.mgr.Save(id, content); err != nil {
		return harnessmem.Entry{}, fmt.Errorf("memory update: %w", err)
	}
	stored, ok := a.mgr.Get(id)
	if !ok {
		return harnessmem.Entry{}, fmt.Errorf("memory update: entry %q not visible after write", id)
	}
	return toHarnessEntry(stored), nil
}

// Remove tombstones an entry. Idempotent — removing an unknown id
// returns nil per the harness contract.
func (a *HarnessAdapter) Remove(_ context.Context, id string) error {
	if _, ok := a.mgr.Get(id); !ok {
		return nil
	}
	if err := a.mgr.Delete(id); err != nil {
		return fmt.Errorf("memory remove: %w", err)
	}
	return nil
}

// List returns all live entries when tag is empty. A non-empty tag
// returns an empty slice (Felix entries don't carry tags).
func (a *HarnessAdapter) List(_ context.Context, tag string) ([]harnessmem.Entry, error) {
	if tag != "" {
		return []harnessmem.Entry{}, nil
	}
	src := a.mgr.Entries()
	out := make([]harnessmem.Entry, 0, len(src))
	for _, e := range src {
		out = append(out, toHarnessEntry(e))
	}
	return out, nil
}

// Get returns one entry. The bool is false (no error) when id is
// unknown.
func (a *HarnessAdapter) Get(_ context.Context, id string) (harnessmem.Entry, bool, error) {
	e, ok := a.mgr.Get(id)
	if !ok {
		return harnessmem.Entry{}, false, nil
	}
	return toHarnessEntry(e), true, nil
}

func toHarnessEntry(e Entry) harnessmem.Entry {
	return harnessmem.Entry{
		ID:        e.ID,
		Content:   e.Content,
		CreatedAt: e.ModTime,
		UpdatedAt: e.ModTime,
	}
}

// generateMemoryID returns "agent-<unix-ms>-<8 hex>". The "agent-"
// prefix makes the origin visible in directory listings (vs. user-named
// memories saved through the Settings UI). 8 hex chars of crypto/rand
// is plenty for collision-avoidance at sub-millisecond write rates.
func generateMemoryID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("agent-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}
