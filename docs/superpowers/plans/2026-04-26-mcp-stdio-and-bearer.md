# MCP stdio Transport + Bearer Auth — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add stdio MCP transport and HTTP bearer-token auth to Felix. After this ships, any stdio-launchable MCP server (`npx @modelcontextprotocol/server-github`, local binaries, Python scripts) and any bearer-authenticated HTTP MCP server can be added through the existing config + Settings UI flow.

**Architecture:** `mcp_servers[]` entries become transport-discriminated (nested `http: {}` / `stdio: {}` blocks under a `transport` field). The existing flat HTTP layout continues to parse for backward compat. `mcp.Manager` dispatches on transport, building either an HTTP `*Client` (existing path, rename) or a stdio `*Client` (new path using `mcpsdk.CommandTransport`). The HTTP client gains a bearer auth scheme alongside OAuth2 client-credentials.

**Tech Stack:** Go 1.25, existing `github.com/modelcontextprotocol/go-sdk` v1.5.0 (already provides `CommandTransport` for stdio), existing `golang.org/x/oauth2`, existing Felix `internal/mcp/`, `internal/config/`, `internal/gateway/settings.go`.

**Spec:** [`docs/superpowers/specs/2026-04-26-mcp-stdio-and-bearer.md`](../specs/2026-04-26-mcp-stdio-and-bearer.md)

**Builds on:** Stage 1 + Stage 2 MVP commits (see `docs/superpowers/plans/2026-04-25-mcp-stage2-mvp-subsystem.md`).

---

## Out of scope (deferred)

- OAuth2 PKCE / authorization-code flows.
- SSE / WebSocket transports.
- Stdio process supervision (restart-on-crash).
- Per-stdio resource limits.
- Hot reload of `mcp_servers` (still requires process restart).

---

## File Structure

**Created:**
- `internal/mcp/stdio.go` — `ConnectStdio(ctx, command, args, env, id)` + stderr forwarder.
- `internal/mcp/stdio_test.go`
- `internal/mcp/bearer.go` — `bearerRoundTripper` + `NewBearerHTTPClient(token string)`.
- `internal/mcp/bearer_test.go`

**Modified:**
- `internal/mcp/types.go` — restructure `ManagerServerConfig` (Transport discriminator + nested HTTP/Stdio configs).
- `internal/mcp/client.go` — rename `Connect` → `ConnectHTTP` (keep symbol); no behavior change.
- `internal/mcp/client_test.go` — rename references.
- `internal/mcp/manager.go` — switch on `Transport`; bump existing test fixtures to nested shape.
- `internal/mcp/manager_test.go`
- `internal/config/config.go` — `MCPServerConfig` gains `Transport`, `HTTP`, `Stdio`; `MCPAuthConfig` gains `bearer` kind + `Token`/`TokenEnv`. Resolver migrates legacy flat HTTP shape in-memory.
- `internal/config/config_test.go` — round-trip tests for nested HTTP, nested stdio, legacy flat HTTP, bearer.
- `internal/gateway/settings.go` — MCP tab gets transport selector + stdio fields + bearer auth-kind option.
- `internal/startup/startup.go` — bump `mcpMgr.Close()` timeout from 3s → 8s (stdio servers need stdin-close + drain).
- `docs/superpowers/specs/2026-04-26-mcp-stdio-and-bearer.md` — append a "What shipped" delta in T7.

**Bundled config example** (if present): `cmd/felix/felix.json5.example` (or wherever the canonical sample lives) gains a commented stdio entry.

---

## Naming and types — pinned upfront

```go
// internal/mcp/types.go (REPLACES current ManagerServerConfig)
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
    TokenURL     string // oauth2_client_credentials
    ClientID     string
    ClientSecret string
    Scope        string
    BearerToken  string // bearer
}

type StdioServerConfig struct {
    Command string
    Args    []string
    Env     map[string]string
}
```

```go
// internal/config/config.go (additions)
type MCPServerConfig struct {
    ID         string         `json:"id"`
    Transport  string         `json:"transport,omitempty"` // defaults to "http"
    HTTP       *MCPHTTPBlock  `json:"http,omitempty"`
    Stdio      *MCPStdioBlock `json:"stdio,omitempty"`
    URL        string         `json:"url,omitempty"`        // legacy flat HTTP
    Auth       MCPAuthConfig  `json:"auth,omitempty"`        // legacy flat HTTP
    Enabled    bool           `json:"enabled"`
    ToolPrefix string         `json:"tool_prefix,omitempty"`
}

type MCPHTTPBlock struct {
    URL  string        `json:"url"`
    Auth MCPAuthConfig `json:"auth"`
}

type MCPStdioBlock struct {
    Command string            `json:"command"`
    Args    []string          `json:"args,omitempty"`
    Env     map[string]string `json:"env,omitempty"`
}

type MCPAuthConfig struct {
    Kind            string `json:"kind"`
    // oauth2_client_credentials
    TokenURL        string `json:"token_url,omitempty"`
    ClientID        string `json:"client_id,omitempty"`
    ClientSecret    string `json:"client_secret,omitempty"`
    ClientSecretEnv string `json:"client_secret_env,omitempty"`
    Scope           string `json:"scope,omitempty"`
    // bearer
    Token           string `json:"token,omitempty"`
    TokenEnv        string `json:"token_env,omitempty"`
}
```

---

## Tasks

### T1 — Config schema, resolver, legacy compat, bearer

**Files:** `internal/config/config.go`, `internal/config/config_test.go`, `internal/mcp/types.go`

- [ ] Replace `internal/mcp/types.go` with the restructured `ManagerServerConfig` + `HTTPServerConfig` + `HTTPAuthConfig` + `StdioServerConfig` from "Naming and types" above.
- [ ] In `internal/config/config.go`: add `Transport`, `HTTP`, `Stdio` fields to `MCPServerConfig`. Add `Token`, `TokenEnv` to `MCPAuthConfig`. Keep `URL` and `Auth` (top-level) for legacy reads.
- [ ] Rewrite `Config.ResolveMCPServers()`:
  - Effective transport = `s.Transport` (default `"http"`).
  - **HTTP path** — choose nested `s.HTTP` if non-nil; else fall back to flat `s.URL`/`s.Auth`. Validate `url` and `auth.kind` non-empty. Switch on `auth.kind`:
    - `oauth2_client_credentials`: existing logic; resolve `client_secret` literal-or-env.
    - `bearer`: resolve `token` literal-or-env; missing → log+skip (matches OAuth behavior).
    - `none`: no secret to resolve.
    - other: log+skip with `unsupported auth.kind`.
  - **stdio path** — `s.Stdio` must be non-nil with non-empty `command`. Args and Env optional.
  - Build `mcp.ManagerServerConfig` with the populated transport-specific block.
- [ ] Tests in `config_test.go`:
  - Round-trip: nested HTTP + oauth2 → resolves with secret intact.
  - Round-trip: nested HTTP + bearer literal → resolves to `BearerToken`.
  - Round-trip: nested HTTP + bearer env → resolves from env var.
  - Round-trip: nested stdio → resolves to `StdioServerConfig` with command/args/env.
  - Legacy flat HTTP entry parses and resolves identically to its nested-form twin.
  - `transport: "http"` + flat `url` set + nested `http` populated → nested wins (debug log; not asserted).
  - Stdio entry missing `command` → resolver returns error.
  - Bearer entry with neither `token` nor `token_env` → server skipped (resolver returns slice without it; assert by length).
- [ ] `go test ./internal/config -run MCP -race` passes.

**Verification:** `go build ./...` succeeds. New tests pass. Existing `Config` tests untouched.

### T2 — Bearer auth HTTP client

**Files:** `internal/mcp/bearer.go`, `internal/mcp/bearer_test.go`

- [ ] Create `internal/mcp/bearer.go` with:
  ```go
  func NewBearerHTTPClient(token string) *http.Client
  ```
  Implementation: a `bearerRoundTripper` wrapping `http.DefaultTransport` that injects `Authorization: Bearer <token>` if not already set. Token captured by closure; never logged.
- [ ] Tests using `httptest.NewServer`:
  - Request gets `Authorization: Bearer <token>` header.
  - Empty token → no header injected (defensive: caller validated, but don't crash).
  - Existing `Authorization` header on the request is preserved (not overwritten).

**Verification:** `go test ./internal/mcp -run Bearer -race` passes.

### T3 — stdio transport client

**Files:** `internal/mcp/stdio.go`, `internal/mcp/stdio_test.go`

- [ ] Create `internal/mcp/stdio.go`:
  ```go
  func ConnectStdio(ctx context.Context, id, command string, args []string, env map[string]string) (*Client, error)
  ```
  Implementation:
  - `cmd := exec.Command(command, args...)`
  - `cmd.Env = mergedEnv(os.Environ(), env)` (later entries win).
  - `stderr, err := cmd.StderrPipe()` — must be called before SDK touches cmd.
  - Spawn goroutine `forwardStderr(stderr, id)` reading line-by-line via `bufio.Scanner`, calling `slog.Debug("mcp stdio stderr", "id", id, "line", line)`. Goroutine returns on EOF; do not log scanner errors at warn level (process exit is normal).
  - `transport := &mcpsdk.CommandTransport{Command: cmd}` (no `TerminateDuration` override; default 5s).
  - `sdkClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "felix", Version: "0.0.0-stdio"}, nil)`.
  - `session, err := sdkClient.Connect(ctx, transport, nil)`. On error, return `fmt.Errorf("mcp stdio connect %s: %w", command, err)`.
  - Return `&Client{session: session}`.
- [ ] Helper `mergedEnv(parent []string, overrides map[string]string) []string` deduplicates by KEY (later wins).
- [ ] Tests:
  - `mergedEnv` correctness: overrides override parent; new keys appended; PATH preserved.
  - `ConnectStdio` happy path against a real subprocess: use `os/exec.LookPath("cat")` as a precondition; spawn `cat` with stdin closed-on-test-end; assert `Connect` returns an error (cat doesn't speak MCP) — this validates the spawn path without requiring an MCP test server. Skip if `cat` not in PATH.
  - `ConnectStdio` with non-existent command (`/no/such/binary`) returns an error from `Connect`. Asserts skip-on-failure works.

**Verification:** `go test ./internal/mcp -run Stdio -race` passes (skipping the cat test on Windows is acceptable; gate with `runtime.GOOS != "windows"`).

### T4 — Manager dispatch on transport

**Files:** `internal/mcp/manager.go`, `internal/mcp/manager_test.go`, `internal/mcp/client.go`, `internal/mcp/client_test.go`

- [ ] Rename `internal/mcp/client.go` `Connect` → `ConnectHTTP`. No behavior change. Update doc comment to clarify it's the HTTP transport entrypoint.
- [ ] Update `client_test.go` references.
- [ ] In `manager.go` `NewManager`:
  - Switch on `cfg.Transport` (treat empty as `"http"` defensively, though resolver normalises).
  - `"http"`: build `*http.Client` based on `cfg.HTTP.Auth.Kind`:
    - `oauth2_client_credentials` → existing `NewClientCredentialsHTTPClient`.
    - `bearer` → `NewBearerHTTPClient(cfg.HTTP.Auth.BearerToken)`.
    - `none` → `http.DefaultClient`.
  - Then `ConnectHTTP(ctx, cfg.HTTP.URL, httpClient)`.
  - `"stdio"`: `ConnectStdio(ctx, cfg.ID, cfg.Stdio.Command, cfg.Stdio.Args, cfg.Stdio.Env)`.
  - Default: log+skip with `unknown transport`.
  - On error: existing log+skip behavior. Log includes `transport` field.
- [ ] Update existing `manager_test.go` fixtures to the new nested shape. Add a stdio fixture (uses `cat` like T3) asserting it appears in `Servers()` (then immediately Close()s to avoid leaks).

**Verification:** `go test ./internal/mcp -race` passes (entire package).

### T5 — Settings UI: transport selector, stdio fields, bearer option

**Files:** `internal/gateway/settings.go`

- [ ] Add `<select name="transport">` with options HTTP / stdio to the per-server form. Default HTTP.
- [ ] Wire JS `renderMCP()` (or its render helpers) so:
  - `transport === "http"` shows: URL, auth kind selector (existing oauth2_client_credentials / new bearer), per-auth-kind fields, scope, tool prefix.
  - `transport === "stdio"` shows: command, args (textarea, one per line), env (key/value rows with add/remove buttons), tool prefix.
- [ ] Add bearer-auth UI: when auth kind = `bearer`, show a single password-style input for `token` (caveat text: "stored in felix.json5"; same posture as `client_secret`).
- [ ] Save handler serialises into nested form:
  - HTTP entries → `{ id, transport: "http", http: { url, auth: {...} }, enabled, tool_prefix }`.
  - stdio entries → `{ id, transport: "stdio", stdio: { command, args, env }, enabled, tool_prefix }`.
- [ ] Load handler accepts both shapes for HTTP entries: prefer nested, fall back to flat. Saved-back form always uses nested (user-initiated migration of legacy entries).
- [ ] Smoke-test in browser: add a stdio entry (`echo test`), save, observe nested form on disk in `felix.json5`. Add a bearer HTTP entry, save, observe `auth.token` in the nested block. Reload page, observe both render correctly.

**Verification:** Manual smoke-test only (no test infrastructure for the embedded HTML). Confirm `go build ./...` and `go test ./internal/gateway -race` (existing tests) still pass.

### T6 — Wiring: bump shutdown timeout

**Files:** `internal/startup/startup.go`

- [ ] In the `mcpMgr.Close()` cleanup block, change the `time.After(3 * time.Second)` to `time.After(8 * time.Second)`. Update the inline comment to mention stdio drain.
- [ ] No tests; verified via the existing manual quit smoke test (kill gateway, observe clean exit logs).

**Verification:** `go build ./...` succeeds. Manually start gateway with a stdio MCP server configured, then `quit`; gateway should exit cleanly within ~8s even if the stdio child is slow.

### T7 — Documentation + bundled example

**Files:** `docs/superpowers/specs/2026-04-26-mcp-stdio-and-bearer.md`, possibly a sample `felix.json5`

- [ ] Append a "What shipped" section to the spec doc with: commit hashes, the actual SDK API used (`CommandTransport`), and a 4–5 line E2E recipe (e.g. `npx @modelcontextprotocol/server-everything` as a stdio entry → list its tools via the chat).
- [ ] If a bundled `felix.json5` example exists in the repo, add a commented stdio block + commented bearer-HTTP block to it. If not, skip — the spec doc covers the schema reference.

**Verification:** Spec doc reads as both design + post-ship reference.

---

## Risks specific to execution

- **CommandTransport pipe ordering.** `cmd.StderrPipe()` MUST be called before `mcpsdk.CommandTransport.Connect` (which calls `cmd.Start`). Get this wrong and `StderrPipe` returns an error after start. T3 step ordering matters.
- **Test flakiness on Windows.** stdio tests using `cat` will fail. Gate with `runtime.GOOS != "windows" || t.Skip(...)`.
- **Bearer token leaking into logs.** Audit the slog calls in T2 and T4; only log `auth.kind`, never `BearerToken` or `Token`.
- **UI form state bugs.** When a user toggles transport HTTP↔stdio mid-edit, fields can carry stale values. Resetting fields on toggle (or just hiding them — letting saved JSON carry both is fine, resolver picks the nested one matching the chosen transport) is acceptable. Keep it simple.

---

## Definition of done

- `go test ./...` passes (excluding the pre-existing `TestAssembleSystemPromptDefault` failure unrelated to MCP).
- `go build ./cmd/felix ./cmd/felix-app` succeeds.
- Manual: a stdio MCP server (e.g. `npx -y @modelcontextprotocol/server-everything` or `npx -y @modelcontextprotocol/server-filesystem /tmp`) added via the Settings UI, saved, observed in `felix.json5` as a nested stdio entry, and its tools appear in a new chat session.
- Manual: a bearer-authed HTTP MCP server (any test endpoint) connects and lists tools.
- Manual: legacy flat HTTP entries already on disk continue to work after pulling this change — no edits required.
- Manual: gateway quits cleanly within ~8s with one stdio server running.
