package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSaveIsAtomicAnd0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "felix.json5")

	c := &Config{}
	c.SetPath(path)
	require.NoError(t, c.Save())

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "felix.json5", entries[0].Name())
}
