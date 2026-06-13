# Security Deeper — Design

**Date:** 2026-06-13
**Status:** Design (pending implementation plan)
**Scope:** Execution-order step 2 from `optimisation.md` — security hardening, deeper tier.
Nine findings: S2, N4, S3 (harness SSRF); S6 (felix config parser); S10, G3, N2, G4
(felix gateway surface); N5 (harness file size cap).
**Repos touched:** `harness` and `felix` (wired via `go.mod replace`).

---

## 1. Goal & constraint

Round 1 took the security fast-wins (CSRF/Origin middleware, `/logs` auth+redact,
`/admin/restart` gate, `/files/raw` hardening, bash newline bypass, settings-secret
redaction, atomic config Save, file-write perms). This round takes the **deeper** security
tier that was skipped when rounds 2–3 jumped to reliability and performance.

The remaining exposure:
- **SSRF is TOCTOU** — `web_fetch`/`web_search`/`browser` validate a hostname, then a second,
  independent DNS resolution at connection time can return a private IP (DNS rebinding). The
  guard is defeatable. `web_search` skips the guard entirely.
- **The JSON5 config parser** silently corrupts valid configs (comma-in-string) and
  hard-fails startup on legitimate configs (a `//` inside a URL).
- **The gateway surface** accepts bearer tokens in URL query strings, has no global
  connection/run/SSE-subscriber caps, and runs manual compaction on an uncancellable context.
- **`read_file`/`edit_file`** read whole files into memory with no size cap.

**Constraint — this round intentionally changes behavior.** Unlike the performance round
(zero-observable-change), a security fix changes behavior by design. The bar is instead: **no
legitimate use breaks.** Every existing valid config must still parse (S6); every public URL
must still fetch (S2/N4/S3); every normal-size file must still read (N5). Each finding ships
paired tests: the attack is blocked **and** the happy path is unchanged. All work validated
under `go test -race` with existing suites kept green.

All nine findings were re-verified against `main` on 2026-06-13 (post rounds 1–3). Two notes
from that verification:
- **S5** (atomic, locked `Config.Save`) is already shipped — `config.go:846-863` uses
  `WriteFileAtomic` under `c.mu.RLock()`. Not in this round.
- **G4** narrowed: `chat.send` already derives `runCtx` from `deps.ServerCtx`
  (`chatexec.go:215-219`), so only the `handleChatCompact` call site (`websocket.go:961`) still
  passes `context.Background()`.

---

## 2. Architecture: four groups, one spec

| Group | Findings | Repo | Risk |
|-------|----------|------|------|
| 1. SSRF hardening | S2, N4, S3 | harness | medium (shared transport + Chrome flag) |
| 2. Config parser | S6 | felix | **high blast radius** (all config loads through it) |
| 3. Gateway surface caps | S10, G3, N2, G4 | felix | low–medium (auth / limits / ctx) |
| 4. File size cap | N5 | harness | low (additive guard) |

Findings within a group share files: G1's three live in `harness/tools/web` +
`harness/tools/browser` (S2/N4 share one new transport; S3 is a sibling Chrome flag); G3's four
are independent gateway edits. Grouping avoids touching the same file twice.

**Decisions locked during brainstorming:**
- **S3:** `--host-resolver-rules` Chrome flag (real mitigation covering in-page fetch +
  evaluate), not a proxy (deferred) and not document-only.
- **S2/N4:** one shared `SafeHTTPClient` in `harness/tools/web`, not inline per-tool.
- **Round size:** one coordinated round, all nine.
- **N5:** hard cap + clear error, ~10 MB text / ~5 MB image, no config field.
- **S6:** hand-written single-pass state machine, no new dependency.

---

## 3. Group 1 — SSRF hardening (S2, N4, S3)

### 3.1 S2 — pin the validated IP in the HTTP transport

**Where:** `harness/tools/web/ssrf.go:46-76` (`ValidateURLNotInternal` resolves + checks but
does not pin); `harness/tools/web/webfetch.go:88-100` (the `&http.Client{...}` whose default
transport re-resolves the hostname independently — the rebinding window; the redirect
`CheckRedirect` at `:95` also re-validates a string then re-resolves).

**Problem:** validation calls `net.LookupIP(host)` and checks the IPs, but `http.Client.Do`
performs its **own** DNS resolution when dialing. An attacker-controlled name can return a
public IP to the validation lookup and a private IP (`169.254.169.254`, `127.0.0.1`) to the
real dial — classic DNS rebinding. Even the redirect re-validation has the gap: it validates
the redirect URL string, then the client re-resolves.

**Fix — new file `harness/tools/web/client.go`:**

```go
package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// SafeHTTPClient returns an *http.Client whose transport resolves and validates
// the destination host, then dials the exact validated IP — closing the
// TOCTOU/DNS-rebinding window where http.Client would otherwise re-resolve the
// hostname independently of ValidateURLNotInternal. Redirects are re-validated.
func SafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("cannot resolve %q — blocking to prevent SSRF", host)
			}
			var dialIP net.IP
			for _, ip := range ips {
				if isPrivateIP(ip) {
					return nil, fmt.Errorf("access to internal address %s (%s) is blocked", host, ip)
				}
				if dialIP == nil {
					dialIP = ip
				}
			}
			if dialIP == nil {
				return nil, fmt.Errorf("no usable address for %q", host)
			}
			// Dial the exact validated IP — no second uncontrolled resolution.
			return dialer.DialContext(ctx, network, net.JoinHostPort(dialIP.String(), port))
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects (max 10)")
			}
			if err := ValidateURLNotInternal(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
}
```

In `webfetch.go`, replace the inline `client := &http.Client{...}` (`:88-100`) with
`client := SafeHTTPClient(fetchTimeout)`. Keep the pre-flight `ValidateURLNotInternal(in.URL)`
at `:70` (fast reject before dialing). `maxFetchSize` / `MaxOutputLength` caps unchanged.

> Note: the DialContext validates on **every** dial including redirect targets, so a redirect
> to a rebinding host is also caught at dial time, not only by `CheckRedirect`'s string check.
> The two layers are complementary.

### 3.2 N4 — route web_search through the safe client

**Where:** `harness/tools/web/websearch.go:125-150` (`duckDuckGoSearch` uses
`http.DefaultClient` at `:134`, no `ValidateURLNotInternal`, no redirect policy); backend
abstraction at `:22-27,90-97`.

**Problem:** `web_fetch` and `browser` go through `ValidateURLNotInternal`, but `web_search`
does not validate the search endpoint or follow a safe redirect policy. The DDG path uses
`http.DefaultClient`, so it follows redirects with no re-validation. A configured backend base
URL (writable via the settings API) pointing at an internal address is fetched with no guard.

**Fix:**
1. `duckDuckGoSearch` uses a package-level safe client instead of `http.DefaultClient`. Because
   the existing function builds the request inline, give the DDG backend a client field:
   construct it with `SafeHTTPClient(searchTimeout)` in `newDDGBackend()` and use
   `b.client.Do(req)`. (The 1 MiB `io.LimitReader` body cap at `:144` stays.)
2. The `WebSearchBackend` interface gains no signature change, but any backend that makes HTTP
   calls is constructed with the shared safe client. Document on the `WebSearchBackend`
   interface that backends MUST validate outbound URLs (use `SafeHTTPClient`) — the contract
   that prevents a future backend from reintroducing the gap.

> Result URLs are returned to the model as text (not auto-fetched), so the direct SSRF surface
> is the search endpoint, which this closes. This brings `web_search` to parity with
> `web_fetch`.

### 3.3 S3 — block private-IP resolution at the browser

**Where:** `harness/tools/browser/browser.go:246-254` (only top-level `url` is SSRF-checked);
`:756-779` (`evaluate` runs arbitrary JS); `:463-502` (`launchBrowser` allocator options — no
network policy).

**Problem:** only the navigation `url` is validated. `evaluate` runs arbitrary JS in the page,
and any loaded page can `fetch('http://169.254.169.254/…')`. The headless Chrome has no network
policy restricting it to public IPs. The Go-side check can never reach in-page or `evaluate`'d
requests.

**Fix — add `host-resolver-rules` to the `chromedp.NewExecAllocator` options in
`launchBrowser` (`:470-479`):**

```go
chromedp.Flag("host-resolver-rules",
	"MAP localhost ~NOTFOUND, "+
	"MAP *.localhost ~NOTFOUND, "+
	"MAP 127.0.0.0/8 ~NOTFOUND, "+
	"MAP 10.0.0.0/8 ~NOTFOUND, "+
	"MAP 172.16.0.0/12 ~NOTFOUND, "+
	"MAP 169.254.0.0/16 ~NOTFOUND, "+
	"MAP 192.168.0.0/16 ~NOTFOUND, "+
	"MAP [::1] ~NOTFOUND"),
```

This makes Chrome itself refuse to resolve private ranges — covering top-level navigation,
sub-resources, in-page `fetch`, and `evaluate`'d JS, none of which the Go check reaches. The
top-level `ValidateURLNotInternal` at `:251` stays as a fast pre-launch reject with a clearer
error.

> **Implementation must verify the exact Chrome syntax.** `~NOTFOUND` is the documented Chrome
> host-resolver sentinel for "fail resolution." During implementation, verify against the
> installed Chrome (the syntax is `MAP <pattern> <replacement>`; the replacement `~NOTFOUND`
> forces resolution failure). If the installed Chrome rejects CIDR patterns in
> `host-resolver-rules`, fall back to the documented form and note it. Add a `//go:build live`
> test asserting an in-page `fetch('http://169.254.169.254/')` from inside an evaluate call
> fails.

**Documented residual (limitations section):** `host-resolver-rules` maps by hostname/CIDR
pattern; a page that dials a **literal** private IP in a URL may bypass name-based rules in some
Chrome builds. The complete fix is an allowlisting proxy (deferred — its own round). The
top-level Go-side `ValidateURLNotInternal` remains the belt for the navigation URL. This round
materially reduces the surface (in-page name-based SSRF, the common metadata-endpoint vector)
without claiming completeness.

### 3.4 Group 1 testing

- **S2/N4 (`client_test.go`):** table test with a controllable resolver/test server asserting
  (a) a public URL succeeds, (b) a host resolving to a private IP is rejected **at dial**,
  (c) a redirect to a private IP is blocked. Use a custom `net.Resolver` or a test that points
  a hostname at `127.0.0.1` and asserts the dial is refused.
- **S2 (`webfetch_test.go`):** existing web_fetch tests stay green; add a test that a
  private-IP host is blocked end-to-end via the tool.
- **N4 (`websearch_test.go`):** DDG backend constructed with the safe client; a backend pointed
  at a private IP is blocked.
- **S3 (`browser_test.go`):** unit test asserting the `host-resolver-rules` flag is present in
  the allocator option list; `//go:build live` test that in-page private-IP fetch fails.

---

## 4. Group 2 — JSON5 parser (S6)

**Where:** `internal/config/config.go:865-923` (`stripJSON5`, `inString`,
`removeTrailingCommas`).

**Problem — two confirmed bugs in the line-based stripper:**
1. `removeTrailingCommas` (`:905-923`) has no string context — a comma inside a string value
   before whitespace then `}`/`]` (e.g. a cron prompt `"do X, "`) is silently deleted,
   corrupting user data.
2. Inline-comment stripping (`:876-883`) inspects only the **first** `//` on a line. For
   `"base_url": "http://x/v1", // note`, the `//` inside the URL is found first, `inString`
   returns true, the real trailing comment is never stripped → `json.Unmarshal` fails → the
   whole gateway refuses to start, with no pointer to the offending line. `inString` also
   mishandles `\\"`.

**Fix — replace all three functions with a single-pass rune state machine.**

```go
// stripJSON5 converts JSON5-lite (// line comments, /* block */ comments, and
// trailing commas before } or ]) into standard JSON. It walks the input once,
// tracking string/escape/comment state so commas and comment markers INSIDE
// string literals are never misread. This replaces the prior line-based
// stripper, which corrupted strings containing ", " and failed on URLs
// containing "//".
func stripJSON5(s string) string {
	const (
		stDefault = iota
		stString
		stEscape
		stLineComment
		stBlockComment
	)
	var b strings.Builder
	b.Grow(len(s))
	runes := []rune(s)
	state := stDefault
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch state {
		case stDefault:
			switch c {
			case '"':
				b.WriteRune(c)
				state = stString
			case '/':
				if i+1 < len(runes) && runes[i+1] == '/' {
					state = stLineComment
					i++ // consume second '/'
				} else if i+1 < len(runes) && runes[i+1] == '*' {
					state = stBlockComment
					i++ // consume '*'
				} else {
					b.WriteRune(c)
				}
			case ',':
				// Look ahead past whitespace/comments for } or ]; drop if found.
				if nextSignificantIsCloser(runes, i+1) {
					// skip the comma (trailing comma)
				} else {
					b.WriteRune(c)
				}
			default:
				b.WriteRune(c)
			}
		case stString:
			b.WriteRune(c)
			if c == '\\' {
				state = stEscape
			} else if c == '"' {
				state = stDefault
			}
		case stEscape:
			b.WriteRune(c) // emit the escaped char verbatim
			state = stString
		case stLineComment:
			if c == '\n' {
				b.WriteRune(c)
				state = stDefault
			}
		case stBlockComment:
			if c == '*' && i+1 < len(runes) && runes[i+1] == '/' {
				i++ // consume '/'
				state = stDefault
			}
		}
	}
	return b.String()
}

// nextSignificantIsCloser reports whether the next non-whitespace,
// non-comment rune starting at index j is } or ] (i.e. the comma at j-1 is a
// trailing comma). It skips whitespace and // / /* comments so a comma
// followed by a comment then a closer is still recognized as trailing.
func nextSignificantIsCloser(runes []rune, j int) bool {
	for j < len(runes) {
		c := runes[j]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			j++
		case c == '/' && j+1 < len(runes) && runes[j+1] == '/':
			for j < len(runes) && runes[j] != '\n' {
				j++
			}
		case c == '/' && j+1 < len(runes) && runes[j+1] == '*':
			j += 2
			for j+1 < len(runes) && !(runes[j] == '*' && runes[j+1] == '/') {
				j++
			}
			j += 2
		case c == '}' || c == ']':
			return true
		default:
			return false
		}
	}
	return false
}
```

`inString` and `removeTrailingCommas` are deleted. The trailing-comma decision folds into the
single pass via `nextSignificantIsCloser`. Block comments (`/* */`) are newly supported — a
strict superset of today's behavior, so nothing that parses now breaks.

**Testing (`config_test.go` — the most adversarial coverage in the round):**
- **Golden corpus:** the bundled/default `felix.json5` and a hand-built config exercising every
  field type strip to JSON that `json.Unmarshal`s into the same `Config` as today's stripper
  produces (for inputs today handles correctly). Assert via deep-equal on the parsed struct.
- **Bug repros:** `"prompt": "say hi, "` before `}` preserves the comma (string survives);
  `"base_url": "http://x/v1", // note` strips the comment and keeps the full URL.
- **Adversarial:** escaped quote `\"`, escaped backslash `\\"`, `//` inside a string, `/*`
  inside a string, `://` in URLs, a block comment, a trailing comma at EOF, an empty input, a
  comment-only file, a comma-then-comment-then-closer.
- **Round-trip:** `json.Valid(stripJSON5(corpus))` is true for every corpus entry.

---

## 5. Group 3 — Gateway surface caps (S10, G3, N2, G4)

### 5.1 S10 — drop query-param bearer auth

**Where:** `internal/gateway/auth.go:27-30`.

**Problem:** `auth = "Bearer " + r.URL.Query().Get("token")` honours `?token=`, which lands in
browser history and proxy/intermediary logs.

**Fix:**
1. Remove the query-param fallback in `BearerAuthMiddleware`. HTTP callers use the
   `Authorization` header (already supported).
2. For browser WebSocket clients that can't set headers, accept the token via the
   `Sec-WebSocket-Protocol` subprotocol: a client sends subprotocol `Bearer.<token>`; the
   upgrade path extracts and validates it (constant-time compare, same as the header path) and
   echoes the selected subprotocol on accept.
3. **Verify during impl:** confirm no built-in client (Settings UI, bundled WS client) relies
   on `?token=`. If one does, switch it to the subprotocol in the same change so nothing breaks.

**Testing:** request authenticating only via `?token=` is rejected; `Authorization: Bearer`
still works; WS subprotocol `Bearer.<token>` authenticates; bad subprotocol token rejected.

### 5.2 G3 — connection + concurrent-run caps

**Where:** `internal/gateway/websocket.go:259-291` (per-`Handle` token bucket, resets each
reconnect), `:439-448` (`chat.send` spawns an unbounded goroutine running a full agent turn).

**Problem:** the 30 msg/s bucket is per-connection, so a client that reconnects (or opens many
connections) gets a fresh bucket each time, and there is no cap on concurrent connections or on
`chat.send`-spawned runs. An unauthenticated local caller (default config) can fan out unbounded
LLM runs.

**Fix:**
1. **Connection cap:** an `atomic.Int64` on the handler incremented at `Handle` entry,
   decremented on exit (deferred). Beyond `maxConnections` (default 64), reject before the
   read loop (close with a policy message). The per-conn 30/s bucket stays.
2. **Concurrent-run semaphore:** a buffered channel `runSem chan struct{}` (default cap 8) on
   the handler. `handleChatSend` does a non-blocking `select` acquire before spawning the
   `RunTurn` goroutine; on failure, return a clear "server busy, retry" RPC error rather than
   spawning. Release in the goroutine's defer. (Defaults are constants this round, not config.)

**Testing:** connection #65 is rejected; with 8 runs in flight, run #9 gets "busy"; releasing a
run admits the next.

### 5.3 N2 — SSE subscriber cap + fan-out off the lock

**Where:** `internal/gateway/logs.go:81-117` (`Handle` fans out under `s.mu`), `:150-157`
(`Subscribe`, no cap).

**Problem:** every `/logs/stream` GET allocates a 64-entry channel registered in `s.subs` with
no maximum, and `Handle` iterates + sends to every subscriber **under `s.mu`** on every log
record, so a large subscriber set slows every `slog` call process-wide.

**Fix:**
1. **Cap:** `Subscribe` returns `nil` beyond `maxSSESubscribers` (default 16);
   `NewLogsStreamHandler` checks for `nil`, writes a "too many log subscribers" SSE comment, and
   returns. Prevents goroutine/channel exhaustion.
2. **Fan-out off `s.mu`:** in `Handle`, snapshot the subscriber channels into a local slice
   under the lock, release `s.mu`, then do the non-blocking `select { case ch <- entry: default: }`
   sends. The ring-buffer write stays under the lock; only the fan-out moves out.

> Ordering note: the ring-buffer append and the snapshot happen under the lock, so a subscriber
> added concurrently either appears in this record's snapshot or the next — no lost-update on
> the buffer itself. Sends are best-effort (non-blocking) as today.

**Testing:** subscriber #17 is refused; a unit test asserts a full/slow subscriber channel does
not block a concurrent `Handle` (fan-out is not under `s.mu`) — e.g. register a subscriber whose
channel is never drained and assert `Handle` returns promptly.

### 5.4 G4 — cancellable manual compaction

**Where:** `internal/gateway/websocket.go:961` (`handleChatCompact` passes
`context.Background()` to `MaybeCompact`).

**Problem:** manual compaction can't be aborted and won't unwind on gateway shutdown — a slow
summarizer LLM call blocks indefinitely.

**Fix:** make the context cancellable by tying it to the handler's `serverCtx` so shutdown and
abort unwind the call. The summarizer **already self-bounds** with its own per-call deadline
(`harness/compaction/summarizer.go:98-102`, `compaction.go:218` — `Summarizer.Timeout`, default
60s), so G4 does **not** need to invent a new timeout constant — the goal is cancellability, not
a deadline.

```go
ctx := h.serverCtx
if ctx == nil {
	ctx = context.Background() // serverCtx is nil-able (websocket.go:62)
}
res, err := mgr.MaybeCompact(ctx, sess, compaction.ReasonManual, params.Instructions)
```

The nil-guard matches the documented `serverCtx` contract ("nil → falls back to
context.Background"). The `chat.send` site at `:440` is **not** changed — `RunTurn` already
derives `runCtx` from `deps.ServerCtx` (`chatexec.go:215-219`).

**Testing:** cancelling `h.serverCtx` (or a derived test ctx) aborts an in-flight manual
compaction via a fake summarizer that blocks until ctx is done.

---

## 6. Group 4 — File size cap (N5)

**Where:** `harness/tools/file/readfile.go:96` (unbounded `os.ReadFile`, ctx ignored —
`Execute(_ context.Context, …)`); `harness/tools/file/editfile.go:77` (same unbounded
`ReadFile`).

**Problem:** a multi-hundred-MB file is read fully into RAM; images base64-expand (+33%) into
the LLM request, blowing the context window or OOMing under concurrent reads (`read_file` is
`IsConcurrencySafe`, so several run in parallel). The workspace clamp prevents escaping the
workspace but not size.

**Fix — stat-then-guard with a clear error, plus a LimitReader belt:**

```go
const (
	maxTextFileSize  = 10 * 1024 * 1024 // 10 MB
	maxImageFileSize = 5 * 1024 * 1024  // 5 MB
)
```

In `read_file.Execute`, after path validation and before reading:
- `fi, err := os.Stat(path)` — on error, fall through to the existing read-error path.
- choose `limit` = `maxImageFileSize` if the extension is an image, else `maxTextFileSize`.
- if `fi.Size() > limit`, return
  `ToolResult{Error: fmt.Sprintf("file too large: %d bytes exceeds the %d byte limit for %s files; read a specific range or use a different tool", fi.Size(), limit, kind)}`.
- read via `io.ReadAll(io.LimitReader(f, limit+1))` (open the file, defer close) so a file that
  grows between stat and read still can't exceed the cap. (If the LimitReader yields exactly
  `limit+1` bytes, treat as oversized — the stat check is the fast path, this is the guarantee.)

`edit_file` gets the same stat-check before its `ReadFile` (text limit). An oversized file
can't be edited in one shot anyway.

> **No config field** (locked decision) — hardcoded constants, generous enough that normal use
> (`~/.felix/` configs, source files, screenshots) is unaffected. A configurable cap is a
> future decision, not this round.

**Testing:** a file at `limit-1` reads fine; a file at `limit+1` returns the size error (not a
partial read); an image over the image cap is refused; the error names the byte limit;
`edit_file` over the cap refuses before matching.

---

## 7. Testing strategy (cross-cutting)

- **Paired tests per finding:** attack blocked **and** happy path unchanged.
- **Full suites green** on both repos under `go test -race`; after each harness change,
  `cd felix && go build ./...` (go.mod replace).
- **No-legitimate-use-breaks bar:** S6 golden corpus (existing configs parse identically),
  S2/N4 public URLs still fetch, N5 normal files still read, S10 header auth still works.
- **Final adversarial review subagent** after all tasks (round 3 found 3 real regressions this
  way; security changes carry the same false-green risk).

---

## 8. Files touched

**harness:**
- `tools/web/client.go` — **new**: `SafeHTTPClient` / safe transport (S2/N4).
- `tools/web/webfetch.go` — use `SafeHTTPClient` (S2).
- `tools/web/websearch.go` — DDG backend uses `SafeHTTPClient`; document backend URL-validation
  contract (N4).
- `tools/browser/browser.go` — `host-resolver-rules` flag in `launchBrowser` (S3).
- `tools/file/readfile.go` — size cap + stat/LimitReader (N5).
- `tools/file/editfile.go` — size cap (N5).
- New tests: `tools/web/client_test.go`, `tools/web/websearch_test.go` (N4),
  `tools/browser/browser_test.go` (S3), `tools/file/readfile_test.go` /`editfile_test.go` (N5).

**felix:**
- `internal/config/config.go` — state-machine `stripJSON5`, delete `inString` +
  `removeTrailingCommas`, add `nextSignificantIsCloser` (S6).
- `internal/gateway/auth.go` — drop query-param auth (S10).
- `internal/gateway/websocket.go` — connection cap + run semaphore (G3); WS subprotocol auth
  (S10); cancellable manual compaction (G4).
- `internal/gateway/logs.go` — SSE subscriber cap + fan-out off `s.mu` (N2).
- New/updated tests: `internal/config/config_test.go` (S6), `internal/gateway/auth_test.go`
  (S10), `internal/gateway/websocket_test.go` (G3, G4), `internal/gateway/logs_test.go` (N2).

---

## 9. Deferred (explicitly not in this round)

| Item | Why |
|------|-----|
| Allowlisting proxy for complete browser SSRF | S3 residual (literal private-IP in-page requests); its own round. |
| Configurable file-size / connection / subscriber / run limits | Hardcoded constants this round; config surface is a separate decision. |
| Per-session store mutex (P5 extension) | Out of scope; global store mutex retained. |
| Remaining correctness tier (R7–R10, N9–N12, L-series) | Later execution-order steps. |
| M1 (MCP subprocess env minimization) | Already shipped — `internal/mcp/stdio.go:37` uses minimal base env with opt-in `inheritEnv`. Verified, not re-done. |
