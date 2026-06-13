# Low-Priority Cleanup Batch Design

**Date:** 2026-06-13
**Status:** Approved
**Catalogue ref:** `optimisation.md` — LOW section (L1–L11) + Gateway/MCP LOW (G6–G9)

## Problem

The optimisation catalogue's substantive tiers (security, performance, reliability,
correctness, P7) are done. What remains is a set of low-severity items tagged
"worth doing opportunistically." This batch sweeps the real ones in a single round
rather than waiting to touch each neighborhood.

## Verification against current code

Every candidate was verified against the current `main` of both repos before
inclusion. Two are already fixed and are **dropped**:

- **L2** (spill files world-readable): already `tool.WriteFileAtomic(path, …, 0o600)`
  in `harness/runtime/context.go:423`. Done in a prior file-permission sweep.
- **L6** (UTF-8-unsafe truncation): done in Round 5.

**G9** is folded into G1/G2 (not a standalone item) — no action.

That leaves **12 items** to fix.

## Scope: 12 fixes across two repos

Wiring note: harness is consumed by felix via a `go.mod replace`. After any
harness change, `cd felix && go build ./...` must pass.

### harness items

**L1 — `tokens.Estimate` ignores images** (`harness/tokens/tokens.go`).
`Estimate` sums `len(m.Content)` etc. but never accounts for `m.Images`
(`[]llm.ImageContent`, defined `llm/provider.go:32`). Image-heavy sessions
under-estimate tokens, delaying preventive compaction.
**Fix:** add a flat per-image estimate (~1500 tokens) for each entry in
`m.Images`. Constant named `perImageTokens`. Test: a message with N images
estimates ≥ N×perImageTokens above the text-only baseline.

**L3 — Qwen never reports usage** (`harness/providers/qwen/qwen.go`).
The request omits `StreamOptions{IncludeUsage: true}`, so usage events never
arrive and the calibrator never learns for Qwen agents. Also uses the
deprecated `MaxTokens` field.
**Fix:** mirror the sibling openai provider exactly (verified
`providers/openai/openai.go:230,232`): set
`MaxCompletionTokens: maxTokens` (replacing the deprecated `MaxTokens`) AND
`StreamOptions: &openai.StreamOptions{IncludeUsage: true}` on the request. Test:
assert the constructed request carries `IncludeUsage: true` (via the existing
provider test harness pattern).

**L4 — Session/calibrator persistence errors are logged-and-dropped**
(`harness/session/store.go`). `AppendEntry` (and siblings) `slog.Error` and
return on write failure; in-memory state then silently diverges from disk, so a
later resume replays truncated history with no signal to the user.
**Fix:** add a one-time (per Store) "degraded persistence" warning — the first
write failure flips an atomic flag and emits a distinct `slog.Warn`
("session persistence degraded; in-memory state may not survive restart").
Subsequent failures stay at Error (or Debug) to avoid spam. Keep the
return-on-error behavior (don't change control flow). Test: inject a failing
write path (unwritable dir) and assert exactly one degraded-persistence warning
across multiple failed appends.

**L5 — `RunSync` swallows aborts** (`harness/runtime/runtime.go:841`).
`RunSync` ranges the event channel handling `EventTextDelta` and `EventError`
but not `EventAborted`, so an aborted run returns partial text with `nil` error.
Cron callers can't distinguish completed from cancelled.
**Fix:** add `case EventAborted:` that returns
`(response.String(), context.Canceled)` (or a sentinel `ErrAborted` if the
package defines one — check; `context.Canceled` is the idiomatic default). Test:
drive a run that emits EventAborted and assert RunSync returns a non-nil error
that `errors.Is(err, context.Canceled)`.

**L8 (skill part):** verified — harness has NO `MatchSkills`/`FormatForPrompt`
(grep across harness returns nothing). The dead skill functions live entirely in
**felix** `internal/skill/skill.go:112,257`. L8 is therefore felix-only (see the
felix L8 item below). No harness action for L8.

**L10 — Retry classifier substring fallback** (`harness/llm/retry.go:54-59`).
`strings.Contains(msg, "429")` / `"529"` can match a request ID or unrelated
digits. **Fix:** tighten the numeric checks to bounded forms —
`"status 429"`, `"code: 429"`, `" 429 "`, and the HTTP status phrase
`"429 too many requests"` (likewise 529). Keep the `"rate limit"` /
`"overloaded"` word matches as-is (those are already specific). Test: a message
containing "429" only as part of a request-id substring (e.g.
`req_abc429def`) must NOT classify as retryable; a real
`"... status 429 ..."` must.

### felix items

**L7 — loose `extractTitle` / `SplitFrontmatter` matching**
(`internal/memory/memory.go:505`, `internal/skill/skill.go:222`).
`extractTitle` does `strings.Index(content, "# ")` which matches `# ` anywhere
(e.g. mid-line, or inside a code block), not just a line-start H1.
`SplitFrontmatter`'s closing-fence detection matches any `---`-prefixed line
(`----`, `---publish:`), not an exact `---` line.
**Fix:** `extractTitle` — only treat `# ` as a title when it's at the start of a
line (offset 0, or preceded by `\n`); scan line-by-line for the first
line that begins with `"# "`. `SplitFrontmatter` — the closing fence must be a
line whose trimmed content is exactly `---`. Tests: title not extracted from a
non-line-start `# `; frontmatter not closed by `----` or `---x`.

**L8 — dead/misleading code** (felix).
- Delete `internal/skill/skill.go` `MatchSkills` (line 112) and `FormatForPrompt`
  (line 257) and their stale doc comments (incl. the line ~282 comment claiming
  bodies are injected via MatchSkills+FormatForPrompt). Confirmed zero callers
  repo-wide; live index path is `FormatIndex`. Remove any now-dead helpers they
  exclusively used (check `matchScore`-type internals; delete only if they become
  unreferenced after the two functions go). Update/remove the associated tests.
- Delete `internal/local/installer.go` `shortDeadline` (line 154) — unused
  (def + comment only; confirmed no callers). **Keep** `bytesReader` /
  `bytesReadCloser` (used 3× at lines 62/94/131).
**Test:** package still builds and existing installer/skill tests pass; remove
tests that exercised the deleted functions.

**L9 — Router match priority not globally enforced**
(`internal/router/router.go:24-62`). The loop returns on the first binding
matching ANY precedence level, so a broad `peer.kind` binding declared before a
specific `peer.id` binding wins incorrectly. (Router is not on a live path today;
fixing per user decision to remove the latent bug.)
**Fix:** evaluate all bindings, track the best (highest-precedence) match seen,
return it after the loop. Precedence high→low: `peer.id` > `peer.kind` >
`accountId` > `channel` > fallback (matches the documented order in CLAUDE.md /
`internal/router` design). Implement by assigning each match a rank and keeping
the max-rank match. Test: a `peer.kind` binding before a `peer.id` binding —
assert the `peer.id` agent wins regardless of declaration order; channel-only
binding loses to any peer/account match.

**L11 — CLI REPL reads stdin one byte per syscall + swallows 2nd SIGTERM**
(`cmd/felix/main.go:602`, `:206-218`).
- The interactive read loop calls `os.Stdin.Read(buf)` per iteration; wrap stdin
  in a `bufio.Reader` for the char-read path (the file already imports `bufio`
  and uses `bufio.NewReader(os.Stdin)` elsewhere at 1288/1469 — reuse that
  pattern for the 602 loop).
- `runStart` registers `signal.Notify(stop, os.Interrupt, syscall.SIGTERM)` then,
  after first signal, a second SIGTERM is swallowed (no force-quit). **Fix:**
  after the first signal initiates graceful shutdown, a second signal should
  force immediate exit (`os.Exit(1)` or stop notifying and re-raise). Implement
  the common pattern: first signal → cancel context + log "shutting down
  (press again to force)"; second signal → `os.Exit(1)`.
**Tests:** L11 is CLI/signal plumbing — hard to unit test hermetically. The
stdin buffering change is verifiable by build + manual reasoning; the
double-signal change should be structured so the signal-handling logic is at
least compile-checked. No fragile signal-timing test required; if a clean unit
test of the buffering helper is feasible, add it, otherwise rely on build +
existing CLI tests.

**G6 — `writeJSON` logs full error on dead conn** (`internal/gateway/websocket.go:1700`).
On a disconnected client, every queued event hits `slog.Error("websocket write
error", …)` — under fan-out this is log spam, not a leak.
**Fix:** downgrade the dead-conn write failure to `slog.Debug`. (A normal client
disconnect is expected, not an error condition.) Test: not easily unit-tested at
the slog level; covered by build + the change being a one-line level swap. If the
existing websocket tests have a hook, assert no Error-level log on a closed conn;
otherwise rely on inspection.

**G7 — Health endpoint hand-builds JSON via `fmt.Fprintf`**
(`internal/gateway/server.go:158`). Harmless today (RFC3339 has no JSON-special
chars) but fragile.
**Fix:** marshal a small struct with `json.NewEncoder(w).Encode(...)` (set
`Content-Type: application/json`). Test: GET /health returns valid JSON with
`status` and `timestamp` fields that round-trip through `json.Unmarshal`.

**G8 — `LoadEnvFile` no quote/escape handling** (`internal/mcp/creds.go:20`).
A real `.env` with quoted values or trailing inline comments is taken literally.
**Fix (per user decision):** support the common `.env` conventions — strip a
single pair of surrounding single or double quotes from the value, and strip a
trailing ` #...` comment ONLY when the `#` is outside quotes and preceded by
whitespace. Leave `export ` prefix handling as-is if already present (check).
Keep it minimal — do NOT implement full shell escaping or variable
interpolation. Tests: `K="v"` → `v`; `K='v'` → `v`; `K=v # comment` → `v`;
`K=a#b` (no preceding space) → `a#b` (not a comment); `K="a # b"` → `a # b`
(comment inside quotes preserved).

## Out of scope

- L2, L6 (already fixed); G9 (folded into G1/G2).
- No behavioral change to control flow in L4 (still returns on error; only adds a
  warning).
- No full shell-grammar parsing in G8 (basic quoting + trailing comment only).
- No new signal-timing integration tests for L11.

## Testing strategy

TDD per item where a hermetic test is feasible (L1, L3, L4, L5, L7, L9, L10, G7,
G8). Items that are inherently hard to unit-test (L11 signals, G6 log level) rely
on build + vet + existing suites + the change being a localized, low-risk swap;
the spec calls these out explicitly so reviewers don't flag a "missing test" as a
gap.

Final gates (both repos): `go build ./...`, `go vet ./...`, `go test ./...`,
`go test -race ./...`; both felix binaries (`cmd/felix`, `cmd/felix-app`) build;
`cd felix && go build ./...` after harness changes.

## Execution note

harness and felix are separate git repos. harness changes land first (own
branch, own commits, verified with felix building against them), then felix
changes. Both merge `--no-ff` and push separately. Commits omit the
Co-Authored-By trailer (project convention).
