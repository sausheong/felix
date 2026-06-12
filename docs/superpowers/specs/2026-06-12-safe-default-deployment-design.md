# Safe-by-Default Deployment Hardening — Design

**Date:** 2026-06-12
**Status:** Design (pending implementation plan)
**Scope:** Execution-order step 1 from `optimisation.md` — make a default `felix start`
(no auth token) safe out of the box.
**Repos touched:** `felix` and `harness` (wired via `go.mod replace`; harness changes
ship to Felix automatically and affect every harness consumer).

---

## 1. Goal & threat model

A default `felix start` binds `127.0.0.1:18789` with **no auth token**. A localhost bind is
not a security boundary against the **operator's own browser**: any web page the operator
visits can issue cross-site requests to the gateway. The audit found that, in this default
configuration, such a page can rewrite config, upload a system-prompt-injecting skill,
restart the process, and read every secret — with zero authentication.

**In scope — the realistic threat:** a malicious/compromised web page open in the operator's
browser making cross-site `fetch()` / `EventSource` requests to the loopback gateway, plus
secret leakage to that vector and to third-party MCP subprocesses.

**Explicitly out of scope (deliberate decision):** authenticating local *non-browser*
processes (e.g. `curl` from another local process). Rationale: any local process can already
read `~/.felix/felix.json5` directly, so gating loopback traffic from non-browser clients
adds little. A local `curl` sends no `Origin`/`Sec-Fetch-Site` header and is therefore
treated as trusted. Operators who need to defend against local processes set a gateway auth
token (existing mechanism, unchanged).

**Non-goals / explicitly deferred** (named so the boundary is unambiguous):
- A synchronizer/double-submit CSRF token system (Origin-checking is sufficient for a
  loopback bind; token plumbing rejected as YAGNI).
- Auto-generated local tokens or off-by-default sensitive routes.
- fsnotify watcher robustness after atomic rename (audit R3) — see §6.1 caveat.
- Ollama supervisor reap-logic correctness (audit S9 deeper half) — perm flip only here.
- App-log rotation (audit S9) — perm flip only here.

---

## 2. Architecture: five independent layers

The step-1 findings share a small number of mechanisms. Rather than implement them by
audit-ID, the work is grouped into five independently-testable layers. This avoids touching
the same file repeatedly (e.g. the `server.go` route table gets exactly one middleware pass)
and gives a clean test-per-layer structure.

| Layer | Mechanism | Findings | Repo |
|-------|-----------|----------|------|
| 1. HTTP origin guard | `RequireSameOrigin` middleware on a chi route group | G1, N7, N1 (browser vector) | felix |
| 2. Secret redaction | Extend settings redaction; LogBuffer scrubber | S4, N1 (redaction) | felix |
| 3. Content safety | `/files/raw` → attachment + `nosniff` + CSP | G2 | felix |
| 4. Subprocess isolation | Minimal MCP env + `inherit_env` opt-out | M1 | felix |
| 5. Filesystem integrity | Atomic+locked Save; file-tool 0600+atomic; fail-closed WorkDir; perm sweep | S5, S7, N6, S9 (perms), L2 | felix + harness |

**Linchpin principle:** Layer 1 reuses the *exact* origin-matching predicate already used by
the WebSocket (`AllowedOrigins`, `internal/gateway/auth.go:51-78`). HTTP and WS share one
definition of "allowed origin" so the two cannot drift.

### 2.1 Drift corrections (audit vs. working tree on 2026-06-12)

The audit was written against slightly older code. Verified against the current tree:

- **S5 narrowed.** `Config.Save` (`internal/config/config.go:845-864`) **already** writes
  `0o600` — the permission half is done. Two issues remain and are the only S5 work here:
  (a) it calls `json.MarshalIndent(c, …)` at `:854` *outside* `c.mu` (races the watcher's
  `UpdateFrom`), and (b) it uses a plain `os.WriteFile` (non-atomic; crash mid-write
  truncates config and loses API keys).
- All other step-1 findings confirmed present as the audit describes.

---

## 3. Layer 1 — HTTP origin guard (G1, N7, N1 browser vector)

### 3.1 New file `internal/gateway/origin.go`

Refactor the origin-matching logic out of `AllowedOrigins` into a shared predicate, then
build HTTP middleware on it.

```go
// originAllowed reports whether origin is permitted given the configured allowlist.
//   - allowed empty  -> localhost-only: the four prefixes
//                       http(s)://127.0.0.1 and http(s)://localhost
//   - allowed set    -> exact match against the trailing-slash-trimmed set
func originAllowed(origin string, allowed []string) bool

// RequireSameOrigin rejects cross-site state-changing (and sensitive) requests.
func RequireSameOrigin(allowed []string) func(http.Handler) http.Handler
```

Middleware logic (executes per guarded request — note there is **no** method
short-circuit; the guard checks every request routed through it, see 3.2 for why):

```
site := r.Header.Get("Sec-Fetch-Site")
switch site:
  "same-origin", "same-site", "none" -> next   // browser says it's safe
  "cross-site"                        -> 403    // browser says it's an attack
  "" (header absent / older client):
      origin := r.Header.Get("Origin")
      if origin == ""                 -> next   // non-browser (curl); trusted
      if originAllowed(origin, allowed) -> next
      otherwise                       -> 403
403 body: {"error":"cross-origin request blocked"}
```

**Why no method short-circuit.** The only GET routes placed inside the guarded group are
`/logs` and `/logs/stream` (the N1 browser-exfil vector, which we *want* checked); every
other guarded route is a POST/DELETE. There are therefore no safe GETs in the group that
would need exempting, so a `GET → next` short-circuit would be dead logic — and worse, it
would silently un-guard `/logs*`. The guard checks unconditionally; safe read-only GETs are
kept *outside* the group (§3.2) rather than exempted by method inside it.

**Why both headers.** `Sec-Fetch-Site` is browser-set, unspoofable, and explicitly labels
cross-site requests — but is absent on older clients. `Origin` is the fallback. A cross-site
`fetch()`/`EventSource` always carries at least one; a local `curl` carries neither and
passes (matches the §1 decision).

`AllowedOrigins` (`auth.go`) is refactored to delegate to `originAllowed` so its behavior is
unchanged and there is a single matching implementation.

### 3.2 Wiring in `internal/gateway/server.go`

Introduce a single chi route group guarded by `RequireSameOrigin(o.AllowedOrigins)`. Safe,
read-only GETs stay outside it.

**Outside the group (unguarded):** `/health`, `/chat`, `/` redirect, `/settings` page,
`/settings/` page, `/files` page, `/jobs`, favicons, `/logo-mark.png`, `/metrics`, `/ui`,
`GET /settings/api/config`, `GET /settings/api/skills*`, `GET /settings/api/memory*`,
`GET /settings/api/tools`, `GET /settings/api/bootstrap`, `GET /files/list`,
`GET /files/raw` (defended instead by Layer 3).

> Note: `GET /settings/api/config` stays unguarded but is defended by Layer 2 redaction —
> a cross-site page cannot read the response body cross-origin without CORS (which the
> gateway never sets), and same-origin reads are by definition the operator's own UI.

**Inside the group (guarded):**
- `POST /settings/api/config`
- `POST /settings/api/skills`, `DELETE /settings/api/skills/{name}`
- `POST /settings/api/memory`, `DELETE /settings/api/memory/{id}`
- `POST /files/upload`, `DELETE /files`, `POST /files/move`, `POST /files/rename`,
  `POST /files/mkdir`
- `POST /api/mcp/reauth/{id}`
- `POST /admin/restart` — **moves into the group** (currently mounted unconditionally at
  `server.go:85`). This is **N7**: a cross-site POST now gets 403; a local curl still works
  (accepted DoS surface per §1).
- `GET /logs`, `GET /logs/stream` — placed in the group **even though they are GETs**,
  because a cross-site `EventSource`/fetch to them is the **N1 browser exfil vector**. Since
  the guard has no method short-circuit (§3.1), simply registering them inside the group is
  sufficient — the `Sec-Fetch-Site`/`Origin` check applies to them like any other guarded
  route. No per-path special-casing needed.

The route table is reorganized into "safe GETs" then the guarded `r.Group`, preserving every
existing handler reference.

### 3.3 What this closes

- **G1:** every mutating endpoint rejects cross-site browser requests.
- **N7:** drive-by `POST /admin/restart` from a web page → 403.
- **N1 (browser half):** a cross-site page can no longer open the log SSE stream. The
  *redaction* half of N1 is Layer 2.

---

## 4. Layer 2 — Secret redaction (S4, N1 redaction)

### 4.1 S4 — extend settings redaction (`internal/gateway/settings.go`)

`redactConfigSecrets` (`settings.go:264`) currently redacts only MCP stdio env (provider
`api_key` handled separately at `:132-144`). Extend it — and the save-time restore — to:

- `Telegram.BotToken`
- `Gateway.Auth.Token`
- `WebSearch.APIKey`
- MCP HTTP `auth.client_secret` and `auth.token` (the literal-value forms; the `*_env`
  name-reference forms are not secrets and stay)
- `OTel.Headers` (redact each value)

**Pattern (mirrors the existing api_key/env handling):**
- `GetConfig` deep-copies, then replaces each of the above with `redactedSentinel`
  (`***redacted***`).
- A new `restoreSecretScalars(newCfg, currentCfg)` runs in `SaveConfig` alongside the
  existing `restoreSecretEnvs` / `restoreSecretProviderKeys` (`settings.go:174-175`): any
  field whose incoming value is exactly `redactedSentinel` is swapped back to the stored
  value. So GET → edit → PUT never drops a secret the user didn't retype.

### 4.2 N1 — LogBuffer scrubber (`internal/gateway/logs.go`)

Add `scrubSecrets(string) string`, applied inside `LogBuffer.Handle` **before** the record
enters the ring buffer and the SSE fan-out (so both the `/logs` page GET and the
`/logs/stream` SSE are covered; the in-memory buffer never holds a known secret shape).

Compiled-once package-level regexes:

```
Bearer\s+\S+                                          -> "Bearer [REDACTED]"
(?i)(api[_-]?key|token|secret|password)\s*[=:]\s*\S+  -> "${1}=[REDACTED]"
sk-[A-Za-z0-9]{16,}                                   -> "[REDACTED]"        // OpenAI-style
bot\d+:[A-Za-z0-9_-]{30,}                             -> "bot[REDACTED]"     // Telegram
//[^/@\s]+:[^/@\s]+@                                  -> "//[REDACTED]@"     // URL userinfo
```

Applied to the formatted message string and to each attribute value. This is best-effort
defense-in-depth — the canonical fix is not logging secrets — but it closes the leak shapes
the audit named (provider auth errors echoing `Authorization`, MCP stderr, `send_message`
Telegram bot-token URLs). Cost is microseconds on an already level-filtered record;
always-on (no fan-out-only mode, so the buffer itself is clean).

---

## 5. Layer 3 — Content safety for `/files/raw` (G2)

`FilesHandlers.Raw` (`internal/gateway/files.go:145-198`) serves workspace files `inline`
with a sniffed Content-Type and no security headers. An agent-written (or uploaded)
`evil.html`/`evil.svg` thus renders **same-origin** as the gateway — combined with G1/S4 a
classic stored-XSS → secret-theft chain. Changes:

- **Default `Content-Disposition: attachment`.** Serve `inline` only for an explicit
  preview-safe allowlist:
  - `image/png`, `image/jpeg`, `image/gif`, `image/webp` (raster images)
  - `text/plain`
  - `application/pdf`
  - `application/json`
  - **SVG is excluded** — `image/svg+xml` is a script carrier; always `attachment`.
  - The existing `?download=1` continues to force `attachment`.
- **`X-Content-Type-Options: nosniff`** — prevent the browser overriding our Content-Type.
- **`Content-Security-Policy: default-src 'none'; sandbox`** — even if a type renders, no
  script execution, no resource loads, no API calls back to the gateway.

The decision uses the sniffed type (`http.DetectContentType`) checked against the allowlist;
anything not on it downloads. Net effect: `/files/raw` becomes a byte pipe, not a rendering
surface.

---

## 6. Layer 4 — MCP subprocess env isolation (M1)

`ConnectStdio` (`internal/mcp/stdio.go:35`) spawns every stdio MCP server with
`cmd.Env = mergedEnv(os.Environ(), env)` — all of Felix's environment (provider API keys,
`OTEL_*`, anything exported) leaks into arbitrary third-party `npx`/`python` binaries.

- **New field `MCPStdioBlock.InheritEnv bool`** (`internal/config/config.go:100`, json
  `inherit_env,omitempty`), threaded through `ResolveMCPServers` (`config.go:986-988`) into
  the connect call.
- **`ConnectStdio` gains an `inheritEnv bool` parameter.**
  - `false` (default): base env = `minimalBaseEnv()` ++ the server's explicit `env` map.
  - `true`: current behavior — `os.Environ()` ++ env (opt-out escape hatch).
- **`minimalBaseEnv()`** selects a curated allowlist from `os.Environ()`:
  - POSIX: `PATH`, `HOME`, `TMPDIR`, `TEMP`, `TMP`, `LANG`, `LC_*` (prefix), `TZ`.
  - Windows additions (node/python won't start without them): `SystemRoot`, `USERPROFILE`,
    `APPDATA`, `LOCALAPPDATA`, `ProgramData`, `PATHEXT`, `ComSpec`.
  - Everything else — including all secrets — is dropped.

Default flips to safe; a server that genuinely needs the full environment sets
`inherit_env: true` explicitly in its config block.

---

## 7. Layer 5 — Filesystem integrity (S5, S7, N6, S9 perms, L2)

### 7.1 Shared helper

Add **`tool.WriteFileAtomic(path string, data []byte, perm os.FileMode) error`** in a new
`harness/tool/atomic.go` (mirrors Felix's `config.WriteFileAtomic`): write to a temp file in
the same directory, `chmod` to `perm`, `fsync`, then `os.Rename`. Used by the harness file
tools and the spill writer.

### 7.2 S5 — `Config.Save` (felix, `internal/config/config.go:845`) — *narrowed*

- Hold `c.mu.RLock()` across `json.MarshalIndent(c, …)` (currently at `:854`, outside the
  lock → data race with the watcher's `UpdateFrom`).
- Replace `os.WriteFile` with `config.WriteFileAtomic(path, data, 0o600)` (already-correct
  perm preserved; non-atomic write fixed).

> **Caveat — interaction with deferred R3.** An atomic rename swaps the file's inode,
> which can silence an fsnotify watcher that was `Add`-ed on the path (audit R3). R3 is out
> of scope here. This is acceptable because hot-reload-after-Save is not a default-*safety*
> property, but the plan must note that S5 and R3 should be sequenced together when R3 is
> eventually addressed (watch the parent dir, or re-`Add` on rename).

### 7.3 S7 + N6 — harness file tools

- **`write_file`** (`harness/tools/file/writefile.go:75`): `os.WriteFile(..., 0o644)` →
  `tool.WriteFileAtomic(path, data, 0o600)` (new files created restrictive).
- **`edit_file`** (`harness/tools/file/editfile.go:94`): `os.WriteFile(..., 0o644)` →
  `tool.WriteFileAtomic`, **preserving the existing file's mode** via `os.Stat` before the
  write (it edits an existing file; don't silently downgrade/upgrade its perms).
- **S7 fail-closed WorkDir lives on the Felix side** (per decision): harness keeps its
  library default (empty `WorkDir` = unclamped) so other consumers (sidecar, examples) are
  unaffected. Felix's runtime builder (`internal/agent/agent.go`, where the file tools are
  constructed with `a.Workspace`) **errors at build time if `a.Workspace == ""`**, so Felix
  can never ship an unclamped agent. Error: `agent %q has no workspace; refusing to build
  unclamped file tools`.

### 7.4 L2 + S9 (perms only) — permission sweep

- **Spill writer** (`harness/runtime/context.go:436`): `0o644` → `0o600` via
  `tool.WriteFileAtomic` (matches the deliberately-`0o600` session JSONL).
- **`cron-jobs.json`** (felix, `internal/startup/startup.go:232`): `0o644` → `0o600` via
  `config.WriteFileAtomic`.
- **`felix-app.log`** (felix, `cmd/felix-app/main.go:70`): `0o644` → `0o600`. (Rotation
  remains a separate, deferred concern.)
- **`ollama.pid`** (felix, `internal/local/supervisor.go:257`): `0o644` → `0o600`. (The
  reap-trusts-pid-file-path and whitespace-split command-match issues — S9's correctness
  half — are **deferred** to a separate reliability fix.)

---

## 8. Testing strategy (per layer)

- **Layer 1:** table test of `RequireSameOrigin` over the matrix
  {GET, POST} × {no headers, Sec-Fetch-Site: cross-site/same-origin/none, Origin allowed/
  disallowed}. Integration test: cross-origin `POST /settings/api/config` → 403; local
  (no Origin) → 200; cross-origin `GET /logs/stream` → 403. Regression test: `AllowedOrigins`
  WS behavior unchanged after refactor.
- **Layer 2:** per new secret field, round-trip test (GET redacts → PUT with sentinel
  preserves stored value → second GET still redacts). `scrubSecrets` table test, one case
  per pattern + a no-false-positive case (ordinary log line unchanged).
- **Layer 3:** `.html` and `.svg` → `attachment` + `nosniff` + CSP headers present; `.png`
  and `.txt` → `inline`; `?download=1` forces attachment regardless.
- **Layer 4:** `minimalBaseEnv()` excludes a planted `FELIX_SECRET=...` from `os.Environ()`
  and includes `PATH`; with `inherit_env:true` the secret is present. Config round-trips
  `inherit_env`.
- **Layer 5:** `write_file`/`edit_file` produce `0o600` / mode-preserved files via rename;
  `WriteFileAtomic` leaves no partial file on a simulated rename failure; Felix builder
  returns an error for an empty-Workspace agent; spill file is `0o600`.

Run `go test ./...` and `go test -race ./...` in both repos; `go vet ./...` and
`golangci-lint run`.

---

## 9. Summary of files touched

**felix:**
- `internal/gateway/origin.go` (new) — `originAllowed`, `RequireSameOrigin`
- `internal/gateway/auth.go` — `AllowedOrigins` delegates to `originAllowed`
- `internal/gateway/server.go` — guarded route group; move `/admin/restart`, `/logs*` in
- `internal/gateway/settings.go` — extend redaction + `restoreSecretScalars`
- `internal/gateway/logs.go` — `scrubSecrets` in `LogBuffer.Handle`
- `internal/gateway/files.go` — `Raw` attachment/allowlist + security headers
- `internal/config/config.go` — `MCPStdioBlock.InheritEnv`; thread through `ResolveMCPServers`;
  `Config.Save` lock + atomic
- `internal/mcp/stdio.go` — `ConnectStdio(inheritEnv)`, `minimalBaseEnv`
- `internal/agent/agent.go` — fail-closed empty-Workspace guard
- `internal/startup/startup.go` — `cron-jobs.json` 0600
- `internal/local/supervisor.go` — `ollama.pid` 0600
- `cmd/felix-app/main.go` — `felix-app.log` 0600

**harness:**
- `tool/atomic.go` (new) — `WriteFileAtomic`
- `tools/file/writefile.go` — atomic + 0600
- `tools/file/editfile.go` — atomic + mode-preserve
- `runtime/context.go` — spill 0600 via atomic helper

---

## 10. Deferred (explicitly not in this spec)

| Item | Audit ID | Why deferred |
|------|----------|--------------|
| CSRF synchronizer token | G1 alt | Origin-checking sufficient for loopback; YAGNI |
| Local-process auth / auto-token | N1/N7 alt | §1 threat-model decision |
| fsnotify watcher post-rename robustness | R3 | Not a default-safety property; sequence with S5 later |
| Ollama reap-logic correctness | S9 (half) | Reliability, not safety; perm flip only here |
| App-log rotation | S9 | Separate concern |
| web_search SSRF, read_file size cap, etc. | N4, N5, … | Later execution-order steps |
