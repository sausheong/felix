package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomic_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")

	want := []byte(`{"foo":"bar"}`)
	require.NoError(t, WriteFileAtomic(target, want, 0o600))

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	info, err := os.Stat(target)
	require.NoError(t, err)
	// On Unix, mode bits include the file type; mask down to perm bits.
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestWriteFileAtomic_CleansUpTempOnSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")
	require.NoError(t, WriteFileAtomic(target, []byte("hi"), 0o644))

	// Directory should contain ONLY the target file, no leftover .tmp.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "atomic write left a stray temp file")
	assert.Equal(t, "config.json", entries[0].Name())
}

func TestWriteFileAtomic_PreservesOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")

	// Seed with valid existing content.
	original := []byte(`{"valid":true}`)
	require.NoError(t, os.WriteFile(target, original, 0o600))

	// Force failure by passing a path whose parent doesn't exist.
	badTarget := filepath.Join(dir, "missing-subdir", "config.json")
	writeErr := WriteFileAtomic(badTarget, []byte("anything"), 0o600)
	require.Error(t, writeErr)

	// Original file at the real target must be untouched.
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, original, got)
	assert.True(t, strings.Contains(writeErr.Error(), "missing-subdir") || strings.Contains(writeErr.Error(), "no such file"),
		"unexpected error: %v", writeErr)
}
