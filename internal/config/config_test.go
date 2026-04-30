package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sausheong/felix/internal/tools"
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
	// Cortex.Provider / LLMModel are deliberately empty by default so cortex
	// mirrors the chatting agent's model. Setting them auto-fills a hard pin
	// that defeats the mirror.
	if got := cfg.Cortex.Provider; got != "" {
		t.Errorf("Cortex.Provider default = %q, want \"\"", got)
	}
	if got := cfg.Cortex.LLMModel; got != "" {
		t.Errorf("Cortex.LLMModel default = %q, want \"\"", got)
	}
}

func TestValidateStripsLegacyCortexAutofill(t *testing.T) {
	// Older builds auto-filled provider="local" + llmModel="gemma4" into the
	// user's config. Validate should strip that pair so cortex falls back to
	// mirroring the chat agent.
	cfg := DefaultConfig()
	cfg.Cortex.Provider = "local"
	cfg.Cortex.LLMModel = "gemma4"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if cfg.Cortex.Provider != "" || cfg.Cortex.LLMModel != "" {
		t.Errorf("legacy auto-fill should be stripped; got (%q, %q)",
			cfg.Cortex.Provider, cfg.Cortex.LLMModel)
	}
}

func TestValidatePreservesExplicitCortexPin(t *testing.T) {
	// A non-legacy explicit pin (e.g. anthropic/sonnet) survives Validate.
	cfg := DefaultConfig()
	cfg.Cortex.Provider = "anthropic"
	cfg.Cortex.LLMModel = "claude-sonnet-4-6"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if cfg.Cortex.Provider != "anthropic" || cfg.Cortex.LLMModel != "claude-sonnet-4-6" {
		t.Errorf("explicit pin should be preserved; got (%q, %q)",
			cfg.Cortex.Provider, cfg.Cortex.LLMModel)
	}
}

func TestResolveMCPServers_HappyPath(t *testing.T) {
	t.Setenv("LTM_SECRET_FOR_TEST", "shhh")
	cfg := &Config{
		MCPServers: []MCPServerConfig{
			{
				ID:        "ltm",
				Transport: "http",
				HTTP: &MCPHTTPBlock{
					URL: "https://example.com/mcp",
					Auth: MCPAuthConfig{
						Kind:            "oauth2_client_credentials",
						TokenURL:        "https://example.com/oauth/token",
						ClientID:        "client-x",
						ClientSecretEnv: "LTM_SECRET_FOR_TEST",
						Scope:           "ltm/api",
					},
				},
				Enabled:    true,
				ToolPrefix: "ltm_",
			},
			{ID: "disabled-one", Enabled: false}, // skipped
		},
	}

	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ltm", got[0].ID)
	assert.Equal(t, "http", got[0].Transport)
	require.NotNil(t, got[0].HTTP)
	assert.Equal(t, "shhh", got[0].HTTP.Auth.ClientSecret)
	assert.Equal(t, "ltm_", got[0].ToolPrefix)
}

func TestResolveMCPServers_LiteralSecretInConfig(t *testing.T) {
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID: "ltm", Transport: "http", Enabled: true,
			HTTP: &MCPHTTPBlock{
				URL: "https://x",
				Auth: MCPAuthConfig{
					Kind: "oauth2_client_credentials", TokenURL: "https://t",
					ClientID: "c", ClientSecret: "literal-secret",
				},
			},
		}},
	}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "literal-secret", got[0].HTTP.Auth.ClientSecret)
}

func TestResolveMCPServers_LiteralBeatsEnv(t *testing.T) {
	t.Setenv("SECRET_THAT_SHOULD_NOT_WIN", "from-env")
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID: "ltm", Transport: "http", Enabled: true,
			HTTP: &MCPHTTPBlock{
				URL: "https://x",
				Auth: MCPAuthConfig{
					Kind: "oauth2_client_credentials", TokenURL: "https://t",
					ClientID: "c",
					ClientSecret:    "from-config",
					ClientSecretEnv: "SECRET_THAT_SHOULD_NOT_WIN",
				},
			},
		}},
	}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "from-config", got[0].HTTP.Auth.ClientSecret)
}

func TestResolveMCPServers_MissingSecretSkipsServer(t *testing.T) {
	// Missing-secret used to be a hard error. We now log+skip instead so a
	// single misconfigured MCP entry can't take down the whole gateway.
	cfg := &Config{
		MCPServers: []MCPServerConfig{
			{
				ID: "ltm-bad", Transport: "http", Enabled: true,
				HTTP: &MCPHTTPBlock{
					URL: "https://x",
					Auth: MCPAuthConfig{
						Kind: "oauth2_client_credentials", TokenURL: "https://t",
						ClientID: "c", ClientSecretEnv: "DEFINITELY_NOT_SET_FELIX_TEST",
					},
				},
			},
			{
				ID: "ltm-good", Transport: "http", Enabled: true,
				HTTP: &MCPHTTPBlock{
					URL: "https://y",
					Auth: MCPAuthConfig{
						Kind: "oauth2_client_credentials", TokenURL: "https://t",
						ClientID: "c", ClientSecret: "ok",
					},
				},
			},
		},
	}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err) // skip, not error
	require.Len(t, got, 1)
	assert.Equal(t, "ltm-good", got[0].ID)
}

func TestResolveMCPServers_EmptyIDSkipsServer(t *testing.T) {
	// A stub entry from clicking "+ Add MCP Server" in the UI without
	// filling in the ID must not take down the whole gateway. Same posture
	// as missing-secret: log+skip.
	cfg := &Config{
		MCPServers: []MCPServerConfig{
			{ID: "", Transport: "http", Enabled: true},
			{
				ID: "ltm-good", Transport: "http", Enabled: true,
				HTTP: &MCPHTTPBlock{
					URL: "https://y",
					Auth: MCPAuthConfig{
						Kind: "oauth2_client_credentials", TokenURL: "https://t",
						ClientID: "c", ClientSecret: "ok",
					},
				},
			},
		},
	}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ltm-good", got[0].ID)
}

func TestResolveMCPServers_UnsupportedAuthKind(t *testing.T) {
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID: "ltm", Transport: "http", Enabled: true,
			HTTP: &MCPHTTPBlock{
				URL:  "https://x",
				Auth: MCPAuthConfig{Kind: "weird-scheme"},
			},
		}},
	}
	_, err := cfg.ResolveMCPServers()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported auth.kind")
}

// --- New coverage: bearer, stdio, legacy flat HTTP, default transport. ---

func TestResolveMCPServers_BearerLiteral(t *testing.T) {
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID: "anthropic", Transport: "http", Enabled: true,
			HTTP: &MCPHTTPBlock{
				URL:  "https://mcp.anthropic.com/v1/x",
				Auth: MCPAuthConfig{Kind: "bearer", Token: "sk-ant-literal"},
			},
		}},
	}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "bearer", got[0].HTTP.Auth.Kind)
	assert.Equal(t, "sk-ant-literal", got[0].HTTP.Auth.BearerToken)
}

func TestResolveMCPServers_BearerEnv(t *testing.T) {
	t.Setenv("BEARER_FOR_TEST", "from-env-tok")
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID: "x", Transport: "http", Enabled: true,
			HTTP: &MCPHTTPBlock{
				URL:  "https://x",
				Auth: MCPAuthConfig{Kind: "bearer", TokenEnv: "BEARER_FOR_TEST"},
			},
		}},
	}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "from-env-tok", got[0].HTTP.Auth.BearerToken)
}

func TestResolveMCPServers_BearerMissingTokenSkips(t *testing.T) {
	cfg := &Config{
		MCPServers: []MCPServerConfig{
			{
				ID: "no-token", Transport: "http", Enabled: true,
				HTTP: &MCPHTTPBlock{
					URL:  "https://x",
					Auth: MCPAuthConfig{Kind: "bearer", TokenEnv: "DEFINITELY_NOT_SET_BEARER_TEST"},
				},
			},
			{
				ID: "ok", Transport: "http", Enabled: true,
				HTTP: &MCPHTTPBlock{
					URL:  "https://y",
					Auth: MCPAuthConfig{Kind: "bearer", Token: "tok"},
				},
			},
		},
	}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "ok", got[0].ID)
}

func TestResolveMCPServers_NoneAuth(t *testing.T) {
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID: "local-mcp", Transport: "http", Enabled: true,
			HTTP: &MCPHTTPBlock{
				URL:  "http://127.0.0.1:9999/mcp",
				Auth: MCPAuthConfig{Kind: "none"},
			},
		}},
	}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "none", got[0].HTTP.Auth.Kind)
}

func TestResolveMCPServers_Stdio(t *testing.T) {
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID: "github", Transport: "stdio", Enabled: true,
			Stdio: &MCPStdioBlock{
				Command: "npx",
				Args:    []string{"-y", "@modelcontextprotocol/server-github"},
				Env:     map[string]string{"GITHUB_TOKEN": "ghp_xxx"},
			},
			ToolPrefix: "gh_",
		}},
	}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "stdio", got[0].Transport)
	require.NotNil(t, got[0].Stdio)
	assert.Equal(t, "npx", got[0].Stdio.Command)
	assert.Equal(t, []string{"-y", "@modelcontextprotocol/server-github"}, got[0].Stdio.Args)
	assert.Equal(t, "ghp_xxx", got[0].Stdio.Env["GITHUB_TOKEN"])
	assert.Equal(t, "gh_", got[0].ToolPrefix)
}

func TestResolveMCPServers_StdioMissingCommand(t *testing.T) {
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID: "broken", Transport: "stdio", Enabled: true,
			Stdio: &MCPStdioBlock{Args: []string{"x"}},
		}},
	}
	_, err := cfg.ResolveMCPServers()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdio.command")
}

func TestResolveMCPServers_LegacyFlatHTTP(t *testing.T) {
	// An entry without `transport` and with top-level URL/Auth must still
	// resolve — backward-compat for users on disk before this change.
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID:      "ltm-legacy",
			URL:     "https://legacy.example.com/mcp",
			Enabled: true,
			Auth: MCPAuthConfig{
				Kind: "oauth2_client_credentials", TokenURL: "https://t",
				ClientID: "c", ClientSecret: "legacy-sec",
				Scope: "x",
			},
		}},
	}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "http", got[0].Transport)
	require.NotNil(t, got[0].HTTP)
	assert.Equal(t, "https://legacy.example.com/mcp", got[0].HTTP.URL)
	assert.Equal(t, "legacy-sec", got[0].HTTP.Auth.ClientSecret)
}

func TestResolveMCPServers_NestedWinsOverFlat(t *testing.T) {
	// Both nested and flat populated → nested wins.
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID:        "x",
			Transport: "http",
			Enabled:   true,
			URL:       "https://flat.example.com",
			Auth: MCPAuthConfig{
				Kind: "oauth2_client_credentials", TokenURL: "https://t",
				ClientID: "c", ClientSecret: "flat-sec",
			},
			HTTP: &MCPHTTPBlock{
				URL: "https://nested.example.com",
				Auth: MCPAuthConfig{
					Kind: "oauth2_client_credentials", TokenURL: "https://t",
					ClientID: "c", ClientSecret: "nested-sec",
				},
			},
		}},
	}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "https://nested.example.com", got[0].HTTP.URL)
	assert.Equal(t, "nested-sec", got[0].HTTP.Auth.ClientSecret)
}

func TestResolveMCPServers_UnknownTransport(t *testing.T) {
	cfg := &Config{
		MCPServers: []MCPServerConfig{{
			ID: "x", Transport: "carrier-pigeon", Enabled: true,
		}},
	}
	_, err := cfg.ResolveMCPServers()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported transport")
}

func TestResolveMCPServers_RoundTripJSON_Stdio(t *testing.T) {
	// Round-trip: serialise a stdio entry to JSON and back, confirm it
	// resolves identically. Catches missing JSON tags / casing drift.
	original := MCPServerConfig{
		ID: "rt", Transport: "stdio", Enabled: true,
		Stdio: &MCPStdioBlock{
			Command: "echo",
			Args:    []string{"hi"},
			Env:     map[string]string{"K": "V"},
		},
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	var parsed MCPServerConfig
	require.NoError(t, json.Unmarshal(data, &parsed))

	cfg := &Config{MCPServers: []MCPServerConfig{parsed}}
	got, err := cfg.ResolveMCPServers()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "echo", got[0].Stdio.Command)
	assert.Equal(t, []string{"hi"}, got[0].Stdio.Args)
	assert.Equal(t, "V", got[0].Stdio.Env["K"])
}

func TestApplyMCPToolNamesToAllowlists(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{
				{ID: "with-allowlist", Tools: ToolPolicy{Allow: []string{"bash", "read_file"}}},
				{ID: "wide-open", Tools: ToolPolicy{Allow: nil}},
				{ID: "empty-allow", Tools: ToolPolicy{Allow: []string{}}},
				{ID: "already-has-one", Tools: ToolPolicy{Allow: []string{"bash", "ltm_search"}}},
			},
		},
	}
	cfg.ApplyMCPToolNamesToAllowlists([]string{"ltm_search", "ltm_store"})

	assert.ElementsMatch(t, []string{"bash", "read_file", "ltm_search", "ltm_store"}, cfg.Agents.List[0].Tools.Allow)
	assert.Empty(t, cfg.Agents.List[1].Tools.Allow, "wide-open agent (nil Allow) should be left alone")
	assert.Empty(t, cfg.Agents.List[2].Tools.Allow, "empty-allow agent should be left alone")
	assert.ElementsMatch(t, []string{"bash", "ltm_search", "ltm_store"}, cfg.Agents.List[3].Tools.Allow, "duplicate ltm_search should not appear twice")
}

func TestApplyMCPToolNamesToAllowlists_Empty(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{List: []AgentConfig{{ID: "x", Tools: ToolPolicy{Allow: []string{"bash"}}}}},
	}
	cfg.ApplyMCPToolNamesToAllowlists(nil)
	assert.ElementsMatch(t, []string{"bash"}, cfg.Agents.List[0].Tools.Allow)
}

func TestStripMCPAutoAdded(t *testing.T) {
	// Setup: a runtime cfg that has had ApplyMCPToolNamesToAllowlists called
	// on it, so its in-memory state and snapshot reflect the augmentation.
	runtime := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{
				{ID: "with-allow", Tools: ToolPolicy{Allow: []string{"bash"}}},
				{ID: "wide-open", Tools: ToolPolicy{Allow: nil}},
			},
		},
	}
	runtime.ApplyMCPToolNamesToAllowlists([]string{"ltm_x", "ltm_y"})
	assert.ElementsMatch(t, []string{"bash", "ltm_x", "ltm_y"}, runtime.Agents.List[0].Tools.Allow)

	// Simulate UI save: the browser POSTed back the in-memory cfg verbatim.
	// Strip should remove ltm_x / ltm_y so they are NOT persisted to disk.
	incoming := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{
				{ID: "with-allow", Tools: ToolPolicy{Allow: []string{"bash", "ltm_x", "ltm_y"}}},
				{ID: "wide-open", Tools: ToolPolicy{Allow: []string{}}},
				// User added a new agent through the UI; it should also be cleaned.
				{ID: "newcomer", Tools: ToolPolicy{Allow: []string{"web_fetch", "ltm_x"}}},
			},
		},
	}
	runtime.StripMCPAutoAdded(incoming)
	assert.ElementsMatch(t, []string{"bash"}, incoming.Agents.List[0].Tools.Allow)
	assert.Empty(t, incoming.Agents.List[1].Tools.Allow)
	assert.ElementsMatch(t, []string{"web_fetch"}, incoming.Agents.List[2].Tools.Allow)
}

func TestStripMCPAutoAdded_NoSnapshot(t *testing.T) {
	// A Config that never had ApplyMCPToolNamesToAllowlists called on it
	// should leave `other` completely untouched.
	runtime := &Config{}
	incoming := &Config{
		Agents: AgentsConfig{List: []AgentConfig{{ID: "x", Tools: ToolPolicy{Allow: []string{"bash", "ltm_x"}}}}},
	}
	runtime.StripMCPAutoAdded(incoming)
	assert.ElementsMatch(t, []string{"bash", "ltm_x"}, incoming.Agents.List[0].Tools.Allow)
}

func TestConfig_ApplyTaskToolToAllowlists(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{
				{ID: "parent", Model: "x/y", Tools: ToolPolicy{Allow: []string{"read_file", "bash"}}},
				{ID: "researcher", Model: "x/y", Subagent: true, Description: "Web", Tools: ToolPolicy{Allow: []string{"web_fetch"}}},
				{ID: "free", Model: "x/y"}, // empty Allow list — left alone
				{ID: "already_has", Model: "x/y", Tools: ToolPolicy{Allow: []string{"read_file", "task"}}},
			},
		},
	}

	cfg.ApplyTaskToolToAllowlists()

	// parent gained "task"
	assert.Contains(t, cfg.Agents.List[0].Tools.Allow, "task")
	// researcher gained "task" too — even subagents themselves
	assert.Contains(t, cfg.Agents.List[1].Tools.Allow, "task")
	// free agent (empty Allow) untouched — empty = allow-all
	assert.Empty(t, cfg.Agents.List[2].Tools.Allow)
	// already_has: no duplicate
	assert.Equal(t, []string{"read_file", "task"}, cfg.Agents.List[3].Tools.Allow)
}

func TestConfig_ApplyTaskToolToAllowlists_NoSubagents(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{
				{ID: "parent", Model: "x/y", Tools: ToolPolicy{Allow: []string{"read_file"}}},
			},
		},
	}
	cfg.ApplyTaskToolToAllowlists()
	// No subagents → no augmentation
	assert.Equal(t, []string{"read_file"}, cfg.Agents.List[0].Tools.Allow)
}

func TestConfig_ApplyTaskToolToAllowlists_Idempotent(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{
				{ID: "parent", Model: "x/y", Tools: ToolPolicy{Allow: []string{"read_file"}}},
				{ID: "sub", Model: "x/y", Subagent: true, Description: "x"},
			},
		},
	}
	cfg.ApplyTaskToolToAllowlists()
	cfg.ApplyTaskToolToAllowlists() // call twice
	assert.Equal(t, []string{"read_file", "task"}, cfg.Agents.List[0].Tools.Allow) // no duplicate
}

func TestConfig_AgentLoop_DefaultsToZero(t *testing.T) {
	// DefaultConfig leaves AgentLoop zero so callers (Runtime methods) fall
	// back to env vars / compiled defaults. The explicit defaulting (10/3/off)
	// belongs to the readers, not to DefaultConfig — that way a user who
	// removes the agentLoop block from felix.json5 gets the same behavior as
	// one who never had it.
	cfg := DefaultConfig()
	assert.Equal(t, 0, cfg.AgentLoop.MaxToolConcurrency, "MaxToolConcurrency default is 0 (means: use env/default)")
	assert.Equal(t, 0, cfg.AgentLoop.MaxAgentDepth, "MaxAgentDepth default is 0 (means: use env/default)")
	assert.False(t, cfg.AgentLoop.StreamingTools, "StreamingTools default is false (means: check env)")
}

func TestConfig_AgentLoop_UnmarshalsExplicitValues(t *testing.T) {
	raw := []byte(`{
		"agents": { "list": [{"id": "a", "model": "x/y"}] },
		"agentLoop": {
			"maxToolConcurrency": 4,
			"maxAgentDepth": 7,
			"streamingTools": true
		}
	}`)
	var cfg Config
	require.NoError(t, json.Unmarshal(raw, &cfg))
	assert.Equal(t, 4, cfg.AgentLoop.MaxToolConcurrency)
	assert.Equal(t, 7, cfg.AgentLoop.MaxAgentDepth)
	assert.True(t, cfg.AgentLoop.StreamingTools)
}

func TestConfig_AgentLoop_LoadFromJSON5File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "felix.json5")
	contents := `{
		"agents": { "list": [{"id": "a", "model": "x/y"}] },
		"agentLoop": {
			// in-line comment is fine — JSON5 path
			"maxToolConcurrency": 12,
			"maxAgentDepth": 5,
			"streamingTools": true,
		},
	}`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, 12, cfg.AgentLoop.MaxToolConcurrency)
	assert.Equal(t, 5, cfg.AgentLoop.MaxAgentDepth)
	assert.True(t, cfg.AgentLoop.StreamingTools)
}

func TestCompactionConfigMessageCapDefault(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, 50, cfg.Agents.Defaults.Compaction.MessageCap,
		"default MessageCap must be 50 (conservative; tool-heavy turns won't insta-trigger)")
}

func TestCompactionConfigMessageCapZeroDisablesCap(t *testing.T) {
	// Documented contract: MessageCap == 0 disables the count-based trigger,
	// leaving only the token-threshold check active. Verify the type and
	// default behavior; runtime exercises this in agent_test.go.
	var cfg CompactionConfig
	cfg.MessageCap = 0
	assert.Equal(t, 0, cfg.MessageCap)
}

func TestAgentConfigReasoningValidation(t *testing.T) {
	cases := map[string]bool{
		"":       true, // empty = off
		"off":    true,
		"low":    true,
		"medium": true,
		"high":   true,
		"ultra":  false,
		"LOW":    false, // case-sensitive
	}
	for in, wantOK := range cases {
		err := ValidateReasoningMode(in)
		if wantOK {
			assert.NoError(t, err, "input %q should validate", in)
		} else {
			assert.Error(t, err, "input %q should error", in)
		}
	}
}

func TestConfig_SubagentRequiresDescription(t *testing.T) {
	// Failure case: Subagent=true with empty Description must fail validation
	// and the error must mention both the agent ID and "description".
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{{
				ID:       "worker",
				Model:    "openai/gpt-4o",
				Subagent: true,
			}},
		},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker")
	assert.Contains(t, err.Error(), "description")

	// Success case: same config but Description is non-empty.
	cfg.Agents.List[0].Description = "Web research subagent"
	require.NoError(t, cfg.Validate())
}

func TestConfig_EligibleSubagents(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{
				{ID: "worker", Model: "openai/gpt-4o", Subagent: true, Description: "Web research"},
				{ID: "summarizer", Model: "openai/gpt-4o", Subagent: true, Description: "Summarizes long text"},
				{ID: "default", Model: "openai/gpt-4o", Subagent: false},
			},
		},
	}

	got := cfg.EligibleSubagents()
	assert.Equal(t, map[string]string{
		"worker":     "Web research",
		"summarizer": "Summarizes long text",
	}, got)
	_, ok := got["default"]
	assert.False(t, ok, "non-subagent must not appear in EligibleSubagents")
}

func TestConfig_EligibleSubagents_NoneEligible(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{
				{ID: "default", Model: "openai/gpt-4o"},
			},
		},
	}

	got := cfg.EligibleSubagents()
	assert.NotNil(t, got, "EligibleSubagents must return non-nil map for len() gating")
	assert.Equal(t, 0, len(got))
}

func TestConfig_BuildPermissionChecker(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{
				{
					ID: "agent_allow",
					Tools: ToolPolicy{
						Allow: []string{"read_file", "web_fetch"},
					},
				},
				{
					ID: "agent_deny",
					Tools: ToolPolicy{
						Deny: []string{"bash"},
					},
				},
			},
		},
	}

	checker := cfg.BuildPermissionChecker()
	require.NotNil(t, checker)

	ctx := context.Background()
	emptyInput := json.RawMessage(`{}`)

	// agent_allow: read_file allowed, bash denied (not in allow list)
	require.Equal(t, tools.DecisionAllow, checker.Check(ctx, "agent_allow", "read_file", emptyInput).Behavior)
	require.Equal(t, tools.DecisionDeny, checker.Check(ctx, "agent_allow", "bash", emptyInput).Behavior)

	// agent_deny: bash denied, read_file allowed (no allow-list constraint)
	require.Equal(t, tools.DecisionDeny, checker.Check(ctx, "agent_deny", "bash", emptyInput).Behavior)
	require.Equal(t, tools.DecisionAllow, checker.Check(ctx, "agent_deny", "read_file", emptyInput).Behavior)

	// Unknown agent: allow-all default
	require.Equal(t, tools.DecisionAllow, checker.Check(ctx, "unknown", "anything", emptyInput).Behavior)
}
