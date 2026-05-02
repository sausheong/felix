package local

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func touch(t *testing.T, path string, exec bool) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o644))
	if exec {
		require.NoError(t, os.Chmod(path, 0o755))
	}
}

func TestDiscoverEnvOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("env-override test uses POSIX exec bit")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ollama")
	touch(t, bin, true)
	t.Setenv("FELIX_OLLAMA_BIN", bin)

	got, err := Discover("/some/other/dir")
	require.NoError(t, err)
	assert.Equal(t, bin, got)
}

func TestDiscoverNextToFelixBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX exec bit")
	}
	t.Setenv("FELIX_OLLAMA_BIN", "")
	t.Setenv("PATH", "")

	felixDir := t.TempDir()
	bin := filepath.Join(felixDir, "bin", "ollama")
	touch(t, bin, true)

	got, err := Discover(felixDir)
	require.NoError(t, err)
	assert.Equal(t, bin, got)
}

func TestDiscoverSiblingToFelixBinary(t *testing.T) {
	// Bundle layout: Felix.app/Contents/Resources/bin/{felix,ollama}.
	// felixBinDir is Resources/bin and ollama is its sibling — must resolve.
	if runtime.GOOS == "windows" {
		t.Skip("POSIX exec bit")
	}
	t.Setenv("FELIX_OLLAMA_BIN", "")
	t.Setenv("PATH", "")

	felixDir := t.TempDir()
	bin := filepath.Join(felixDir, "ollama")
	touch(t, bin, true)

	got, err := Discover(felixDir)
	require.NoError(t, err)
	assert.Equal(t, bin, got)
}

func TestDiscoverNotFound(t *testing.T) {
	t.Setenv("FELIX_OLLAMA_BIN", "")
	t.Setenv("PATH", "")
	dir := t.TempDir()

	_, err := Discover(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBinaryNotFound)
}
