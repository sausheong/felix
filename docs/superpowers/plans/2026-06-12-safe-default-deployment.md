# Safe-by-Default Deployment Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a default `felix start` (no auth token) safe against cross-site browser attacks and secret leakage, by adding an HTTP origin guard, secret redaction, content-safety headers, MCP subprocess env isolation, and a filesystem-integrity sweep.

**Architecture:** Five independent layers. Layer 1 adds a `RequireSameOrigin` chi middleware reusing the existing WebSocket origin predicate. Layer 2 extends settings redaction and adds a log scrubber. Layer 3 hardens `/files/raw`. Layer 4 minimizes the MCP stdio subprocess environment. Layer 5 makes config/file writes atomic and restrictive across both repos.

**Tech Stack:** Go 1.25, `go-chi/chi/v5`, `log/slog`, `stretchr/testify`. Two repos: `felix` (`/Users/sausheong/projects/felix`) and `harness` (`/Users/sausheong/projects/harness`, wired via `go.mod replace`).

**Spec:** `docs/superpowers/specs/2026-06-12-safe-default-deployment-design.md`

**Conventions:**
- Tests use `testify` (`github.com/stretchr/testify/require`, `/assert`), matching the existing suite.
- Run `go test ./...` from the relevant repo root; harness changes are verified in `/Users/sausheong/projects/harness`.
- Commit messages omit any Co-Authored-By trailer.
- Run `cd /Users/sausheong/projects/felix && go build ./...` after harness changes to confirm the replace-wired build still compiles.

---

## File Structure

**Layer 1 — HTTP origin guard (felix)**
- Create: `internal/gateway/origin.go` — `originAllowed`, `RequireSameOrigin`
- Create: `internal/gateway/origin_test.go`
- Modify: `internal/gateway/auth.go` — `AllowedOrigins` delegates to `originAllowed`
- Modify: `internal/gateway/server.go` — guarded route group

**Layer 2 — Secret redaction (felix)**
- Modify: `internal/gateway/settings.go` — extend `redactConfigSecrets`, add `restoreSecretScalars`
- Modify: `internal/gateway/settings_test.go` (create if absent)
- Modify: `internal/gateway/logs.go` — `scrubSecrets`, wire into `Handle`
- Create: `internal/gateway/logs_scrub_test.go`

**Layer 3 — Content safety (felix)**
- Modify: `internal/gateway/files.go` — `Raw` headers + disposition allowlist
- Modify: `internal/gateway/files_test.go` (create if absent)

**Layer 4 — MCP env isolation (felix)**
- Modify: `internal/config/config.go` — `MCPStdioBlock.InheritEnv`, thread through `ResolveMCPServers`
- Modify: `internal/mcp/stdio.go` — `ConnectStdio(inheritEnv)`, `minimalBaseEnv`
- Create: `internal/mcp/stdio_env_test.go`
- Modify: caller(s) of `ConnectStdio` (found in Task 4.2)

**Layer 5 — Filesystem integrity (harness + felix)**
- Create: `tool/atomic.go` (harness) — `WriteFileAtomic`
- Create: `tool/atomic_test.go` (harness)
- Modify: `tools/file/writefile.go` (harness) — atomic + 0600
- Modify: `tools/file/editfile.go` (harness) — atomic + mode-preserve
- Modify: `runtime/context.go` (harness) — spill 0600 via atomic helper
- Modify: `internal/config/config.go` (felix) — `Config.Save` lock + atomic
- Modify: `internal/startup/startup.go` (felix) — cron-jobs.json 0600; fail-closed Workspace guard
- Modify: `internal/local/supervisor.go` (felix) — ollama.pid 0600
- Modify: `cmd/felix-app/main.go` (felix) — felix-app.log 0600

---

## Layer 1 — HTTP origin guard

### Task 1.1: `originAllowed` predicate + `RequireSameOrigin` middleware

**Files:**
- Create: `internal/gateway/origin.go`
- Create: `internal/gateway/origin_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/gateway/origin_test.go`:

```go
package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginAllowed(t *testing.T) {
	// empty allowlist => localhost-only
	require.True(t, originAllowed("http://127.0.0.1:18789", nil))
	require.True(t, originAllowed("http://localhost:3000", nil))
	require.True(t, originAllowed("https://localhost", nil))
	require.False(t, originAllowed("http://evil.com", nil))
	require.False(t, originAllowed("http://127.0.0.1.evil.com", nil))

	// explicit allowlist => exact match (trailing slash tolerated)
	allow := []string{"https://app.example.com"}
	require.True(t, originAllowed("https://app.example.com", allow))
	require.True(t, originAllowed("https://app.example.com/", allow))
	require.False(t, originAllowed("https://other.example.com", allow))
}

func TestRequireSameOrigin(t *testing.T) {
	mw := RequireSameOrigin(nil) // localhost-only
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := mw(next)

	call := func(method string, headers map[string]string) int {
		req := httptest.NewRequest(method, "/settings/api/config", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	// No headers (curl / non-browser) => allowed.
	require.Equal(t, http.StatusOK, call("POST", nil))

	// Sec-Fetch-Site authoritative.
	require.Equal(t, http.StatusOK, call("POST", map[string]string{"Sec-Fetch-Site": "same-origin"}))
	require.Equal(t, http.StatusOK, call("POST", map[string]string{"Sec-Fetch-Site": "none"}))
	require.Equal(t, http.StatusForbidden, call("POST", map[string]string{"Sec-Fetch-Site": "cross-site"}))

	// Origin fallback when Sec-Fetch-Site absent.
	require.Equal(t, http.StatusOK, call("POST", map[string]string{"Origin": "http://localhost:5173"}))
	require.Equal(t, http.StatusForbidden, call("POST", map[string]string{"Origin": "http://evil.com"}))

	// No method short-circuit: a cross-site GET is still blocked
	// (the guard is only mounted on routes that must be checked).
	require.Equal(t, http.StatusForbidden, call("GET", map[string]string{"Sec-Fetch-Site": "cross-site"}))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run 'TestOriginAllowed|TestRequireSameOrigin' -v`
Expected: FAIL — `undefined: originAllowed`, `undefined: RequireSameOrigin`.

- [ ] **Step 3: Write the implementation**

Create `internal/gateway/origin.go`:

```go
package gateway

import (
	"net/http"
	"strings"
)

// originAllowed reports whether origin is permitted given the configured
// allowlist. An empty allowlist means localhost-only (the four
// http(s)://{127.0.0.1,localhost} prefixes). A non-empty allowlist requires
// an exact match (trailing slash tolerated). This is the single source of
// truth shared by the WebSocket origin check (AllowedOrigins) and the HTTP
// RequireSameOrigin middleware.
func originAllowed(origin string, allowed []string) bool {
	if len(allowed) == 0 {
		return strings.HasPrefix(origin, "http://127.0.0.1") ||
			strings.HasPrefix(origin, "http://localhost") ||
			strings.HasPrefix(origin, "https://127.0.0.1") ||
			strings.HasPrefix(origin, "https://localhost")
	}
	want := strings.TrimRight(origin, "/")
	for _, a := range allowed {
		if strings.TrimRight(a, "/") == want {
			return true
		}
	}
	return false
}

// RequireSameOrigin returns middleware that rejects cross-site requests.
// It is mounted only on routes that must be checked (all mutating routes
// plus the sensitive /logs* GETs), so it deliberately has NO safe-method
// short-circuit: every request routed through it is checked.
//
// Decision order: Sec-Fetch-Site (browser-set, unspoofable) is authoritative
// when present; otherwise fall back to Origin. A request with neither header
// (a local non-browser client such as curl) is trusted — a localhost bind is
// not defended against local processes by design (see the spec threat model).
func RequireSameOrigin(allowed []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("Sec-Fetch-Site") {
			case "same-origin", "same-site", "none":
				next.ServeHTTP(w, r)
				return
			case "cross-site", "cross-origin":
				forbidCrossOrigin(w)
				return
			}
			// Sec-Fetch-Site absent: fall back to Origin.
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r) // non-browser client; trusted
				return
			}
			if originAllowed(origin, allowed) {
				next.ServeHTTP(w, r)
				return
			}
			forbidCrossOrigin(w)
		})
	}
}

func forbidCrossOrigin(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"cross-origin request blocked"}`))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run 'TestOriginAllowed|TestRequireSameOrigin' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/gateway/origin.go internal/gateway/origin_test.go
git commit -m "feat(gateway): add RequireSameOrigin middleware + originAllowed predicate"
```

---

### Task 1.2: Refactor `AllowedOrigins` to delegate to `originAllowed`

**Files:**
- Modify: `internal/gateway/auth.go:51-78`

- [ ] **Step 1: Write the failing test**

Append to `internal/gateway/origin_test.go`:

```go
func TestAllowedOriginsDelegates(t *testing.T) {
	check := AllowedOrigins(nil)

	mk := func(origin string) *http.Request {
		req := httptest.NewRequest("GET", "/ws", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		return req
	}

	require.True(t, check(mk("")))                         // no Origin => allowed (CLI)
	require.True(t, check(mk("http://localhost:3000")))    // localhost => allowed
	require.False(t, check(mk("http://evil.com")))         // cross-site => blocked

	checkAllow := AllowedOrigins([]string{"https://app.example.com"})
	require.True(t, checkAllow(mk("https://app.example.com")))
	require.False(t, checkAllow(mk("https://other.example.com")))
}
```

- [ ] **Step 2: Run test to verify current behavior still holds (baseline)**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run TestAllowedOriginsDelegates -v`
Expected: PASS already (this captures existing behavior before the refactor — it's a regression guard).

- [ ] **Step 3: Refactor `AllowedOrigins`**

Replace the body of `AllowedOrigins` in `internal/gateway/auth.go` (lines 51-78) with:

```go
// AllowedOrigins returns a WebSocket CheckOrigin function that validates the
// request origin. An empty origins slice means localhost-only. A request with
// no Origin header (CLI tools, curl) is allowed. Delegates to originAllowed so
// the WebSocket and HTTP RequireSameOrigin share one definition.
func AllowedOrigins(origins []string) func(r *http.Request) bool {
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // no origin header (e.g. CLI tools, curl)
		}
		return originAllowed(origin, origins)
	}
}
```

Remove the now-unused `strings` import from `auth.go` **only if** nothing else in the file uses it (the bearer code uses `strings.HasPrefix`/`TrimPrefix`, so keep the import).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run 'TestAllowedOrigins|TestOriginAllowed|TestRequireSameOrigin' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/gateway/auth.go internal/gateway/origin_test.go
git commit -m "refactor(gateway): AllowedOrigins delegates to shared originAllowed"
```

---

### Task 1.3: Mount the guarded route group in `server.go`

**Files:**
- Modify: `internal/gateway/server.go:72-152` (`routes`)

- [ ] **Step 1: Write the failing integration test**

Create `internal/gateway/server_origin_test.go`:

```go
package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildGuardedRouter mirrors the guarded group wiring in routes() with stub
// handlers, so we can assert the middleware is actually applied to the
// sensitive routes without standing up the full gateway.
func TestGuardedRoutesRejectCrossSite(t *testing.T) {
	wsHandler := &WebSocketHandler{} // zero value is fine; /ws not exercised
	s := NewServer("127.0.0.1", 0, wsHandler, ServerOptions{})

	srv := httptest.NewServer(s.router)
	defer srv.Close()

	// A cross-site POST to a mutating route must be 403.
	req, _ := http.NewRequest("POST", srv.URL+"/admin/restart", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// A same-origin POST passes the guard (handler then does its own thing;
	// /admin/restart schedules an exit, so target a safer route instead).
	req2, _ := http.NewRequest("POST", srv.URL+"/admin/restart", nil)
	req2.Header.Set("Sec-Fetch-Site", "same-origin")
	// We don't actually want the process to exit during tests. Assert the
	// guard let it through by checking it is NOT 403. The restart handler
	// returns 200 then exits after a delay, so 200 is the pass signal.
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.NotEqual(t, http.StatusForbidden, resp2.StatusCode)
}
```

> **Note for the implementer:** `/admin/restart` calls `os.Exit` after a delay (`admin.go`). The same-origin assertion above returns before the delay fires, but to be safe during the test run, verify `NewRestartHandler`'s delay is ≥1s (it is — see `admin.go`). If flakiness appears, swap the same-origin assertion to a non-exiting guarded route such as `POST /files/mkdir` with a `Files` handler stubbed; the cross-site 403 assertion is the load-bearing one.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run TestGuardedRoutesRejectCrossSite -v`
Expected: FAIL — `/admin/restart` is currently unguarded, so the cross-site POST returns 200, not 403.

- [ ] **Step 3: Rewrite `routes()` with a guarded group**

Replace the `routes` method body in `internal/gateway/server.go` (lines 72-152) with the following. Safe read-only GETs stay outside the group; all mutating routes plus `/admin/restart` and `/logs*` move inside it.

```go
func (s *Server) routes() {
	// --- Unguarded: health, websocket, static assets, read-only GETs ---
	s.router.Get("/health", s.handleHealth)
	s.router.Get("/ws", s.wsHandler.Handle)

	s.router.Mount("/favicon.ico", FaviconHandler())
	s.router.Mount("/favicon.png", FaviconHandler())
	s.router.Mount("/logo-mark.png", LogoMarkHandler())

	if s.opts.MetricsHandler != nil {
		s.router.Get("/metrics", s.opts.MetricsHandler)
	}
	if s.opts.UIHandler != nil {
		s.router.Mount("/ui", s.opts.UIHandler)
	}
	if s.opts.ChatHandler != nil {
		s.router.Get("/chat", s.opts.ChatHandler)
		s.router.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/chat", http.StatusFound)
		})
	}
	if s.opts.JobsHandler != nil {
		s.router.Get("/jobs", s.opts.JobsHandler)
	}
	if s.opts.Settings != nil {
		s.router.Get("/settings", s.opts.Settings.Page)
		s.router.Get("/settings/", s.opts.Settings.Page)
		s.router.Get("/settings/api/config", s.opts.Settings.GetConfig)
		if s.opts.Settings.ListTools != nil {
			s.router.Get("/settings/api/tools", s.opts.Settings.ListTools)
		}
		if s.opts.Settings.BootstrapStatus != nil {
			s.router.Get("/settings/api/bootstrap", s.opts.Settings.BootstrapStatus)
		}
	}
	if s.opts.Skills != nil {
		s.router.Get("/settings/api/skills", s.opts.Skills.List)
		s.router.Get("/settings/api/skills/{name}", s.opts.Skills.Get)
	}
	if s.opts.Memory != nil {
		s.router.Get("/settings/api/memory", s.opts.Memory.List)
		s.router.Get("/settings/api/memory/{id}", s.opts.Memory.Get)
	}
	if s.opts.Files != nil {
		s.router.Get("/files", NewFilesPageHandler())
		s.router.Get("/files/list", s.opts.Files.List)
		s.router.Get("/files/raw", s.opts.Files.Raw)
	}

	// --- Guarded: mutating routes + sensitive GETs (/logs*) + restart ---
	s.router.Group(func(r chi.Router) {
		r.Use(RequireSameOrigin(s.opts.AllowedOrigins))

		r.Post("/admin/restart", NewRestartHandler())

		if s.opts.Settings != nil {
			r.Post("/settings/api/config", s.opts.Settings.SaveConfig)
		}
		if s.opts.Skills != nil {
			r.Post("/settings/api/skills", s.opts.Skills.Upload)
			r.Delete("/settings/api/skills/{name}", s.opts.Skills.Delete)
		}
		if s.opts.Memory != nil {
			r.Post("/settings/api/memory", s.opts.Memory.Save)
			r.Delete("/settings/api/memory/{id}", s.opts.Memory.Delete)
		}
		if s.opts.MCP != nil {
			r.Post("/api/mcp/reauth/{id}", s.opts.MCP.Reauth)
		}
		if s.opts.LogBuffer != nil {
			r.Get("/logs", NewLogsHandler(s.opts.LogBuffer))
			r.Get("/logs/stream", NewLogsStreamHandler(s.opts.LogBuffer))
		}
		if s.opts.Files != nil {
			r.Post("/files/upload", s.opts.Files.Upload)
			r.Delete("/files", s.opts.Files.Delete)
			r.Post("/files/move", s.opts.Files.Move)
			r.Post("/files/rename", s.opts.Files.Rename)
			r.Post("/files/mkdir", s.opts.Files.MkDir)
		}
	}
}
```

> **Important:** `/admin/restart` was previously registered unconditionally outside any `if`. It is now inside the guarded group but still unconditional (no `s.opts` gate) — preserve that. Confirm the `chi` import is already present (it is, line 11).

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run TestGuardedRoutesRejectCrossSite -v`
Expected: PASS.

- [ ] **Step 5: Run the full gateway package + build**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ && go build ./...`
Expected: PASS / clean build.

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/gateway/server.go internal/gateway/server_origin_test.go
git commit -m "feat(gateway): guard mutating routes + /logs + /admin/restart with same-origin check"
```

---

## Layer 2 — Secret redaction

### Task 2.1: Extend `redactConfigSecrets` + `restoreSecretScalars` for all secret fields

**Files:**
- Modify: `internal/gateway/settings.go:264-325`
- Create/Modify: `internal/gateway/settings_secrets_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/gateway/settings_secrets_test.go`:

```go
package gateway

import (
	"testing"

	"github.com/sausheong/felix/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRedactAndRestoreScalars(t *testing.T) {
	cur := &config.Config{}
	cur.Telegram.BotToken = "bot-secret"
	cur.Gateway.Auth.Token = "gw-secret"
	cur.WebSearch.APIKey = "ws-secret"
	cur.OTel.Headers = map[string]string{"authorization": "Bearer otel-secret"}

	// Redaction masks every scalar secret.
	clone := *cur
	clone.OTel.Headers = map[string]string{"authorization": "Bearer otel-secret"}
	redactConfigSecrets(&clone)
	require.Equal(t, redactedSentinel, clone.Telegram.BotToken)
	require.Equal(t, redactedSentinel, clone.Gateway.Auth.Token)
	require.Equal(t, redactedSentinel, clone.WebSearch.APIKey)
	require.Equal(t, redactedSentinel, clone.OTel.Headers["authorization"])

	// Restore swaps sentinels back to the stored values; a genuinely new
	// value is preserved.
	incoming := &config.Config{}
	incoming.Telegram.BotToken = redactedSentinel        // unchanged by user
	incoming.Gateway.Auth.Token = "gw-rotated"           // user typed a new one
	incoming.WebSearch.APIKey = redactedSentinel         // unchanged
	incoming.OTel.Headers = map[string]string{"authorization": redactedSentinel}
	restoreSecretScalars(incoming, cur)
	require.Equal(t, "bot-secret", incoming.Telegram.BotToken)
	require.Equal(t, "gw-rotated", incoming.Gateway.Auth.Token)
	require.Equal(t, "ws-secret", incoming.WebSearch.APIKey)
	require.Equal(t, "Bearer otel-secret", incoming.OTel.Headers["authorization"])
}

func TestRedactMCPHTTPAuthSecrets(t *testing.T) {
	cur := &config.Config{
		MCPServers: []config.MCPServerConfig{{
			ID:   "srv1",
			Auth: config.MCPAuthConfig{ClientSecret: "cs", Token: "tk"},
		}},
	}
	clone := &config.Config{
		MCPServers: []config.MCPServerConfig{{
			ID:   "srv1",
			Auth: config.MCPAuthConfig{ClientSecret: "cs", Token: "tk"},
		}},
	}
	redactConfigSecrets(clone)
	require.Equal(t, redactedSentinel, clone.MCPServers[0].Auth.ClientSecret)
	require.Equal(t, redactedSentinel, clone.MCPServers[0].Auth.Token)

	incoming := &config.Config{
		MCPServers: []config.MCPServerConfig{{
			ID:   "srv1",
			Auth: config.MCPAuthConfig{ClientSecret: redactedSentinel, Token: redactedSentinel},
		}},
	}
	restoreSecretScalars(incoming, cur)
	require.Equal(t, "cs", incoming.MCPServers[0].Auth.ClientSecret)
	require.Equal(t, "tk", incoming.MCPServers[0].Auth.Token)
}
```

> **Implementer note:** verify the exact field paths against `internal/config/config.go` before writing the impl: `Telegram.BotToken` (`:57`), `Gateway.Auth.Token` (`AuthConfig.Token`, `:155`), `WebSearch.APIKey` (`:380`), `OTel.Headers` (`:302`), `MCPServerConfig.Auth` (`MCPAuthConfig` with `ClientSecret` `:128` and `Token` `:136`). The top-level MCP server slice field is `MCPServers` and each element has `ID` and `Auth`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run 'TestRedactAndRestoreScalars|TestRedactMCPHTTPAuthSecrets' -v`
Expected: FAIL — `undefined: restoreSecretScalars`, and the existing `redactConfigSecrets` does not mask the scalar fields.

- [ ] **Step 3: Extend `redactConfigSecrets` and add `restoreSecretScalars`**

In `internal/gateway/settings.go`, replace `redactConfigSecrets` (lines 264-277) with:

```go
// redactConfigSecrets mutates cfg in place, replacing every secret-bearing
// value with redactedSentinel: MCP stdio env values, MCP HTTP auth literals
// (client_secret/token), the Telegram bot token, the gateway auth token, the
// web-search API key, and every OTel header value. The *_env name-reference
// forms are NOT secrets and are left intact. Provider api_key is handled
// separately in GetConfig.
func redactConfigSecrets(cfg *config.Config) {
	for i := range cfg.MCPServers {
		s := &cfg.MCPServers[i]
		if s.Stdio != nil && s.Stdio.Env != nil {
			s.Stdio.Env = redactSecretEnvMap(s.Stdio.Env)
		}
		if s.Auth.ClientSecret != "" {
			s.Auth.ClientSecret = redactedSentinel
		}
		if s.Auth.Token != "" {
			s.Auth.Token = redactedSentinel
		}
	}
	if cfg.Telegram.BotToken != "" {
		cfg.Telegram.BotToken = redactedSentinel
	}
	if cfg.Gateway.Auth.Token != "" {
		cfg.Gateway.Auth.Token = redactedSentinel
	}
	if cfg.WebSearch.APIKey != "" {
		cfg.WebSearch.APIKey = redactedSentinel
	}
	for k, v := range cfg.OTel.Headers {
		if v != "" {
			cfg.OTel.Headers[k] = redactedSentinel
		}
	}
}

// restoreSecretScalars mirrors restoreSecretEnvs/restoreSecretProviderKeys for
// the non-map scalar secrets. Any incoming field whose value is exactly
// redactedSentinel is swapped back to the stored value from current, so a
// GET -> edit -> PUT round-trip never drops a secret the user did not retype.
// A non-sentinel value is a genuine user edit and is left as-is.
func restoreSecretScalars(incoming, current *config.Config) {
	if incoming.Telegram.BotToken == redactedSentinel {
		incoming.Telegram.BotToken = current.Telegram.BotToken
	}
	if incoming.Gateway.Auth.Token == redactedSentinel {
		incoming.Gateway.Auth.Token = current.Gateway.Auth.Token
	}
	if incoming.WebSearch.APIKey == redactedSentinel {
		incoming.WebSearch.APIKey = current.WebSearch.APIKey
	}
	for k, v := range incoming.OTel.Headers {
		if v == redactedSentinel {
			incoming.OTel.Headers[k] = current.OTel.Headers[k]
		}
	}
	curByID := make(map[string]*config.MCPServerConfig, len(current.MCPServers))
	for i := range current.MCPServers {
		curByID[current.MCPServers[i].ID] = &current.MCPServers[i]
	}
	for i := range incoming.MCPServers {
		s := &incoming.MCPServers[i]
		cur := curByID[s.ID]
		if cur == nil {
			continue
		}
		if s.Auth.ClientSecret == redactedSentinel {
			s.Auth.ClientSecret = cur.Auth.ClientSecret
		}
		if s.Auth.Token == redactedSentinel {
			s.Auth.Token = cur.Auth.Token
		}
	}
}
```

> If `redactConfigSecrets` is called on the live `cfg` anywhere other than the cloned config in `GetConfig`, double-check it only ever runs on the marshal/unmarshal clone (it does — `settings.go:115`). Mutating `OTel.Headers` in place is safe on the clone because the clone has its own map after unmarshal.

- [ ] **Step 4: Wire `restoreSecretScalars` into `SaveConfig`**

In `internal/gateway/settings.go`, find the `SaveConfig` block where `restoreSecretEnvs` and `restoreSecretProviderKeys` are called (lines 174-175) and add the new call immediately after:

```go
			restoreSecretEnvs(&newCfg, cfg)
			restoreSecretProviderKeys(&newCfg, cfg)
			restoreSecretScalars(&newCfg, cfg)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run 'TestRedactAndRestoreScalars|TestRedactMCPHTTPAuthSecrets' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/gateway/settings.go internal/gateway/settings_secrets_test.go
git commit -m "feat(gateway): redact all config secrets (telegram/auth/websearch/otel/mcp-http) on settings API"
```

---

### Task 2.2: Log scrubber in `LogBuffer.Handle`

**Files:**
- Modify: `internal/gateway/logs.go:56-93`
- Create: `internal/gateway/logs_scrub_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/gateway/logs_scrub_test.go`:

```go
package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScrubSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bearer", "auth failed: Authorization: Bearer sk-abc123XYZ tail", "auth failed: Authorization: Bearer [REDACTED] tail"},
		{"apikey", "connecting with api_key=supersecretvalue now", "connecting with api_key=[REDACTED] now"},
		{"token kv", "token: abcdef123456 done", "token: [REDACTED] done"},
		{"openai sk", "key sk-ABCDEFGHIJKLMNOPQR in use", "key [REDACTED] in use"},
		{"telegram bot", "url https://api.telegram.org/bot123456789:AAEhBOweik6ad9r_QXVvQ_abcdefghij/send", "url https://api.telegram.org/bot[REDACTED]/send"},
		{"url userinfo", "dialing https://user:p4ss@host/path", "dialing https://[REDACTED]@host/path"},
		{"clean line untouched", "agent loop completed in 1.2s with 3 tool calls", "agent loop completed in 1.2s with 3 tool calls"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, scrubSecrets(c.in))
		})
	}
}
```

> **Implementer note:** the exact `[REDACTED]` substitutions above must match the regex replacements you write in Step 3. If a pattern is hard to make exact (e.g. the `token:` colon-form keeping the `:` vs `=`), adjust the `want` strings to match your implementation rather than forcing a brittle regex — the load-bearing requirement is that the secret value is gone, not the exact punctuation. Keep the "clean line untouched" case strict.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run TestScrubSecrets -v`
Expected: FAIL — `undefined: scrubSecrets`.

- [ ] **Step 3: Implement `scrubSecrets` and wire it into `Handle`**

Add to the top of `internal/gateway/logs.go` (after the imports — add `regexp` to the import block):

```go
// secretScrubbers are applied to every log message + attribute before the
// record enters the ring buffer and SSE fan-out. Best-effort defense in depth:
// the canonical fix is to not log secrets, but these close the known leak
// shapes (provider auth errors, MCP stderr, Telegram bot-token URLs).
var secretScrubbers = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)bearer\s+\S+`), "Bearer [REDACTED]"},
	{regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)(\s*[=:]\s*)\S+`), "${1}${2}[REDACTED]"},
	{regexp.MustCompile(`sk-[A-Za-z0-9]{16,}`), "[REDACTED]"},
	{regexp.MustCompile(`bot\d+:[A-Za-z0-9_-]{30,}`), "bot[REDACTED]"},
	{regexp.MustCompile(`//[^/@\s]+:[^/@\s]+@`), "//[REDACTED]@"},
}

// scrubSecrets returns s with known secret shapes masked.
func scrubSecrets(s string) string {
	for _, sc := range secretScrubbers {
		s = sc.re.ReplaceAllString(s, sc.repl)
	}
	return s
}
```

Then in `Handle`, scrub the message and attrs before constructing the entry. Replace lines 57-73 (the attr-formatting + entry construction) with:

```go
func (b *LogBuffer) Handle(ctx context.Context, r slog.Record) error {
	// Format attributes, scrubbing each value.
	attrs := ""
	r.Attrs(func(a slog.Attr) bool {
		if attrs != "" {
			attrs += " "
		}
		attrs += a.Key + "=" + scrubSecrets(a.Value.String())
		return true
	})

	entry := LogEntry{
		Time:    r.Time,
		Level:   r.Level,
		Message: scrubSecrets(r.Message),
		Attrs:   attrs,
	}
```

> The rest of `Handle` (the ring-buffer write, fan-out, and `b.inner.Handle(ctx, r)` forward) is unchanged. Note: the forwarded `r` to `b.inner` is the *original* unscrubbed record — that's intentional, the inner handler is the operator's own stderr/file sink, not a network-exposed surface. Only the buffer (which feeds `/logs`) is scrubbed.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run TestScrubSecrets -v`
Expected: PASS.

- [ ] **Step 5: Run full gateway package**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/gateway/logs.go internal/gateway/logs_scrub_test.go
git commit -m "feat(gateway): scrub secret patterns from /logs buffer + stream"
```

---

## Layer 3 — Content safety for `/files/raw`

### Task 3.1: Harden `Raw` with attachment default + security headers

**Files:**
- Modify: `internal/gateway/files.go:142-198`
- Create: `internal/gateway/files_raw_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/gateway/files_raw_test.go`:

```go
package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRawDispositionForContentType(t *testing.T) {
	// HTML and SVG must download (attachment); preview-safe types inline.
	require.Equal(t, "attachment", rawDisposition("text/html; charset=utf-8"))
	require.Equal(t, "attachment", rawDisposition("image/svg+xml"))
	require.Equal(t, "attachment", rawDisposition("application/octet-stream"))
	require.Equal(t, "inline", rawDisposition("image/png"))
	require.Equal(t, "inline", rawDisposition("text/plain; charset=utf-8"))
	require.Equal(t, "inline", rawDisposition("application/pdf"))
	require.Equal(t, "inline", rawDisposition("application/json"))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run TestRawDispositionForContentType -v`
Expected: FAIL — `undefined: rawDisposition`.

- [ ] **Step 3: Add `rawDisposition` helper + security headers in `Raw`**

In `internal/gateway/files.go`, add the helper (near the other file helpers, e.g. above `Raw`):

```go
// inlineSafeTypes are MIME types safe to render inline in the browser. SVG is
// deliberately excluded — it can carry script. Everything else downloads.
var inlineSafeTypes = map[string]bool{
	"image/png":        true,
	"image/jpeg":       true,
	"image/gif":        true,
	"image/webp":       true,
	"text/plain":       true,
	"application/pdf":  true,
	"application/json": true,
}

// rawDisposition returns "inline" only for an explicit allowlist of
// preview-safe content types; everything else (HTML, SVG, unknown binary)
// returns "attachment" so the browser downloads rather than renders it.
func rawDisposition(contentType string) string {
	// Strip any "; charset=..." parameter before matching.
	base := contentType
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSpace(strings.ToLower(base))
	if inlineSafeTypes[base] {
		return "inline"
	}
	return "attachment"
}
```

Then in `Raw`, replace the disposition + header block (lines 184-192) with:

```go
	// Sniff Content-Type from first 512 bytes.
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	ct := http.DetectContentType(buf[:n])
	w.Header().Set("Content-Type", ct)

	// Security headers: never let the browser sniff a different type, and
	// sandbox any content that does render so it cannot run script or call
	// back into the gateway API.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")

	disposition := rawDisposition(ct)
	if download {
		disposition = "attachment" // explicit ?download=1 always attaches
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename=%q`, disposition, filepath.Base(abs)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.WriteHeader(http.StatusOK)
```

> Confirm `strings` is imported in `files.go`; add it to the import block if not.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ -run TestRawDispositionForContentType -v`
Expected: PASS.

- [ ] **Step 5: Build + full package test**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/gateway/ && go build ./...`
Expected: PASS / clean.

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/gateway/files.go internal/gateway/files_raw_test.go
git commit -m "feat(gateway): files/raw attachment-by-default + nosniff + CSP sandbox"
```

---

## Layer 4 — MCP subprocess env isolation

### Task 4.1: Add `InheritEnv` to config + thread through resolution

**Files:**
- Modify: `internal/config/config.go:100-104` (`MCPStdioBlock`), `:984-990` (`ResolveMCPServers` stdio branch)

- [ ] **Step 1: Locate the resolved-server struct field**

Run: `cd /Users/sausheong/projects/felix && sed -n '975,995p' internal/config/config.go`
Expected: shows the stdio resolution branch building a struct with `Command`, `Args`, `Env`. Note the **name of the resolved struct type** (the value `ResolveMCPServers` returns) — you need to add an `InheritEnv` field there too. Call it `<ResolvedType>` below.

- [ ] **Step 2: Write the failing test**

Create `internal/config/mcp_inherit_env_test.go`:

```go
package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPStdioInheritEnvRoundTrips(t *testing.T) {
	raw := `{"command":"npx","args":["x"],"env":{"A":"1"},"inherit_env":true}`
	var b MCPStdioBlock
	require.NoError(t, json.Unmarshal([]byte(raw), &b))
	require.True(t, b.InheritEnv)
	require.Equal(t, "npx", b.Command)

	out, err := json.Marshal(b)
	require.NoError(t, err)
	require.Contains(t, string(out), `"inherit_env":true`)

	// Defaults to false when absent.
	var b2 MCPStdioBlock
	require.NoError(t, json.Unmarshal([]byte(`{"command":"x"}`), &b2))
	require.False(t, b2.InheritEnv)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/config/ -run TestMCPStdioInheritEnvRoundTrips -v`
Expected: FAIL — `b.InheritEnv undefined`.

- [ ] **Step 4: Add the field + thread it through**

In `internal/config/config.go`, add to `MCPStdioBlock` (after `Env`, line 103):

```go
	Env        map[string]string `json:"env,omitempty"`
	InheritEnv bool              `json:"inherit_env,omitempty"` // opt-in: pass full parent env to the subprocess (default: minimal base + Env)
```

Then in the `ResolveMCPServers` stdio branch (around lines 984-990), add `InheritEnv` to the resolved struct it builds:

```go
				Command:    s.Stdio.Command,
				Args:       s.Stdio.Args,
				Env:        s.Stdio.Env,
				InheritEnv: s.Stdio.InheritEnv,
```

Add the matching `InheritEnv bool` field to the resolved struct type identified in Step 1 (search for the type with `Command string` / `Args []string` / `Env map[string]string` that `ResolveMCPServers` returns; it is the stdio resolved-server shape).

- [ ] **Step 5: Run test + build to verify**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/config/ -run TestMCPStdioInheritEnvRoundTrips -v && go build ./...`
Expected: PASS / clean (the build surfaces the resolved-struct field name if missed).

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/config/config.go internal/config/mcp_inherit_env_test.go
git commit -m "feat(config): add mcp stdio inherit_env opt-out flag"
```

---

### Task 4.2: `minimalBaseEnv` + `ConnectStdio(inheritEnv)`

**Files:**
- Modify: `internal/mcp/stdio.go:30-57`
- Create: `internal/mcp/stdio_env_test.go`
- Modify: the caller of `ConnectStdio` (found below)

- [ ] **Step 1: Find the `ConnectStdio` caller**

Run: `cd /Users/sausheong/projects/felix && grep -rn "ConnectStdio" internal/`
Expected: one or more call sites (likely in `internal/mcp/manager.go`). Note each — they need the new `inheritEnv` argument wired from the resolved server's `InheritEnv`.

- [ ] **Step 2: Write the failing test**

Create `internal/mcp/stdio_env_test.go`:

```go
package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMinimalBaseEnvExcludesSecrets(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"LANG=en_US.UTF-8",
		"LC_ALL=C",
		"TZ=UTC",
		"FELIX_SECRET=topsecret",
		"OPENAI_API_KEY=sk-xyz",
		"OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer z",
	}
	got := minimalBaseEnv(parent)
	asMap := map[string]bool{}
	for _, kv := range got {
		asMap[kv] = true
	}
	require.True(t, asMap["PATH=/usr/bin"])
	require.True(t, asMap["HOME=/home/u"])
	require.True(t, asMap["LANG=en_US.UTF-8"])
	require.True(t, asMap["LC_ALL=C"])
	require.True(t, asMap["TZ=UTC"])
	require.False(t, asMap["FELIX_SECRET=topsecret"])
	require.False(t, asMap["OPENAI_API_KEY=sk-xyz"])
	require.False(t, asMap["OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer z"])
}

func TestStdioEnvForUsesMinimalByDefault(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "SECRET=x"}
	overrides := map[string]string{"FOO": "bar"}

	minimal := stdioEnvFor(parent, overrides, false)
	mm := map[string]bool{}
	for _, kv := range minimal {
		mm[kv] = true
	}
	require.True(t, mm["PATH=/usr/bin"])
	require.True(t, mm["FOO=bar"])
	require.False(t, mm["SECRET=x"])

	full := stdioEnvFor(parent, overrides, true)
	fm := map[string]bool{}
	for _, kv := range full {
		fm[kv] = true
	}
	require.True(t, fm["SECRET=x"]) // inheritEnv=true => full parent
	require.True(t, fm["FOO=bar"])
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/mcp/ -run 'TestMinimalBaseEnv|TestStdioEnvFor' -v`
Expected: FAIL — `undefined: minimalBaseEnv`, `undefined: stdioEnvFor`.

- [ ] **Step 4: Implement `minimalBaseEnv` + `stdioEnvFor`, update `ConnectStdio`**

In `internal/mcp/stdio.go`, add:

```go
// minimalBaseEnvKeys are the parent-env variables passed to a stdio MCP
// subprocess by default. PATH/HOME/temp/locale are needed for node/python to
// start; secrets (provider keys, OTEL_*, anything else) are deliberately
// excluded. Windows names cover what node/python need to launch there.
var minimalBaseEnvKeys = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "TEMP": true, "TMP": true,
	"LANG": true, "TZ": true,
	// Windows:
	"SystemRoot": true, "USERPROFILE": true, "APPDATA": true,
	"LOCALAPPDATA": true, "ProgramData": true, "PATHEXT": true, "ComSpec": true,
}

// minimalBaseEnv filters parent down to the curated allowlist (plus any LC_*
// locale vars). Order is preserved.
func minimalBaseEnv(parent []string) []string {
	out := make([]string, 0, len(minimalBaseEnvKeys))
	for _, kv := range parent {
		key, _, _ := strings.Cut(kv, "=")
		if minimalBaseEnvKeys[key] || strings.HasPrefix(key, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}

// stdioEnvFor builds the subprocess environment. When inheritEnv is false
// (default) the base is the minimal allowlist; when true it is the full
// parent. The server's explicit overrides are merged on top in both cases.
func stdioEnvFor(parent []string, overrides map[string]string, inheritEnv bool) []string {
	base := parent
	if !inheritEnv {
		base = minimalBaseEnv(parent)
	}
	return mergedEnv(base, overrides)
}
```

Update `ConnectStdio`'s signature and the env line. Change line 30 and line 35:

```go
func ConnectStdio(ctx context.Context, id, command string, args []string, env map[string]string, inheritEnv bool) (*Client, error) {
	if command == "" {
		return nil, fmt.Errorf("mcp stdio %s: empty command", id)
	}
	cmd := exec.Command(command, args...)
	cmd.Env = stdioEnvFor(os.Environ(), env, inheritEnv)
```

Update the doc comment block above `ConnectStdio` to note the env behavior change (replace the "env is merged onto os.Environ()" sentence with a note that the base is minimal unless `inheritEnv` is set).

- [ ] **Step 5: Update the caller(s) from Step 1**

At each `ConnectStdio(...)` call site, pass the resolved server's `InheritEnv`. Example (adjust to the actual variable name holding the resolved stdio server):

```go
client, err := ConnectStdio(ctx, srv.ID, srv.Command, srv.Args, srv.Env, srv.InheritEnv)
```

- [ ] **Step 6: Run tests + build**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/mcp/ -run 'TestMinimalBaseEnv|TestStdioEnvFor' -v && go build ./...`
Expected: PASS / clean.

- [ ] **Step 7: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/mcp/stdio.go internal/mcp/stdio_env_test.go internal/mcp/
git commit -m "feat(mcp): minimal stdio subprocess env by default; inherit_env opt-out"
```

---

## Layer 5 — Filesystem integrity

### Task 5.1: `tool.WriteFileAtomic` helper (harness)

**Files:**
- Create: `/Users/sausheong/projects/harness/tool/atomic.go`
- Create: `/Users/sausheong/projects/harness/tool/atomic_test.go`

- [ ] **Step 1: Write the failing test**

Create `/Users/sausheong/projects/harness/tool/atomic_test.go`:

```go
package tool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	require.NoError(t, WriteFileAtomic(path, []byte("hello"), 0o600))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Overwrite atomically with new perms.
	require.NoError(t, WriteFileAtomic(path, []byte("world"), 0o644))
	data, _ = os.ReadFile(path)
	require.Equal(t, "world", string(data))

	// No leftover temp files in the directory.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "out.txt", entries[0].Name())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/harness && go test ./tool/ -run TestWriteFileAtomic -v`
Expected: FAIL — `undefined: WriteFileAtomic`.

- [ ] **Step 3: Implement the helper**

Create `/Users/sausheong/projects/harness/tool/atomic.go`:

```go
package tool

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path atomically: it writes to a temp file in
// the same directory, fsyncs, sets perm, then renames over path. A crash
// mid-write leaves either the old file or the complete new file, never a
// truncated one. The rename is atomic on POSIX within a single filesystem.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/sausheong/projects/harness && go test ./tool/ -run TestWriteFileAtomic -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/sausheong/projects/harness
git add tool/atomic.go tool/atomic_test.go
git commit -m "feat(tool): add WriteFileAtomic helper"
```

---

### Task 5.2: `write_file` → atomic + 0600 (harness)

**Files:**
- Modify: `/Users/sausheong/projects/harness/tools/file/writefile.go:75`
- Create: `/Users/sausheong/projects/harness/tools/file/writefile_perm_test.go`

- [ ] **Step 1: Write the failing test**

Create `/Users/sausheong/projects/harness/tools/file/writefile_perm_test.go`:

```go
package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteFileIsRestrictive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")

	tool := &WriteFileTool{WorkDir: dir}
	in, _ := json.Marshal(map[string]string{"path": path, "content": "data"})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	require.Empty(t, res.Error)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/file/ -run TestWriteFileIsRestrictive -v`
Expected: FAIL — file mode is `0o644`, not `0o600`.

- [ ] **Step 3: Switch to the atomic helper**

In `/Users/sausheong/projects/harness/tools/file/writefile.go`, replace line 75:

```go
	if err := tool.WriteFileAtomic(in.Path, []byte(in.Content), 0o600); err != nil {
		return tool.ToolResult{Error: fmt.Sprintf("failed to write file: %v", err)}, nil
	}
```

(`tool` is already imported.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/file/ -run TestWriteFileIsRestrictive -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/sausheong/projects/harness
git add tools/file/writefile.go tools/file/writefile_perm_test.go
git commit -m "feat(tools/file): write_file atomic + 0600"
```

---

### Task 5.3: `edit_file` → atomic + mode-preserve (harness)

**Files:**
- Modify: `/Users/sausheong/projects/harness/tools/file/editfile.go:92-96`
- Create: `/Users/sausheong/projects/harness/tools/file/editfile_perm_test.go`

- [ ] **Step 1: Read the current edit write site**

Run: `cd /Users/sausheong/projects/harness && sed -n '60,100p' tools/file/editfile.go`
Expected: shows the read (`os.ReadFile`), the match/replace, and the `os.WriteFile(in.Path, []byte(newContent), 0o644)` at line 94.

- [ ] **Step 2: Write the failing test**

Create `/Users/sausheong/projects/harness/tools/file/editfile_perm_test.go`:

```go
package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEditFilePreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.txt")
	require.NoError(t, os.WriteFile(path, []byte("alpha beta"), 0o640))

	tool := &EditFileTool{WorkDir: dir}
	in, _ := json.Marshal(map[string]string{
		"path":       path,
		"old_string": "alpha",
		"new_string": "gamma",
	})
	res, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	require.Empty(t, res.Error)

	data, _ := os.ReadFile(path)
	require.Equal(t, "gamma beta", string(data))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm()) // preserved
}
```

> **Implementer note:** confirm the edit tool's JSON field names (`old_string`/`new_string`) against `editfile.go`'s input struct before running; adjust the test's keys to match if they differ.

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/file/ -run TestEditFilePreservesMode -v`
Expected: FAIL — mode becomes `0o644`, not the preserved `0o640`.

- [ ] **Step 4: Preserve mode + write atomically**

In `/Users/sausheong/projects/harness/tools/file/editfile.go`, replace the write (line 94) with a stat-then-atomic-write. Insert before the write:

```go
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(in.Path); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := tool.WriteFileAtomic(in.Path, []byte(newContent), mode); err != nil {
		return tool.ToolResult{Error: fmt.Sprintf("failed to write file: %v", err)}, nil
	}
```

Remove the old `os.WriteFile(in.Path, []byte(newContent), 0o644)` line. Confirm `tool` and `os` are imported (they are).

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /Users/sausheong/projects/harness && go test ./tools/file/ -run TestEditFilePreservesMode -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/harness
git add tools/file/editfile.go tools/file/editfile_perm_test.go
git commit -m "feat(tools/file): edit_file atomic + preserve existing mode"
```

---

### Task 5.4: Spill writer → 0600 via atomic helper (harness)

**Files:**
- Modify: `/Users/sausheong/projects/harness/runtime/context.go:432-436`

- [ ] **Step 1: Read the spill write site**

Run: `cd /Users/sausheong/projects/harness && sed -n '425,445p' runtime/context.go`
Expected: shows `os.MkdirAll(dir, 0o755)` then `os.WriteFile(path, []byte(content), 0o644)`.

- [ ] **Step 2: Switch the spill write to atomic 0600**

In `/Users/sausheong/projects/harness/runtime/context.go`, replace the `os.WriteFile(path, []byte(content), 0o644)` (line 436) with:

```go
	if err := tool.WriteFileAtomic(path, []byte(content), 0o600); err != nil {
```

Confirm the `runtime` package imports `github.com/sausheong/harness/tool`. Run: `grep -n '"github.com/sausheong/harness/tool"' runtime/context.go`. If absent, add it to the import block. (The `MkdirAll(dir, 0o755)` line above stays — directories remain traversable; only the file is restricted.)

- [ ] **Step 3: Build + run runtime tests**

Run: `cd /Users/sausheong/projects/harness && go build ./... && go test ./runtime/`
Expected: clean build, tests PASS.

- [ ] **Step 4: Verify Felix still builds against the modified harness**

Run: `cd /Users/sausheong/projects/felix && go build ./...`
Expected: clean (confirms the replace-wired harness changes compile in Felix).

- [ ] **Step 5: Commit**

```bash
cd /Users/sausheong/projects/harness
git add runtime/context.go
git commit -m "feat(runtime): spill files 0600 via atomic write"
```

---

### Task 5.5: `Config.Save` lock + atomic (felix)

**Files:**
- Modify: `internal/config/config.go:845-864`
- Create: `internal/config/save_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/config/save_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSaveIsAtomicAnd0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "felix.json5")

	c := &Config{}
	c.SetPath(path)
	require.NoError(t, c.Save())

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// No leftover temp files.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "felix.json5", entries[0].Name())
}
```

> **Implementer note:** confirm `SetPath` exists (it's used in `settings.go:185`). If the setter has a different name, adjust.

- [ ] **Step 2: Run test to verify it fails or passes**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/config/ -run TestSaveIsAtomicAnd0600 -v`
Expected: This may PASS for perms (Save already uses 0o600) but the **leftover temp file** assertion is the new guarantee. If it passes fully, the test still documents the contract — proceed to harden the marshal-under-lock (the race fix has no direct unit assertion; it's verified by `-race` in Step 5).

- [ ] **Step 3: Rewrite `Config.Save` to marshal under lock + write atomically**

In `internal/config/config.go`, replace `Save` (lines 845-864) with:

```go
func (c *Config) Save() error {
	c.mu.RLock()
	path := c.path
	data, err := json.MarshalIndent(c, "", "  ")
	c.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if path == "" {
		path = DefaultConfigPath()
	}

	if err := WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
```

> This holds `c.mu.RLock()` across the `MarshalIndent` (fixing the data race with the watcher's `UpdateFrom`) and uses the package's own `WriteFileAtomic` (`config/writefile.go:17`) instead of plain `os.WriteFile`.
>
> **Caveat to record in the commit body:** atomic rename swaps the inode, which can silence an fsnotify watcher `Add`-ed on the path (audit R3). R3 is deferred; this is acceptable because hot-reload-after-Save is not a default-safety property. Note it so the R3 fix is sequenced with this.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/config/ -run TestSaveIsAtomicAnd0600 -v`
Expected: PASS.

- [ ] **Step 5: Race check**

Run: `cd /Users/sausheong/projects/felix && go test -race ./internal/config/`
Expected: PASS, no race reports.

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/config/config.go internal/config/save_test.go
git commit -m "fix(config): Save marshals under RLock + writes atomically (R3 caveat noted)"
```

---

### Task 5.6: Fail-closed empty-Workspace guard (felix)

**Files:**
- Modify: `internal/startup/startup.go:746,811` (the two `RegisterCoreToolsWithSearch` call sites)

- [ ] **Step 1: Read both call sites**

Run: `cd /Users/sausheong/projects/felix && sed -n '740,750p;805,815p' internal/startup/startup.go`
Expected: shows `tools.RegisterCoreToolsWithSearch(reg, a.Workspace, ...)` at ~746 and `tools.RegisterCoreToolsWithSearch(cronToolReg, agentCfg.Workspace, ...)` at ~811. **Confirmed field/loop facts:** `AgentConfig` has both `ID string` and `Workspace string`. Site ~746 sits in a function returning `(agent.RuntimeInputs, error)` (it already does `return agent.RuntimeInputs{}, fmt.Errorf(...)` at line 743). Site ~811 sits in a cron closure returning `(string, error)` (it already does `return "", fmt.Errorf(...)` at line 806). The guards below match those two distinct return signatures — do not copy one into the other site.

- [ ] **Step 2: Add the guard before the primary call site (746)**

Immediately before the `tools.RegisterCoreToolsWithSearch(reg, a.Workspace, ...)` line, insert (this function returns `(agent.RuntimeInputs, error)`):

```go
		if strings.TrimSpace(a.Workspace) == "" {
			return agent.RuntimeInputs{}, fmt.Errorf("agent %q has no workspace; refusing to build unclamped file tools", a.ID)
		}
```

> `strings`, `fmt`, and the `agent` package are already imported in this file.

- [ ] **Step 3: Add the guard before the cron call site (811)**

Immediately before `tools.RegisterCoreToolsWithSearch(cronToolReg, agentCfg.Workspace, ...)`, insert the analogous guard using `agentCfg` (this closure returns `(string, error)` — note the empty-string first return):

```go
			if strings.TrimSpace(agentCfg.Workspace) == "" {
				return "", fmt.Errorf("agent %q has no workspace; refusing to build unclamped file tools", agentCfg.ID)
			}
```

> Verify the exact return signature against the lines printed in Step 1 (the closure already returns `"", fmt.Errorf(...)` at ~806). If the build reports a mismatch, adjust the first return value to match the printed signature.

- [ ] **Step 4: Build to verify**

Run: `cd /Users/sausheong/projects/felix && go build ./... && go vet ./internal/startup/`
Expected: clean.

- [ ] **Step 5: Run startup tests**

Run: `cd /Users/sausheong/projects/felix && go test ./internal/startup/`
Expected: PASS. (The existing `startup_test.go` sets a Workspace inside tmp — see the comment at `startup_test.go:43` — so it won't trip the guard.)

- [ ] **Step 6: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/startup/startup.go
git commit -m "feat(startup): fail closed when an agent has no workspace (clamp file tools)"
```

---

### Task 5.7: Permission sweep — cron-jobs.json, ollama.pid, felix-app.log (felix)

**Files:**
- Modify: `internal/startup/startup.go:233` (cron-jobs tmp write)
- Modify: `internal/local/supervisor.go:257` (ollama.pid)
- Modify: `cmd/felix-app/main.go:70-71` (felix-app.log)

- [ ] **Step 1: cron-jobs.json tmp write → 0600**

In `internal/startup/startup.go`, line 233, change the perm on the tmp write (the rename is already atomic):

```go
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
```

- [ ] **Step 2: ollama.pid → 0600**

In `internal/local/supervisor.go`, line 257:

```go
	if err := os.WriteFile(s.pidFile, []byte(line), 0o600); err != nil {
```

- [ ] **Step 3: felix-app.log → 0600**

In `cmd/felix-app/main.go`, line 70-71, change the open mode:

```go
	f, err := os.OpenFile(filepath.Join(dir, "felix-app.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
```

> Perm on `OpenFile` only applies when the file is *created*; an existing `0o644` log keeps its mode. That's acceptable — new installs get `0o600`. (A `chmod`-on-open is out of scope; rotation is deferred.)

- [ ] **Step 4: Build both binaries**

Run: `cd /Users/sausheong/projects/felix && go build ./cmd/felix ./cmd/felix-app`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/startup/startup.go internal/local/supervisor.go cmd/felix-app/main.go
git commit -m "feat: restrict cron-jobs.json, ollama.pid, felix-app.log to 0600"
```

---

## Final verification

### Task 6.1: Full test + race + vet across both repos

- [ ] **Step 1: harness full suite**

Run: `cd /Users/sausheong/projects/harness && go build ./... && go test ./... && go vet ./...`
Expected: clean build, all tests PASS, no vet issues.

- [ ] **Step 2: harness race check on touched packages**

Run: `cd /Users/sausheong/projects/harness && go test -race ./tool/ ./tools/file/ ./runtime/`
Expected: PASS, no race reports.

- [ ] **Step 3: felix full suite**

Run: `cd /Users/sausheong/projects/felix && go build ./... && go test ./... && go vet ./...`
Expected: clean build, all tests PASS, no vet issues.

- [ ] **Step 4: felix race check on touched packages**

Run: `cd /Users/sausheong/projects/felix && go test -race ./internal/gateway/ ./internal/config/ ./internal/mcp/`
Expected: PASS, no race reports.

- [ ] **Step 5: lint (if configured)**

Run: `cd /Users/sausheong/projects/felix && golangci-lint run` then `cd /Users/sausheong/projects/harness && golangci-lint run`
Expected: clean (or no new findings vs. baseline).

- [ ] **Step 6: Manual smoke (optional but recommended)**

Run: `cd /Users/sausheong/projects/felix && go run ./cmd/felix start` in one terminal, then in another:
```bash
# cross-site POST is blocked
curl -s -o /dev/null -w "%{http_code}\n" -X POST -H "Sec-Fetch-Site: cross-site" http://127.0.0.1:18789/admin/restart   # expect 403
# local curl (no Origin) still works for reads
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:18789/health   # expect 200
```
Expected: 403 then 200. Stop the server (Ctrl-C).

---

## Self-Review Notes (coverage map)

| Spec section | Finding | Task |
|--------------|---------|------|
| §3 Layer 1 | G1, N7, N1-browser | 1.1, 1.2, 1.3 |
| §4 Layer 2 | S4 | 2.1 |
| §4 Layer 2 | N1-redaction | 2.2 |
| §5 Layer 3 | G2 | 3.1 |
| §6 Layer 4 | M1 | 4.1, 4.2 |
| §7.1 helper | (shared) | 5.1 |
| §7.3 | S7 (perms), N6-write | 5.2 |
| §7.3 | N6-edit | 5.3 |
| §7.4 | L2 (spill) | 5.4 |
| §7.2 | S5 (narrowed) | 5.5 |
| §7.3 | S7 (fail-closed) | 5.6 |
| §7.4 | S9 (perms) | 5.7 |
| §8 | testing | per-task + 6.1 |

All five layers and every in-scope finding map to a task. Deferred items (R3, S9-reap, log rotation, CSRF token, local-process auth) are intentionally absent per spec §10.
