# Settings → Providers & MCP Servers: collapsible cards

**Status:** design
**Date:** 2026-05-30

## Goal

Apply the same collapse pattern used in Settings → Agents (see `2026-05-29-agents-collapse-default-design.md`) to the **Providers** and **MCP Servers** tabs. With more than one entry, each card shows a compact header (id/name + key field + enabled/key indicator + remove button); body is hidden until the header is clicked.

No "default" flag for either tab — both are keyed by unique name and have no notion of a primary instance. The chat dropdown selection only makes sense for agents.

## Why this change

Settings tabs hit the same overload at scale. A user with 3 providers and 4 MCP servers sees ~10 form rows per card × 7 cards = >70 visible fields in two tabs. Finding the one to edit means scrolling. The Agents tab proved the collapse pattern works without adding cognitive load, so the same fix is the cheapest win here.

## Scope

**In:**

- `renderMCP` (`internal/gateway/settings.go:1344`) gains a collapsible header (chevron + id + transport + enabled dot + remove). Body wraps the existing per-server fields. Click header → toggle.
- `renderProviders` (`internal/gateway/settings.go:1671`) gains a collapsible header (chevron + name + kind + key-status indicator + remove). Body wraps the existing fields. Click header → toggle.
- Force-expand rules (mirroring agents): only one card, just-added, validation error in body, user-clicked-open.
- Extract the existing `.agent-card-header` / `.agent-card-chevron` / etc. CSS into generic `.collapse-card-*` classes. Update `renderAgents` to use the generic class names. Same visual result; no behavioral change for agents.
- Persistent expand-state maps keyed appropriately: MCP by array index (`renderMCP._expanded`), Providers by name (`renderProviders._expanded`).

**Out:**

- Default / primary flag for either type — N/A.
- Drag-to-reorder.
- Touching other tabs (Memory, Security, etc.). They have at most one of each entity.
- Per-server enable toggle changes (the existing "Enabled" form field stays where it is, inside the body).
- Migrating the existing legacy-HTTP-entry rewrite logic in `renderMCP` — it stays as-is.

## Architecture

```
.collapse-card-header                 <- click-to-toggle header
  chevron (svg, rotates 90° collapsed)
  id/name (monospace, primary)
  meta-left (subdued, "http" or "anthropic")
  status-dot OR key-status badge (right-aligned via margin-left:auto on the next sibling)
  spacer
  remove button (×, right)
.<body class="collapse-card-body">    <- collapsed = display:none

renderMCP / renderProviders:
    if (!fn._expanded) fn._expanded = {};   // closure-local, persists across renders

    for each entry:
        isOnly        = list.length === 1
        isJustAdded   = !!entry.__justAdded
        startsExpanded = isOnly || isJustAdded || expanded[key]
        // applied after body render:
        hasErr        = body.querySelector('.field-with-error')
        if (!isOnly && !startsExpanded && !hasErr) item.classList.add('collapsed')
```

## Components and contracts

### Generic CSS class names

Rename in `internal/gateway/settings.go`:

| Existing (agent-only) | New (shared) |
|---|---|
| `.agent-card-header` | `.collapse-card-header` |
| `.agent-card-chevron` | `.collapse-card-chevron` |
| `.agent-card-id` | `.collapse-card-id` |
| `.agent-card-name` | `.collapse-card-name` |
| `.agent-card-model` | `.collapse-card-meta` |
| `.agent-card-default-badge` | (keep agent-only — only agents have a Default) |
| `.dynamic-item.collapsed .agent-card-chevron` | `.dynamic-item.collapsed .collapse-card-chevron` |
| `.dynamic-item.collapsed .agent-body` | `.dynamic-item.collapsed .collapse-card-body` |

The body wrapper class also renames: `.agent-body` → `.collapse-card-body`.

The class `.agent-card` on `.dynamic-item` and the default badge stay agent-specific.

### MCP header markup

```js
header.appendChild(chevron);          // .collapse-card-chevron
hId.textContent = s.id || '(new)';    // .collapse-card-id (monospace)
hTransport.textContent = s.transport; // .collapse-card-meta (subdued, "http"/"stdio")

dot = span.collapse-card-dot;
dot.dataset.on = s.enabled ? 'true' : 'false';   // CSS colors it
dot.title = s.enabled ? 'Enabled' : 'Disabled';
header.appendChild(dot);

rm.className = 'remove-btn';          // existing class, floats right
header.appendChild(rm);
```

`.collapse-card-meta` has `margin-left: auto` so the dot + remove button hug the right edge.

### Provider header markup

```js
header.appendChild(chevron);

// For _isNew providers, the name is an input; for saved ones, a span.
// Existing code in renderProviders already creates this distinction —
// just move it into the header instead of inside the body, and give it
// the .collapse-card-id class.
header.appendChild(title);            // .collapse-card-id

hKind.textContent = prov.kind || '(no kind)';   // .collapse-card-meta

keyBadge.textContent = (prov.api_key || prov._hasKey) ? 'key set' : 'no key';
keyBadge.className = 'collapse-card-key-badge';

header.appendChild(rm);
```

Provider api_key is write-only in the settings UI (the password field never reveals the stored value). So the badge needs a different signal. Two options:

1. **Trust the in-memory cfg** — show "key set" when `prov.api_key` is truthy. This works only just after the user types one in; on a fresh load the field is empty even when a key is saved. Misleading.
2. **Add a server-side hint** — `handleSettingsGet` could include a `_hasKey` boolean per provider. The UI shows "key set" when `_hasKey || prov.api_key`. Accurate on load.

Use #2. Adds one line to the existing `handleSettingsGet` redaction loop. The settings UI already redacts secrets when sending the config to the browser; this just adds a parallel boolean.

### Form-row reuse

Existing `renderHTTPBlock` / `renderStdioBlock` (MCP) and the existing per-provider fields all currently receive the `item` (the `.dynamic-item`) as their parent. They now receive the new `body` `<div>` instead. One-line change at each call site.

### Click handler

Identical to agents:

```js
header.addEventListener('click', function(e) {
    if (e.target.closest('button, input, select, textarea')) return;
    expanded[key] = !item.classList.contains('collapsed') ? false : true;
    item.classList.toggle('collapsed');
});
```

`key` is `idx` for MCP, `name` for providers.

### Expanded-state map lifecycle

| Event | Action |
|---|---|
| `+ Add MCP/Provider` | New entry gets `__justAdded`, render() runs, force-expand rule kicks in |
| Remove an entry | Reset the whole map (`renderMCP._expanded = {}`) to avoid stale index/name keys |
| Transport change (MCP) re-render | Map persists; the same entry stays expanded because it was the one the user just clicked |
| Provider rename | The old name is still in the map (stale, no harm — never looked up again); new name has no entry yet, falls through to default rules. Since the rename triggers `render()`, the renamed card collapses unless it's the only one. **Mitigation:** after rename, set `renderProviders._expanded[newName] = true` so the card stays open through the re-render. |

### `handleSettingsGet` change

In `internal/gateway/websocket.go` (or wherever the settings GET handler lives — confirm via grep before writing the plan). One line per provider in the redaction loop:

```go
sanitized.Providers[name]._hasKey = (prov.APIKey != "")
sanitized.Providers[name].APIKey = ""
```

Provider struct in `internal/config/config.go` needs an unexported-but-marshalable field. The cleanest minimal change: emit `_hasKey` as a JSON-only ghost field on the wire (not stored on disk). Implementation detail for the plan.

## CSS additions

Add to the existing settings stylesheet block, alongside the renamed generic classes:

```css
.collapse-card-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
    background: var(--color-text-muted);   /* off by default */
}
.collapse-card-dot[data-on="true"] {
    background: #10b981;                    /* green-500-ish, matches existing success */
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

The renamed `.collapse-card-*` rules reuse the existing block verbatim — just the selector strings change.

## Risks and mitigations

1. **Renaming `.agent-card-*` could break someone's custom stylesheet override.** Felix doesn't expose a theming hook for these classes; this risk is internal-only. Acceptable.

2. **Provider key-status badge requires backend change.** Without `_hasKey`, the badge always reads "no key" until the user re-types one. Acceptable as a worst-case fallback, but the backend addition is cheap and worth doing in the same commit.

3. **MCP transport change re-renders the panel.** Re-renders preserve the expand-state map (closure-persistent), so the user's card stays open. Verified by the same pattern working in agents (re-render on Default toggle).

4. **Stale keys in `_expanded` after rename/remove.** Renames: pre-populate the new key (see table above). Removes: reset the whole map. Stale agent entries are already handled this way — same pattern.

5. **Click on the `_isNew` provider title input is captured by header listener.** The `e.target.closest('input')` guard prevents the toggle. Verified via the same guard in the agent click handler (Name input inside an expanded body works the same way).

## Testing

Manual smoke:

1. `/settings` → MCP tab, single server → header visible, no chevron, body expanded, dot reflects enabled.
2. Add second MCP server → first card collapses, second auto-expanded.
3. Toggle Enabled on collapsed card via expanding, save → dot color updates after the next render trigger (or live via the dot ref captured in the closure — preferred).
4. Repeat for Providers: single → no chevron; add second → first collapses.
5. Enter an invalid URL in a provider → save → reload `/settings` → the offending provider's card auto-expands due to `.field-with-error`.
6. Rename a `_isNew` provider → after blur-triggered re-render, the renamed card stays expanded.
7. Visual: confirm `.agent-card` (agents tab) still renders identically post-CSS-rename.

Unit:

- No JS tests in Felix. Manual smoke covers all UI.
- If `_hasKey` is added: `internal/gateway` tests for settings GET should assert it appears and `api_key` is empty in the JSON response.

## Migration / compatibility

- **Config files:** no schema change. The `_hasKey` ghost field on providers is wire-only (not persisted to disk, never round-trips through `Save`).
- **API:** settings GET response gains one optional boolean per provider. Older clients ignore it.
- **Behaviour:** users with one server / one provider see no UI difference. Users with multiple see collapse the next time they open the tab.

## Phasing

Single commit. CSS rename + agent-tab class swap + MCP rewrite + Provider rewrite + backend `_hasKey` are tightly coupled (the generic CSS names must land at the same time as the renamed call sites, or one tab will lose its styling).

## Follow-ups (not in this wave)

- "Move up / down" reorder controls on dynamic-item lists.
- A summary line in MCP collapsed header showing N tools discovered (requires the runtime's MCP catalog, which the settings panel doesn't fetch today).
- Inline enable/disable toggle in the collapsed header (would require capturing the dot in a closure and re-wiring the existing Enabled field's onChange).
