# MCP Stage 2 MVP: Config-Driven Subsystem — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Promote the Stage 1 wrappers into a config-driven subsystem so the chat and gateway agents can call remote MCP tools (e.g. AgentCore LTM) end-to-end. Minimum-viable: no hot reload, only `client_secret_env` for the secret. Hot reload and `client_secret_file` are explicitly deferred to a follow-up plan.

**Architecture:** Add `mcp_servers[]` to `felix.json5`. A new `mcp.Manager` opens one persistent session per enabled server at startup. Each remote tool is wrapped in a `tools.Tool` adapter and registered into the existing `tools.Registry` at every site that currently calls `RegisterCoreTools`. Naming collisions are caught at registration time; an optional per-server `tool_prefix` is used to disambiguate.

**Tech Stack:** Go 1.25, existing `github.com/modelcontextprotocol/go-sdk` v1.5.0 + `golang.org/x/oauth2` (added in Stage 1), existing `internal/mcp/` wrappers, existing `internal/config` and `internal/tools` packages.

**Spec:** [`docs/superpowers/specs/2026-04-25-mcp-integration-design.md`](../specs/2026-04-25-mcp-integration-design.md)

**Stage 1 commits this plan builds on:** c2ad339, 0813ffb, 05018f3, af35319, 9c84ffb, 41b4142.

---

## Out of scope (deferred to a follow-up plan)

- fsnotify-driven hot reload of `mcp_servers` (config changes require process restart for now).
- `client_secret_file` auth source (`client_secret_env` only).
- Background reconnect loop (the SDK's `MaxRetries` handles transient failures; a server unreachable at startup is logged + skipped).
- Per-agent MCP server gating beyond the existing `ToolPolicy` allow/deny lists.

---

## File Structure

**Created:**
- `internal/mcp/types.go` — `ManagerServerConfig` struct (defined in T1 Step 0 so config can reference it before Manager exists).
- `internal/mcp/manager.go` — owns one Client per enabled server; `Tools()` returns adapters.
- `internal/mcp/manager_test.go`
- `internal/mcp/adapter.go` — `mcpToolAdapter` implementing `tools.Tool`.
- `internal/mcp/adapter_test.go`
- `internal/mcp/register.go` — `RegisterTools(reg *tools.Registry, mgr *Manager) error` with collision detection.
- `internal/mcp/register_test.go`

**Modified:**
- `internal/config/config.go` — add `MCPServers []MCPServerConfig` field + types.
- `internal/config/config_test.go` — round-trip parsing test for the new block.
- `cmd/felix/main.go` — chat path: build Manager + register MCP tools at the two sites that currently call `RegisterCoreTools`.
- `internal/startup/startup.go` — gateway path: same wiring at the four `RegisterCoreTools` sites.
- `docs/superpowers/specs/2026-04-25-mcp-integration-design.md` — append a "Stage 2 MVP delta" note pointing to deferred items (do this in T8).

---

## Naming and types — pinned upfront

These names are referenced across multiple tasks. Any drift between tasks must be fixed before later tasks proceed.

```go
// internal/config/config.go
type MCPServerConfig struct {
    ID         string         `json:"id"`           // unique; used as fallback prefix on collision
    URL        string         `json:"url"`
    Auth       MCPAuthConfig  `json:"auth"`
    Enabled    bool           `json:"enabled"`
    ToolPrefix string         `json:"tool_prefix,omitempty"`
}

type MCPAuthConfig struct {
    Kind            string `json:"kind"`              // only "oauth2_client_credentials" in MVP
    TokenURL        string `json:"token_url"`
    ClientID        string `json:"client_id"`
    ClientSecretEnv string `json:"client_secret_env"` // env var NAME holding the secret
    Scope           string `json:"scope"`
}
```

```go
// internal/mcp/manager.go
type Manager struct { /* unexported fields */ }
type ServerEntry struct {
    ID         string
    Client     *Client
    ToolPrefix string
}

func NewManager(ctx context.Context, servers []ManagerServerConfig) (*Manager, error)
func (m *Manager) Servers() []*ServerEntry
func (m *Manager) Close() error

// ManagerServerConfig is the resolved-secret form passed to NewManager — keeps
// the mcp package independent of internal/config types.
type ManagerServerConfig struct {
    ID           string
    URL          string
    TokenURL     string
    ClientID     string
    ClientSecret string // already resolved from env
    Scope        string
    ToolPrefix   string
}
```

```go
// internal/mcp/adapter.go
type mcpToolAdapter struct {
    fullName    string          // prefixed name registered into the Registry
    remoteName  string          // name as the MCP server knows it
    description string
    schema      json.RawMessage
    client      *Client
}
// implements tools.Tool: Name, Description, Parameters, Execute
```

```go
// internal/mcp/register.go
func RegisterTools(reg *tools.Registry, mgr *Manager) error
```

---

## Task 1: Config schema for `mcp_servers`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

Adds the new top-level `mcp_servers` field to `Config` and a helper that resolves each server's secret from the named env var, returning `[]mcp.ManagerServerConfig` ready for `mcp.NewManager`.

The helper lives in `config` (not `mcp`) so the `mcp` package has zero knowledge of `internal/config`. We pass an opaque slice across the boundary.

> **Heads-up on import cycles:** `internal/config` cannot import `internal/mcp` (mcp would need types defined in config). Solution: the helper returns `[]ManagerServerConfig` defined IN the `mcp` package, and `config` imports `mcp`. This is fine — `mcp` already exists and depends on nothing from `config`. Verify with `go vet` after the change.

> **Build-order note:** the `Manager` itself lands in Task 2, but `Config.ResolveMCPServers` references `mcp.ManagerServerConfig`. Step 0 below adds JUST that struct (no Manager) so this task's commit compiles standalone. Task 2 then adds Manager in the same `internal/mcp/types.go` file (or a separate file — either works).

- [ ] **Step 0: Stub the type in `internal/mcp/types.go`**

Create `internal/mcp/types.go`:

```go
package mcp

// ManagerServerConfig is the resolved-secret shape Manager consumes. The
// caller (typically Config.ResolveMCPServers) is responsible for pulling
// the client secret out of its source (env var, file, etc.) before calling
// NewManager.
//
// Defined here so internal/config can return this type from
// ResolveMCPServers without depending on Manager itself (which lands in
// the next task).
type ManagerServerConfig struct {
	ID           string
	URL          string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string
	ToolPrefix   string
}
```

This file gets ZERO test coverage on its own — it's just a struct. Coverage comes from Task 2's manager_test.go.

- [ ] **Step 1: Add types to `internal/config/config.go`**

Add to the `Config` struct (in the same `struct { ... }` block):

```go
	MCPServers []MCPServerConfig        `json:"mcp_servers"`
```

Below the existing `TelegramConfig` declaration (or anywhere in the file), add:

```go
// MCPServerConfig declares one remote MCP server that Felix should connect to
// at startup. Tools exposed by the server are registered into the agent's
// tool registry as if they were core tools.
type MCPServerConfig struct {
	ID         string        `json:"id"`                      // unique within the list
	URL        string        `json:"url"`                     // MCP Streamable-HTTP endpoint
	Auth       MCPAuthConfig `json:"auth"`
	Enabled    bool          `json:"enabled"`
	ToolPrefix string        `json:"tool_prefix,omitempty"`   // optional name prefix
}

// MCPAuthConfig describes how Felix authenticates to an MCP server. MVP
// supports only OAuth2 client-credentials; additional kinds (e.g.
// "bearer_static") will plug in via the Kind discriminator.
type MCPAuthConfig struct {
	Kind            string `json:"kind"`              // "oauth2_client_credentials"
	TokenURL        string `json:"token_url"`
	ClientID        string `json:"client_id"`
	ClientSecretEnv string `json:"client_secret_env"` // env var NAME holding the secret
	Scope           string `json:"scope"`
}
```

- [ ] **Step 2: Add the resolver helper to `internal/config/config.go`**

Append at the bottom of the file:

```go
// ResolveMCPServers returns one mcp.ManagerServerConfig per enabled MCPServers
// entry, with the client secret resolved from the named environment variable.
// Disabled servers are skipped silently. Returns an error if any enabled
// server has missing required fields or its secret env var is unset.
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
		if s.Auth.TokenURL == "" || s.Auth.ClientID == "" || s.Auth.ClientSecretEnv == "" {
			return nil, fmt.Errorf("mcp_servers[%s]: token_url, client_id, and client_secret_env are required", s.ID)
		}
		secret := os.Getenv(s.Auth.ClientSecretEnv)
		if secret == "" {
			return nil, fmt.Errorf("mcp_servers[%s]: env var %s is empty or unset", s.ID, s.Auth.ClientSecretEnv)
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
```

Add the import at the top (alphabetical order in the existing import block):

```go
	"github.com/sausheong/felix/internal/mcp"
```

(`fmt` and `os` are already imported.)

- [ ] **Step 3: Write tests in `internal/config/config_test.go`**

Open `internal/config/config_test.go`, find an existing test as a template, then append:

```go
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
```

(Confirm `require` and `assert` are already imported in `config_test.go`. If not, add them.)

- [ ] **Step 4: Run tests**

```bash
go test ./internal/config/... -count=1 -v -run TestResolveMCPServers
```

Expected: all 3 tests PASS.

- [ ] **Step 5: Make sure full config tests still pass and build is clean**

```bash
go test ./internal/config/... -count=1
go build ./...
go vet ./...
```

Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add mcp_servers schema + secret resolver

ResolveMCPServers reads each enabled mcp_servers entry, pulls the
client secret from the named env var, and returns the ready-to-use
form for mcp.NewManager. MVP only supports oauth2_client_credentials."
```

---

## Task 2: `mcp.Manager` — own one Client per enabled server

**Files:**
- Create: `internal/mcp/manager.go`
- Test: `internal/mcp/manager_test.go`

`Manager` opens all configured servers at construction time. Servers that fail to connect are logged and skipped — startup continues with whatever did connect. `Tools()` returns the full set of remote tools (across all servers) along with the server they came from.

- [ ] **Step 1: Write the failing test**

Create `internal/mcp/manager_test.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMCPWithTools spins up the same fake protocol from client_test.go but
// allows the caller to supply the tool list returned by tools/list.
func fakeMCPWithTools(t *testing.T, tools []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		method, _ := req["method"].(string)
		id := req["id"]
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "fake", "version": "0"},
				},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"tools": tools},
			})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
}

// fakeTokenServer returns a static OAuth token, so Manager can be tested with
// real OAuth wiring (not just http.DefaultClient).
func fakeTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-abc", "token_type": "Bearer", "expires_in": 3600,
		})
	}))
}

func TestManager_OpensAllEnabledServers(t *testing.T) {
	srvA := fakeMCPWithTools(t, []map[string]any{
		{"name": "a_tool", "description": "from A", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srvA.Close()
	srvB := fakeMCPWithTools(t, []map[string]any{
		{"name": "b_tool", "description": "from B", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srvB.Close()
	tok := fakeTokenServer(t)
	defer tok.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mgr, err := NewManager(ctx, []ManagerServerConfig{
		{ID: "a", URL: srvA.URL, TokenURL: tok.URL, ClientID: "cid", ClientSecret: "sec", ToolPrefix: "a_"},
		{ID: "b", URL: srvB.URL, TokenURL: tok.URL, ClientID: "cid", ClientSecret: "sec"},
	})
	require.NoError(t, err)
	defer mgr.Close()

	servers := mgr.Servers()
	require.Len(t, servers, 2)

	ids := []string{servers[0].ID, servers[1].ID}
	assert.ElementsMatch(t, []string{"a", "b"}, ids)
}

func TestManager_SkipsUnreachableServer(t *testing.T) {
	srvOK := fakeMCPWithTools(t, []map[string]any{
		{"name": "ok", "description": "alive", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srvOK.Close()
	tok := fakeTokenServer(t)
	defer tok.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	mgr, err := NewManager(ctx, []ManagerServerConfig{
		{ID: "ok", URL: srvOK.URL, TokenURL: tok.URL, ClientID: "c", ClientSecret: "s"},
		{ID: "dead", URL: "http://127.0.0.1:1/closed", TokenURL: tok.URL, ClientID: "c", ClientSecret: "s"},
	})
	require.NoError(t, err) // no hard failure; dead server is logged and skipped
	defer mgr.Close()

	servers := mgr.Servers()
	require.Len(t, servers, 1)
	assert.Equal(t, "ok", servers[0].ID)
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/mcp/... -run TestManager
```

Expected: FAIL — `undefined: NewManager`. (`ManagerServerConfig` already exists from T1 Step 0.)

- [ ] **Step 3: Implement `internal/mcp/manager.go`**

(`ManagerServerConfig` was added in T1 Step 0 — do NOT redeclare it. Reference it directly.)

```go
package mcp

import (
	"context"
	"fmt"
	"log/slog"
)

// ServerEntry is a connected MCP server known to the Manager. Exposed so
// callers (the Tool registration code) can iterate without reaching into
// Manager internals.
type ServerEntry struct {
	ID         string
	Client     *Client
	ToolPrefix string
}

// Manager owns a Client per enabled MCP server. Servers that fail to
// connect at startup are logged and skipped — Manager construction still
// succeeds so the rest of the gateway can start.
type Manager struct {
	servers []*ServerEntry
}

// NewManager opens a session against each ManagerServerConfig in cfgs.
// Returns an error only on construction failures the caller can do nothing
// about (currently: none — every per-server failure is non-fatal).
func NewManager(ctx context.Context, cfgs []ManagerServerConfig) (*Manager, error) {
	m := &Manager{}
	for _, cfg := range cfgs {
		httpClient := NewClientCredentialsHTTPClient(ClientCredentialsConfig{
			TokenURL:     cfg.TokenURL,
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Scope:        cfg.Scope,
		})
		client, err := Connect(ctx, cfg.URL, httpClient)
		if err != nil {
			slog.Warn("mcp: failed to connect to server, skipping",
				"id", cfg.ID, "url", cfg.URL, "error", err)
			continue
		}
		m.servers = append(m.servers, &ServerEntry{
			ID:         cfg.ID,
			Client:     client,
			ToolPrefix: cfg.ToolPrefix,
		})
		slog.Info("mcp: connected to server", "id", cfg.ID, "url", cfg.URL)
	}
	return m, nil
}

// Servers returns the connected server entries.
func (m *Manager) Servers() []*ServerEntry {
	if m == nil {
		return nil
	}
	return m.servers
}

// Close terminates every server session. Errors are aggregated into a single
// returned error (joined with newlines) but Close always attempts every server.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	var combined string
	for _, s := range m.servers {
		if err := s.Client.Close(); err != nil {
			if combined != "" {
				combined += "\n"
			}
			combined += fmt.Sprintf("close %s: %v", s.ID, err)
		}
	}
	if combined != "" {
		return fmt.Errorf("%s", combined)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/mcp/... -count=1 -v -run TestManager
```

Expected: 2 PASS.

- [ ] **Step 5: Run full mcp suite**

```bash
go test ./internal/mcp/... -count=1
```

Expected: all green (existing 8 + new 2).

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/manager.go internal/mcp/manager_test.go
git commit -m "feat(mcp): Manager owns connected sessions per server

NewManager opens a Client per enabled ManagerServerConfig and skips
unreachable servers (logged at warn level). Servers() exposes the
connected set for the upcoming tool-registration code."
```

---

## Task 3: `mcpToolAdapter` implementing `tools.Tool`

**Files:**
- Create: `internal/mcp/adapter.go`
- Test: `internal/mcp/adapter_test.go`

The adapter is what makes a remote MCP tool look like a Felix tool. It holds the prefixed name, description, JSON-schema, and a reference to the `*Client` that owns the underlying session. `Execute` unmarshals the input, calls `Client.CallTool`, and converts the `CallResult` to a `tools.ToolResult`.

- [ ] **Step 1: Write the failing test**

Create `internal/mcp/adapter_test.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeMCPWithEcho(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		method, _ := req["method"].(string)
		id := req["id"]
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "fake", "version": "0"},
				},
			})
		case "tools/call":
			params, _ := req["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			text, _ := args["text"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "echo: " + text},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
}

func TestAdapter_Execute(t *testing.T) {
	srv := fakeMCPWithEcho(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	defer c.Close()

	a := newToolAdapter("ltm_echo", "echo", "Echo back text",
		json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		c)

	assert.Equal(t, "ltm_echo", a.Name())
	assert.Equal(t, "Echo back text", a.Description())
	assert.JSONEq(t, `{"type":"object","properties":{"text":{"type":"string"}}}`, string(a.Parameters()))

	res, err := a.Execute(ctx, json.RawMessage(`{"text":"hi"}`))
	require.NoError(t, err)
	assert.Equal(t, "echo: hi", res.Output)
	assert.Empty(t, res.Error)
}

func TestAdapter_BadInput(t *testing.T) {
	a := newToolAdapter("x", "x", "", nil, nil)
	res, err := a.Execute(context.Background(), json.RawMessage(`not json`))
	require.NoError(t, err) // tool errors are surfaced via res.Error, not err
	assert.Contains(t, res.Error, "invalid arguments")
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/mcp/... -run TestAdapter
```

Expected: FAIL — `undefined: newToolAdapter`.

- [ ] **Step 3: Implement `internal/mcp/adapter.go`**

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sausheong/felix/internal/tools"
)

// mcpToolAdapter wraps a remote MCP tool as a Felix tools.Tool. The adapter
// is constructed by RegisterTools (one per remote tool per server) and
// registered into a tools.Registry alongside core tools.
type mcpToolAdapter struct {
	fullName    string          // name as Felix sees it (with prefix applied)
	remoteName  string          // name as the MCP server knows it
	description string
	schema      json.RawMessage
	client      *Client
}

// newToolAdapter is package-private constructor. RegisterTools is the only
// in-package caller; tests may use it via the same package.
func newToolAdapter(fullName, remoteName, description string, schema json.RawMessage, client *Client) *mcpToolAdapter {
	return &mcpToolAdapter{
		fullName:    fullName,
		remoteName:  remoteName,
		description: description,
		schema:      schema,
		client:      client,
	}
}

func (a *mcpToolAdapter) Name() string                { return a.fullName }
func (a *mcpToolAdapter) Description() string         { return a.description }
func (a *mcpToolAdapter) Parameters() json.RawMessage { return a.schema }

func (a *mcpToolAdapter) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return tools.ToolResult{Error: fmt.Sprintf("invalid arguments JSON: %v", err)}, nil
		}
	}
	res, err := a.client.CallTool(ctx, a.remoteName, args)
	if err != nil {
		// Transport-level failure — surface as tool error, not a Go error,
		// so the agent loop can keep going.
		return tools.ToolResult{Error: err.Error()}, nil
	}
	tr := tools.ToolResult{Output: res.Text}
	if res.IsError {
		// Tool ran but reported an error. Put the text in Error so the agent
		// sees it as such; keep Output empty to avoid double-display.
		tr.Output = ""
		tr.Error = res.Text
		if tr.Error == "" {
			tr.Error = "tool returned isError without text"
		}
	}
	return tr, nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/mcp/... -count=1 -run TestAdapter -v
```

Expected: 2 PASS.

- [ ] **Step 5: Make sure nothing else broke**

```bash
go test ./internal/mcp/... -count=1
go build ./...
go vet ./...
```

Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/adapter.go internal/mcp/adapter_test.go
git commit -m "feat(mcp): mcpToolAdapter implements tools.Tool

Wraps a remote MCP tool so it can be registered into the existing
tools.Registry. Tool-side errors (isError true) are mapped to
tools.ToolResult.Error; transport errors are surfaced the same way
so the agent loop doesn't crash on a flaky upstream."
```

---

## Task 4: `mcp.RegisterTools` with collision detection + prefix

**Files:**
- Create: `internal/mcp/register.go`
- Test: `internal/mcp/register_test.go`

`RegisterTools(reg, mgr)` walks every server's tools, computes the registered name (prefix + remote name, or just remote name if no prefix), checks for collisions against names already in the registry, and registers the adapter. Collisions are fatal — startup must fail loudly so the operator picks a `tool_prefix`.

- [ ] **Step 1: Write the failing test**

Create `internal/mcp/register_test.go`:

```go
package mcp

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sausheong/felix/internal/tools"
)

func TestRegisterTools_AddsPrefixedAdapters(t *testing.T) {
	srv := fakeMCPWithTools(t, []map[string]any{
		{"name": "search", "description": "search", "inputSchema": map[string]any{"type": "object"}},
		{"name": "store", "description": "store", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	mgr := &Manager{servers: []*ServerEntry{{ID: "ltm", Client: c, ToolPrefix: "ltm_"}}}
	defer mgr.Close()

	reg := tools.NewRegistry()
	require.NoError(t, RegisterTools(reg, mgr))

	names := reg.Names()
	assert.ElementsMatch(t, []string{"ltm_search", "ltm_store"}, names)
}

func TestRegisterTools_NoPrefix_NoCollision(t *testing.T) {
	srv := fakeMCPWithTools(t, []map[string]any{
		{"name": "remote_only", "description": "x", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	mgr := &Manager{servers: []*ServerEntry{{ID: "x", Client: c, ToolPrefix: ""}}}
	defer mgr.Close()

	reg := tools.NewRegistry()
	require.NoError(t, RegisterTools(reg, mgr))
	assert.ElementsMatch(t, []string{"remote_only"}, reg.Names())
}

func TestRegisterTools_CollisionFails(t *testing.T) {
	srv := fakeMCPWithTools(t, []map[string]any{
		{"name": "bash", "description": "fake bash", "inputSchema": map[string]any{"type": "object"}},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx, srv.URL, http.DefaultClient)
	require.NoError(t, err)
	mgr := &Manager{servers: []*ServerEntry{{ID: "x", Client: c, ToolPrefix: ""}}}
	defer mgr.Close()

	reg := tools.NewRegistry()
	tools.RegisterCoreTools(reg, "", nil) // installs real bash; collision ahead

	err = RegisterTools(reg, mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bash")
	assert.Contains(t, err.Error(), "collision")
}
```

- [ ] **Step 2: Run test to verify failure**

```bash
go test ./internal/mcp/... -run TestRegisterTools
```

Expected: FAIL — `undefined: RegisterTools`.

- [ ] **Step 3: Implement `internal/mcp/register.go`**

```go
package mcp

import (
	"context"
	"fmt"

	"github.com/sausheong/felix/internal/tools"
)

// RegisterTools registers every tool exposed by mgr's servers into reg, with
// the per-server ToolPrefix applied. Collisions with names already in reg
// (e.g. core tools) cause a hard error — operators must set tool_prefix to
// disambiguate. Server enumeration order matches Manager.Servers().
//
// Uses a fresh background context for tools/list with no per-call timeout —
// the overall startup deadline (held by the caller) governs total time.
func RegisterTools(reg *tools.Registry, mgr *Manager) error {
	if mgr == nil {
		return nil
	}
	ctx := context.Background()
	for _, s := range mgr.Servers() {
		toolList, err := s.Client.ListTools(ctx)
		if err != nil {
			return fmt.Errorf("mcp[%s]: list tools: %w", s.ID, err)
		}
		for _, t := range toolList {
			fullName := s.ToolPrefix + t.Name
			if _, exists := reg.Get(fullName); exists {
				return fmt.Errorf("mcp[%s]: tool name collision on %q — set tool_prefix in mcp_servers config", s.ID, fullName)
			}
			reg.Register(newToolAdapter(fullName, t.Name, t.Description, t.InputSchema, s.Client))
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/mcp/... -count=1 -run TestRegisterTools -v
```

Expected: 3 PASS.

- [ ] **Step 5: Full sweep**

```bash
go test ./... -count=1
go build ./...
go vet ./...
```

Expected: all green except the pre-existing `internal/agent.TestAssembleSystemPromptDefault` failure (unrelated).

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/register.go internal/mcp/register_test.go
git commit -m "feat(mcp): RegisterTools registers MCP adapters into tools.Registry

Applies per-server tool_prefix and fails loud on name collisions so
operators are forced to disambiguate before startup proceeds."
```

---

## Task 5: Wire MCP into the chat path (`cmd/felix/main.go`)

**Files:**
- Modify: `cmd/felix/main.go`

The chat command builds a tool registry once for the interactive agent and one per cron job. Both need MCP tools. The Manager is built once at the start of `runChat` and passed into both registration sites; it's closed when `runChat` returns.

- [ ] **Step 1: Add the import**

In the existing import block of `cmd/felix/main.go`, add:

```go
	"github.com/sausheong/felix/internal/mcp"
```

(Alphabetically it slots between `llm` and `memory` — match the existing style.)

- [ ] **Step 2: Build the Manager and register MCP tools at the main chat site**

Open `cmd/felix/main.go`, find the chat tool-registration block (around lines 305–318):

```go
	// Init tools
	toolReg := tools.NewRegistry()
	execPolicy := &tools.ExecPolicy{
		Level:     cfg.Security.ExecApprovals.Level,
		Allowlist: cfg.Security.ExecApprovals.Allowlist,
	}
	tools.RegisterCoreTools(toolReg, agentCfg.Workspace, execPolicy)
	tools.RegisterSendMessage(toolReg, func() tools.SendMessageRegistration { ... })
```

Insert MCP construction + registration directly AFTER the `RegisterSendMessage` call:

```go
	// Connect to configured MCP servers and register their tools alongside core tools.
	mcpServerCfgs, err := cfg.ResolveMCPServers()
	if err != nil {
		return fmt.Errorf("resolve mcp_servers: %w", err)
	}
	mcpInitCtx, mcpInitCancel := context.WithTimeout(context.Background(), 30*time.Second)
	mcpMgr, err := mcp.NewManager(mcpInitCtx, mcpServerCfgs)
	mcpInitCancel()
	if err != nil {
		return fmt.Errorf("init mcp manager: %w", err)
	}
	defer mcpMgr.Close()
	if err := mcp.RegisterTools(toolReg, mcpMgr); err != nil {
		return fmt.Errorf("register mcp tools: %w", err)
	}
```

(`time` is already imported in this file. `context` too.)

- [ ] **Step 3: Wire MCP into the cron sub-registry**

Find the cron-job factory block (around lines 332–357). The factory builds a fresh `cronToolReg` per invocation. Add MCP registration to it too. Inside the factory function, AFTER `tools.RegisterCoreTools(cronToolReg, agentCfg.Workspace, execPolicy)`:

```go
				if err := mcp.RegisterTools(cronToolReg, mcpMgr); err != nil {
					return "", fmt.Errorf("register mcp tools for cron: %w", err)
				}
```

Note: this reuses the SAME `mcpMgr` from the outer scope — we do NOT open a new connection per cron run. The Manager and its sessions live for the lifetime of `runChat`.

- [ ] **Step 4: Build and verify the chat path still compiles**

```bash
go build -o felix ./cmd/felix
./felix chat --help
```

Expected: clean build; help text unchanged.

- [ ] **Step 5: Smoke test with NO mcp_servers configured**

The new code MUST be a no-op when `cfg.MCPServers` is empty. Verify:

```bash
./felix chat 2>&1 | head -5
```

(Send Ctrl-C immediately — we just want startup to succeed without any MCP-related errors.)

Expected: chat starts normally. If it errors with anything mentioning mcp/manager/resolve, the empty-list path needs a guard — go back and check `ResolveMCPServers` returns `nil, nil` when there are no entries, and `NewManager(ctx, nil)` succeeds.

- [ ] **Step 6: Commit**

```bash
git add cmd/felix/main.go
git commit -m "feat(cli): wire MCP manager into chat tool registries

Both the interactive agent's registry and the per-cron registry now
include MCP tools alongside core tools. One Manager is shared across
both for the lifetime of the chat session."
```

---

## Task 6: Wire MCP into the gateway path (`internal/startup/startup.go`)

**Files:**
- Modify: `internal/startup/startup.go`

Same pattern as Task 5, but the gateway has FOUR registration sites (main + heartbeat + 2 cron sites). The Manager is built once in `StartGateway` and stored in the result so its `Cleanup()` can close it.

- [ ] **Step 1: Read the existing structure first**

```bash
grep -n "RegisterCoreTools\|Cleanup\|StartupResult\|return result" internal/startup/startup.go | head -20
```

You'll see exactly where the four sites are and where the cleanup chain lives. The plan's line numbers (375-380, 515-516, 562-563, 606-607 from earlier `grep`) may have drifted — locate by symbol, not by number.

- [ ] **Step 2: Add the import**

In the existing import block of `internal/startup/startup.go`, add:

```go
	"github.com/sausheong/felix/internal/mcp"
```

- [ ] **Step 3: Build the Manager once, near the main `RegisterCoreTools`**

Find the block in `StartGateway` (around line 375–392) that constructs `toolReg`. Directly AFTER the `tools.RegisterSendMessage(toolReg, sendMsgConfigFn)` line, add:

```go
	// Initialize MCP manager once and register tools into the main registry.
	mcpServerCfgs, err := cfg.ResolveMCPServers()
	if err != nil {
		return nil, fmt.Errorf("resolve mcp_servers: %w", err)
	}
	mcpInitCtx, mcpInitCancel := context.WithTimeout(context.Background(), 30*time.Second)
	mcpMgr, err := mcp.NewManager(mcpInitCtx, mcpServerCfgs)
	mcpInitCancel()
	if err != nil {
		return nil, fmt.Errorf("init mcp manager: %w", err)
	}
	if err := mcp.RegisterTools(toolReg, mcpMgr); err != nil {
		mcpMgr.Close()
		return nil, fmt.Errorf("register mcp tools: %w", err)
	}
```

(Match the function's actual error-return shape — if `StartGateway` returns `(*StartupResult, error)`, use `nil, err`. Adjust to whatever's there.)

- [ ] **Step 4: Hook `mcpMgr.Close()` into the cleanup chain**

Find where `Cleanup` (or equivalent) is assembled in the result struct. Add an `mcpMgr.Close()` call to it, ahead of any logger flushes so connection-close errors get captured in logs. If the existing pattern uses `defer` chains, add the close to the same chain.

- [ ] **Step 5: Add MCP registration to each per-call sub-registry**

Search for every other `tools.RegisterCoreTools(...)` site in the file. For each one, immediately AFTER the call, add:

```go
		if err := mcp.RegisterTools(<thatLocalRegistry>, mcpMgr); err != nil {
			slog.Warn("mcp: failed to register tools for sub-registry, continuing", "error", err)
		}
```

Use `slog.Warn` and continue rather than returning an error from a sub-registry — the heartbeat/cron paths are best-effort and shouldn't kill the gateway over an MCP hiccup.

The four sites (per earlier grep) are at startup.go:380 (main, returns error — handled in Step 3), :516, :563, :607. Verify all of them are covered.

- [ ] **Step 6: Build and run the unit suite**

```bash
go build ./...
go test ./... -count=1
```

Expected: green except the pre-existing `internal/agent.TestAssembleSystemPromptDefault` failure.

- [ ] **Step 7: Commit**

```bash
git add internal/startup/startup.go
git commit -m "feat(gateway): wire MCP manager into all gateway tool registries

The main, heartbeat, and cron tool registries all get the same
Manager-owned MCP tools. Manager close is tied into the gateway
cleanup chain. Sub-registry MCP failures are warned and skipped
to keep heartbeat/cron robust to upstream blips."
```

---

## Task 7: End-to-end verification with the agent in chat

**Files:**
- None (manual verification — and a temporary edit to `~/.felix/felix.json5`).

This is the proof point: a real Felix chat agent calls `target-ltm___whoami` through the MCP wiring. No automated test can cover this because it touches the user's actual Felix data dir.

- [ ] **Step 1: Add an `mcp_servers` block to your felix config**

Open `~/.felix/felix.json5` (or wherever your config lives). Add the `mcp_servers` block at the top level:

```json5
mcp_servers: [
  {
    id: "agentcore-ltm",
    url: "https://test-gateway-ltm-qmqgb0e2ls.gateway.bedrock-agentcore.ap-southeast-1.amazonaws.com/mcp",
    enabled: true,
    tool_prefix: "ltm_",
    auth: {
      kind: "oauth2_client_credentials",
      token_url: "https://ap-southeast-1j88qbg6pb.auth.ap-southeast-1.amazoncognito.com/oauth2/token",
      client_id: "1jf3u0qgcukfh1rf2r92fo5aj",
      client_secret_env: "FELIX_LTM_SECRET",
      scope: "test-ltm-sr/ltm-api-access",
    },
  },
],
```

- [ ] **Step 2: Export the secret**

```bash
export FELIX_LTM_SECRET='1cuspboi4m9ddpu2v4g7l323rbochgnteb0dqimqj7v4lu5ql0eh'
```

(Same value as `LTM_CLIENT_SECRET` in `gt-harness.txt`.)

- [ ] **Step 3: Build and start a chat**

```bash
go build -o felix ./cmd/felix
./felix chat
```

Watch the slog output for `mcp: connected to server id=agentcore-ltm`. If it warns about a connection failure, double-check the env var is exported in this same shell.

- [ ] **Step 4: Ask the agent to use the tool**

In the chat prompt, type something like:

```
use the ltm_target-ltm___whoami tool with empty args and tell me what it returns
```

Note the tool name: `tool_prefix` is `ltm_` and the remote name is `target-ltm___whoami`, so the full registered name is `ltm_target-ltm___whoami`. The agent will see it in its tool list.

Expected: the agent calls the tool, gets back the JSON object with `user_id`/`agent_id`/`session_id`, and reports it.

- [ ] **Step 5: Capture sanitized evidence**

Copy the relevant chat turn (with IDs redacted) into the commit message of Task 8. If the agent successfully called the tool but the response had IDs in it, redact them as `<redacted>`.

If the call FAILED (for any reason), debug:
- Did the manager connect? (Check slog for `mcp: connected to server`.)
- Was the tool registered? (Add a temporary `slog.Info("registered", "names", reg.Names())` before chat starts, or check the agent's `tool_use` debug output.)
- Did the agent pick the wrong tool name? (The full prefixed name is what it sees.)

Do NOT proceed to Task 8 until the call succeeds — the whole point of Stage 2 is this round-trip working.

---

## Task 8: Documentation pass + closing commit

**Files:**
- Modify: `docs/superpowers/specs/2026-04-25-mcp-integration-design.md`
- Modify: `README.md` (if it has a config example section — quick check)

Document what shipped vs what was deferred so the next person (or future you) doesn't re-litigate it.

- [ ] **Step 1: Append a "Stage 2 MVP — what shipped" section to the spec**

Open `docs/superpowers/specs/2026-04-25-mcp-integration-design.md`. At the very bottom (after "Open questions"), append:

```markdown
---

## Stage 2 MVP — what shipped (2026-04-25)

The MVP plan (`docs/superpowers/plans/2026-04-25-mcp-stage2-mvp-subsystem.md`)
shipped the config-driven subsystem minus two deferred features:

- **Hot reload of `mcp_servers`:** config changes still require a process
  restart. The fsnotify watcher fires `cfg.callback` but Manager has no
  Reconfigure method yet.
- **`client_secret_file` auth source:** only `client_secret_env` is supported
  in MVP. The schema field is reserved for the follow-up.
- **Background reconnect loop:** the SDK transport's `MaxRetries` (default 5)
  handles transient HTTP failures. Servers that fail at startup are logged
  + skipped; recovery requires a restart.

These are tracked for a Stage 2.1 plan.

End-to-end verified: chat agent successfully called `ltm_target-ltm___whoami`
through the wired Manager against the live AgentCore gateway.
```

- [ ] **Step 2: Quick check — does README.md need an entry?**

```bash
grep -n "mcp_servers\|telegram\|providers\|cortex" README.md | head
```

If README has a config section that lists top-level keys, add `mcp_servers` to it briefly. If not, skip — the spec is the source of truth for now.

- [ ] **Step 3: Final test run**

```bash
go test ./... -count=1
```

Expected: green except the pre-existing `internal/agent.TestAssembleSystemPromptDefault` failure.

- [ ] **Step 4: Commit (with the verification evidence from Task 7 in the body)**

```bash
cat > /tmp/t8-msg.txt <<'EOF'
docs(mcp): document Stage 2 MVP and deferred items

Stage 2 of MCP integration verified end-to-end via chat:

  > use the ltm_target-ltm___whoami tool with empty args and tell me what it returns
  [agent calls tool, returns the whoami JSON]
  Result: user_id=<redacted>, agent_id=<redacted>, session_id=<ephemeral>

Deferred to Stage 2.1: hot reload, client_secret_file, background
reconnect. Tracked in the spec.
EOF
git commit -F /tmp/t8-msg.txt
rm /tmp/t8-msg.txt
```

(Substitute the real captured chat output, with IDs redacted, into the heredoc.)

---

## Done criteria

Stage 2 MVP is complete when:
- `go test ./... -count=1` is green (except the unrelated pre-existing agent test).
- A configured `mcp_servers` entry causes Manager to connect at startup (visible in slog).
- An agent in a chat session can successfully call a remote MCP tool through the registered adapter.
- Empty `mcp_servers` is a no-op (chat and gateway both start cleanly).
- Name collisions cause a clear startup error.
- The spec document is updated with what shipped vs what was deferred.

Stage 2.1 (hot reload + `client_secret_file` + reconnect) gets its own plan, written if/when needed.
