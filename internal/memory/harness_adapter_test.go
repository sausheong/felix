package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	harnessmem "github.com/sausheong/harness/tool/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestAdapter(t *testing.T) *HarnessAdapter {
	t.Helper()
	mgr := NewManager(t.TempDir())
	require.NoError(t, mgr.Load())
	return NewHarnessAdapter(mgr)
}

func TestHarnessAdapter_SaveGeneratesIDWhenEmpty(t *testing.T) {
	a := newTestAdapter(t)
	got, err := a.Save(context.Background(), harnessmem.Entry{Content: "remember this"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got.ID, "agent-"), "auto id should be prefixed: %s", got.ID)
	assert.Equal(t, "remember this", got.Content)
	assert.False(t, got.CreatedAt.IsZero())
}

func TestHarnessAdapter_SaveHonorsCallerID(t *testing.T) {
	a := newTestAdapter(t)
	got, err := a.Save(context.Background(), harnessmem.Entry{ID: "user-pref", Content: "dark mode"})
	require.NoError(t, err)
	assert.Equal(t, "user-pref", got.ID)
	round, ok, err := a.Get(context.Background(), "user-pref")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "dark mode", round.Content)
}

func TestHarnessAdapter_SaveRejectsEmptyContent(t *testing.T) {
	a := newTestAdapter(t)
	_, err := a.Save(context.Background(), harnessmem.Entry{Content: ""})
	assert.ErrorIs(t, err, harnessmem.ErrInvalidContent)
}

func TestHarnessAdapter_UpdateRewritesInPlace(t *testing.T) {
	a := newTestAdapter(t)
	saved, err := a.Save(context.Background(), harnessmem.Entry{ID: "fact-1", Content: "v1"})
	require.NoError(t, err)
	updated, err := a.Update(context.Background(), saved.ID, "v2")
	require.NoError(t, err)
	assert.Equal(t, saved.ID, updated.ID, "Felix adapter intentionally keeps id stable on update")
	assert.Equal(t, "v2", updated.Content)
}

func TestHarnessAdapter_UpdateUnknownReturnsNotFound(t *testing.T) {
	a := newTestAdapter(t)
	_, err := a.Update(context.Background(), "nope", "x")
	assert.True(t, errors.Is(err, harnessmem.ErrNotFound))
}

func TestHarnessAdapter_RemoveIsIdempotent(t *testing.T) {
	a := newTestAdapter(t)
	_, err := a.Save(context.Background(), harnessmem.Entry{ID: "tmp", Content: "x"})
	require.NoError(t, err)
	require.NoError(t, a.Remove(context.Background(), "tmp"))
	require.NoError(t, a.Remove(context.Background(), "tmp"), "second remove must be a no-op")
	_, ok, err := a.Get(context.Background(), "tmp")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestHarnessAdapter_ListReturnsAllWhenTagEmpty(t *testing.T) {
	a := newTestAdapter(t)
	_, _ = a.Save(context.Background(), harnessmem.Entry{ID: "a", Content: "1"})
	_, _ = a.Save(context.Background(), harnessmem.Entry{ID: "b", Content: "2"})
	all, err := a.List(context.Background(), "")
	require.NoError(t, err)
	ids := []string{all[0].ID, all[1].ID}
	assert.ElementsMatch(t, []string{"a", "b"}, ids)
}

func TestHarnessAdapter_ListWithTagReturnsEmpty(t *testing.T) {
	a := newTestAdapter(t)
	_, _ = a.Save(context.Background(), harnessmem.Entry{ID: "a", Content: "1"})
	got, err := a.List(context.Background(), "any-tag")
	require.NoError(t, err)
	assert.Empty(t, got, "Felix entries don't carry tags; tag filter must short-circuit to empty")
}

func TestHarnessAdapter_GetUnknownReturnsFalseNoError(t *testing.T) {
	a := newTestAdapter(t)
	_, ok, err := a.Get(context.Background(), "missing")
	require.NoError(t, err)
	assert.False(t, ok)
}
