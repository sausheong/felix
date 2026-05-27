# Port cloudcat settings UI to Felix

**Status:** design
**Date:** 2026-05-27

## Goal

Replace `internal/gateway/settings.go` in Felix with cloudcat's settings page, minus the Fleet tab and plus Felix's existing Models tab. Felix users get the side-nav layout, tab icons, polished CSS, and the panel refinements cloudcat has made since Felix's settings.go forked.

## Scope

**In:**

- New tab chrome: vertical side-nav (200px column at ≥720px), top-tabs fallback at <720px, inline-SVG icon per tab.
- All non-Fleet panel JS lifted from cloudcat: `renderAgents`, `renderProviders`, `renderMemory` (cloudcat's combined Intelligence+Memory panel), `renderSecurity`, `renderMessaging`, `renderMCP`, `renderOpenTelemetry`, `renderSkills`.
- Felix's existing `renderModels` panel and its supporting JS (`ollamaBase`, `fmtBytes`, `CURATED_MODELS`, `startPull`, `pollPullStatus`) preserved and re-mounted inside the new chrome.
- Shared JS lifted verbatim from cloudcat: `loadConfig`, `saveConfig`, `showStatus`, theme toggle, hamburger menu, loading-state plumbing.
- Tab label change: the tab currently labeled "Gateway" in Felix (id `gateway`) gets cloudcat's label "OpenTelemetry" — same id, accurate name.

**Out:**

- Fleet tab — removed entirely (Felix has no fleet subsystem; per-agent `AgentConfig.Fleet` field is left unread by the UI but stays in the Go struct so the schema is unchanged).
- Backend handler changes — `SettingsHandlers` keeps the existing `Page/GetConfig/SaveConfig/ListTools/BootstrapStatus` signatures. Both Felix and cloudcat already share this shape.
- Config schema changes — verified that Felix's `Config`, `AgentConfig`, `MCPServerConfig`, `ProviderConfig` are line-for-line identical to cloudcat's (except the trailing per-agent `Fleet` block, which the dropped tab is the only consumer of).
- Splitting Felix's combined Memory tab into Intelligence + Memory — the new combined panel from cloudcat handles both Memory (markdown entries + embedder) and Cortex (knowledge graph + extractor + recall threshold) settings.

## Final tab list (top-to-bottom in side-nav)

Display label is what the user sees; `data-tab` is the DOM id used by `panel-<id>` and the active-tab switch logic. The mismatch between label and id ("Memory" labeled, `intelligence` id) is inherited from cloudcat and preserved verbatim so panel JS doesn't need rewriting.

| # | Label          | `data-tab` id    | Source       |
| - | -------------- | ---------------- | ------------ |
| 1 | Agents         | `agents`         | cloudcat     |
| 2 | Providers      | `providers`      | cloudcat     |
| 3 | Models         | `models`         | **Felix**    |
| 4 | Memory         | `intelligence`   | cloudcat     |
| 5 | Security       | `security`       | cloudcat     |
| 6 | Messaging      | `messaging`      | cloudcat     |
| 7 | MCP            | `mcp`            | cloudcat     |
| 8 | OpenTelemetry  | `gateway`        | cloudcat     |
| 9 | Skills         | `skills`         | cloudcat     |

Models is inserted after Providers because it configures the local provider's backing endpoint. The Memory panel is cloudcat's combined Memory+Cortex view; it replaces Felix's separate Intelligence and Memory tabs.

## Architecture

```
GET /settings
  └─► SettingsHandlers.Page (existing handler, new HTML payload)
       └─► <html> with cloudcat chrome + 9 finger-panel divs
            └─► <script> loadConfig() → GET /settings/api/config
                 ├─► renderAgents(cfg.agents)        ← cloudcat
                 ├─► renderProviders(cfg.providers)  ← cloudcat
                 ├─► renderModels(cfg.providers.local) ← Felix
                 ├─► renderMemory(cfg.memory + cfg.cortex) ← cloudcat
                 ├─► renderSecurity(cfg.security)    ← cloudcat
                 ├─► renderMessaging(cfg.telegram + cfg.web_search) ← cloudcat
                 ├─► renderMCP(cfg.mcp_servers)      ← cloudcat
                 ├─► renderOpenTelemetry(cfg.otel)   ← cloudcat
                 └─► renderSkills() → GET /settings/api/skills (existing)
            └─► saveConfig() → POST /settings/api/config
```

No new HTTP routes. No new handler types. The backend contract is unchanged; only the HTML/CSS/JS payload `Page` returns is replaced.

## Components and contracts

### `SettingsHandlers` (unchanged)

```go
type SettingsHandlers struct {
    Page            http.HandlerFunc  // serves the new HTML payload
    GetConfig       http.HandlerFunc  // unchanged
    SaveConfig      http.HandlerFunc  // unchanged
    ListTools       http.HandlerFunc  // unchanged
    BootstrapStatus http.HandlerFunc  // unchanged
}
```

The constructor `NewSettingsHandlers(cfg, toolReg, bootstrap, onSave)` keeps its signature. The only function body that changes is `Page`'s embedded HTML string.

### HTML payload structure (rendered by `Page`)

```html
<!DOCTYPE html>
<html>
<head>
  <title>Felix Settings</title>
  <link rel="icon" type="image/png" href="/favicon.png">
  <style>/* cloudcat's stylesheet, verbatim */</style>
</head>
<body>
  <main>
    <div class="container">
      <div id="loading">Loading configuration…</div>
      <div id="settings-root" style="display:none">
        <div class="settings-shell">
          <nav class="finger-tabs" id="tabs">
            <button class="finger-tab active" data-tab="agents">
              <svg class="ft-icon" …/>Agents
            </button>
            <button class="finger-tab" data-tab="providers">…Providers</button>
            <button class="finger-tab" data-tab="models">…Models</button>      <!-- Felix-only -->
            <button class="finger-tab" data-tab="intelligence">…Memory</button>
            <button class="finger-tab" data-tab="security">…Security</button>
            <button class="finger-tab" data-tab="messaging">…Messaging</button>
            <button class="finger-tab" data-tab="mcp">…MCP</button>
            <button class="finger-tab" data-tab="gateway">…OpenTelemetry</button>
            <button class="finger-tab" data-tab="skills">…Skills</button>
            <!-- NO data-tab="fleet" -->
          </nav>
          <div class="finger-panels">
            <div class="finger-panel active" id="panel-agents"></div>
            <div class="finger-panel" id="panel-providers"></div>
            <div class="finger-panel" id="panel-models"></div>       <!-- Felix-only -->
            <div class="finger-panel" id="panel-intelligence"></div>
            <div class="finger-panel" id="panel-security"></div>
            <div class="finger-panel" id="panel-messaging"></div>
            <div class="finger-panel" id="panel-mcp"></div>
            <div class="finger-panel" id="panel-gateway"></div>
            <div class="finger-panel" id="panel-skills"></div>
            <!-- NO panel-fleet -->
          </div>
        </div>
      </div>
    </div>
  </main>
  <script>/* cloudcat JS verbatim minus Fleet, plus Felix renderModels */</script>
</body>
</html>
```

### JS render functions to lift from cloudcat (verbatim)

- `loadConfig()` — fetches `/settings/api/config` and dispatches to each `render*`.
- `saveConfig()` — collects field values from all panels, POSTs to `/settings/api/config`.
- `renderAgents(agents)` — list of agent cards with edit/delete/clone, add-agent button.
- `renderProviders(providers)` — map editor (one row per provider id).
- `renderMemory(cfg)` — combined panel: embedder selector + chunk size + recall threshold + cortex extractor type + cortex enabled toggle.
- `renderSecurity(security)` — tool allow/deny grids; exec policy radio; group policy table.
- `renderMessaging(cfg)` — Telegram + web search.
- `renderMCP(servers)` — list of server cards with transport/auth nested editors.
- `renderOpenTelemetry(otel)` — endpoint + headers + sample rate + enabled.
- `renderSkills()` — fetches `/settings/api/skills`, lists with upload/delete.
- Shared helpers: `showStatus(msg, kind)`, `markDirty()`, hamburger menu, theme toggle.

### JS to carry over from Felix's current settings.go

- `CURATED_MODELS` constant — array of `{name, label, size, note}` for the curated download list.
- `ollamaBase()` — reads `cfg.providers.local.base_url`, normalizes off `/v1` suffix.
- `fmtBytes(n)` — bytes to KB/MB/GB string.
- `renderModels(cfg)` — installed list + download grid + per-card download button.
- `startPull(name)` — POST to Ollama pull endpoint.
- `pollPullStatus(name, btn)` — recursive poll of in-progress pulls, updates button text.

These are self-contained: their only dependency on the rest of the JS is reading `cfg.providers.local.base_url` once. Drop them into the new file's `<script>` block as a contiguous section labeled `// === Felix: Models tab ===`.

### CSS to lift from cloudcat (verbatim)

- `.settings-shell` flex container.
- `.finger-tabs` vertical column (200px) + responsive override `@media (max-width: 720px)`.
- `.finger-tab`, `.finger-tab:hover`, `.finger-tab.active`, `.finger-tab .ft-icon`.
- `.finger-panels`, `.finger-panel`, `.finger-panel.active`.
- All form-group, button, toggle, badge styles cloudcat ships.

CSS variables (`--color-primary`, `--color-bg-soft`, `--color-surface`, `--color-text-muted`, `--color-border`, `--radius`) are already defined elsewhere in Felix — the chat UI port in v0.8.1 brought them in. No theme-token changes needed.

## Data flow

Identical to current Felix:

1. Browser `GET /settings` → HTML payload (new).
2. JS `fetch('/settings/api/config')` → JSON of `*config.Config`.
3. JS distributes the cfg blob into each render function.
4. User edits → `markDirty()` lights the save button.
5. Save button → JS rebuilds the cfg blob from form values → `POST /settings/api/config`.
6. `SaveConfig` handler validates, atomic-writes `~/.felix/felix.json5`, fsnotify triggers hot reload (existing behavior).
7. `Skills` and `MCP` tabs additionally call `/settings/api/skills` and (for OAuth servers) `/api/mcp/reauth/{id}` — existing endpoints.

## Risks and mitigations

1. **JS variable-name collisions between cloudcat panel code and Felix Models code.** Cloudcat uses `cfg`, `availableTools`, `loading`, `settingsRoot` as outer-scope vars; Felix Models code references the same `cfg`. Mitigation: keep Felix's Models JS inside the same IIFE (the existing cloudcat script is one big `(function() { ... })()`), reading the shared `cfg`. Verified the names don't clash.

2. **Tab-id `gateway` semantic drift.** Both projects use `data-tab="gateway"` for the OpenTelemetry tab; just the label differs. Switching the label is a string change — no JS hook reads the label. Risk = nil.

3. **CSS specificity regressions in Skills/MCP panels.** The Skills panel uses upload-styled buttons that may interact with cloudcat's new `.btn` defaults. Mitigation: side-by-side QA — load `/settings`, open Skills tab, attempt an upload. If button styling is broken, scope a follow-up CSS fix (out of this commit).

4. **Models tab's inline-styled cards look out of place against cloudcat's polished panels.** Mitigation: this is a known cosmetic gap. The cards already use the CSS variables, so they pick up Felix's theme. A pass to migrate inline styles into proper `.model-card` classes is a follow-up, not a blocker for this port.

5. **Embedded HTML byte-budget.** The new `settings.go` will be ~2700 LOC vs current 2027. Compiled binary grows by ≤30KB (HTML compresses well at the Go-string-literal level). Negligible.

6. **Existing settings.go tests reference removed markers.** If `internal/gateway/settings_test.go` asserts on specific substrings ("Felix Settings", a tab id, etc.), update those assertions in the same commit.

## Migration / compatibility

- **On-disk config:** unchanged. `~/.felix/felix.json5` reads and writes the same fields.
- **API endpoints:** unchanged. `/settings/api/config`, `/settings/api/tools`, `/settings/api/bootstrap`, `/settings/api/skills` all stay.
- **Hot reload:** unchanged. The save button still triggers fsnotify → provider rebuild.
- **User-facing behavior:** the Intelligence tab disappears; users find its settings under Memory. The Gateway tab is renamed OpenTelemetry. Otherwise everything saves the same fields it did before.

## Testing

1. `go build ./...` — clean.
2. `go test ./internal/gateway/...` — green. Update any test assertion that grepped for "Gateway" tab label or "Intelligence" panel id.
3. `go vet ./...` — quiet.
4. Manual smoke: `go run ./cmd/felix start`, open `/settings`, walk every tab, edit one field per tab, save, reload, confirm persisted to `~/.felix/felix.json5`.
5. Verify Models tab functions: installed list populates, "Download" button triggers a pull, progress polls correctly.

Playwright smoke is nice-to-have but not required — the diff is UI chrome plus already-tested cloudcat panel JS.

## Phasing

Single commit. Splitting would leave the file in inconsistent states (cloudcat panels under Felix chrome won't render, and vice versa) — there's no useful intermediate to land separately.

## Follow-ups (not in this port)

- Migrate Models tab inline styles into proper CSS classes.
- A focused pass to ensure all `.btn` variants render identically across Skills/MCP/Models panels.
- Decide whether to remove the now-orphan `AgentConfig.Fleet` field from Felix's config struct (separate cleanup, not blocking this port).
