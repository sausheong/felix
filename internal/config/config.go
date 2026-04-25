package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/sausheong/felix/internal/mcp"
)

// Config is the top-level Felix configuration.
type Config struct {
	Gateway   GatewayConfig            `json:"gateway"`
	Providers map[string]ProviderConfig `json:"providers"`
	Agents    AgentsConfig             `json:"agents"`
	Bindings  []Binding                `json:"bindings"`
	Channels  ChannelsConfig           `json:"channels"`
	Heartbeat HeartbeatConfig          `json:"heartbeat"`
	Memory    MemoryConfig             `json:"memory"`
	Cortex    CortexConfig             `json:"cortex"`
	Security  SecurityConfig           `json:"security"`
	Local     LocalConfig              `json:"local"`
	Telegram  TelegramConfig           `json:"telegram"`
	MCPServers []MCPServerConfig       `json:"mcp_servers"`

	mu   sync.RWMutex
	path string
}

// TelegramConfig enables outbound Telegram messages via the send_message tool's
// telegram channel.
// Felix does NOT receive Telegram messages — this is send-only.
type TelegramConfig struct {
	Enabled       bool   `json:"enabled"`         // master switch; tool is also disabled if BotToken is empty
	BotToken      string `json:"bot_token"`       // from @BotFather
	DefaultChatID string `json:"default_chat_id"` // optional; used when the agent omits chat_id
}

// MCPServerConfig declares one remote MCP server that Felix should connect to
// at startup. Tools exposed by the server are registered into the agent's
// tool registry as if they were core tools.
type MCPServerConfig struct {
	ID         string        `json:"id"`                    // unique within the list
	URL        string        `json:"url"`                   // MCP Streamable-HTTP endpoint
	Auth       MCPAuthConfig `json:"auth"`
	Enabled    bool          `json:"enabled"`
	ToolPrefix string        `json:"tool_prefix,omitempty"` // optional name prefix
}

// MCPAuthConfig describes how Felix authenticates to an MCP server. MVP
// supports only OAuth2 client-credentials; additional kinds (e.g.
// "bearer_static") will plug in via the Kind discriminator.
//
// The client secret can be supplied directly via ClientSecret (matches the
// existing Felix convention for telegram.bot_token and providers.api_key)
// or via ClientSecretEnv (env var name) for operators who prefer to keep
// secrets out of config files. ClientSecret wins when both are set.
type MCPAuthConfig struct {
	Kind            string `json:"kind"`                          // "oauth2_client_credentials"
	TokenURL        string `json:"token_url"`
	ClientID        string `json:"client_id"`
	ClientSecret    string `json:"client_secret,omitempty"`       // literal secret value
	ClientSecretEnv string `json:"client_secret_env,omitempty"`   // env var NAME holding the secret (alternative)
	Scope           string `json:"scope"`
}

// ProviderConfig holds connection details for an LLM provider.
type ProviderConfig struct {
	Kind    string `json:"kind"`     // "openai", "anthropic", "openai-compatible"
	BaseURL string `json:"base_url"` // custom API endpoint (e.g. LiteLLM)
	APIKey  string `json:"api_key"`  // API key or auth token
}

type GatewayConfig struct {
	Host   string     `json:"host"`
	Port   int        `json:"port"`
	Auth   AuthConfig `json:"auth"`
	Reload ReloadConfig `json:"reload"`
}

type AuthConfig struct {
	Token string `json:"token"`
}

type ReloadConfig struct {
	Mode string `json:"mode"` // "hybrid", "manual", "auto-restart"
}

type AgentsConfig struct {
	List     []AgentConfig  `json:"list"`
	Defaults AgentsDefaults `json:"defaults"`
}

// AgentsDefaults holds defaults applied across all agents unless overridden.
type AgentsDefaults struct {
	Compaction CompactionConfig `json:"compaction"`
}

// CompactionConfig configures session compaction.
type CompactionConfig struct {
	Enabled       bool    `json:"enabled"`
	Model         string  `json:"model"`         // "provider/model-id", e.g. "local/qwen2.5:3b-instruct"
	Threshold     float64 `json:"threshold"`     // fraction of context window that triggers preventive compaction
	PreserveTurns int     `json:"preserveTurns"` // K — last K user turns kept verbatim
	TimeoutSec    int     `json:"timeoutSec"`    // per-summarizer-call deadline
}

type AgentConfig struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Workspace    string       `json:"workspace"`
	Model        string       `json:"model"`
	Fallbacks    []string     `json:"fallbacks"`
	Sandbox      string       `json:"sandbox"`                    // "none", "docker", "namespace"
	MaxTurns     int          `json:"maxTurns,omitempty"`         // max tool-use loop iterations (0 = default 25)
	SystemPrompt string       `json:"system_prompt,omitempty"`    // inline system prompt (overrides IDENTITY.md)
	Tools        ToolPolicy   `json:"tools"`
	Cron         []CronConfig `json:"cron,omitempty"`
}

type CronConfig struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"` // duration string: "30m", "1h", "24h"
	Prompt   string `json:"prompt"`
}

type ToolPolicy struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

type Binding struct {
	AgentID string       `json:"agentId"`
	Match   BindingMatch `json:"match"`
}

type BindingMatch struct {
	Channel   string     `json:"channel,omitempty"`
	AccountID string     `json:"accountId,omitempty"`
	ChatType  string     `json:"chatType,omitempty"`
	Peer      *PeerMatch `json:"peer,omitempty"`
}

type PeerMatch struct {
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind,omitempty"`
}

type ChannelsConfig struct {
	CLI CLIConfig `json:"cli"`
}

type CLIConfig struct {
	Enabled     bool `json:"enabled"`
	Interactive bool `json:"interactive"`
}

type HeartbeatConfig struct {
	Interval string `json:"interval"`
	Enabled  bool   `json:"enabled"`
}

type MemoryConfig struct {
	Enabled           bool   `json:"enabled"`
	EmbeddingProvider string `json:"embeddingProvider"`
	EmbeddingModel    string `json:"embeddingModel"`
	MaxEntries        int    `json:"maxEntries"`
}

// LocalConfig configures the bundled Ollama supervisor.
type LocalConfig struct {
	Enabled   bool   `json:"enabled"`    // master switch
	ModelsDir string `json:"models_dir"` // override; empty → ~/.felix/ollama/models
	KeepAlive string `json:"keep_alive"` // OLLAMA_KEEP_ALIVE
}

type CortexConfig struct {
	Enabled  bool   `json:"enabled"`
	DBPath   string `json:"dbPath"`   // path to brain.db (default: ~/.felix/brain.db)
	Provider string `json:"provider"` // provider name matching a key in cfg.Providers (e.g. "openai", "anthropic")
	LLMModel string `json:"llmModel"` // model for extraction/decomposition (default: gpt-5.4-mini for openai, claude-sonnet-4-5-20250929 for anthropic)
}

type SecurityConfig struct {
	ExecApprovals ExecApprovalsConfig `json:"execApprovals"`
	GroupPolicy   GroupPolicyConfig   `json:"groupPolicy"`
}

type ExecApprovalsConfig struct {
	Level     string   `json:"level"` // "deny", "allowlist", "full"
	Allowlist []string `json:"allowlist"`
}

type GroupPolicyConfig struct {
	RequireMention bool `json:"requireMention"`
}

// DefaultDataDir returns the default Felix data directory.
func DefaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".felix"
	}
	return filepath.Join(home, ".felix")
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() string {
	return filepath.Join(DefaultDataDir(), "felix.json5")
}

// Load reads and parses a Felix config file. It supports JSON5 by
// stripping comments and trailing commas before unmarshalling.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			cfg.path = path
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Warn if config file is readable by group or others (may expose API keys).
	// Skip on Windows where Unix file permissions are not meaningful.
	if runtime.GOOS != "windows" {
		if info, statErr := os.Stat(path); statErr == nil {
			mode := info.Mode().Perm()
			if mode&0o077 != 0 {
				slog.Warn("config file has overly permissive permissions",
					"path", path,
					"mode", fmt.Sprintf("%04o", mode),
					"recommended", "0600",
					"fix", fmt.Sprintf("chmod 600 %s", path),
				)
			}
		}
	}

	// Strip JSON5 features (single-line comments, trailing commas) for stdlib JSON parsing.
	cleaned := stripJSON5(string(data))

	var cfg Config
	if err := json.Unmarshal([]byte(cleaned), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.path = path

	// Backfill compaction defaults if the user's config is silent.
	// "All numeric fields zero" is our proxy for "no compaction block at
	// all" — in that case copy the full default. Otherwise only backfill
	// missing numeric fields and trust the user's Enabled. Model is allowed
	// to remain empty; BuildManager auto-mirrors the default agent's model.
	d := DefaultConfig().Agents.Defaults.Compaction
	cur := cfg.Agents.Defaults.Compaction
	// If the configured local compaction model isn't used by any configured
	// agent, it's almost certainly not pulled (the historical default
	// "local/qwen2.5:3b-instruct" is the common case, but the same logic
	// catches typos and stale configs after model removal). Clear it so
	// BuildManager auto-mirrors the default agent's model — guarantees the
	// summarizer hits an actually-loaded model.
	if strings.HasPrefix(cur.Model, "local/") {
		used := false
		for _, a := range cfg.Agents.List {
			if a.Model == cur.Model {
				used = true
				break
			}
		}
		if !used {
			slog.Warn("compaction.model not used by any agent; clearing so it auto-mirrors", "model", cur.Model)
			cur.Model = ""
		}
	}
	if cur.Threshold == 0 && cur.PreserveTurns == 0 && cur.TimeoutSec == 0 {
		cfg.Agents.Defaults.Compaction = d
		// Preserve the cleared model from migration above.
		cfg.Agents.Defaults.Compaction.Model = cur.Model
	} else {
		if cur.Threshold == 0 {
			cur.Threshold = d.Threshold
		}
		if cur.PreserveTurns == 0 {
			cur.PreserveTurns = d.PreserveTurns
		}
		if cur.TimeoutSec == 0 {
			cur.TimeoutSec = d.TimeoutSec
		}
		cfg.Agents.Defaults.Compaction = cur
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Gateway: GatewayConfig{
			Host: "127.0.0.1",
			Port: 18789,
			Reload: ReloadConfig{Mode: "hybrid"},
		},
		Providers: map[string]ProviderConfig{},
		Agents: AgentsConfig{
			List: []AgentConfig{
				{
					ID:        "default",
					Name:      "Felix",
					Workspace: filepath.Join(DefaultDataDir(), "workspace-default"),
					Model:     "local/gemma4",
					Sandbox:   "none",
					Tools: ToolPolicy{
						Allow: []string{"read_file", "write_file", "edit_file", "bash", "web_fetch", "web_search", "browser", "cron"},
					},
				},
			},
			Defaults: AgentsDefaults{
				Compaction: CompactionConfig{
					Enabled: true,
					// Empty → BuildManager auto-mirrors the default agent's
					// model. The previous hardcoded "qwen2.5:3b-instruct"
					// silently disabled compaction on stock installs since
					// that model isn't bundled.
					Model:         "",
					Threshold:     0.6,
					PreserveTurns: 4,
					TimeoutSec:    60,
				},
			},
		},
		Bindings: []Binding{
			{AgentID: "default", Match: BindingMatch{Channel: "cli"}},
		},
		Channels: ChannelsConfig{
			CLI: CLIConfig{Enabled: true, Interactive: true},
		},
		Heartbeat: HeartbeatConfig{
			Interval: "30m",
			Enabled:  false,
		},
		Memory: MemoryConfig{
			Enabled:           true,
			EmbeddingProvider: "local",
			EmbeddingModel:    "nomic-embed-text",
		},
		Cortex: CortexConfig{
			Enabled:  true,
			Provider: "local",
			LLMModel: "gemma4",
		},
		Security: SecurityConfig{
			ExecApprovals: ExecApprovalsConfig{
				Level:     "full",
				Allowlist: []string{"ls", "cat", "find", "grep", "head", "tail", "wc", "pwd", "date"},
			},
			GroupPolicy: GroupPolicyConfig{RequireMention: true},
		},
		Local: LocalConfig{
			Enabled:   true,
			KeepAlive: "24h",
		},
	}
}

// GetProvider returns the provider config for the given name, falling back to
// env vars if not explicitly configured.
func (c *Config) GetProvider(name string) ProviderConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p, ok := c.Providers[name]; ok {
		return p
	}
	return ProviderConfig{}
}

// Validate checks the config for required fields and applies defaults.
func (c *Config) Validate() error {
	if c.Gateway.Port == 0 {
		c.Gateway.Port = 18789
	}
	if c.Gateway.Host == "" {
		c.Gateway.Host = "127.0.0.1"
	}
	if c.Gateway.Reload.Mode == "" {
		c.Gateway.Reload.Mode = "hybrid"
	}

	// Backfill Memory defaults if the section is absent (all zero values).
	// We can't distinguish "user explicitly disabled" from "field missing"
	// in plain JSON unmarshalling, so the heuristic is: if every Memory
	// field is the Go zero value, treat the section as missing.
	if c.Memory == (MemoryConfig{}) {
		c.Memory = DefaultConfig().Memory
	} else if c.Memory.EmbeddingModel == "" {
		c.Memory.EmbeddingModel = "nomic-embed-text"
	}

	// Same heuristic for Cortex.
	if c.Cortex == (CortexConfig{}) {
		c.Cortex = DefaultConfig().Cortex
	} else {
		if c.Cortex.Provider == "" {
			c.Cortex.Provider = "local"
		}
		if c.Cortex.LLMModel == "" {
			c.Cortex.LLMModel = "gemma4"
		}
	}

	if len(c.Agents.List) == 0 {
		return errors.New("at least one agent must be configured")
	}

	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		if a.ID == "" {
			return fmt.Errorf("agent at index %d has no id", i)
		}
		if a.Model == "" {
			return fmt.Errorf("agent %q has no model", a.ID)
		}
		if a.Workspace == "" {
			a.Workspace = filepath.Join(DefaultDataDir(), "workspace-"+a.ID)
		}
		if a.Sandbox == "" {
			a.Sandbox = "none"
		}
	}

	return nil
}

// GetAgent returns the agent config for the given ID.
func (c *Config) GetAgent(id string) (*AgentConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for i := range c.Agents.List {
		if c.Agents.List[i].ID == id {
			return &c.Agents.List[i], true
		}
	}
	return nil, false
}

// Path returns the file path this config was loaded from.
func (c *Config) Path() string {
	return c.path
}

// UpdateFrom copies all configuration fields from src into c under c's lock.
// Use this to refresh the in-memory config after a save without replacing the pointer.
func (c *Config) UpdateFrom(src *Config) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Gateway = src.Gateway
	c.Providers = src.Providers
	c.Agents = src.Agents
	c.Bindings = src.Bindings
	c.Channels = src.Channels
	c.Heartbeat = src.Heartbeat
	c.Memory = src.Memory
	c.Cortex = src.Cortex
	c.Security = src.Security
	c.Local = src.Local
}

// SetPath sets the file path for saving.
func (c *Config) SetPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path = path
}

// Save writes the config to disk as formatted JSON.
func (c *Config) Save() error {
	c.mu.RLock()
	path := c.path
	c.mu.RUnlock()

	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

// stripJSON5 removes single-line comments and trailing commas from JSON5
// to produce valid JSON for the stdlib parser.
func stripJSON5(s string) string {
	var b strings.Builder
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip full-line comments
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		// Remove inline comments (naive: doesn't handle // inside strings,
		// but sufficient for typical config files)
		if idx := strings.Index(line, "//"); idx >= 0 {
			// Only strip if not inside a quoted string
			if !inString(line, idx) {
				line = line[:idx]
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}

	// Remove trailing commas before } or ]
	result := b.String()
	result = removeTrailingCommas(result)
	return result
}

// inString checks if position pos in line is inside a JSON string literal.
func inString(line string, pos int) bool {
	inStr := false
	for i := 0; i < pos; i++ {
		if line[i] == '"' && (i == 0 || line[i-1] != '\\') {
			inStr = !inStr
		}
	}
	return inStr
}

// removeTrailingCommas removes commas that appear before } or ] (with optional whitespace).
func removeTrailingCommas(s string) string {
	runes := []rune(s)
	var out []rune
	for i := 0; i < len(runes); i++ {
		if runes[i] == ',' {
			// Look ahead past whitespace for } or ]
			j := i + 1
			for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t' || runes[j] == '\n' || runes[j] == '\r') {
				j++
			}
			if j < len(runes) && (runes[j] == '}' || runes[j] == ']') {
				continue // skip this trailing comma
			}
		}
		out = append(out, runes[i])
	}
	return string(out)
}

// ResolveMCPServers returns one mcp.ManagerServerConfig per enabled MCPServers
// entry, with the client secret resolved from either the literal config value
// (Auth.ClientSecret) or the named environment variable (Auth.ClientSecretEnv),
// preferring the literal when both are set.
//
// Disabled servers are skipped silently. Returns an error if any enabled
// server has missing required fields. Missing-secret on an otherwise-valid
// enabled server is logged and skipped (not a hard fail) so a misconfigured
// MCP entry doesn't take down the whole gateway — matches how Manager handles
// unreachable servers.
func (c *Config) ResolveMCPServers() ([]mcp.ManagerServerConfig, error) {
	out := make([]mcp.ManagerServerConfig, 0, len(c.MCPServers))
	for _, s := range c.MCPServers {
		if !s.Enabled {
			continue
		}
		if s.ID == "" {
			return nil, fmt.Errorf("mcp_servers: entry with empty id")
		}
		if s.URL == "" {
			return nil, fmt.Errorf("mcp_servers[%s]: url is required", s.ID)
		}
		if s.Auth.Kind != "oauth2_client_credentials" {
			return nil, fmt.Errorf("mcp_servers[%s]: unsupported auth.kind %q (only oauth2_client_credentials in MVP)", s.ID, s.Auth.Kind)
		}
		if s.Auth.TokenURL == "" || s.Auth.ClientID == "" {
			return nil, fmt.Errorf("mcp_servers[%s]: token_url and client_id are required", s.ID)
		}
		secret := s.Auth.ClientSecret
		if secret == "" && s.Auth.ClientSecretEnv != "" {
			secret = os.Getenv(s.Auth.ClientSecretEnv)
		}
		if secret == "" {
			slog.Warn("mcp_servers: skipping server with no resolvable client secret",
				"id", s.ID,
				"hint", "set auth.client_secret in config, or auth.client_secret_env to a populated env var",
			)
			continue
		}
		out = append(out, mcp.ManagerServerConfig{
			ID:           s.ID,
			URL:          s.URL,
			TokenURL:     s.Auth.TokenURL,
			ClientID:     s.Auth.ClientID,
			ClientSecret: secret,
			Scope:        s.Auth.Scope,
			ToolPrefix:   s.ToolPrefix,
		})
	}
	return out, nil
}

// ApplyMCPToolNamesToAllowlists augments each configured agent's Tools.Allow
// list with the supplied MCP tool names. Agents with an empty Allow list are
// left alone (empty = allow all per FilteredRegistry policy). Duplicate names
// are skipped. Modifies the in-memory Config only — not persisted to disk.
//
// Called once at startup AFTER mcp.RegisterTools returns. The mutation is
// ephemeral: on the next process start with no MCP servers configured, the
// allowlists revert to whatever's on disk.
func (c *Config) ApplyMCPToolNamesToAllowlists(names []string) {
	if len(names) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.Agents.List {
		agent := &c.Agents.List[i]
		if len(agent.Tools.Allow) == 0 {
			continue // empty Allow = allow all; no augmentation needed
		}
		existing := make(map[string]bool, len(agent.Tools.Allow))
		for _, n := range agent.Tools.Allow {
			existing[n] = true
		}
		for _, n := range names {
			if !existing[n] {
				agent.Tools.Allow = append(agent.Tools.Allow, n)
				existing[n] = true
			}
		}
	}
}
