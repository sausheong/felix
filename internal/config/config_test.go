package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	require.NotNil(t, cfg)

	assert.Equal(t, "127.0.0.1", cfg.Gateway.Host)
	assert.Equal(t, 18789, cfg.Gateway.Port)
	assert.Len(t, cfg.Agents.List, 1)
	assert.Equal(t, "default", cfg.Agents.List[0].ID)
	assert.Equal(t, "local/gemma4", cfg.Agents.List[0].Model)
}

func TestLoadMissingFile(t *testing.T) {
	cfg, err := Load("/nonexistent/path/felix.json5")
	require.NoError(t, err)
	assert.Equal(t, "default", cfg.Agents.List[0].ID)
}

func TestLoadJSON5(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "felix.json5")

	content := `{
  // This is a comment
  "gateway": {
    "host": "0.0.0.0",
    "port": 9999,
  },
  "agents": {
    "list": [
      {
        "id": "test",
        "name": "Test Agent",
        "model": "openai/gpt-4o",
      },
    ],
  },
}`

	err := os.WriteFile(path, []byte(content), 0o644)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, "0.0.0.0", cfg.Gateway.Host)
	assert.Equal(t, 9999, cfg.Gateway.Port)
	assert.Equal(t, "test", cfg.Agents.List[0].ID)
	assert.Equal(t, "openai/gpt-4o", cfg.Agents.List[0].Model)
}

func TestValidateNoAgents(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "at least one agent")
}

func TestValidateNoModel(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{{ID: "x"}},
		},
	}
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no model")
}

func TestGetAgent(t *testing.T) {
	cfg := DefaultConfig()

	a, ok := cfg.GetAgent("default")
	assert.True(t, ok)
	assert.Equal(t, "Felix", a.Name)

	_, ok = cfg.GetAgent("nonexistent")
	assert.False(t, ok)
}

func TestDefaultConfigLocalSection(t *testing.T) {
	cfg := DefaultConfig()
	require.NotNil(t, cfg)

	assert.True(t, cfg.Local.Enabled, "local should default to enabled")
	assert.Equal(t, "24h", cfg.Local.KeepAlive)
	assert.Equal(t, "", cfg.Local.ModelsDir, "models_dir should default to empty (resolved at runtime)")
}

func TestLocalConfigParsing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "felix.json5")
	contents := `{
		"agents": { "list": [{"id": "a1", "model": "local/qwen2.5:0.5b"}] },
		"local": { "enabled": false, "keep_alive": "30m", "models_dir": "/tmp/m" }
	}`
	require.NoError(t, os.WriteFile(cfgPath, []byte(contents), 0o600))

	cfg, err := Load(cfgPath)
	require.NoError(t, err)
	assert.False(t, cfg.Local.Enabled)
	assert.Equal(t, "30m", cfg.Local.KeepAlive)
	assert.Equal(t, "/tmp/m", cfg.Local.ModelsDir)
}

func TestStripJSON5(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "strip single-line comment",
			input: "// comment\n{\"key\": \"value\"}",
			want:  "{\"key\": \"value\"}\n",
		},
		{
			name:  "strip trailing comma before }",
			input: `{"key": "value",}`,
			want:  "{\"key\": \"value\"}\n",
		},
		{
			name:  "strip trailing comma before ]",
			input: `["a", "b",]`,
			want:  "[\"a\", \"b\"]\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripJSON5(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCompactionDefaultsAreSensible(t *testing.T) {
	cfg := DefaultConfig()
	c := cfg.Agents.Defaults.Compaction
	assert.True(t, c.Enabled)
	assert.Empty(t, c.Model, "Model is empty by default; BuildManager auto-mirrors from the default agent")
	assert.InDelta(t, 0.6, c.Threshold, 0.001)
	assert.Equal(t, 4, c.PreserveTurns)
	assert.Equal(t, 60, c.TimeoutSec)
}

func TestCompactionConfigUnmarshals(t *testing.T) {
	raw := []byte(`{
		"agents": {
			"defaults": {
				"compaction": {
					"enabled": false,
					"model": "local/gemma2:2b",
					"threshold": 0.5,
					"preserveTurns": 6,
					"timeoutSec": 30
				}
			}
		}
	}`)
	var cfg Config
	require.NoError(t, json.Unmarshal(raw, &cfg))
	c := cfg.Agents.Defaults.Compaction
	assert.False(t, c.Enabled)
	assert.Equal(t, "local/gemma2:2b", c.Model)
	assert.InDelta(t, 0.5, c.Threshold, 0.001)
	assert.Equal(t, 6, c.PreserveTurns)
	assert.Equal(t, 30, c.TimeoutSec)
}

func TestDefaultConfigCortexEmbedDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if got := cfg.Memory.EmbeddingProvider; got != "local" {
		t.Errorf("Memory.EmbeddingProvider default = %q, want \"local\"", got)
	}
	if got := cfg.Memory.EmbeddingModel; got != "nomic-embed-text" {
		t.Errorf("Memory.EmbeddingModel default = %q, want \"nomic-embed-text\"", got)
	}
	if !cfg.Memory.Enabled {
		t.Errorf("Memory.Enabled default should be true")
	}
	if got := cfg.Cortex.Provider; got != "local" {
		t.Errorf("Cortex.Provider default = %q, want \"local\"", got)
	}
	if got := cfg.Cortex.LLMModel; got != "gemma4" {
		t.Errorf("Cortex.LLMModel default = %q, want \"gemma4\"", got)
	}
}

func TestValidateBackfillsCortexProvider(t *testing.T) {
	cfg := DefaultConfig()
	// Wipe the cortex provider/model and confirm Validate restores them.
	cfg.Cortex.Provider = ""
	cfg.Cortex.LLMModel = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if cfg.Cortex.Provider != "local" {
		t.Errorf("Validate should backfill Cortex.Provider to \"local\"; got %q", cfg.Cortex.Provider)
	}
	if cfg.Cortex.LLMModel != "gemma4" {
		t.Errorf("Validate should backfill Cortex.LLMModel to \"gemma4\"; got %q", cfg.Cortex.LLMModel)
	}
}

func TestResolveMCPServers_HappyPath(t *testing.T) {
	t.Setenv("LTM_SECRET_FOR_TEST", "shhh")
	cfg := &Config{
		MCPServers: []MCPServerConfig{
			{
				ID:      "ltm",
				URL:     "https://example.com/mcp",
				Enabled: true,
				Auth: MCPAuthConfig{
					Kind:            "oauth2_client_credentials",
					TokenURL:        "https://example.com/oauth/token",
					ClientID:        "client-x",
					ClientSecretEnv: "LTM_SECRET_FOR_TEST",
					Scope:           "ltm/api",
				},
				ToolPrefix: "ltm_",
			},
			{ID: "disabled-one", Enabled: false}, // skipped
		},
	}

	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ltm", got[0].ID)
	assert.Equal(t, "shhh", got[0].ClientSecret)
	assert.Equal(t, "ltm_", got[0].ToolPrefix)
}

func TestResolveMCPServers_MissingSecretEnv(t *testing.T) {
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID: "ltm", URL: "https://x", Enabled: true,
			Auth: MCPAuthConfig{
				Kind: "oauth2_client_credentials", TokenURL: "https://t",
				ClientID: "c", ClientSecretEnv: "DEFINITELY_NOT_SET_FELIX_TEST",
			},
		}},
	}
	_, err := cfg.ResolveMCPServers()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DEFINITELY_NOT_SET_FELIX_TEST")
}

func TestResolveMCPServers_UnsupportedAuthKind(t *testing.T) {
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID: "ltm", URL: "https://x", Enabled: true,
			Auth: MCPAuthConfig{Kind: "bearer_static"},
		}},
	}
	_, err := cfg.ResolveMCPServers()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported auth.kind")
}
