package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/sausheong/felix/internal/mcp"
	"github.com/sausheong/felix/internal/tools"
)

// Config is the top-level Felix configuration.
type Config struct {
	Gateway    GatewayConfig             `json:"gateway"`
	Providers  map[string]ProviderConfig `json:"providers"`
	Agents     AgentsConfig              `json:"agents"`
	Bindings   []Binding                 `json:"bindings"`
	Channels   ChannelsConfig            `json:"channels"`
	Memory     MemoryConfig              `json:"memory"`
	Cortex     CortexConfig              `json:"cortex"`
	AgentLoop  AgentLoopConfig           `json:"agentLoop"`
	Security   SecurityConfig            `json:"security"`
	Local      LocalConfig               `json:"local"`
	Telegram   TelegramConfig            `json:"telegram"`
	WebSearch  WebSearchConfig           `json:"web_search"`
	MCPServers []MCPServerConfig         `json:"mcp_servers"`
	OTel       OTelConfig                `json:"otel"`

	mu   sync.RWMutex
	path string

	// mcpAutoAddedNames tracks which tool names ApplyMCPToolNamesToAllowlists
	// added to agent allowlists at startup. Used by StripMCPAutoAdded so the
	// UI's save-config endpoint can write back to disk WITHOUT persisting the
	// runtime-only augmentation. Not JSON-serialized.
	mcpAutoAddedNames []string
}

// TelegramConfig enables outbound Telegram messages via the send_message tool's
// telegram channel.
// Felix does NOT receive Telegram messages — this is send-only.
type TelegramConfig struct {
	Enabled       bool   `json:"enabled"`         // master switch; tool is also disabled if BotToken is empty
	BotToken      string `json:"bot_token"`       // from @BotFather
	DefaultChatID string `json:"default_chat_id"` // optional; used when the agent omits chat_id
}

// MCPServerConfig declares one MCP server that Felix should connect to at
// startup. Tools exposed by the server are registered into the agent's tool
// registry as if they were core tools.
//
// Transport-discriminated. New entries should set `transport` ("http" |
// "stdio") and populate the matching nested block (HTTP or Stdio). The
// legacy flat HTTP layout (top-level URL+Auth) is still accepted for
// backward compatibility — Felix never silently rewrites felix.json5.
type MCPServerConfig struct {
	ID        string         `json:"id"`                  // unique within the list
	Transport string         `json:"transport,omitempty"` // "http" (default) | "stdio"
	HTTP      *MCPHTTPBlock  `json:"http,omitempty"`      // populated when Transport == "http"
	Stdio     *MCPStdioBlock `json:"stdio,omitempty"`     // populated when Transport == "stdio"
	URL       string         `json:"url,omitempty"`       // legacy flat HTTP — accepted on read, never written
	Auth      MCPAuthConfig  `json:"auth,omitempty"`      // legacy flat HTTP — accepted on read, never written
	Enabled   bool           `json:"enabled"`
	// ParallelSafe is a per-server hint that all tools exposed by this server
	// are pure / read-only and may be invoked in parallel by the agent loop.
	// Conservative default false. Flip to true ONLY for servers whose tool
	// surface you've audited as side-effect-free (e.g., search/read tools).
	// A mutating tool on a parallel-safe server is undefined behavior — at
	// best, ordering is non-deterministic; at worst, lost writes.
	//
	// Read live by Config.IsServerParallelSafe (which the MCP tool adapter
	// consults on every IsConcurrencySafe call) so settings-UI toggles take
	// effect on the next agent run without a restart.
	ParallelSafe bool   `json:"parallelSafe,omitempty"`
	ToolPrefix   string `json:"tool_prefix,omitempty"` // optional name prefix
}

// MCPHTTPBlock is the nested HTTP-transport configuration.
type MCPHTTPBlock struct {
	URL  string        `json:"url"`
	Auth MCPAuthConfig `json:"auth"`
}

// MCPStdioBlock is the nested stdio-transport configuration. Env entries
// are merged onto os.Environ() at spawn time so the child inherits PATH
// (and any other parent env vars) unless explicitly overridden.
type MCPStdioBlock struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// MCPAuthConfig describes how Felix authenticates to an HTTP MCP server.
// Supported Kinds:
//   - "oauth2_client_credentials": client-credentials grant; uses TokenURL,
//     ClientID, and one of ClientSecret/ClientSecretEnv (literal wins).
//   - "oauth2_authorization_code": authorization-code + PKCE (RFC 7636) with
//     a loopback redirect (RFC 8252). Uses AuthURL, TokenURL, ClientID,
//     RedirectURI, and one of ClientSecret/ClientSecretEnv (Cognito-style
//     confidential PKCE clients require a secret; pure public clients can
//     leave it empty). On first use Felix opens the OS browser; tokens are
//     persisted under <data-dir>/mcp-tokens/<id>.json and refreshed via the
//     refresh_token grant. Scope defaults to "openid offline_access" so the
//     IdP issues a refresh token.
//   - "bearer": static bearer token; uses one of Token/TokenEnv (literal
//     wins).
//   - "none": no Authorization header.
type MCPAuthConfig struct {
	Kind string `json:"kind"`

	// oauth2_client_credentials, oauth2_authorization_code
	TokenURL        string `json:"token_url,omitempty"`
	ClientID        string `json:"client_id,omitempty"`
	ClientSecret    string `json:"client_secret,omitempty"`     // literal secret value
	ClientSecretEnv string `json:"client_secret_env,omitempty"` // env var NAME holding the secret
	Scope           string `json:"scope,omitempty"`

	// oauth2_authorization_code
	AuthURL     string `json:"auth_url,omitempty"`     // IdP authorize endpoint
	RedirectURI string `json:"redirect_uri,omitempty"` // must be loopback (http://localhost:PORT/...) and registered with the IdP

	// bearer
	Token    string `json:"token,omitempty"`     // literal bearer token
	TokenEnv string `json:"token_env,omitempty"` // env var NAME holding the token
}

// ProviderConfig holds connection details for an LLM provider.
type ProviderConfig struct {
	Kind    string `json:"kind"`     // "openai", "anthropic", "openai-compatible"
	BaseURL string `json:"base_url"` // custom API endpoint (e.g. LiteLLM)
	APIKey  string `json:"api_key"`  // API key or auth token
}

type GatewayConfig struct {
	Host   string       `json:"host"`
	Port   int          `json:"port"`
	Auth   AuthConfig   `json:"auth"`
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
	// MessageCap is a hard backstop on total message count before compaction
	// fires, regardless of token threshold. Local models commonly report
	// 32K-token windows that translate to ~76K chars at our 0.6 default
	// threshold — far above typical Felix prefill (5-25K). Without a count
	// cap, sessions with low-cost tool-heavy turns can grow indefinitely.
	// 0 disables the cap (use only the token threshold). Default 50.
	MessageCap int `json:"messageCap"`
}

type AgentConfig struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Workspace    string       `json:"workspace"`
	Model        string       `json:"model"`
	Reasoning    string       `json:"reasoning,omitempty"` // off | low | medium | high; default off
	Fallbacks    []string     `json:"fallbacks"`
	Sandbox      string       `json:"sandbox"`                 // "none", "docker", "namespace"
	MaxTurns     int          `json:"maxTurns,omitempty"`      // max tool-use loop iterations (0 = default 25)
	SystemPrompt string       `json:"system_prompt,omitempty"` // inline system prompt (overrides IDENTITY.md)
	Tools        ToolPolicy   `json:"tools"`
	Cron         []CronConfig `json:"cron,omitempty"`
	// Subagent marks this agent as opt-in for invocation via the task tool.
	// When true, parent agents can dispatch work to it as a subagent.
	// Defaults to false so existing agents are unaffected.
	Subagent bool `json:"subagent,omitempty"`
	// Description is shown to a parent agent's LLM in the task tool's
	// description so it knows which subagent to pick. Required when
	// Subagent is true.
	Description string `json:"description,omitempty"`
	// InheritContext, when true on a Subagent: copies the parent's session
	// entries into the subagent's fresh in-memory session so the
	// subagent's first LLM call sees the parent's conversation history.
	// Useful for read-only "explore" subagents that should reason over
	// what the parent already knows; CacheLastMessage on the subagent's
	// own subsequent turns naturally caches the inherited prefix.
	// Defaults to false: subagents start cold, isolated from parent state.
	InheritContext bool `json:"inheritContext,omitempty"`
	// FallbackModel, when set, names a "provider/model" string to retry
	// against on a synchronous LLM error matching IsRetryableModelError
	// (Anthropic 429/529, OpenAI 429/5xx). One retry only; mid-stream
	// errors are not recoverable here. Empty disables fallback.
	FallbackModel string `json:"fallbackModel,omitempty"`
	// ContextWindow overrides the auto-detected context window (in
	// tokens) for this agent's model. Use when the auto-detection is
	// wrong — e.g. a proxy exposes Claude under a custom provider with a
	// non-standard window, or you want to clamp a local model below its
	// advertised limit to leave room for output tokens. 0 = use
	// auto-detection (tokens.ContextWindow). Drives the preventive
	// compaction threshold and the token-usage display in the UI.
	ContextWindow int `json:"contextWindow,omitempty"`
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

type MemoryConfig struct {
	Enabled           bool   `json:"enabled"`
	EmbeddingProvider string `json:"embeddingProvider"`
	EmbeddingModel    string `json:"embeddingModel"`
	MaxEntries        int    `json:"maxEntries"`
}

// OTelConfig configures Felix's OpenTelemetry exporter. When Enabled is
// false (the default), Felix emits no OTLP traffic and the existing
// local-only instrumentation (slog logs, /metrics, /logs view) is the
// only telemetry surface.
//
// Endpoint is a full URL, e.g. "http://collector.example.com/" or
// "https://otel.example.com:4318". The scheme decides http-vs-tls and
// the SDK appends /v1/{traces,metrics,logs} per signal.
//
// Standard OTel env vars override the file values when set:
//   - OTEL_EXPORTER_OTLP_ENDPOINT → Endpoint
//   - OTEL_SERVICE_NAME           → ServiceName
//   - OTEL_EXPORTER_OTLP_HEADERS  → Headers (parsed as key1=v1,key2=v2)
//   - OTEL_TRACES_SAMPLER_ARG     → SampleRatio (parsed as float64)
//   - OTEL_SDK_DISABLED=true      → forces Enabled=false
type OTelConfig struct {
	Enabled     bool              `json:"enabled"`
	Endpoint    string            `json:"endpoint"`
	ServiceName string            `json:"serviceName"`
	SampleRatio float64           `json:"sampleRatio"`
	Headers     map[string]string `json:"headers,omitempty"`
	Signals     OTelSignals       `json:"signals"`
}

// OTelSignals toggles the three signal pipelines independently. All three
// default to true when OTel.Enabled flips on, since the typical "connect
// me to a collector" intent wants everything.
type OTelSignals struct {
	Traces  bool `json:"traces"`
	Metrics bool `json:"metrics"`
	Logs    bool `json:"logs"`
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

// AgentLoopConfig tunes the agent runtime's tool-execution behavior.
// All fields are optional; absent/zero values fall back to env vars
// (FELIX_MAX_TOOL_CONCURRENCY, FELIX_MAX_AGENT_DEPTH, FELIX_STREAMING_TOOLS)
// then to compiled-in defaults.
//
// Hot-reloaded via fsnotify; menubar / settings-page edits take effect
// on the next agent run without restart.
type AgentLoopConfig struct {
	// MaxToolConcurrency caps parallel tool dispatch within a safe batch
	// (Phase B). 0 = use FELIX_MAX_TOOL_CONCURRENCY or default 10.
	MaxToolConcurrency int `json:"maxToolConcurrency,omitempty"`

	// MaxAgentDepth caps subagent recursion depth (Phase C). 0 = use
	// FELIX_MAX_AGENT_DEPTH or default 3.
	MaxAgentDepth int `json:"maxAgentDepth,omitempty"`

	// StreamingTools enables mid-stream concurrency-safe tool kickoff
	// (Phase D). Default false. When true, safe tools start as their
	// tool_use blocks land instead of waiting for the LLM stream to end.
	// Note: when this field is absent (false) from felix.json5, the runtime
	// checks FELIX_STREAMING_TOOLS=1 as a fallback. Setting it explicitly
	// to true in JSON5 always wins.
	StreamingTools bool `json:"streamingTools,omitempty"`
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

// WebSearchConfig selects the backend used by the web_search tool.
// Defaults (empty Backend) keep the historical DDG-scraper behavior.
// "brave" / "tavily" need APIKey; "searxng" needs BaseURL.
type WebSearchConfig struct {
	Backend string `json:"backend,omitempty"` // "" | "duckduckgo" | "brave" | "tavily" | "searxng"
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
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

// DataDir returns the directory holding Felix state for this Config. Derived
// from the loaded config-file path (its parent directory). Falls back to
// DefaultDataDir() when the path is unset, e.g. for in-memory test configs.
func (c *Config) DataDir() string {
	if c == nil || c.path == "" {
		return DefaultDataDir()
	}
	return filepath.Dir(c.path)
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

	// Apply standard OTel env-var overrides BEFORE the validate/backfill
	// pass below so the defaults code sees the final intended values.
	// Env wins over file: ops convention, also matches the OTel SDK spec.
	applyOTelEnvOverrides(&cfg.OTel)

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
		// TODO: 0 means "use default" here, but the field doc says 0 disables.
		// Migrate to *int or a sentinel when the broader Compaction merge
		// gets revisited (same dissonance affects Threshold/PreserveTurns/
		// TimeoutSec).
		if cur.MessageCap == 0 {
			cur.MessageCap = d.MessageCap
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
			Host:   "127.0.0.1",
			Port:   18789,
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
					MessageCap:    50,
				},
			},
		},
		Bindings: []Binding{
			{AgentID: "default", Match: BindingMatch{Channel: "cli"}},
		},
		Channels: ChannelsConfig{
			CLI: CLIConfig{Enabled: true, Interactive: true},
		},
		Memory: MemoryConfig{
			Enabled:           true,
			EmbeddingProvider: "local",
			EmbeddingModel:    "nomic-embed-text",
		},
		Cortex: CortexConfig{
			Enabled: true,
			// Provider/LLMModel intentionally empty — cortex mirrors the
			// chatting agent's model. Set these in felix.json5 only to pin
			// cortex to a specific provider regardless of the chat agent.
		},
		OTel: OTelConfig{
			Enabled:     false,
			ServiceName: "felix",
			SampleRatio: 1.0,
			Signals:     OTelSignals{Traces: true, Metrics: true, Logs: true},
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

	// Same heuristic for Cortex, but Provider/LLMModel are deliberately not
	// auto-filled — when both are empty cortex mirrors the chatting agent's
	// model. Setting them here would persist a hard pin into felix.json5 and
	// defeat that mirroring.
	if c.Cortex == (CortexConfig{}) {
		c.Cortex = CortexConfig{Enabled: true}
	}
	// Migration: older builds auto-filled provider="local" + llmModel="gemma4"
	// into the user's config. After the per-call mirror refactor those values
	// became a hard pin that prevented cortex from following the chat agent.
	// Strip the legacy default pair so the mirror kicks in.
	if c.Cortex.Provider == "local" && c.Cortex.LLMModel == "gemma4" {
		c.Cortex.Provider = ""
		c.Cortex.LLMModel = ""
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
		if err := ValidateReasoningMode(a.Reasoning); err != nil {
			return fmt.Errorf("agent %q: %w", a.ID, err)
		}
		if a.Subagent && a.Description == "" {
			return fmt.Errorf("agent %q: subagent=true requires non-empty description", a.ID)
		}
	}

	return nil
}

// EligibleSubagents returns a map of agent_id → description for all agents
// flagged as subagents. Used by the task tool to advertise available subagents
// to a parent LLM and to enforce that only opt-in agents are invocable.
func (c *Config) EligibleSubagents() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]string{}
	for _, a := range c.Agents.List {
		if a.Subagent {
			out[a.ID] = a.Description
		}
	}
	return out
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

// BuildPermissionChecker returns a tools.PermissionChecker covering every
// agent in the config. Single source of truth — used at startup and on
// hot-reload by both entry points (startup.go and cmd/felix/main.go).
//
// An empty Policy{} (no Allow, no Deny) for an agent allows all tools,
// matching the existing FilterToolDefs/Check semantics for the allow-all
// default.
func (c *Config) BuildPermissionChecker() tools.PermissionChecker {
	policies := map[string]tools.Policy{}
	for _, a := range c.Agents.List {
		policies[a.ID] = tools.Policy{
			Allow: a.Tools.Allow,
			Deny:  a.Tools.Deny,
		}
	}
	return tools.NewStaticChecker(policies)
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
	c.Memory = src.Memory
	c.Cortex = src.Cortex
	c.AgentLoop = src.AgentLoop
	c.Security = src.Security
	c.Local = src.Local
	c.Telegram = src.Telegram
	c.WebSearch = src.WebSearch
	// MCPServers must be copied so the live-read parallelSafe closure
	// (Config.IsServerParallelSafe) sees toggles made via the settings UI
	// without a restart. Was previously omitted, silently breaking hot-reload
	// of every mcp_servers field.
	c.MCPServers = src.MCPServers
	c.OTel = src.OTel
}

// applyOTelEnvOverrides folds standard OTel SDK environment variables
// into the parsed config. Env wins over file. Empty env vars are
// ignored (so unsetting an override doesn't accidentally blank the
// configured value).
//
// Recognised:
//   - OTEL_EXPORTER_OTLP_ENDPOINT  → cfg.Endpoint
//   - OTEL_SERVICE_NAME            → cfg.ServiceName
//   - OTEL_EXPORTER_OTLP_HEADERS   → cfg.Headers (parsed as key1=v1,key2=v2)
//   - OTEL_TRACES_SAMPLER_ARG      → cfg.SampleRatio (parsed as float)
//   - OTEL_SDK_DISABLED=true       → forces cfg.Enabled=false
//
// Setting OTEL_EXPORTER_OTLP_ENDPOINT alone is enough to "turn on" OTel
// from the command line without editing felix.json5 — that's the most
// common ops use case.
func applyOTelEnvOverrides(cfg *OTelConfig) {
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); v != "" {
		cfg.Endpoint = v
		cfg.Enabled = true // setting endpoint via env is an implicit enable
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); v != "" {
		cfg.ServiceName = v
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")); v != "" {
		if cfg.Headers == nil {
			cfg.Headers = map[string]string{}
		}
		for _, pair := range strings.Split(v, ",") {
			k, val, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok {
				continue
			}
			cfg.Headers[strings.TrimSpace(k)] = strings.TrimSpace(val)
		}
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG")); v != "" {
		if r, err := strconv.ParseFloat(v, 64); err == nil && r >= 0 {
			cfg.SampleRatio = r
		}
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")); strings.EqualFold(v, "true") {
		cfg.Enabled = false
	}
	// If OTel is enabled but the signals block is empty (every value
	// false), default to all three on. This catches two cases: (1) the
	// user enabled OTel via env var but never wrote `otel.signals` to
	// felix.json5; (2) DefaultConfig() applied to a config file that
	// has an explicit `otel` block but no `signals` sub-block (the
	// unmarshal leaves them zero, overriding our defaults). Without
	// this, "set OTEL_EXPORTER_OTLP_ENDPOINT" silently produces a
	// useless setup with all three signals off.
	if cfg.Enabled && cfg.Signals == (OTelSignals{}) {
		cfg.Signals = OTelSignals{Traces: true, Metrics: true, Logs: true}
	}
	if cfg.Enabled && cfg.SampleRatio == 0 {
		cfg.SampleRatio = 1.0
	}
	if cfg.Enabled && cfg.ServiceName == "" {
		cfg.ServiceName = "felix"
	}
}

// IsServerParallelSafe reports whether the named MCP server is currently
// flagged parallelSafe. Used by mcp.mcpToolAdapter via a closure passed
// at startup, so settings-page toggles take effect on the next agent run
// without a restart. Returns false for unknown server IDs.
func (c *Config) IsServerParallelSafe(id string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, s := range c.MCPServers {
		if s.ID == id {
			return s.ParallelSafe
		}
	}
	return false
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

// ResolveMCPServers returns one mcp.ManagerServerConfig per enabled
// MCPServers entry. The transport (http vs stdio) is selected from the
// Transport discriminator (default "http"), and per-transport secrets are
// resolved from either the literal config field or its named env-var
// fallback (literal wins).
//
// Disabled servers are skipped silently. Returns an error for hard
// validation failures (missing id, unsupported auth kind, missing required
// nested fields). Missing-secret on an otherwise-valid enabled server is
// logged and skipped — matches how Manager handles unreachable servers and
// keeps a single misconfigured entry from taking down the gateway.
//
// For HTTP transport, the nested `http` block wins over the legacy flat
// URL+Auth fields. The legacy flat layout is accepted on read but never
// written back; UI saves emit the nested form.
func (c *Config) ResolveMCPServers() ([]mcp.ManagerServerConfig, error) {
	out := make([]mcp.ManagerServerConfig, 0, len(c.MCPServers))
	for _, s := range c.MCPServers {
		if !s.Enabled {
			continue
		}
		if s.ID == "" {
			// A stub entry (e.g. user clicked "+ Add MCP Server" in the UI
			// but didn't fill in the ID before saving) shouldn't crash the
			// gateway. Log and skip — same posture as missing-secret.
			slog.Warn("mcp_servers: skipping entry with empty id")
			continue
		}

		transport := s.Transport
		if transport == "" {
			transport = "http"
		}

		switch transport {
		case "http":
			httpBlock, skip, err := resolveHTTPBlock(s, c.DataDir())
			if err != nil {
				return nil, err
			}
			if skip {
				continue
			}
			out = append(out, mcp.ManagerServerConfig{
				ID:           s.ID,
				ToolPrefix:   s.ToolPrefix,
				Transport:    "http",
				HTTP:         httpBlock,
				ParallelSafe: s.ParallelSafe,
			})

		case "stdio":
			if s.Stdio == nil || s.Stdio.Command == "" {
				return nil, fmt.Errorf("mcp_servers[%s]: stdio transport requires stdio.command", s.ID)
			}
			out = append(out, mcp.ManagerServerConfig{
				ID:         s.ID,
				ToolPrefix: s.ToolPrefix,
				Transport:  "stdio",
				Stdio: &mcp.StdioServerConfig{
					Command: s.Stdio.Command,
					Args:    s.Stdio.Args,
					Env:     s.Stdio.Env,
				},
				ParallelSafe: s.ParallelSafe,
			})

		default:
			return nil, fmt.Errorf("mcp_servers[%s]: unsupported transport %q", s.ID, transport)
		}
	}
	return out, nil
}

// resolveHTTPBlock picks between the nested HTTP block and the legacy flat
// URL+Auth layout, validates required fields, and resolves auth secrets.
// Returns (block, skip, err): skip=true means the entry should be silently
// dropped (e.g. missing-secret on a bearer/oauth2 server).
//
// dataDir is the resolved Felix data directory; it's used only by the
// oauth2_authorization_code branch to compute the per-server token cache
// path under <dataDir>/mcp-tokens/<id>.json.
func resolveHTTPBlock(s MCPServerConfig, dataDir string) (*mcp.HTTPServerConfig, bool, error) {
	url := ""
	auth := MCPAuthConfig{}
	switch {
	case s.HTTP != nil:
		url = s.HTTP.URL
		auth = s.HTTP.Auth
	case s.URL != "" || s.Auth.Kind != "":
		// Legacy flat layout. Logged at debug only — the user hasn't done
		// anything wrong; this is a backward-compat path.
		slog.Debug("mcp_servers: using legacy flat HTTP layout", "id", s.ID)
		url = s.URL
		auth = s.Auth
	default:
		return nil, false, fmt.Errorf("mcp_servers[%s]: http transport requires either http block or legacy url field", s.ID)
	}
	if url == "" {
		return nil, false, fmt.Errorf("mcp_servers[%s]: http.url is required", s.ID)
	}

	resolved := mcp.HTTPAuthConfig{Kind: auth.Kind}
	switch auth.Kind {
	case "oauth2_client_credentials":
		if auth.TokenURL == "" || auth.ClientID == "" {
			return nil, false, fmt.Errorf("mcp_servers[%s]: oauth2_client_credentials requires token_url and client_id", s.ID)
		}
		secret := auth.ClientSecret
		if secret == "" && auth.ClientSecretEnv != "" {
			secret = os.Getenv(auth.ClientSecretEnv)
		}
		if secret == "" {
			slog.Warn("mcp_servers: skipping server with no resolvable client secret",
				"id", s.ID,
				"hint", "set auth.client_secret in config, or auth.client_secret_env to a populated env var",
			)
			return nil, true, nil
		}
		resolved.TokenURL = auth.TokenURL
		resolved.ClientID = auth.ClientID
		resolved.ClientSecret = secret
		resolved.Scope = auth.Scope

	case "oauth2_authorization_code":
		if auth.AuthURL == "" || auth.TokenURL == "" || auth.ClientID == "" || auth.RedirectURI == "" {
			return nil, false, fmt.Errorf("mcp_servers[%s]: oauth2_authorization_code requires auth_url, token_url, client_id, redirect_uri", s.ID)
		}
		// Secret is optional here — pure public PKCE clients don't have one,
		// while Cognito-style confidential PKCE clients do. The IdP will
		// reject the token exchange if a secret was required and missing,
		// surfacing a clear error to the user.
		secret := auth.ClientSecret
		if secret == "" && auth.ClientSecretEnv != "" {
			secret = os.Getenv(auth.ClientSecretEnv)
		}
		scope := auth.Scope
		if scope == "" {
			// offline_access asks the IdP for a refresh token so we don't
			// pop the browser every hour. openid is the universally accepted
			// minimal scope. Override by setting auth.scope explicitly.
			scope = "openid offline_access"
		}
		if dataDir == "" {
			dataDir = DefaultDataDir()
		}
		resolved.AuthURL = auth.AuthURL
		resolved.TokenURL = auth.TokenURL
		resolved.ClientID = auth.ClientID
		resolved.ClientSecret = secret
		resolved.Scope = scope
		resolved.RedirectURI = auth.RedirectURI
		resolved.TokenStorePath = filepath.Join(dataDir, "mcp-tokens", s.ID+".json")

	case "bearer":
		token := auth.Token
		if token == "" && auth.TokenEnv != "" {
			token = os.Getenv(auth.TokenEnv)
		}
		if token == "" {
			slog.Warn("mcp_servers: skipping bearer server with no resolvable token",
				"id", s.ID,
				"hint", "set auth.token in config, or auth.token_env to a populated env var",
			)
			return nil, true, nil
		}
		resolved.BearerToken = token

	case "none", "":
		// no auth header; nothing to resolve. Normalise empty Kind to "none"
		// so Manager dispatch doesn't have to handle "" specially.
		resolved.Kind = "none"

	default:
		return nil, false, fmt.Errorf("mcp_servers[%s]: unsupported auth.kind %q", s.ID, auth.Kind)
	}

	return &mcp.HTTPServerConfig{URL: url, Auth: resolved}, false, nil
}

// ApplyMCPToolNamesToAllowlists augments each configured agent's Tools.Allow
// list with the supplied MCP tool names. Agents with an empty Allow list are
// left alone (empty = allow all per Policy semantics). Duplicate names
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
	// Snapshot so StripMCPAutoAdded can undo this mutation when the UI's
	// SaveConfig endpoint writes back to disk.
	c.mcpAutoAddedNames = append(c.mcpAutoAddedNames[:0], names...)
}

// ApplyTaskToolToAllowlists augments each agent's Tools.Allow list with the
// "task" tool name when at least one agent in the config is flagged
// Subagent: true. Agents with an empty Allow list are left alone (empty =
// allow all per Policy semantics). Without this augmentation, FilterToolDefs
// would strip the task tool from agents whose Allow list omits it — silently
// disabling subagent delegation for users who carefully curated their
// allowlists.
//
// Modifies the in-memory Config only — not persisted to disk. Tracked in
// mcpAutoAddedNames so StripMCPAutoAdded undoes it on UI save (use that same
// snapshot since the augmentation has identical semantics — runtime-only,
// re-applied next start).
//
// Idempotent: safe to call multiple times (no-op if "task" is already present).
func (c *Config) ApplyTaskToolToAllowlists() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasSubagentLocked() {
		return
	}
	const taskName = "task"
	added := false
	for i := range c.Agents.List {
		agent := &c.Agents.List[i]
		if len(agent.Tools.Allow) == 0 {
			continue
		}
		present := false
		for _, n := range agent.Tools.Allow {
			if n == taskName {
				present = true
				break
			}
		}
		if !present {
			agent.Tools.Allow = append(agent.Tools.Allow, taskName)
			added = true
		}
	}
	if added {
		// Track in the same snapshot used by the MCP path so StripMCPAutoAdded
		// doesn't persist this either.
		present := false
		for _, n := range c.mcpAutoAddedNames {
			if n == taskName {
				present = true
				break
			}
		}
		if !present {
			c.mcpAutoAddedNames = append(c.mcpAutoAddedNames, taskName)
		}
	}
}

// hasSubagentLocked reports whether any agent has Subagent: true. Caller must
// hold c.mu (read or write). Lighter-weight than calling EligibleSubagents
// just to check len > 0.
func (c *Config) hasSubagentLocked() bool {
	for _, a := range c.Agents.List {
		if a.Subagent {
			return true
		}
	}
	return false
}

// StripMCPAutoAdded removes from `other`'s agent allowlists any tool names
// that were auto-added to THIS Config by ApplyMCPToolNamesToAllowlists.
// Used by the UI's SaveConfig handler so saving a config edited via the
// browser does not persist the runtime-only MCP allowlist augmentation
// into felix.json5 (which would leave ghost entries when MCP servers are
// later removed).
func (c *Config) StripMCPAutoAdded(other *Config) {
	c.mu.RLock()
	names := c.mcpAutoAddedNames
	c.mu.RUnlock()
	if len(names) == 0 {
		return
	}
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	for i := range other.Agents.List {
		allow := other.Agents.List[i].Tools.Allow
		if len(allow) == 0 {
			continue
		}
		kept := make([]string, 0, len(allow))
		for _, n := range allow {
			if !nameSet[n] {
				kept = append(kept, n)
			}
		}
		other.Agents.List[i].Tools.Allow = kept
	}
}

// ValidateReasoningMode returns nil for "", "off", "low", "medium",
// "high"; an error otherwise. Case-sensitive — matches the parsing
// done by llm.ParseReasoningMode.
func ValidateReasoningMode(s string) error {
	switch s {
	case "", "off", "low", "medium", "high":
		return nil
	default:
		return fmt.Errorf("reasoning %q invalid (want off|low|medium|high)", s)
	}
}
