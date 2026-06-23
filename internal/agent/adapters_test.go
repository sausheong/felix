package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/memory"
	"github.com/sausheong/felix/internal/skill"
)

func TestSkillProviderAdapter_GetAndFormatIndex(t *testing.T) {
	dir := t.TempDir()
	body := "---\nname: greeter\ndescription: says hello\n---\nthe greeter body"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "greeter.md"), []byte(body), 0o600))

	l := skill.NewLoader()
	require.NoError(t, l.LoadFrom(dir))

	ad := skillProviderAdapter{l: l}

	got, ok := ad.Get("greeter")
	require.True(t, ok, "Get must find a loaded skill by name")
	require.Contains(t, got, "the greeter body")

	_, ok = ad.Get("does-not-exist")
	require.False(t, ok, "Get must report missing skill")

	require.Contains(t, ad.FormatIndex(), "greeter", "FormatIndex must list the skill")
}

func TestMemoryProviderAdapter_GetAndFormatIndex(t *testing.T) {
	m := memory.NewManager(t.TempDir())
	require.NoError(t, m.Load())
	require.NoError(t, m.Save("note1", "the remembered content"))

	ad := memoryProviderAdapter{m: m}

	got, ok := ad.Get("note1")
	require.True(t, ok, "Get must find a saved entry by id")
	require.Equal(t, "the remembered content", got)

	_, ok = ad.Get("missing")
	require.False(t, ok, "Get must report a missing entry")

	require.Contains(t, ad.FormatIndex(), "note1", "FormatIndex must list the entry")
}
