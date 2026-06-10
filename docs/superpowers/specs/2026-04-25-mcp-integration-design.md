# MCP Integration — Design

**Date:** 2026-04-25
**Branch:** `experiment/gt-harness`
**Status:** Approved (verbal), pending written review

## Context

Felix has no Model Context Protocol (MCP) client today. We want to connect Felix to remote MCP servers, starting with an AWS Bedrock AgentCore Gateway that fronts a long-term memory (LTM) service. Auth to AgentCore is OAuth2 client-credentials via Cognito; transport is MCP Streamable HTTP.

This design covers two stages:
- **Stage 1:** A standalone smoke-test harness that proves the OAuth + MCP plumbing works end-to-end. Throwaway-shaped, but built from packages that lift cleanly into Stage 2.
- **Stage 2:** A real MCP client subsystem wired into Felix's agent runtime, with config-driven server registration, tool injection, and hot reload.

## Goals

- Validate that Felix can fetch a Bearer token from Cognito and complete an MCP session against the AgentCore gateway.
- Discover remote tools via `tools/list` and invoke them via `tools/call`.
- Eventually let the agent runtime call MCP tools the same way it calls core tools — same `Tool` interface, same `ExecPolicy`, same registry.
- Keep the MCP layer generic so future MCP servers (Anthropic-hosted, GitHub, custom) work with config-only changes.

## Non-Goals

- stdio MCP transport (HTTP-only for now).
- MCP resources, prompts, or sampling. Tools only.
- Auth schemes beyond OAuth2 client-credentials in Stage 2 (bearer-static and OAuth2 auth-code can be added later).
- Sandboxing remote calls — MCP tools are remote by definition; we gate them through `ExecPolicy` instead.

---

## Stage 1 — Smoke-test harness

### Surface

New cobra subcommand:

```
felix gt-harness [--env-file gt-harness.txt] [--call <tool>] [--args '<json>']
```

- `--env-file` (default `gt-harness.txt`): path to a KEY=VALUE credentials file.
- `--call`: optional. If set, invoke that tool name after listing.
- `--args`: JSON object of arguments for `--call`. Defaults to `{}`.

Behavior:
1. Load credentials.
2. Build OAuth-aware HTTP client.
3. Open MCP Streamable-HTTP session.
4. Print `tools/list` results (name, description, input schema).
5. If `--call` set, invoke and pretty-print the result.
6. Exit.

### Credentials file format

`gt-harness.txt` (and any file passed via `--env-file`) is parsed as a minimal dotenv:
- `KEY=VALUE` per line.
- Blank lines and lines starting with `#` ignored.
- No shell expansion, no quoting rules — value is everything after the first `=`, trimmed of surrounding whitespace.
- Required keys: `MCP_SERVER_URL`, `LTM_CLIENT_ID`, `LTM_CLIENT_SECRET`, `LTM_TOKEN_URL`, `LTM_SCOPE`.
- Missing keys → fail fast with a clear error listing which keys are absent.

`.gitignore` will gain `gt-harness.txt` and `*.env`.

### New package: `internal/mcp/`

Three files, each with a focused purpose:

**`creds.go`** — `LoadEnvFile(path string) (map[string]string, error)`. Parser only. No knowledge of which keys are required (caller validates).

**`oauth.go`** — `NewClientCredentialsHTTPClient(cfg ClientCredentialsConfig) *http.Client`. Wraps `golang.org/x/oauth2/clientcredentials.Config`. Returns an HTTP client that auto-injects `Authorization: Bearer …` and refreshes the token before expiry.
```go
type ClientCredentialsConfig struct {
    TokenURL     string
    ClientID     string
    ClientSecret string
    Scope        string
}
```

**`client.go`** — `NewClient(ctx, serverURL string, httpClient *http.Client) (*Client, error)`. Thin wrapper around `github.com/modelcontextprotocol/go-sdk/mcp` configured with Streamable-HTTP transport using the supplied `*http.Client`. Exposes:
```go
func (c *Client) ListTools(ctx) ([]ToolInfo, error)
func (c *Client) CallTool(ctx, name string, args map[string]any) (CallResult, error)
func (c *Client) Close() error
```

### Harness command (`cmd/felix/gtharness.go`)

Glue only — no business logic. Loads creds, builds the three pieces, runs the steps, prints output. ~80–120 LOC.

### Stage 1 — out of scope

- Config integration (`felix.json5`).
- Agent runtime integration.
- Persistence beyond process lifetime.
- Multi-server.
- Streaming tool results to stdout (just print final result).

---

## Stage 2 — Promote to MCP client subsystem

### Config schema

New `mcp_servers` array in `felix.json5`:

```json5
mcp_servers: [
  {
    id: "agentcore-ltm",                                  // unique identifier
    url: "https://...amazonaws.com/mcp",
    auth: {
      kind: "oauth2_client_credentials",
      token_url: "https://...amazoncognito.com/oauth2/token",
      client_id: "...",
      client_secret_env: "LTM_CLIENT_SECRET",             // OR client_secret_file: "..."
      scope: "test-ltm-sr/ltm-api-access",
    },
    enabled: true,
    tool_prefix: "ltm_",                                   // optional, see Naming
  },
],
```

`auth.kind` is the discriminator. Stage 2 ships only `oauth2_client_credentials`. Future kinds (`bearer_static`, `oauth2_authorization_code`) plug in without breaking existing configs.

`client_secret_env` and `client_secret_file` are mutually exclusive — never inline the secret in the config file.

### Runtime wiring

**New file: `internal/mcp/manager.go`**

The `Manager` owns one `*Client` per enabled server. On gateway startup:
1. For each enabled server: build OAuth client, open MCP session, call `tools/list`.
2. For each remote tool: construct an adapter implementing `tools.Tool` (`Name`, `Description`, `Parameters`, `Execute`). `Execute` forwards to MCP `tools/call` and converts the result to `tools.ToolResult`.
3. Register each adapter into the existing `tools.Registry`.

**Hot reload:** `Manager` subscribes to config changes via the existing `fsnotify` infrastructure in `internal/config`. On change, it diffs `mcp_servers` by `id` — adds new, removes deleted, reconnects on auth/url change. Tool registrations are updated atomically.

**Token refresh:** handled entirely by the oauth2 transport — no Felix-side plumbing.

### Tool naming

- If `tool_prefix` is set, every remote tool is registered as `<prefix><remote_name>` (e.g. `ltm_search`).
- If `tool_prefix` is empty and a name collision is detected at registration time (with core tools or another MCP server's tools), startup fails with an error listing the conflict. This forces the operator to make an explicit choice.

### Tool policy

MCP tools are subject to the existing `ExecPolicy`. Operators can deny or restrict specific MCP tools the same way they restrict `bash` or `web_fetch`. There is no sandbox layer — the call is remote — so the policy layer is the only gate.

### Failure modes

- **Server unreachable on startup:** log a warning, skip that server, continue gateway boot. Other servers and core tools still come up.
- **Server fails mid-session:** the tool call returns an error to the agent (which can recover or surface it). Manager attempts reconnect on the next call to a tool from that server. No background retry loop.
- **Token fetch fails:** treated identically to "server unreachable" at the transport layer — surfaced through the same code path.

### Stage 2 — out of scope

- stdio transport.
- MCP resources, prompts, sampling.
- Server-initiated requests.
- Per-agent MCP server bindings (all servers are global; per-agent gating happens via `ExecPolicy`).

---

## Dependencies

New Go module dependencies:
- `github.com/modelcontextprotocol/go-sdk` — official MCP SDK.
- `golang.org/x/oauth2` (and its `/clientcredentials` subpackage) — likely already pulled transitively, but will be listed explicitly.

## Testing

**Stage 1:**
- Unit test `creds.go` parser against fixture files (well-formed, missing keys, comments, whitespace).
- Unit test `oauth.go` against an `httptest.Server` impersonating the Cognito token endpoint.
- Manual end-to-end test: run `felix gt-harness` against the real AgentCore gateway with `gt-harness.txt`. Document the expected output.

**Stage 2:**
- Unit test the MCP→Tool adapter (mock `Client.CallTool` returns various result shapes).
- Unit test `Manager` startup/reload diffing.
- Unit test naming-collision detection.
- Integration test against a local mock MCP server (the SDK provides one).

## Open questions

None blocking. To revisit during implementation:
- Exact mapping of MCP `CallToolResult.content` (which can include text, images, embedded resources) into `tools.ToolResult` (which has `Output string` plus `Images []llm.ImageContent`).
- Whether `tool_prefix` defaults should be derived from server `id` automatically when collisions occur.

---

## Stage 2 MVP — what shipped (2026-04-25)

The MVP plan (`docs/superpowers/plans/2026-04-25-mcp-stage2-mvp-subsystem.md`) shipped the config-driven subsystem plus one enhancement:

**Shipped:**
- `mcp_servers[]` config schema with `oauth2_client_credentials` auth + `client_secret_env` secret source.
- `mcp.Manager` connects one session per enabled server at startup; unreachable servers are logged and skipped.
- `mcpToolAdapter` implements `tools.Tool`; `mcp.RegisterTools` registers adapters into the agent's `tools.Registry` with per-server `tool_prefix` and hard-fail collision detection.
- Wiring at all 6 tool-registry construction sites (chat main + chat cron, gateway main + heartbeat + 2 cron sites). One Manager is shared across all sites for the process lifetime.
- **Auto-allowlist (added during MVP execution):** `RegisterTools` returns the names it registered; `Config.ApplyMCPToolNamesToAllowlists` augments every agent's `Tools.Allow` list (in-memory only) so MCP tools are usable without manual config edits. Agents with empty `Allow` (allow-all) are left alone. "Removal on disconnect" is implicit — at next restart with no MCP servers, the on-disk allowlists take effect verbatim.

**Deferred to a follow-up plan:**
- fsnotify-driven hot reload of `mcp_servers` (config changes still require process restart).
- `client_secret_file` auth source (`client_secret_env` only in MVP).
- Background reconnect loop (the SDK transport's `MaxRetries` handles transient HTTP failures; servers that fail at startup are logged + skipped; recovery requires a restart).

**End-to-end verified:** chat agent with `anthropic/claude-haiku-4-5` saw the `ltm_target-ltm___whoami` tool in its registry, invoked it, and received the whoami JSON from the live AgentCore gateway through the wired Manager. 11 tools total: 9 core + 2 MCP.
