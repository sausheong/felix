# MCP stdio Transport + Bearer Auth — Design

**Status:** Proposed
**Author:** Felix maintainers
**Date:** 2026-04-26
**Builds on:** [`2026-04-25-mcp-integration-design.md`](2026-04-25-mcp-integration-design.md) (Stage 1 + Stage 2 MVP)

## Problem

Felix's MCP client today supports only Streamable HTTP transport with OAuth2 client-credentials auth. This is right for AWS Bedrock AgentCore but excludes the majority of the MCP ecosystem:

- Almost every developer-facing MCP server (filesystem, git, GitHub, Slack, browser automation, IDE bridges) ships as a **stdio** server — a process Felix spawns, talks to over stdin/stdout in newline-delimited JSON-RPC, and tears down on exit.
- Many hosted MCP servers authenticate with a **static bearer token** rather than an OAuth2 token endpoint.

This spec adds both. After it ships, a Felix user can connect to:
- Any stdio-launchable MCP server (`npx @modelcontextprotocol/server-github`, local Python scripts, compiled binaries).
- Any HTTP MCP server using bearer-token auth (Anthropic-hosted, Vercel-hosted, custom internal).

## Out of scope (deferred)

- OAuth2 authorization-code or PKCE flows (per-user OAuth). Defer until a concrete user need surfaces.
- Server-Sent Events / WebSocket transports. Streamable HTTP and stdio together cover ~all current MCP servers.
- Stdio process supervision (auto-restart on crash). The SDK's stdin-close → SIGTERM flow handles graceful exit; restart-on-crash adds complexity without a current driver.
- Per-server resource limits (CPU/mem/timeout) on spawned stdio processes. Deferred to a future sandbox pass.

## Schema

`mcp_servers[]` becomes transport-discriminated. The existing flat HTTP layout is preserved as a legacy read path — Felix never silently rewrites the user's `felix.json5`. New entries (and entries saved via the Settings UI) use the nested form.

```jsonc
mcp_servers: [
  // Nested HTTP — preferred for new entries.
  {
    id: "agentcore-ltm",
    transport: "http",
    http: {
      url: "https://...amazonaws.com/mcp",
      auth: {
        kind: "oauth2_client_credentials",
        token_url: "https://cognito-idp.../token",
        client_id: "...",
        client_secret: "...",         // or client_secret_env: "FELIX_LTM_SECRET"
        scope: "agentcore/invoke",
      },
    },
    enabled: true,
    tool_prefix: "ltm_",
  },

  // Nested HTTP, bearer auth.
  {
    id: "anthropic-hosted",
    transport: "http",
    http: {
      url: "https://mcp.anthropic.com/v1/...",
      auth: {
        kind: "bearer",
        token: "sk-ant-...",          // or token_env: "ANTHROPIC_MCP_TOKEN"
      },
    },
    enabled: true,
  },

  // stdio — process spawned at startup.
  {
    id: "github",
    transport: "stdio",
    stdio: {
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-github"],
      env: { GITHUB_TOKEN: "ghp_..." },  // merged onto the parent env
    },
    enabled: true,
  },

  // Legacy flat HTTP (still accepted; never rewritten silently).
  {
    id: "legacy-ltm",
    url: "...",
    auth: { kind: "oauth2_client_credentials", ... },
    enabled: true,
  },
]
```

**Discriminator semantics.** `transport` is optional and defaults to `"http"` for backward compat. Validation rules per transport:

| transport | required nested block | also accepts |
|-----------|----------------------|--------------|
| `"http"`  | `http.url`, `http.auth.kind` | flat `url` + `auth` (legacy) |
| `"stdio"` | `stdio.command` | — |

If a user sets `transport: "http"` AND populates flat `url`, the nested form wins; we log a debug-level note that the flat fields are ignored.

## Auth schemes

`MCPAuthConfig.Kind` now spans:

| Kind | Required fields | Notes |
|------|-----------------|-------|
| `oauth2_client_credentials` | `token_url`, `client_id`, `client_secret` (or `client_secret_env`), `scope` | Existing path. No change. |
| `bearer` | `token` (or `token_env`) | New. Static `Authorization: Bearer <token>` on every request. |
| `none` | — | New. No auth header. Useful for local HTTP MCP servers (rare but possible). |

Resolution precedence is identical to existing OAuth: literal field wins over env-var fallback. Missing-secret on a `bearer` auth logs+skips the server (consistent with existing OAuth behavior).

## stdio transport details

The MCP Go SDK ships `mcpsdk.CommandTransport{Command *exec.Cmd, TerminateDuration time.Duration}` which already handles the stdin/stdout pipe wiring, JSON-RPC framing, and graceful shutdown (close stdin → wait → SIGTERM after `TerminateDuration`). Felix's job is purely:

1. Build `*exec.Cmd` with the configured command, args, and merged env.
2. Capture stderr (before SDK touches the cmd) and forward each line to `slog.Debug` under a structured `mcp_stdio_id` field.
3. Pass the cmd to `&mcpsdk.CommandTransport{Command: cmd}` and feed it to `mcpsdk.NewClient(...).Connect`.
4. On `Close()`, the SDK closes stdin; if the process doesn't exit within `TerminateDuration` (default 5s) it's SIGTERM'd. Felix's existing 3s timeout on `mcpMgr.Close()` in `startup.go` becomes 8s to give stdio servers room to drain.

**Env merge.** The configured `env` map is appended to `os.Environ()` so child processes inherit the parent's `PATH` (essential for `npx`, `python`, etc.) plus any user-specified additions. Later entries override earlier ones (Go `exec.Cmd.Env` semantics).

**No PATH lookup of `command`.** `exec.Cmd` already does `LookPath` when `command` has no slash. If the binary is missing, `Command.Start()` fails inside `CommandTransport.Connect` and Felix falls back to its existing skip-on-failure path — server logged, gateway continues.

**Process leak avoidance.** `exec.CommandContext` is *not* used. The SDK owns shutdown via stdin-close; tying spawn to a context that might be cancelled mid-Connect risks killing the child before the JSON-RPC handshake completes. Cleanup is centralised in `Manager.Close()` which is bounded by the 8s timeout in `startup.go`.

**Stderr forwarder.** A goroutine reads `cmd.StderrPipe()` line-by-line and calls `slog.Debug("mcp stdio stderr", "id", serverID, "line", line)`. Goroutine exits when stderr EOFs (process exit). No error if stderr produces nothing.

## Manager dispatch

`mcp.ManagerServerConfig` is restructured to be transport-agnostic:

```go
type ManagerServerConfig struct {
    ID         string
    ToolPrefix string
    Transport  string                 // "http" | "stdio"
    HTTP       *HTTPServerConfig      // populated when Transport == "http"
    Stdio      *StdioServerConfig     // populated when Transport == "stdio"
}

type HTTPServerConfig struct {
    URL  string
    Auth HTTPAuthConfig
}

type HTTPAuthConfig struct {
    Kind         string // "oauth2_client_credentials" | "bearer" | "none"
    // oauth2_client_credentials
    TokenURL     string
    ClientID     string
    ClientSecret string
    Scope        string
    // bearer
    BearerToken  string
}

type StdioServerConfig struct {
    Command string
    Args    []string
    Env     map[string]string
}
```

`NewManager` switches on `Transport`, builds the appropriate `*Client` via either the existing `Connect` (renamed to `ConnectHTTP`) or the new `ConnectStdio`, and continues to log+skip on failure. The existing per-server `ToolPrefix` / collision logic is unchanged.

## Settings UI

The MCP tab gains a transport selector (radio buttons: **HTTP** / **stdio**). Field visibility flips on selection:

- HTTP: existing fields (URL, auth-kind selector, OAuth2 fields *or* bearer token field, scope, tool prefix).
- stdio: command (text), args (textarea, one per line), env (repeating key/value rows), tool prefix.

The save handler serialises into the nested form. Loading a legacy flat HTTP entry continues to work — the form populates from the flat fields and re-saves into the nested form (user-initiated migration; never silent).

`StripMCPAutoAdded` is unchanged — it operates on tool names, not server config shape.

## Risks & mitigations

| Risk | Mitigation |
|------|-----------|
| stdio process never exits on Close → gateway shutdown hangs | SDK SIGTERMs after 5s; manager's 8s outer timeout in `startup.go` force-continues. |
| Misconfigured stdio command spams stderr | Forwarded to `slog.Debug` so it's silent at default verbosity but inspectable when needed. |
| User confuses `command_env` (process spawn env) with `client_secret_env` (auth secret env-var name) | Different schema paths (`stdio.env` vs `http.auth.client_secret_env`); UI labels them distinctly. |
| Legacy flat HTTP entries break after schema change | Resolver explicitly accepts both shapes, with a regression test. |
| stdio process inherits Felix's full env including secrets | Documented; users wanting isolation can use the sandbox layer (separate concern). |
| Bearer token logged accidentally | Token is never logged; only the auth kind appears in startup logs. |

## What ships

- Config schema extended; legacy flat HTTP still parses.
- `internal/mcp/client.go` gains `ConnectStdio`; existing `Connect` renamed `ConnectHTTP`.
- `internal/mcp/manager.go` dispatches on `Transport`.
- `internal/mcp/oauth.go` (or new `auth.go`) gains a bearer `RoundTripper`.
- Settings UI extended with transport selector + stdio fields + bearer field.
- Bundled `felix.json5` example updated.
- E2E smoke recipe documented (npx-launched filesystem server).

## What does not ship

- OAuth2 PKCE / authorization code.
- WebSocket / SSE transports.
- Stdio process supervision / restart-on-crash.
- Per-stdio-server resource limits.

---

## What shipped (2026-04-26)

**Status:** implemented end-to-end against the design above. Commit hashes to be pinned after merge.

### Code delta

| File | Change |
|------|--------|
| `internal/mcp/types.go` | Replaced `ManagerServerConfig` with transport-discriminated form; added `HTTPServerConfig`, `HTTPAuthConfig`, `StdioServerConfig`. |
| `internal/mcp/client.go` | Renamed `Connect` → `ConnectHTTP`. No behaviour change. |
| `internal/mcp/manager.go` | `NewManager` dispatches on `Transport`. New `connectOne` + `buildHTTPClient` helpers. |
| `internal/mcp/bearer.go`, `bearer_test.go` | New. `NewBearerHTTPClient(token)` + `bearerRoundTripper`. Empty-token defensive no-op; preserves caller-set `Authorization`. |
| `internal/mcp/stdio.go`, `stdio_test.go` | New. `ConnectStdio(ctx, id, command, args, env)` + `mergedEnv` + stderr forwarder. Tests gated on `cat`/non-Windows. |
| `internal/mcp/manager_test.go` | Fixtures updated to nested transport shape. |
| `internal/mcp/register_test.go`, `client_test.go`, `adapter_test.go`, `cmd/felix/gtharness.go` | `Connect` → `ConnectHTTP` callsite renames. |
| `internal/config/config.go` | `MCPServerConfig` gains `Transport`, `HTTP`, `Stdio`. `MCPAuthConfig` gains `Token`, `TokenEnv`. New `MCPHTTPBlock`, `MCPStdioBlock`. `ResolveMCPServers` rewritten with nested+legacy+bearer+stdio paths via new `resolveHTTPBlock`. |
| `internal/config/config_test.go` | Existing tests updated to nested field paths; new tests for bearer literal/env, bearer-missing-skip, none-auth, stdio happy-path, stdio-missing-command, legacy flat HTTP, nested-wins-over-flat, unknown transport, JSON round-trip. |
| `internal/gateway/settings.go` | `renderMCP()` extended with transport selector + auth-kind selector. New `renderHTTPBlock`, `renderStdioBlock`. Legacy flat HTTP entries auto-migrate to nested shape on first render (user-initiated; never silent at parse time). |
| `internal/startup/startup.go` | `mcpMgr.Close()` timeout bumped 3s → 8s for stdio drain. |

### SDK confirmation

The Go MCP SDK (`github.com/modelcontextprotocol/go-sdk` v1.5.0) ships `mcpsdk.CommandTransport{Command *exec.Cmd, TerminateDuration time.Duration}` — Felix consumes it as a struct literal, no constructor. Pipe ordering matters: `cmd.StderrPipe()` MUST be wired before passing the cmd to `CommandTransport` (which calls `cmd.Start()` internally during `Connect`). Felix does this in `ConnectStdio`.

### E2E smoke recipe

```jsonc
// ~/.felix/felix.json5
mcp_servers: [
  {
    id: "fs",
    transport: "stdio",
    stdio: {
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
    },
    enabled: true,
    tool_prefix: "fs_",
  },
]
```

1. `go build -o felix ./cmd/felix && ./felix gateway` — gateway logs `mcp: connected to server id=fs transport=stdio`.
2. `./felix chat` — agent's tool registry includes `fs_read_file`, `fs_write_file`, `fs_list_directory`, etc.
3. Quit the gateway — exit completes within ~8s even with the npx child still running (SDK SIGTERMs after 5s, outer 8s timeout force-continues if it hangs).
