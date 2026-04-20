package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/config"
)

func TestInjectLocalProviderWritesBlock(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "felix.json5")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`{
  "agents": { "list": [{"id":"default","model":"openai/gpt-5.4"}] }
}`), 0o600))

	require.NoError(t, InjectLocalProvider(cfgPath, 18790))

	raw, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	providers := got["providers"].(map[string]any)
	local := providers["local"].(map[string]any)
	assert.Equal(t, "local", local["kind"])
	assert.Equal(t, "http://127.0.0.1:18790/v1", local["base_url"])
}

func TestInjectLocalProviderUpdatesPort(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "felix.json5")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`{
  "agents": { "list": [{"id":"default","model":"local/qwen2.5:0.5b"}] },
  "providers": { "local": { "kind":"local", "base_url":"http://127.0.0.1:18790/v1" } }
}`), 0o600))

	require.NoError(t, InjectLocalProvider(cfgPath, 18793))

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:18793/v1", cfg.Providers["local"].BaseURL)
}
