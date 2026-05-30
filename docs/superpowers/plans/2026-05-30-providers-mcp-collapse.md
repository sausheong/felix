# Providers & MCP Servers: collapsible cards — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply the existing Agents-tab collapse pattern to Settings → Providers and Settings → MCP Servers, with id-and-key-field headers, generic `.collapse-card-*` CSS shared across all three tabs, and a wire-only `_hasKey` boolean so the Providers "key set" badge is accurate after page load.

**Architecture:** Single Go file modified (`internal/gateway/settings.go`). The CSS block is renamed in place (agent-card-* → collapse-card-*); `renderAgents` switches to the new class names with no visual change; `renderMCP` and `renderProviders` gain header markup mirroring `renderAgents`; the `GetConfig` HTTP handler does a typed-then-map round-trip to inject `_hasKey` on each provider and blank `api_key`.

**Tech Stack:** Go 1.25, embedded HTML/CSS/JS in Go raw-string literal (cannot contain backticks).

**Spec:** `docs/superpowers/specs/2026-05-30-providers-mcp-collapse-design.md`

---

## File structure

- Modify: `internal/gateway/settings.go`
  - CSS block (~lines 660-700): rename selectors, add three new classes (`.collapse-card-dot`, `.collapse-card-key-badge`, and the badge data-attribute variant)
  - `renderAgents` (~line 2064): swap class name strings only
  - `renderMCP` (~line 1344): rewrite per-server loop to build header + body wrapper
  - `renderProviders` (~line 1671): rewrite per-provider loop to build header + body wrapper, hoist the existing `_isNew` title input into the header
  - `GetConfig` handler (~line 97): switch the clone-then-marshal path through `map[string]any` to inject `_hasKey` per provider and blank `api_key`

No new files. No test files (Felix has no JS test infrastructure; manual smoke covers UI).

---

## Single-commit phasing

The whole change ships in **one commit at the end of Task 5**. Intermediate tasks may produce uncommitted working trees because the CSS rename in Task 1 breaks Agents-tab styling until Task 1's class swap is also done; the `_hasKey` backend change in Task 3 is dormant until Task 4 reads it. Do not commit until Task 5's manual smoke passes.

---

## Task 1: CSS rename + Agents-tab class swap

**Files:**
- Modify: `internal/gateway/settings.go` (CSS block + `renderAgents` class strings)

- [ ] **Step 1: Read the current CSS block**

Run: `grep -n "agent-card" /Users/sausheong/projects/felix/internal/gateway/settings.go`

Expected: matches at lines ~660-700 (CSS rules) and ~2082-2266 (JS class assignments).

- [ ] **Step 2: Rename the CSS selectors and add new classes**

Edit the CSS block to apply this exact mapping (the rest of each rule's body stays identical — only the selector changes):

| Find | Replace |
|---|---|
| `.agent-card-header {` | `.collapse-card-header {` |
| `.agent-card-header:hover` | `.collapse-card-header:hover` |
| `.agent-card-chevron {` | `.collapse-card-chevron {` |
| `.dynamic-item.collapsed .agent-card-chevron` | `.dynamic-item.collapsed .collapse-card-chevron` |
| `.agent-card-id {` | `.collapse-card-id {` |
| `.agent-card-name {` | `.collapse-card-name {` |
| `.agent-card-model {` | `.collapse-card-meta {` |
| `.dynamic-item.collapsed .agent-body` | `.dynamic-item.collapsed .collapse-card-body` |

Leave these unchanged (agent-specific):
- `.agent-card` (the `.dynamic-item` modifier)
- `.agent-card-default-badge`

Then append these new rules at the end of the renamed block (before the next non-collapse rule):

```css
.collapse-card-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
    background: var(--color-text-muted);
}
.collapse-card-dot[data-on="true"] {
    background: #10b981;
}
.collapse-card-key-badge {
    padding: 0.1rem 0.45rem;
    border-radius: 999px;
    background: var(--color-surface-muted, rgba(0,0,0,0.06));
    color: var(--color-text-muted);
    font-size: 0.7rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
}
.collapse-card-key-badge[data-set="true"] {
    background: color-mix(in oklch, var(--color-primary) 14%, transparent);
    color: var(--color-primary);
}
```

- [ ] **Step 3: Update renderAgents to use the new class names**

In `renderAgents` (~line 2064), apply this exact JS-string mapping:

| Find (each occurs once) | Replace |
|---|---|
| `header.className = 'agent-card-header';` | `header.className = 'collapse-card-header';` |
| `chevron.setAttribute('class', 'agent-card-chevron');` | `chevron.setAttribute('class', 'collapse-card-chevron');` |
| `hId.className = 'agent-card-id';` | `hId.className = 'collapse-card-id';` |
| `hName.className = 'agent-card-name';` | `hName.className = 'collapse-card-name';` |
| `hModel.className = 'agent-card-model';` | `hModel.className = 'collapse-card-meta';` |
| `body.className = 'agent-body';` | `body.className = 'collapse-card-body';` |

Leave unchanged:
- `item.className = 'dynamic-item agent-card';` (still keeps the agent-specific marker, which carries no styling of its own but is useful for future hooks)
- `badge.className = 'agent-card-default-badge';`

- [ ] **Step 4: Build to confirm the rename compiles**

Run: `cd /Users/sausheong/projects/felix && go build ./...`
Expected: no errors. (CSS/JS changes are just string content inside `settingsHTML`.)

- [ ] **Step 5: Smoke-test Agents tab**

Run: `cd /Users/sausheong/projects/felix && go run ./cmd/cloudcat start` (or use your normal Felix launch path). Open `/settings` → Agents tab. Confirm with 2+ agents that:
- Cards collapse and expand the same as before.
- Default badge still renders.
- New-agent auto-expand still works.

Expected: zero visual difference from main. Do not commit yet.

---

## Task 2: Rewrite renderMCP with collapse header

**Files:**
- Modify: `internal/gateway/settings.go` — `renderMCP` (~line 1344) and the two helpers it delegates to (`renderHTTPBlock`, `renderStdioBlock`)

- [ ] **Step 1: Initialise the persistent expand-state map**

At the very start of `renderMCP`, after `var p = document.getElementById('panel-mcp'); p.innerHTML = '';`, insert:

```js
if (!renderMCP._expanded) renderMCP._expanded = {};
var expanded = renderMCP._expanded;
```

- [ ] **Step 2: Replace the per-server item construction (current lines ~1395-1434)**

Find the loop body that starts with:

```js
var item = document.createElement('div');
item.className = 'dynamic-item';

var rm = document.createElement('button');
rm.className = 'remove-btn';
rm.innerHTML = '&times;';
rm.onclick = function() { cfg.mcp_servers.splice(idx, 1); render(); };
item.appendChild(rm);
```

Replace from that line through the end of the per-server fields (just before `list.appendChild(item);`) with:

```js
var item = document.createElement('div');
item.className = 'dynamic-item mcp-card';

var isOnly = servers.length === 1;
var isJustAdded = !!s.__justAdded;
var startsExpanded = isOnly || isJustAdded || !!expanded[idx];

// === Header ===
var header = document.createElement('div');
header.className = 'collapse-card-header';

var chevron = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
chevron.setAttribute('viewBox', '0 0 24 24');
chevron.setAttribute('class', 'collapse-card-chevron');
chevron.innerHTML = '<path d="M6 9l6 6 6-6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>';
header.appendChild(chevron);

var hId = document.createElement('span');
hId.className = 'collapse-card-id';
hId.textContent = s.id || '(new)';
header.appendChild(hId);

var hMeta = document.createElement('span');
hMeta.className = 'collapse-card-meta';
hMeta.textContent = s.transport || 'http';
header.appendChild(hMeta);

var dot = document.createElement('span');
dot.className = 'collapse-card-dot';
dot.dataset.on = s.enabled ? 'true' : 'false';
dot.title = s.enabled ? 'Enabled' : 'Disabled';
header.appendChild(dot);

var rm = document.createElement('button');
rm.className = 'remove-btn';
rm.innerHTML = '&times;';
rm.onclick = function(e) {
    e.stopPropagation();
    cfg.mcp_servers.splice(idx, 1);
    renderMCP._expanded = {};
    render();
};
header.appendChild(rm);

item.appendChild(header);

if (isOnly) {
    chevron.style.visibility = 'hidden';
    header.style.cursor = 'default';
} else {
    header.addEventListener('click', function(e) {
        if (e.target.closest('button, input, select, textarea')) return;
        expanded[idx] = !item.classList.contains('collapsed') ? false : true;
        item.classList.toggle('collapsed');
    });
}

// === Body ===
var body = document.createElement('div');
body.className = 'collapse-card-body';
item.appendChild(body);

var row1 = makeRow(body);
makeField(row1, 'ID', 'text', s.id || '', function(v) {
    cfg.mcp_servers[idx].id = v;
    hId.textContent = v || '(new)';
});
makeField(row1, 'Tool Prefix', 'text', s.tool_prefix || '', function(v) { cfg.mcp_servers[idx].tool_prefix = v; });

makeField(body, 'Transport', 'select', {
    value: s.transport,
    options: [
        {value: 'http', label: 'HTTP (Streamable)'},
        {value: 'stdio', label: 'stdio (subprocess)'}
    ]
}, function(v) {
    cfg.mcp_servers[idx].transport = v;
    if (v === 'stdio' && !cfg.mcp_servers[idx].stdio) {
        cfg.mcp_servers[idx].stdio = {command: '', args: [], env: {}};
    }
    if (v === 'http' && !cfg.mcp_servers[idx].http) {
        cfg.mcp_servers[idx].http = {url: '', auth: {kind: 'oauth2_client_credentials'}};
    }
    render();
});

makeField(body, 'Enabled', 'toggle', !!s.enabled, function(v) {
    cfg.mcp_servers[idx].enabled = v;
    dot.dataset.on = v ? 'true' : 'false';
    dot.title = v ? 'Enabled' : 'Disabled';
});
makeField(body, 'Parallel-safe', 'toggle', !!s.parallelSafe, function(v) { cfg.mcp_servers[idx].parallelSafe = v; });

if (s.transport === 'http') {
    renderHTTPBlock(body, idx, s);
} else if (s.transport === 'stdio') {
    renderStdioBlock(body, idx, s);
}

// === Apply collapsed state ===
var hasErr = body.querySelector('.field-with-error');
if (!isOnly && !startsExpanded && !hasErr) {
    item.classList.add('collapsed');
}
if (s.__justAdded) delete cfg.mcp_servers[idx].__justAdded;

list.appendChild(item);
```

Then verify that `renderHTTPBlock` and `renderStdioBlock` accept their first parameter as a generic element (they already take `item`); renaming the parameter is optional and not required for correctness. The replacement above passes `body` to those helpers in place of `item`, which works because both helpers only `appendChild` to the parameter.

- [ ] **Step 3: Update the + Add MCP Server handler to mark new servers**

Find:

```js
cfg.mcp_servers.push({
    id: '',
    transport: 'http',
    http: { ... },
    enabled: true,
    parallelSafe: false,
    tool_prefix: ''
});
```

Add the `__justAdded: true` sentinel:

```js
cfg.mcp_servers.push({
    id: '',
    transport: 'http',
    http: {
        url: '',
        auth: {
            kind: 'oauth2_client_credentials',
            token_url: '',
            client_id: '',
            client_secret: '',
            scope: ''
        }
    },
    enabled: true,
    parallelSafe: false,
    tool_prefix: '',
    __justAdded: true
});
```

- [ ] **Step 4: Build and smoke-test MCP tab**

Run: `cd /Users/sausheong/projects/felix && go build ./...`
Expected: no errors.

Manual smoke on `/settings`:
- One MCP server → no chevron, body expanded, dot reflects enabled state.
- Add second server → first collapses, second stays expanded (justAdded).
- Toggle Enabled inside the expanded second card → dot color changes immediately (live closure update).
- Switch Transport on the second card from http → stdio → card stays expanded (state map preserves it across re-render).
- Remove a card → indices reset cleanly, no stale expanded state.

Expected: all pass. Do not commit yet.

---

## Task 3: GetConfig backend `_hasKey` injection

**Files:**
- Modify: `internal/gateway/settings.go` — `GetConfig` handler (~line 97)

- [ ] **Step 1: Replace the clone-marshal block with a map-based redaction**

Find this block in `GetConfig`:

```go
var clone config.Config
if err := json.Unmarshal(raw, &clone); err != nil {
    http.Error(w, `{"error":"clone config"}`, http.StatusInternalServerError)
    return
}
redactConfigSecrets(&clone)

data, err := json.MarshalIndent(&clone, "", "  ")
if err != nil {
    http.Error(w, `{"error":"marshal config"}`, http.StatusInternalServerError)
    return
}
w.Write(data)
```

Replace with:

```go
var clone config.Config
if err := json.Unmarshal(raw, &clone); err != nil {
    http.Error(w, `{"error":"clone config"}`, http.StatusInternalServerError)
    return
}
redactConfigSecrets(&clone)

// Re-marshal then unmarshal-to-map so we can inject the wire-only
// _hasKey field per provider without polluting the typed config
// schema. The badge in the Providers tab uses this to show "key set"
// accurately on fresh page loads (api_key itself is blanked here).
cloneBytes, err := json.Marshal(&clone)
if err != nil {
    http.Error(w, `{"error":"marshal config"}`, http.StatusInternalServerError)
    return
}
var asMap map[string]any
if err := json.Unmarshal(cloneBytes, &asMap); err != nil {
    http.Error(w, `{"error":"map config"}`, http.StatusInternalServerError)
    return
}
if provs, ok := asMap["providers"].(map[string]any); ok {
    for _, prov := range provs {
        pm, ok := prov.(map[string]any)
        if !ok {
            continue
        }
        apiKey, _ := pm["api_key"].(string)
        pm["_hasKey"] = apiKey != ""
        pm["api_key"] = ""
    }
}

data, err := json.MarshalIndent(asMap, "", "  ")
if err != nil {
    http.Error(w, `{"error":"marshal config"}`, http.StatusInternalServerError)
    return
}
w.Write(data)
```

- [ ] **Step 2: Build**

Run: `cd /Users/sausheong/projects/felix && go build ./...`
Expected: no errors.

- [ ] **Step 3: Sanity-check the response shape**

With Felix running, set `providers.anthropic.api_key` to anything in `~/.felix/felix.json5`, restart, then:

Run: `curl -s -H "Cookie: <your-felix-cookie>" http://localhost:18789/settings/api/config | jq '.providers'`

Expected: each provider object has `"api_key": ""` and `"_hasKey": true|false` (true for any with a saved key).

- [ ] **Step 4: Verify SaveConfig still round-trips cleanly**

In the browser, open `/settings`, edit any provider's Base URL, save, reload. Expected: save succeeds, and on next load the `_hasKey` is unchanged (the client sends back `_hasKey` but our Unmarshal into `config.Config` discards unknown fields by default — confirmed because `ProviderConfig` does not declare `_hasKey`).

Do not commit yet.

---

## Task 4: Rewrite renderProviders with collapse header

**Files:**
- Modify: `internal/gateway/settings.go` — `renderProviders` (~line 1671)

- [ ] **Step 1: Initialise the persistent expand-state map**

At the start of `renderProviders`, after `var p = document.getElementById('panel-providers'); p.innerHTML = '';`, insert:

```js
if (!renderProviders._expanded) renderProviders._expanded = {};
var expanded = renderProviders._expanded;
```

- [ ] **Step 2: Replace the per-provider loop body (current lines ~1682-1755)**

Find the per-provider IIFE body that starts with:

```js
var prov = providers[name];
var item = document.createElement('div');
item.className = 'dynamic-item';

var title;
if (prov && prov._isNew) {
    title = document.createElement('input');
    title.className = 'provider-name dynamic-item-title';
    ...
```

Replace from there through `list.appendChild(item);` with:

```js
var prov = providers[name];
var item = document.createElement('div');
item.className = 'dynamic-item provider-card';

var isOnly = names.length === 1;
var isJustAdded = !!(prov && prov._isNew);
var startsExpanded = isOnly || isJustAdded || !!expanded[name];

// === Header ===
var header = document.createElement('div');
header.className = 'collapse-card-header';

var chevron = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
chevron.setAttribute('viewBox', '0 0 24 24');
chevron.setAttribute('class', 'collapse-card-chevron');
chevron.innerHTML = '<path d="M6 9l6 6 6-6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>';
header.appendChild(chevron);

// Title: editable input for _isNew, plain span otherwise.
var title;
if (prov && prov._isNew) {
    title = document.createElement('input');
    title.className = 'collapse-card-id';
    title.type = 'text';
    title.value = name;
    title.placeholder = 'Provider name (e.g. anthropic, openai)';
    title.addEventListener('blur', function() {
        var newName = title.value.trim();
        if (!newName || newName === name) return;
        if (cfg.providers[newName]) {
            title.classList.add('field-with-error');
            return;
        }
        cfg.providers[newName] = cfg.providers[name];
        delete cfg.providers[newName]._isNew;
        delete cfg.providers[name];
        // Carry expand state across rename so the renamed card stays open.
        expanded[newName] = true;
        render();
    });
} else {
    title = document.createElement('span');
    title.className = 'collapse-card-id';
    title.textContent = name;
}
header.appendChild(title);

var hKind = document.createElement('span');
hKind.className = 'collapse-card-meta';
hKind.textContent = prov.kind || '(no kind)';
header.appendChild(hKind);

var keyBadge = document.createElement('span');
keyBadge.className = 'collapse-card-key-badge';
var hasKey = !!(prov.api_key || prov._hasKey);
keyBadge.dataset.set = hasKey ? 'true' : 'false';
keyBadge.textContent = hasKey ? 'key set' : 'no key';
header.appendChild(keyBadge);

var rm = document.createElement('button');
rm.className = 'remove-btn';
rm.innerHTML = '&times;';
rm.onclick = function(e) {
    e.stopPropagation();
    delete cfg.providers[name];
    renderProviders._expanded = {};
    render();
};
header.appendChild(rm);

item.appendChild(header);

if (isOnly) {
    chevron.style.visibility = 'hidden';
    header.style.cursor = 'default';
} else {
    header.addEventListener('click', function(e) {
        if (e.target.closest('button, input, select, textarea')) return;
        expanded[name] = !item.classList.contains('collapsed') ? false : true;
        item.classList.toggle('collapsed');
    });
}

// === Body ===
var body = document.createElement('div');
body.className = 'collapse-card-body';
item.appendChild(body);

var row = makeRow(body);
row.style.gridTemplateColumns = 'minmax(180px, 0.3fr) 1fr';
makeField(row, 'Kind', 'select', {
    value: prov.kind || '',
    options: [
        {value: '', label: '(choose)'},
        {value: 'anthropic', label: 'anthropic'},
        {value: 'openai', label: 'openai'},
        {value: 'openai-compatible', label: 'openai-compatible'},
        {value: 'gemini', label: 'gemini'},
        {value: 'qwen', label: 'qwen'},
        {value: 'local', label: 'local'},
    ],
}, function(v) {
    cfg.providers[name].kind = v;
    hKind.textContent = v || '(no kind)';
});
var urlField = makeField(row, 'Base URL', 'text', prov.base_url || '', function(v) { cfg.providers[name].base_url = v; });
var urlInput = urlField.querySelector('input');
urlInput.setAttribute('type', 'url');
urlInput.setAttribute('inputmode', 'url');
validateField(urlField, function(v) {
    v = (v || '').trim();
    if (!v) return '';
    var u;
    try { u = new URL(v); } catch (e) { return 'Not a valid URL.'; }
    if (u.protocol !== 'https:' && u.protocol !== 'http:') {
        return 'URL must use https:// (or http:// for local dev).';
    }
    return '';
});
makeField(body, 'API Key', 'password', '', function(v) {
    if (v) {
        cfg.providers[name].api_key = v;
        keyBadge.dataset.set = 'true';
        keyBadge.textContent = 'key set';
    }
});

// === Apply collapsed state ===
var hasErr = body.querySelector('.field-with-error');
if (!isOnly && !startsExpanded && !hasErr) {
    item.classList.add('collapsed');
}

list.appendChild(item);
```

- [ ] **Step 3: Build**

Run: `cd /Users/sausheong/projects/felix && go build ./...`
Expected: no errors.

- [ ] **Step 4: Smoke-test Providers tab**

Manual on `/settings` → Providers tab:
- Single provider → no chevron, body expanded, badge shows "key set" / "no key" correctly based on saved state.
- Add second provider → first collapses, new one expanded with editable name input in the header.
- Rename the new provider via the header input (tab/blur) → renamed card stays expanded after re-render.
- Type into the API Key field on an expanded card → badge flips to "key set" live.
- Enter invalid URL → save → reload → that provider's card auto-expands on next load (field-with-error force-expand rule).
- Remove a card → no styling artifacts.

Do not commit yet.

---

## Task 5: Full smoke + single commit

**Files:** none modified.

- [ ] **Step 1: Full build + vet + tests**

Run: `cd /Users/sausheong/projects/felix && go build ./... && go vet ./... && go test ./internal/gateway/...`

Expected: all pass. (Existing gateway tests do not exercise the embedded HTML, so they should be unaffected.)

- [ ] **Step 2: End-to-end smoke on all three tabs**

Restart Felix. Open `/settings`:

1. **Agents tab** — confirm visually identical to main: cards, headers, default badge, chevron rotation, auto-expand of justAdded.
2. **Providers tab** — confirm the new collapse pattern works as described in Task 4 Step 4.
3. **MCP Servers tab** — confirm the new collapse pattern works as described in Task 2 Step 4.
4. **Cross-tab** — switch tabs, return, confirm expand state persists (closure-local maps survive panel re-renders within the same SPA load).
5. **Reload** — confirm everything re-paints correctly with `_hasKey` driving the Providers badges and the MCP dot reflecting saved Enabled state.

- [ ] **Step 3: Commit**

```bash
cd /Users/sausheong/projects/felix
git add internal/gateway/settings.go
git commit -m "$(cat <<'EOF'
feat(settings): collapsible cards for Providers & MCP Servers

Mirrors the Agents-tab collapse pattern (2026-05-29) on the other two
multi-entry tabs. Headers show id/name + key field + status indicator
+ remove button; body collapses with the same chevron and force-expand
rules (only-one, just-added, has-validation-error, user-clicked).

Refactors the agent-card-* CSS into shared .collapse-card-* classes so
all three tabs share one styling block (.agent-card itself and the
default badge remain agent-specific).

Adds a wire-only _hasKey boolean per provider in GetConfig so the
Providers "key set" badge stays accurate after page reload despite the
api_key field being redacted to "" over the wire. The field is
injected via a map-based pass after the typed clone marshal, so
ProviderConfig keeps its lean on-disk schema.

Spec: docs/superpowers/specs/2026-05-30-providers-mcp-collapse-design.md

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

Expected: clean commit, working tree empty after.

- [ ] **Step 4: Confirm with a final `git status` and `git log -1`**

Run: `cd /Users/sausheong/projects/felix && git status && git log -1 --stat`
Expected: clean tree, one new commit modifying only `internal/gateway/settings.go`.

---

## Self-review checklist

- **Spec coverage:**
  - CSS rename → Task 1 ✓
  - MCP collapse header (id + transport + enabled-dot + remove) → Task 2 ✓
  - Provider collapse header (name + kind + key-set badge + remove, with hoisted _isNew input) → Task 4 ✓
  - Force-expand rules (only / justAdded / hasErr / user-clicked) → Tasks 2 & 4 ✓
  - Persistent expand-state map (idx-keyed for MCP, name-keyed for Providers, with rename-carry) → Tasks 2 & 4 ✓
  - `_hasKey` backend addition (map-based, no struct change) → Task 3 ✓
  - Single commit at the end → Task 5 ✓
- **Placeholder scan:** no TBDs; every code block is the full, paste-ready content.
- **Type consistency:** `hId`, `chevron`, `header`, `body`, `dot`, `keyBadge`, `hKind`, `hMeta` — names align across MCP and Provider rewrites. Expand-state map naming is consistent (`renderXxx._expanded`).

## Follow-ups deferred to future PRs

- Inline Enabled toggle in MCP collapsed header (would require a click-stop-propagation toggle button next to the dot).
- "Tools discovered: N" line in the MCP body (requires runtime MCP catalog access).
- Move-up / move-down reorder controls.
