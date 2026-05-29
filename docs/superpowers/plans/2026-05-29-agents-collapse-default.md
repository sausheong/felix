# Settings → Agents: Collapse + Default Flag Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-agent `Default` flag and collapse all agent cards in Settings → Agents (when there are 2+ agents) except the default one.

**Architecture:** One Go field (`AgentConfig.Default`), one validator rule (at most one default), one extra field in the `agent.status` JSON-RPC response, one stable client-side sort, and a refactor of `renderAgents` to render a clickable header + collapsible body per card.

**Tech Stack:** Go 1.25, embedded HTML/CSS/JS, JSON-RPC over WebSocket.

**Spec:** `docs/superpowers/specs/2026-05-29-agents-collapse-default-design.md`

---

## File structure

| Path | Action | Responsibility |
|------|--------|----------------|
| `internal/config/config.go` | Modify | Add `AgentConfig.Default bool`. Add dedup rule in `Validate`. |
| `internal/config/config_test.go` | Modify | Add `TestValidate_ClearsMultipleDefaults`. |
| `internal/gateway/websocket.go` | Modify | Add `"default": a.Default` to `handleAgentStatus` per-agent map. |
| `internal/gateway/chat.go` | Modify | Stable-sort the agents array default-first before populating `agentSelect`. |
| `internal/gateway/settings.go` | Modify | `renderAgents` refactored: per-card header + collapse + Default toggle. New CSS block. |

No new files. No new HTTP routes. No new RPC methods.

---

## Pre-flight context for the implementer

You're adding a per-agent "Default" flag (radio-style: at most one agent default at a time) and giving Settings → Agents a collapsed/expanded card layout so users with multiple agents aren't drowning in scroll.

**Why one task with many steps, not many tasks:** UI + handler + sort are tightly coupled. If you split, intermediate states are broken (handler emits a field the UI doesn't read; or UI sorts on a field the handler doesn't yet send). Single commit.

**Reference reading before you start:**

- The spec at `docs/superpowers/specs/2026-05-29-agents-collapse-default-design.md` — has the data flow, CSS skeleton, edge cases, and risks already analyzed. Don't re-derive any of it.
- `internal/gateway/settings.go` lines ~2021–2141 — current `renderAgents` body. You'll restructure this.
- `internal/gateway/chat.go` lines ~3608–3640 — current `agent.status` response handling. You add a sort here.
- `internal/gateway/websocket.go` lines ~984–1004 — current `handleAgentStatus`. You add one field.
- `internal/config/config.go` lines ~584+ — `Config.Validate`. You append one cleanup rule.

**Tab id convention:** The Settings UI uses `data-tab="agents"` for this panel; that's unchanged.

**The "+ Add Agent" button** at the bottom of `panel-agents` calls `cfg.agents.list.push({id: '', name: '', model: '', tools: {allow: []}})` then `render()`. To make new cards force-expanded, set a transient `__justAdded = true` flag on the pushed object; clear it inside `renderAgents` after the first render of that card consumes it.

---

## Task 1: Add Default field + Validate dedup + propagation + UI collapse

This task is a single atomic change. Multiple steps, one commit at the end.

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/gateway/websocket.go`
- Modify: `internal/gateway/chat.go`
- Modify: `internal/gateway/settings.go`

- [ ] **Step 1: Verify clean working tree**

```bash
cd ~/projects/felix && git status
```

Expected: only untracked files under `docs/superpowers/plans/` and `docs/superpowers/specs/`. Stop if anything inside `internal/` is modified.

- [ ] **Step 2: Baseline tests pass**

```bash
cd ~/projects/felix && go test -count=1 ./internal/config/... ./internal/gateway/... 2>&1 | tail -5
```

Expected: green. Note this; you'll re-check at Step 13.

- [ ] **Step 3: Add `Default` field to AgentConfig**

In `internal/config/config.go`, find the `AgentConfig` struct (search for `type AgentConfig struct`). After the existing `ContextWindow int` field (the last field today), add:

```go
	// Default marks this agent as the default in the chat UI dropdown:
	// the dropdown lists it first, and a fresh browser session with no
	// localStorage selection picks it on initial page load. At most one
	// agent in cfg.Agents.List may have Default=true; Validate enforces
	// this and silently clears extra trues, logging a warning.
	Default bool `json:"default,omitempty"`
```

The `omitempty` matters: false is the zero value, so existing felix.json5 files round-trip without sprouting `"default": false` everywhere.

- [ ] **Step 4: Add the dedup rule to Validate**

Still in `internal/config/config.go`, locate `func (c *Config) Validate() error` (line ~584). At the END of the function body — just before the final `return nil` — append:

```go
	// At most one agent may have Default=true. If the on-disk config has
	// multiple (hand-edited or copied across machines), keep the first
	// and clear the rest so the runtime sees a well-formed state. The UI's
	// radio semantics prevent this through Settings.
	seenDefault := false
	for i := range c.Agents.List {
		if !c.Agents.List[i].Default {
			continue
		}
		if seenDefault {
			slog.Warn("config: clearing duplicate Default=true on agent",
				"agent_id", c.Agents.List[i].ID)
			c.Agents.List[i].Default = false
			continue
		}
		seenDefault = true
	}
```

If `slog` is not already imported in this file, add `"log/slog"` to the import block. Check with `grep '"log/slog"' internal/config/config.go`.

- [ ] **Step 5: Write the test**

In `internal/config/config_test.go`, add:

```go
func TestValidate_ClearsMultipleDefaults(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			List: []AgentConfig{
				{ID: "a", Model: "p/m", Default: true},
				{ID: "b", Model: "p/m", Default: true},
				{ID: "c", Model: "p/m", Default: true},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if !cfg.Agents.List[0].Default {
		t.Errorf("agent[0] (first) should keep Default=true")
	}
	if cfg.Agents.List[1].Default {
		t.Errorf("agent[1] should have been cleared")
	}
	if cfg.Agents.List[2].Default {
		t.Errorf("agent[2] should have been cleared")
	}
}
```

Run it and verify it passes (the code from Step 4 makes it pass):

```bash
cd ~/projects/felix && go test -count=1 -run TestValidate_ClearsMultipleDefaults ./internal/config/
```

Expected: PASS. If FAIL with "Default undefined", you skipped Step 3.

- [ ] **Step 6: Emit `default` in handleAgentStatus**

In `internal/gateway/websocket.go`, locate `handleAgentStatus` (around line 984). The per-agent map literal looks like:

```go
statuses = append(statuses, map[string]any{
    "id":             a.ID,
    "name":           a.Name,
    "model":          a.Model,
    "workspace":      a.Workspace,
    "context_window": tokens.ContextWindowFor(a.Model, a.ContextWindow),
})
```

Add one line so it becomes:

```go
statuses = append(statuses, map[string]any{
    "id":             a.ID,
    "name":           a.Name,
    "model":          a.Model,
    "workspace":      a.Workspace,
    "context_window": tokens.ContextWindowFor(a.Model, a.ContextWindow),
    "default":        a.Default,
})
```

- [ ] **Step 7: Sort agents default-first in the chat dropdown**

In `internal/gateway/chat.go`, find the `agent.status` response handler at ~line 3613 (search for `if (resp.id === 'agents')`). The current loop starts with:

```js
if (resp.id === 'agents') {
    var agents = resp.result.agents || [];
    agentSelect.innerHTML = '';
    agentWindows = {};
```

Insert a stable sort directly after the `var agents = …` line:

```js
if (resp.id === 'agents') {
    var agents = resp.result.agents || [];
    // Stable sort: default-flagged agent first, rest in config order.
    // Array.prototype.sort is stable per ECMAScript 2019+ (all evergreen
    // browsers we target). Comparator yields negative when 'a' comes first.
    agents.sort(function(a, b) { return (b['default'] ? 1 : 0) - (a['default'] ? 1 : 0); });
    agentSelect.innerHTML = '';
    agentWindows = {};
```

Use `b['default']` bracket notation (not `b.default`) because `default` is a reserved word in some strict-mode contexts; bracket notation is always safe and is the style felix uses elsewhere in this file for similar names.

- [ ] **Step 8: Add CSS for collapse + Default badge**

In `internal/gateway/settings.go`, find the existing `.dynamic-list` / `.dynamic-item` CSS block (search for `.dynamic-item {`). Directly AFTER that block, before the next CSS rule, append:

```css
/* === Agent card collapse + Default badge (Task 1 of agents-collapse-default plan) === */
.agent-card-header {
	display: flex;
	align-items: center;
	gap: 0.6rem;
	padding: 0.5rem 0.75rem;
	cursor: pointer;
	border-radius: 8px;
	margin: -0.5rem -0.75rem 0.5rem;
	user-select: none;
}
.agent-card-header:hover { background: var(--color-bg-soft); }
.agent-card-chevron {
	width: 14px;
	height: 14px;
	flex-shrink: 0;
	color: var(--color-text-muted);
	transition: transform 0.12s ease;
}
.dynamic-item.collapsed .agent-card-chevron { transform: rotate(-90deg); }
.agent-card-id {
	font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
	font-size: 0.85rem;
	color: var(--color-text-muted);
}
.agent-card-name { font-weight: 600; }
.agent-card-model {
	color: var(--color-text-muted);
	font-size: 0.85rem;
	margin-left: auto;
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
	flex-shrink: 0;
}
.dynamic-item.collapsed .agent-body { display: none; }
```

NOTE: this CSS lives inside a Go string literal. Don't paste raw backticks anywhere — the comment uses `===` markers, no backticks.

- [ ] **Step 9: Refactor renderAgents to add header + collapsible body**

In `internal/gateway/settings.go`, replace the body of `renderAgents` (search for `function renderAgents() {`). The current implementation puts every field directly inside `item`. The new implementation wraps the field stack inside an `agent-body` div and prepends an `agent-card-header`.

Replace the entire current function body — from `var p = document.getElementById('panel-agents');` through `sec.appendChild(addBtn);` and the closing `}` — with the following. Read every line; the changes are surgical.

```js
function renderAgents() {
    var p = document.getElementById('panel-agents');
    p.innerHTML = '';
    var sec = makeSection(p, null);
    var agents = (cfg.agents || {}).list || [];
    var list = document.createElement('div');
    list.className = 'dynamic-list';
    sec.appendChild(list);

    // Per-card expanded state. Map key = agent index. Closure-local —
    // rebuilt every render(). Force-rules below override.
    if (!renderAgents._expanded) renderAgents._expanded = {};
    var expanded = renderAgents._expanded;

    for (var i = 0; i < agents.length; i++) {
        (function(idx) {
            var a = agents[idx];
            var item = document.createElement('div');
            item.className = 'dynamic-item agent-card';

            var isOnly = agents.length === 1;
            var isJustAdded = !!a.__justAdded;
            // hasFieldError computed after body fields render; default to false here.
            var startsExpanded = isOnly || isJustAdded || !!a.default || !!expanded[idx];

            // === Header (always visible; click toggles when not isOnly) ===
            var header = document.createElement('div');
            header.className = 'agent-card-header';

            var chevron = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
            chevron.setAttribute('viewBox', '0 0 24 24');
            chevron.setAttribute('class', 'agent-card-chevron');
            chevron.innerHTML = '<path d="M6 9l6 6 6-6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>';
            header.appendChild(chevron);

            var hId = document.createElement('span');
            hId.className = 'agent-card-id';
            hId.textContent = a.id || '(new)';
            header.appendChild(hId);

            var hName = document.createElement('span');
            hName.className = 'agent-card-name';
            hName.textContent = a.name || '';
            header.appendChild(hName);

            if (a.default) {
                var badge = document.createElement('span');
                badge.className = 'agent-card-default-badge';
                badge.textContent = 'Default';
                header.appendChild(badge);
            }

            var hModel = document.createElement('span');
            hModel.className = 'agent-card-model';
            hModel.textContent = a.model || '';
            header.appendChild(hModel);

            item.appendChild(header);

            // Hide chevron and disable click when there's only one agent.
            if (isOnly) {
                chevron.style.visibility = 'hidden';
                header.style.cursor = 'default';
            } else {
                header.addEventListener('click', function(e) {
                    // Don't toggle if the user clicked a control inside the
                    // header (currently none, but future-proof for badges
                    // that become interactive).
                    if (e.target.closest('button, input, select, textarea')) return;
                    expanded[idx] = !item.classList.contains('collapsed') ? false : true;
                    item.classList.toggle('collapsed');
                });
            }

            // === Remove button (visible on header; stop propagation) ===
            var rm = document.createElement('button');
            rm.className = 'remove-btn';
            rm.innerHTML = '&times;';
            rm.onclick = function(e) {
                e.stopPropagation();
                cfg.agents.list.splice(idx, 1);
                renderAgents._expanded = {};  // indices shifted; reset
                render();
            };
            header.appendChild(rm);

            // === Body (collapsible) ===
            var body = document.createElement('div');
            body.className = 'agent-body';
            item.appendChild(body);

            var row1 = makeRow(body);
            var idField = makeField(row1, 'ID', 'text', a.id || '', function(v) {
                cfg.agents.list[idx].id = v;
                hId.textContent = v || '(new)';
            });
            (function(idx) {
                var inp = idField.querySelector('input');
                inp.setAttribute('pattern', '[a-zA-Z0-9._-]+');
                inp.setAttribute('required', '');
                validateField(idField, function(v) {
                    v = (v || '').trim();
                    if (!v) return 'ID is required.';
                    if (!/^[a-zA-Z0-9._-]+$/.test(v)) return 'ID may contain letters, digits, dot, dash, underscore.';
                    for (var j = 0; j < (cfg.agents.list || []).length; j++) {
                        if (j === idx) continue;
                        if ((cfg.agents.list[j].id || '').trim() === v) return 'Duplicate ID — must be unique.';
                    }
                    return '';
                });
            })(idx);

            var nameField = makeField(row1, 'Name', 'text', a.name || '', function(v) {
                cfg.agents.list[idx].name = v;
                hName.textContent = v || '';
            });
            nameField.querySelector('input').setAttribute('maxlength', '200');
            validateField(nameField, function(v) {
                if ((v || '').length > 200) return 'Name must be 200 characters or fewer.';
                return '';
            });

            var row2 = makeRow(body);
            var modelField = makeField(row2, 'Model', 'text', a.model || '', function(v) {
                cfg.agents.list[idx].model = v;
                hModel.textContent = v || '';
            });
            // modelField return value unused but keeps the variable line readable.
            void modelField;
            makeField(row2, 'Max Turns', 'number', a.maxTurns || 0, function(v) { cfg.agents.list[idx].maxTurns = v; });

            makeField(body, 'Context Window (0 = auto-detect)', 'number', a.contextWindow || 0, function(v) {
                cfg.agents.list[idx].contextWindow = v;
            });

            var row3 = makeRow(body);
            var reasonField = makeField(row3, 'Reasoning', 'select', a.reasoning || 'off', function(v) {
                cfg.agents.list[idx].reasoning = (v === 'off') ? '' : v;
            });
            reasonField.querySelector('select').innerHTML =
                '<option value="off">off</option>' +
                '<option value="low">low</option>' +
                '<option value="medium">medium</option>' +
                '<option value="high">high</option>';
            reasonField.querySelector('select').value = a.reasoning || 'off';
            var reasonHint = document.createElement('span');
            reasonHint.style.cssText = 'color:var(--color-text-muted); font-size:0.78rem; line-height:1.4;';
            reasonHint.textContent = 'Maps to Anthropic thinking budget, OpenAI reasoning_effort, Gemini ThinkingConfig, Qwen enable_thinking. Ignored by models that do not support extended reasoning.';
            row3.appendChild(reasonHint);

            var togglesRow = makeRow(body);
            makeField(togglesRow, 'Subagent (callable via task tool)', 'toggle', !!a.subagent, function(v) {
                cfg.agents.list[idx].subagent = v;
            });
            makeField(togglesRow, 'Inherit Context (subagent sees parent history)', 'toggle', !!a.inheritContext, function(v) {
                cfg.agents.list[idx].inheritContext = v;
            });

            // Default toggle: radio-style. Setting on clears others, re-renders.
            makeField(body, 'Default agent (shown first in chat dropdown)', 'toggle', !!a.default, function(v) {
                if (v) {
                    for (var j = 0; j < cfg.agents.list.length; j++) {
                        cfg.agents.list[j].default = (j === idx);
                    }
                } else {
                    cfg.agents.list[idx].default = false;
                }
                render();
            });

            makeField(body, 'Subagent Description (shown to supervisor; required when Subagent is on)', 'text', a.description || '', function(v) {
                cfg.agents.list[idx].description = v;
            });

            makeReadOnlyField(body, 'Sandbox', 'sandbox-' + idx, 'not implemented yet');

            makeField(body, 'System Prompt', 'textarea', a.system_prompt || '', function(v) {
                cfg.agents.list[idx].system_prompt = v;
            });

            makeToolsCheckboxes(body, idx, a);

            // === Apply initial collapsed state ===
            // Also force-expand if any field already has an error on first paint
            // (e.g. user submitted invalid Save and came back).
            var hasErr = body.querySelector('.field-error');
            if (!isOnly && !startsExpanded && !hasErr) {
                item.classList.add('collapsed');
            }

            // Consume the __justAdded sentinel after first render.
            if (a.__justAdded) delete cfg.agents.list[idx].__justAdded;

            list.appendChild(item);
        })(i);
    }

    var addBtn = document.createElement('button');
    addBtn.className = 'add-btn';
    addBtn.textContent = '+ Add Agent';
    addBtn.onclick = function() {
        if (!cfg.agents) cfg.agents = {list: []};
        if (!cfg.agents.list) cfg.agents.list = [];
        cfg.agents.list.push({id: '', name: '', model: '', tools: {allow: []}, __justAdded: true});
        render();
        focusAndFlashNewRow(list);
    };
    sec.appendChild(addBtn);
}
```

Critical details about this rewrite:

1. The function attaches state to itself: `renderAgents._expanded`. Static-on-function-object is the cheapest closure for JS module state without rewriting the IIFE shape.
2. `.dynamic-item.collapsed` is the CSS hook (Step 8 defined `.dynamic-item.collapsed .agent-body { display: none; }`).
3. The header's `click` listener checks for inner buttons/inputs to avoid swallowing remove-button clicks. The remove button additionally calls `e.stopPropagation()` for double safety.
4. After delete (remove button), `renderAgents._expanded = {}` resets the state map because the index→agent mapping shifted.
5. Header ID/name/model labels update live as the form fields edit them (the field callbacks update `hId.textContent`, `hName.textContent`, `hModel.textContent`). No re-render needed for those edits.
6. The Default toggle re-renders the whole panel — that's intentional. Toggle is rare; re-render is the simplest way to (a) collapse other cards that were forced-expanded by being default, and (b) refresh the badge.

- [ ] **Step 10: Build**

```bash
cd ~/projects/felix && go build ./... 2>&1 | tail -10
```

Expected: clean. If errors mention:
- `slog undefined` → add `"log/slog"` to `internal/config/config.go` imports.
- `a.Default undefined` → Step 3 didn't take.
- `expected ';'` inside chat.go or settings.go → likely a stray backtick in your edit closed the Go raw-string early. Search the edit region for `` ` `` and replace with a quote or apostrophe.

- [ ] **Step 11: Run all tests**

```bash
cd ~/projects/felix && go test -count=1 ./... 2>&1 | grep -E "FAIL|^ok " | tail -20
```

Expected: all green. If a config test you didn't touch starts failing because it now sees an extra "default" field in JSON output, double-check that you used `json:"default,omitempty"` in Step 3.

- [ ] **Step 12: Manual smoke — start server**

Stage a config with two agents so you can see the collapse behavior:

```bash
mkdir -p /tmp/felix-agents-smoke && rm -rf /tmp/felix-agents-smoke/.felix
cat > /tmp/felix-agents-smoke/felix.json5 <<'EOF'
{
  "gateway": {"host": "127.0.0.1", "port": 18895},
  "providers": {"anthropic": {"kind": "anthropic", "base_url": "https://api.anthropic.com", "api_key": "STUB"}},
  "agents": {"list": [
    {"id": "alpha", "name": "Alpha", "model": "anthropic/claude-sonnet-4-6", "workspace": "/tmp/felix-agents-smoke/ws", "sandbox": "none", "tools": {"allow": []}, "default": true},
    {"id": "beta",  "name": "Beta",  "model": "anthropic/claude-sonnet-4-6", "workspace": "/tmp/felix-agents-smoke/ws", "sandbox": "none", "tools": {"allow": []}}
  ]},
  "memory": {"enabled": false},
  "cortex": {"enabled": false}
}
EOF
mkdir -p /tmp/felix-agents-smoke/ws
cd ~/projects/felix && go build -o /tmp/felix-agents-smoke/felix-bin ./cmd/felix
cd /tmp/felix-agents-smoke && FELIX_HOME=/tmp/felix-agents-smoke/.felix HOME=/tmp/felix-agents-smoke ./felix-bin start --config /tmp/felix-agents-smoke/felix.json5 &
sleep 2
```

- [ ] **Step 13: Manual smoke — verify behavior**

Open `http://127.0.0.1:18895/settings#agents` in a browser. Verify:

1. The "Alpha" card is expanded; its header shows a "DEFAULT" badge to the right of the name.
2. The "Beta" card is collapsed; clicking its header expands it (chevron rotates).
3. Inside the expanded Beta card, toggle "Default agent" ON. The page re-renders. Alpha's badge disappears, Beta's appears. Alpha collapses (since it's no longer default), Beta stays expanded.
4. Click Save. Page persists. Reload `/settings#agents`. Beta still has the badge, Alpha is collapsed.
5. Open `/chat`. Open the agent dropdown. Beta should be the first option.

Now stop the server:

```bash
kill %1 2>/dev/null; rm -rf /tmp/felix-agents-smoke
```

If any of the 5 checks fail, stop and report which one with what you observed.

- [ ] **Step 14: Commit**

```bash
cd ~/projects/felix
git add internal/config/config.go internal/config/config_test.go \
    internal/gateway/websocket.go internal/gateway/chat.go internal/gateway/settings.go
git commit -m "feat(agents): per-agent Default flag + collapse in Settings UI

Adds AgentConfig.Default (json: 'default,omitempty'). Config.Validate
enforces at-most-one-default and silently clears duplicates with a
slog warning.

handleAgentStatus emits the flag per agent; the chat-UI agent.status
handler stable-sorts default-first before populating agentSelect, so
fresh sessions land on the default while localStorage of last-picked
still wins on reload.

renderAgents in settings.go is refactored to render a clickable header
(id, name, model, optional DEFAULT badge, remove button) plus a
collapsible body. Header click toggles. Single-agent case has no
chevron and stays expanded. Newly added agents force-expand via a
transient __justAdded flag. Validation errors keep the card open.
Toggling Default re-renders the whole panel so badges + collapse
state refresh."
```

---

## Self-review checklist (run by the coordinator after the implementer reports done)

1. `go build ./...` clean.
2. `go test -count=1 ./...` all green; in particular `TestValidate_ClearsMultipleDefaults`.
3. `go vet ./...` quiet.
4. Browser smoke from Step 13 passes all 5 checks.
5. One commit, message matches the template above.

---

## If a step blocks

- **Validation rule places `Default` cleanup before Validate's existing "at least one agent" check** → that's fine; the cleanup is a side-effect, not a precondition. If the agent list is empty, the for-loop simply doesn't run.
- **`hasErr` check in Step 9 always returns null on first paint** because validateField hasn't fired yet → that's expected; the spec lists this as "card with a failed validation field stays expanded" meaning AFTER the user submits + Save fails. First-paint state with no prior submission has no field-error class to find. Leave the logic as written; it's correct for the submitted-and-came-back path.
- **The chat dropdown's localStorage logic clobbers the default selection** → that's intentional per the spec ("don't override deliberate user choice"). Don't try to make default override localStorage.
- **Add-Agent's `__justAdded` flag leaks into the saved config** → the consume-after-render code at the bottom of the per-card block in Step 9 deletes it. If you still see it persist into felix.json5 after a Save, verify the `delete cfg.agents.list[idx].__justAdded;` line executed (add a console.log temporarily). The `__` prefix and explicit delete are designed to keep it out of the serialized payload.
- **CSS color-mix() not supported** → felix's existing chat UI already uses `color-mix(in oklch, ...)`, so support is assumed. If a target browser breaks, that's a wider felix issue, not this task.

## Out of scope (DO NOT do as part of this task)

- Adding a drag-handle for reordering agents in Settings.
- Surfacing "Default" anywhere in the cron-tool or task-tool flows.
- Changing the chat dropdown's appearance beyond the sort order.
- Touching any file outside the 5 listed in File Structure above.
