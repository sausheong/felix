# Settings → Agents: collapse + default flag

**Status:** design
**Date:** 2026-05-29

## Goal

Settings → Agents becomes unwieldy past 2-3 agents because every card renders fully expanded. Two coupled changes:

1. **Collapse cards** when there are 2+ agents. Each card shows a compact header (id, name, model, optional Default badge, remove button); body is hidden. Click header to expand. The agent marked Default stays expanded by default.
2. **Per-agent Default flag** (radio-style: only one agent can be Default at a time). The default agent shows a "Default" badge in its collapsed header AND sorts first in the chat-UI agent dropdown.

## Why this change

Today's `renderAgents` (`internal/gateway/settings.go`) renders every agent as a flat `.dynamic-item` div with ~10 form rows. With 5 agents the page is several screens long; finding one to edit means scrolling and visually parsing similar-looking cards.

The "default agent" concept also doesn't exist today. The chat dropdown shows agents in config-list order; users who add a new experimental agent at the end of the list (a common pattern) find their primary agent buried in position N. Pre-selecting the right agent on first load matters because the dropdown choice persists through localStorage afterward.

## Scope

**In:**

- New optional field `AgentConfig.Default bool` (json: `"default,omitempty"`).
- `Config.Validate` enforces "at most one agent may have `Default=true`". When the on-disk config has multiple, only the first wins; the others are silently cleared and a warning is logged. (Settings UI's radio semantics make multi-true impossible going forward.)
- `renderAgents` in `internal/gateway/settings.go` gains:
  - A collapsed/expanded state per card (closure-local map keyed by agent index).
  - A header row with chevron, id, name, model (muted), Default badge (when true), remove button. Click chevron or header → toggle.
  - A "Default" toggle inside the expanded body. Setting it on walks `cfg.agents.list` and clears `Default` on the others, then re-renders.
- `handleAgentStatus` in `internal/gateway/websocket.go` emits `"default": a.Default` per agent.
- The chat-UI `agent.status` response handler in `internal/gateway/chat.go` sorts the agents list with default-first before populating `agentSelect`. Initial selection picks the default if any, else preserves today's behavior.

**Out:**

- Drag-to-reorder agent cards.
- Cron-job "use default agent" semantics — cron specifies agent by id and stays that way.
- Per-binding default — bindings already pick agent by id.
- Migration help for existing configs — the no-default-field case falls back to list[0] for dropdown selection, same as today.

## Architecture

```
config.AgentConfig {
    ... existing fields ...
    Default bool `json:"default,omitempty"`     // new
}

config.(*Config).Validate():
    seen := 0
    for i := range c.Agents.List:
        if c.Agents.List[i].Default:
            seen++
            if seen > 1:
                c.Agents.List[i].Default = false
                slog.Warn("clearing duplicate Default=true on agent", ...)

renderAgents (Settings UI):
    expanded := make(map[int]bool)        // closure-local; rebuilt on each render
    for i, agent := range cfg.agents.list:
        switch {
        case len(list) == 1:        force-expanded, no chevron
        case agent.justAdded:       force-expanded
        case agent.hasValidationErr: force-expanded
        case agent.default:          force-expanded (initial)
        case expanded[i]:            force-expanded (user clicked)
        default:                     collapsed
        }

handleAgentStatus → emits {id, name, model, workspace, context_window, default}

chat.go agent.status handler:
    agents.sort((a,b) => (b.default?1:0) - (a.default?1:0))   // stable sort
    populate agentSelect in that order
    if no localStorage selection yet, select the first (which is now the default)
```

## Components and contracts

### `AgentConfig.Default`

```go
type AgentConfig struct {
    // ... existing fields ...

    // Default marks this agent as the default in the chat UI: the dropdown
    // shows it first, and a fresh browser session selects it on initial
    // page load. At most one agent in cfg.Agents.List may have Default=true;
    // Validate() enforces this and clears extra trues, logging a warning.
    Default bool `json:"default,omitempty"`
}
```

### `Config.Validate` rule (add to existing validator)

```go
// At most one agent may have Default=true. If the on-disk config has
// multiple, keep the first and clear the rest so the runtime sees a
// well-formed state. Logs a warning so the user can fix the source file.
seen := false
for i := range c.Agents.List {
    if !c.Agents.List[i].Default {
        continue
    }
    if seen {
        slog.Warn("config: clearing duplicate Default=true on agent",
            "agent_id", c.Agents.List[i].ID)
        c.Agents.List[i].Default = false
        continue
    }
    seen = true
}
```

Returns no error — the cleanup is silent (slog only). This rule fires on `Validate`, which runs from `LoadConfig`, `Save`, and `SaveConfig` (settings UI).

### `renderAgents` collapse contract (JS, in `internal/gateway/settings.go`'s embedded script)

State: a closure-local `var agentExpanded = {};` map keyed by index, scoped to the IIFE that wraps the settings JS. Rebuilt every `render()`.

Per-card initial expanded state, in priority order:
1. `cfg.agents.list.length === 1` → expanded, no chevron rendered.
2. `agent.__justAdded === true` (set by the + Add handler, cleared after render) → expanded.
3. card has a `.field-error` class on any field (validation failure) → expanded.
4. `agent.default === true` → expanded.
5. `agentExpanded[i] === true` (user clicked to open) → expanded.
6. Otherwise → collapsed.

Click handler on `.agent-card-header`:
- Toggles `agentExpanded[i]`.
- Updates `.agent-card.collapsed` class without re-rendering the whole panel (preserve scroll, no flash).

Click handler on the Default toggle:
- If turning ON: walk `cfg.agents.list`, set each `[j].default = (j === idx)`, then re-render the panel (so other cards collapse and lose their badge).
- If turning OFF: just set `cfg.agents.list[idx].default = false` and re-render so this card's badge clears.

### `handleAgentStatus` response shape

Add one field to the per-agent map:

```go
statuses = append(statuses, map[string]any{
    "id":             a.ID,
    "name":           a.Name,
    "model":          a.Model,
    "workspace":      a.Workspace,
    "context_window": tokens.ContextWindowFor(a.Model, a.ContextWindow),
    "default":        a.Default,    // NEW
})
```

### Chat dropdown initial selection (`internal/gateway/chat.go`)

In the `agent.status` response handler (~line 3613):

```js
var agents = resp.result.agents || [];
// Stable sort: default agent first. Array.sort is stable in modern engines.
agents.sort(function(a, b) { return (b.default ? 1 : 0) - (a.default ? 1 : 0); });

agentSelect.innerHTML = '';
agentWindows = {};
for (var i = 0; i < agents.length; i++) {
    var opt = document.createElement('option');
    opt.value = agents[i].id;
    opt.textContent = agents[i].name || agents[i].id;
    agentSelect.appendChild(opt);
    if (agents[i].context_window) {
        agentWindows[agents[i].id] = agents[i].context_window;
    }
}
// After population, agentSelect.value is the first option (the default
// agent, if any). localStorage may override below.
```

The downstream localStorage-of-last-picked logic is unchanged. So a user who picks a non-default agent and reloads stays on their pick; a fresh browser/session lands on the default.

## CSS

Add to the settings.go embedded stylesheet:

```css
.agent-card {
    /* re-use existing .dynamic-item border, padding, radius */
}
.agent-card-header {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.5rem 0.75rem;
    cursor: pointer;
    border-radius: 8px;
    margin: -0.5rem -0.75rem 0.5rem;  /* hug the card edge */
}
.agent-card-header:hover {
    background: var(--color-bg-soft);
}
.agent-card-chevron {
    width: 14px;
    height: 14px;
    flex-shrink: 0;
    color: var(--color-text-muted);
    transition: transform 0.12s ease;
}
.agent-card.collapsed .agent-card-chevron {
    transform: rotate(-90deg);
}
.agent-card-id {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.85rem;
    color: var(--color-text-muted);
}
.agent-card-name {
    font-weight: 600;
}
.agent-card-model {
    color: var(--color-text-muted);
    font-size: 0.85rem;
    margin-left: auto;  /* push to the right */
}
.agent-card-default-badge {
    padding: 0.1rem 0.45rem;
    border-radius: 999px;
    background: color-mix(in oklch, var(--color-primary) 14%, transparent);
    color: var(--color-primary);
    font-size: 0.7rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
}
.agent-card.collapsed .agent-body {
    display: none;
}
```

## Data flow per turn

Unchanged. The only data-flow touchpoint is the one extra `default` field in the `agent.status` response, which the chat UI uses for ordering. Nothing else in the runtime cares about Default.

## Risks and mitigations

1. **Existing felix.json5 without `default` field.** All agents end up with `Default=false`. Dropdown initial selection falls back to `list[0]` (today's behavior). No migration required.

2. **Multiple `Default=true` in on-disk config** (hand-edited or copied between machines). Validate keeps the first, silently clears the rest, logs warn. UI's radio semantics prevent future occurrence.

3. **Default agent ID is empty/invalid.** A newly added (and unsaved) agent could be toggled Default. The dropdown sort still works (empty id → still first), but the dropdown option text shows blank. Edge case; acceptable since the user is mid-edit.

4. **Re-render-during-edit churn.** Toggling Default re-renders the whole panel, which closes the user's keyboard focus. Acceptable because the Default toggle is rare and explicit. Avoid `render()` for the collapse-click handler — it's pure DOM class toggling, no re-render.

5. **localStorage of last-selected agent still wins.** Users who have already picked a non-default agent stay on their pick after reload. Only fresh sessions / never-picked installs land on the default. This is the right behavior — don't override deliberate user choice.

6. **Collapsed-state map keyed by index** can desynchronize if cards reorder. Felix never reorders cards; only add/remove. After add: the new card forces expanded via `__justAdded`. After remove: indices shift, the state map gets stale entries but no harm (lookups return undefined → falls through to default rules).

## Testing

Manual smoke (browser, end-to-end):
1. Open `/settings` with a fresh single-agent config → no chevron visible, card expanded, no Default badge.
2. Add a second agent via "+ Add Agent" → first card collapses (no Default flag), second card auto-expanded (new).
3. Toggle Default on the second agent → first card still collapsed (not default), second card stays expanded with "DEFAULT" badge.
4. Save, reload `/settings` → second agent's body collapsed because Default still set, badge visible in header.
5. Open `/chat` with no localStorage selection → dropdown's first option is the second agent.
6. Hand-edit `~/.felix/felix.json5` to set Default on two agents → restart felix → warning in logs, only first kept.

Unit:
- `internal/config/config_test.go` — `TestValidate_ClearsMultipleDefaults` asserts the cleanup + warning.
- (No JS tests in felix today; manual smoke covers the UI changes.)

## Migration / compatibility

- **On-disk config:** unchanged shape (new optional field). Existing files round-trip cleanly because `omitempty` skips the false case.
- **API:** `handleAgentStatus` adds one field; clients that don't read it are unaffected.
- **Behavior:** users with one agent see no UI difference. Users with multiple agents see the collapse the first time they open Settings post-upgrade.

## Phasing

Single commit. UI + config + handler are tightly coupled; splitting would leave intermediate states broken (e.g. handler emits `default` but UI doesn't read it = no sort; or UI sorts on a field the handler doesn't yet emit = sort always says false).

## Follow-ups (not in this wave)

- "Set as default" right-click context menu on the chat dropdown options.
- Drag-to-reorder Settings cards (separate brainstorm).
- Default-agent-aware cron job dispatch.
