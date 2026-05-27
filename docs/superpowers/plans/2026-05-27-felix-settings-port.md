# Felix Settings UI Port Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Felix's `internal/gateway/settings.go` with cloudcat's settings page, minus the Fleet tab and plus Felix's existing Models tab.

**Architecture:** One-file replacement. Backend handlers (`SettingsHandlers`) keep their existing signatures — only the HTML/CSS/JS payload that `Page` serves is replaced. Cloudcat's settings.go is copied in verbatim; the Fleet tab markup + JS block is excised; Felix's existing Models tab markup + JS is grafted in.

**Tech Stack:** Go 1.25, embedded HTML/CSS/JS, finger-tab side-nav layout.

**Spec:** `docs/superpowers/specs/2026-05-27-felix-settings-port-design.md`

---

## File Structure

| Path                                  | Action  | Responsibility                                            |
| ------------------------------------- | ------- | --------------------------------------------------------- |
| `internal/gateway/settings.go`        | Replace | Embedded HTML/CSS/JS for the settings page + Page handler |
| `internal/gateway/settings_test.go`   | Modify  | Update any assertions that reference renamed labels/ids   |

No new files. No other Go packages touched. No config-schema changes. No HTTP-route changes.

---

## Pre-flight context for the implementer

You are porting cloudcat's `internal/gateway/settings.go` (in the sibling repo at `~/projects/cloudcat`) into felix (this repo). The settings page is one Go file that embeds a large HTML string with inline CSS and JS — about 2700 LOC in cloudcat, 2027 LOC in felix today.

**What's already verified by the spec author (do not redo unless something breaks):**

1. `SettingsHandlers` struct is identical in both repos (same field names + signatures).
2. `Config`, `AgentConfig`, `MCPServerConfig`, `ProviderConfig` are line-for-line identical except cloudcat's `AgentConfig` has a trailing `Fleet FleetConfig` block. That field stays in felix's struct but no UI code reads it after this port.
3. CSS variables (`--color-primary`, `--color-bg-soft`, `--color-surface`, `--color-text-muted`, `--color-border`, `--radius`) are already defined in felix's other pages (chat UI ported from cloudcat in v0.8.1).

**Cloudcat's anchors (verified by `wc -l` and `grep -n` runs):**

- Fleet tab button: `internal/gateway/settings.go:774-777`
- Fleet panel div: `internal/gateway/settings.go:788`
- Fleet dispatch in `renderAll`: line 981 (the line `renderFleet();`)
- Fleet JS block (renderFleet + renderFleetStatus): lines 1980-2489

**Felix's Models block to preserve (in current `internal/gateway/settings.go`):**

- Section comment + `CURATED_MODELS` + `pullState`/`pollTimer`/`bootstrapTimer`: lines 627-637
- `ollamaBase`, `fmtBytes`: 639-651
- `renderModels`: 653-733
- `refreshBootstrap`: 734-776
- `refreshInstalled`: 777-814
- `removeInstalledModel`: 815-832
- `applyPullState`: 833-861
- `startPull`: 862-917

That's a 291-line contiguous block (627-917 inclusive). Lift it as one unit.

**Tab list (final order, top-to-bottom):**

| # | Label          | `data-tab` id    | Source            |
| - | -------------- | ---------------- | ----------------- |
| 1 | Agents         | `agents`         | cloudcat          |
| 2 | Providers      | `providers`      | cloudcat          |
| 3 | Models         | `models`         | **Felix** (insert)|
| 4 | Memory         | `intelligence`   | cloudcat          |
| 5 | Security       | `security`       | cloudcat          |
| 6 | Messaging      | `messaging`      | cloudcat          |
| 7 | MCP            | `mcp`            | cloudcat          |
| 8 | OpenTelemetry  | `gateway`        | cloudcat          |
| 9 | Skills         | `skills`         | cloudcat          |

Note the label-vs-id mismatch on row 4 ("Memory" labeled, `intelligence` id) — this is inherited from cloudcat. Do NOT rename the id. Panel JS keys off the id, not the label.

---

## Task 1: Port settings.go

This task is a single atomic port. Multiple steps, one commit at the end. Each step is small enough to verify in isolation before moving on.

**Files:**
- Replace: `~/projects/felix/internal/gateway/settings.go`
- Modify if needed: `~/projects/felix/internal/gateway/settings_test.go`

- [ ] **Step 1: Verify clean working tree**

Run:
```bash
cd ~/projects/felix && git status
```

Expected: only untracked `docs/superpowers/plans/*.md` and `docs/superpowers/specs/*.md` files. No modifications to tracked files. If there are uncommitted edits to anything in `internal/`, STOP and check with the user before proceeding.

- [ ] **Step 2: Baseline tests pass**

Run:
```bash
cd ~/projects/felix && go test -count=1 ./internal/gateway/...
```

Expected: `ok  github.com/sausheong/felix/internal/gateway` and `ok  github.com/sausheong/felix/internal/gateway/runs`. Note this output — you'll want to confirm the same packages stay green at the end.

- [ ] **Step 3: Copy cloudcat settings.go verbatim**

Run:
```bash
cp ~/projects/cloudcat/internal/gateway/settings.go ~/projects/felix/internal/gateway/settings.go
```

This temporarily breaks felix's build (the file still references cloudcat-specific things like `internal/admin/fleet` — actually it doesn't, settings.go is self-contained). Verify:
```bash
cd ~/projects/felix && go build ./internal/gateway/... 2>&1 | head -20
```

If build errors mention `internal/admin`, `internal/fleet`, or `internal/cortex`, report which lines and stop — the spec assumed self-containment and we need to revisit. If errors only mention things like `var declared and not used` or similar local issues from the Fleet code we're about to strip, that's expected — proceed.

- [ ] **Step 4: Strip the Fleet tab button from HTML**

In `~/projects/felix/internal/gateway/settings.go`, locate this block (it was at lines 774-777 in cloudcat, find it by string match — the file is now identical):

```html
				<button class="finger-tab" data-tab="fleet">
					<svg class="ft-icon" viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/></svg>
					Fleet
				</button>
```

Delete those 4 lines.

- [ ] **Step 5: Strip the Fleet panel div**

Locate this exact line:

```html
				<div class="finger-panel" id="panel-fleet"></div>
```

Delete it.

- [ ] **Step 6: Strip the Fleet dispatch call**

Locate this line (was line 981 in cloudcat):

```javascript
		renderFleet();
```

Delete it. There should be only one occurrence inside the dispatch function (which is `renderAll()` or similar — search for `renderFleet()` and you'll find it). If grep shows more than one match outside the upcoming Fleet block deletion, report what you found and stop.

- [ ] **Step 7: Strip the Fleet JS block (renderFleet + renderFleetStatus)**

Locate this comment header in the JS:

```javascript
	// === Fleet tab ===
	// Per-agent toggles for cross-VM fleet messaging. Both ends must enable
	// fleet for the relevant agent. Toggle mutations are picked up by the
	// fleet/* subsystem after a save (or reload).
	function renderFleet() {
```

Delete from that header all the way through the closing `}` of `renderFleetStatus`. The deletion ends where `function refreshSkillList() {` begins. The total span is about 510 lines.

Verify the deletion is clean:
```bash
cd ~/projects/felix && grep -nE "fleet|Fleet" internal/gateway/settings.go
```

Expected matches (anything else is a leftover and needs to be removed):
- Possibly nothing at all, or
- Only matches inside the rendered HTML for unrelated text — none expected since Fleet was an isolated feature.

If grep returns any line referencing `fleet` or `Fleet`, paste those lines and stop.

- [ ] **Step 8: Build after Fleet removal**

Run:
```bash
cd ~/projects/felix && go build ./internal/gateway/... 2>&1 | tail -10
```

Expected: clean build. If errors, they likely come from a stray Fleet reference you missed; grep again and remove.

- [ ] **Step 9: Insert the Models tab button**

In the tab nav (search for `<nav class="finger-tabs"`), find the Providers button:

```html
				<button class="finger-tab" data-tab="providers">
					<svg class="ft-icon" viewBox="0 0 24 24"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v6c0 1.66 4.03 3 9 3s9-1.34 9-3V5M3 11v6c0 1.66 4.03 3 9 3s9-1.34 9-3v-6"/></svg>
					Providers
				</button>
```

Immediately AFTER it, insert the Models button:

```html
				<button class="finger-tab" data-tab="models">
					<svg class="ft-icon" viewBox="0 0 24 24"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.27 6.96 12 12.01 20.73 6.96"/><line x1="12" y1="22.08" x2="12" y2="12"/></svg>
					Models
				</button>
```

- [ ] **Step 10: Insert the Models panel div**

In the `<div class="finger-panels">` block, find:

```html
				<div class="finger-panel" id="panel-providers"></div>
```

Immediately AFTER it, insert:

```html
				<div class="finger-panel" id="panel-models"></div>
```

- [ ] **Step 11: Insert the Models JS block**

Cloudcat has no Models tab — pull the block from felix's pre-port settings.go via git. Because we haven't committed yet, `HEAD` still points at the original felix file:

```bash
cd ~/projects/felix && git show HEAD:internal/gateway/settings.go > /tmp/felix-settings-pre-port.go
sed -n '627,917p' /tmp/felix-settings-pre-port.go > /tmp/felix-models-block.js
wc -l /tmp/felix-models-block.js
```

Expected: 291 lines.

Now in the working `internal/gateway/settings.go`, locate the cloudcat `renderProviders` function. It will be near `	function renderProviders() {`. Find its closing brace, then immediately after, insert the entire contents of `/tmp/felix-models-block.js` as a new section.

The inserted block starts with this comment (already in the file you extracted):

```javascript
	// === Models tab — talks directly to bundled Ollama via providers.local.base_url ===
```

And ends with the closing brace of `startPull`. Do not modify any code inside this block.

- [ ] **Step 12: Add Models to the renderAll dispatch**

Find the dispatch function in the JS (the one that previously contained `renderFleet();`). It's a chain like:

```javascript
	function renderAll() {
		renderAgents();
		renderProviders();
		renderMemory();          // cloudcat's combined panel
		renderSecurity();
		renderMessaging();
		renderMCP();
		renderOpenTelemetry();   // function may also be named renderGateway
		renderSkills();
	}
```

(Exact function names may differ — match what's in the file.) Add a call to `renderModels();` immediately after `renderProviders();`:

```javascript
	function renderAll() {
		renderAgents();
		renderProviders();
		renderModels();          // ← NEW
		renderMemory();
		renderSecurity();
		renderMessaging();
		renderMCP();
		renderOpenTelemetry();
		renderSkills();
	}
```

If cloudcat's code uses a different dispatch shape (e.g., an `activateTab` switch instead of a sequential render call), find the equivalent extension point — wherever `renderProviders` is invoked at load time, add `renderModels` next to it.

- [ ] **Step 13: Build**

Run:
```bash
cd ~/projects/felix && go build ./... 2>&1 | tail -10
```

Expected: clean build, no output (or only the "Shell cwd was reset" notice). If there are errors:
- "undefined: foo" with foo being a Go symbol → cloudcat references something felix doesn't have. Check `internal/gateway/server.go` or `startup` for the missing wiring.
- Syntax errors in the HTML string → an unterminated heredoc or backtick. Pinpoint with `gofmt`.

- [ ] **Step 14: Run gateway tests**

Run:
```bash
cd ~/projects/felix && go test -count=1 ./internal/gateway/... 2>&1 | tail -10
```

Expected: both `internal/gateway` and `internal/gateway/runs` packages green.

If a test fails with a substring assertion against an old tab label or panel id, update the assertion to the new one. The label changes that may need a test update:
- "Gateway" → "OpenTelemetry"
- "Intelligence" panel → may or may not be referenced

Show the failing test output, identify the assertion, update it, and re-run.

- [ ] **Step 15: go vet**

Run:
```bash
cd ~/projects/felix && go vet ./... 2>&1 | tail -5
```

Expected: no output.

- [ ] **Step 16: Search for leftover Fleet references one more time**

Run:
```bash
cd ~/projects/felix && grep -rn "fleet\|Fleet" internal/gateway/settings.go
```

Expected: zero matches. If any match, decide whether it's substantive (e.g., the word "fleet" inside a string somewhere) or unrelated. Substantive matches mean Step 7 missed something — go back and clean up.

- [ ] **Step 17: Visual smoke test — start the server**

In one terminal:
```bash
cd ~/projects/felix && go run ./cmd/felix start
```

(Felix's default port is 18789.)

- [ ] **Step 18: Visual smoke test — load each tab**

Open `http://localhost:18789/settings` in a browser. Walk through every tab in the side-nav:

1. Click **Agents** — verify the agent list renders with the cards/edit buttons.
2. Click **Providers** — verify the provider rows render.
3. Click **Models** — verify the "Local models" heading + installed list + curated download cards render. (The installed list may say "Loading…" if Ollama isn't running; that's fine.)
4. Click **Memory** — verify both memory entry list AND cortex toggle/threshold fields are visible. This panel covers what used to be two tabs (Intelligence + Memory).
5. Click **Security** — verify tool allow/deny grids render.
6. Click **Messaging** — verify Telegram + web search blocks.
7. Click **MCP** — verify server list renders (may be empty).
8. Click **OpenTelemetry** — verify endpoint/headers/sample-rate inputs render.
9. Click **Skills** — verify upload button + skills list (may be empty or list bundled skills).

Stop the server (Ctrl+C). If any panel renders blank or errors in the browser console, capture the error and pause to triage.

- [ ] **Step 19: Visual smoke test — save round-trip**

Restart the server (`go run ./cmd/felix start`), open `/settings`, change one field (e.g., toggle a setting on Messaging), click Save, reload the page, confirm the change persisted. Stop the server.

- [ ] **Step 20: Stage and commit**

Run:
```bash
cd ~/projects/felix && git add internal/gateway/settings.go internal/gateway/settings_test.go
git commit -m "feat(gateway): port cloudcat settings UI to Felix

Replaces internal/gateway/settings.go with cloudcat's settings page,
adapted for Felix:

- Side-nav layout (200px vertical column at >=720px, top-tabs fallback
  at <720px), inline SVG icon per tab — visual lift from cloudcat.
- Cloudcat's combined Memory panel (Memory entries + Cortex config)
  replaces felix's separate Intelligence and Memory tabs.
- 'Gateway' tab renamed to 'OpenTelemetry' (data-tab id stays 'gateway'
  so panel-routing JS doesn't change).
- Fleet tab dropped entirely (Felix has no fleet subsystem; the
  AgentConfig.Fleet struct field is left unread).
- Felix's Models tab preserved verbatim: same CURATED_MODELS catalog,
  same Ollama pull/install/remove flow, inserted between Providers and
  Memory in the new nav.

Backend handlers (SettingsHandlers shape, /settings/api/* routes) are
unchanged. Config schema is unchanged."
```

Note: if `settings_test.go` did not need changes, drop it from the `git add` line.

---

## Self-review checklist (run by the human/coordinator after the implementer reports done)

1. `go build ./...` clean.
2. `go test -count=1 ./internal/gateway/...` green.
3. `go vet ./...` quiet.
4. `grep -n "fleet\|Fleet" internal/gateway/settings.go` returns nothing.
5. `/settings` loads in a browser and all 9 tabs render without console errors.
6. Save round-trip persists to `~/.felix/felix.json5`.
7. One commit, message matches the template above.

---

## Out of scope (DO NOT do as part of this task)

- Removing the now-orphan `AgentConfig.Fleet` field from `internal/config/config.go`. Separate cleanup.
- Refactoring the Models tab's inline styles into proper CSS classes. Follow-up.
- Adding new Playwright smoke tests. Not required.
- Touching any file outside `internal/gateway/`.
- Bumping the v0.8.2 release. Separate request.

---

## If a step blocks

- **"Settings page won't load at all"** → revert with `git checkout HEAD -- internal/gateway/settings.go` and report the error.
- **"Build fails on a name from internal/admin or internal/fleet"** → cloudcat's settings.go was less self-contained than the spec assumed. Show the failing import and stop — needs revisit.
- **"renderAll dispatch shape doesn't match the plan"** → don't guess; show what's there and ask.
- **"Schema mismatch — cloudcat panel writes a field felix doesn't have"** → spec was wrong; show the mismatch and stop.
