package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/sausheong/felix/internal/config"
)

// SettingsHandlers holds the HTTP handlers for the settings page and config API.
type SettingsHandlers struct {
	Page       http.HandlerFunc
	GetConfig  http.HandlerFunc
	SaveConfig http.HandlerFunc
}

// NewSettingsHandlers returns handlers for the settings page and config REST API.
func NewSettingsHandlers(cfg *config.Config, onSave func(*config.Config)) *SettingsHandlers {
	return &SettingsHandlers{
		Page: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprint(w, settingsHTML)
		},

		GetConfig: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			data, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				http.Error(w, `{"error":"marshal config"}`, http.StatusInternalServerError)
				return
			}
			w.Write(data)
		},

		SaveConfig: func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
			if err != nil {
				http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
				return
			}

			var newCfg config.Config
			if err := json.Unmarshal(body, &newCfg); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"error":"invalid JSON: %s"}`, err.Error())
				return
			}

			if err := newCfg.Validate(); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"error":"validation: %s"}`, err.Error())
				return
			}

			// Copy path from current config so Save writes to the right file.
			newCfg.SetPath(cfg.Path())

			if err := newCfg.Save(); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(w, `{"error":"save: %s"}`, err.Error())
				return
			}

			slog.Info("config saved via settings page")

			// Trigger hot-reload callback if configured.
			if onSave != nil {
				onSave(&newCfg)
			}

			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true}`)
		},
	}
}

const settingsHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Felix Settings</title>
<style>
:root {
	--bg: #1a1a2e;
	--bg-header: #16213e;
	--bg-card: #16213e;
	--bg-input: #0d1b36;
	--border: #0f3460;
	--text: #e0e0e0;
	--text-muted: #888;
	--text-strong: #fff;
	--accent: #16dbaa;
	--accent2: #53a8b6;
	--btn-text: #1a1a2e;
	--placeholder: #555;
	--error: #e74c3c;
	--success: #27ae60;
}
html.light {
	--bg: #f5f5f5;
	--bg-header: #ffffff;
	--bg-card: #ffffff;
	--bg-input: #ffffff;
	--border: #ddd;
	--text: #1a1a1a;
	--text-muted: #777;
	--text-strong: #000;
	--accent: #0fa888;
	--accent2: #3a7f8c;
	--btn-text: #fff;
	--placeholder: #999;
	--error: #d32f2f;
	--success: #219a52;
}
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
	font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, monospace;
	background: var(--bg);
	color: var(--text);
	min-height: 100vh;
	transition: background 0.3s, color 0.3s;
}
#header {
	background: var(--bg-header);
	padding: 0.75rem 1.5rem;
	border-bottom: 1px solid var(--border);
	display: flex;
	align-items: center;
	gap: 0.75rem;
}
#header h1 { font-size: 1.1rem; color: var(--accent); }
#header .spacer { margin-left: auto; }
.header-btn {
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.3rem 0.5rem;
	cursor: pointer;
	font-size: 0.8rem;
	line-height: 1;
	color: var(--text);
	transition: border-color 0.3s;
}
.header-btn:hover { border-color: var(--accent); }
#save-btn {
	background: var(--accent);
	color: var(--btn-text);
	border: none;
	border-radius: 6px;
	padding: 0.4rem 1rem;
	font-size: 0.85rem;
	font-weight: 600;
	cursor: pointer;
}
#save-btn:hover { opacity: 0.85; }
#save-btn:disabled { opacity: 0.4; cursor: not-allowed; }
#status-msg {
	font-size: 0.8rem;
	margin-left: 0.5rem;
}
#content {
	max-width: 800px;
	margin: 1.5rem auto;
	padding: 0 1.5rem 3rem;
}
.section {
	background: var(--bg-card);
	border: 1px solid var(--border);
	border-radius: 10px;
	padding: 1.25rem;
	margin-bottom: 1rem;
}
.section h2 {
	font-size: 0.95rem;
	color: var(--accent2);
	margin-bottom: 1rem;
	border-bottom: 1px solid var(--border);
	padding-bottom: 0.5rem;
}
.field {
	margin-bottom: 0.75rem;
	display: flex;
	align-items: center;
	gap: 0.75rem;
}
.field label {
	min-width: 140px;
	font-size: 0.85rem;
	color: var(--text-muted);
	flex-shrink: 0;
}
.field input[type="text"],
.field input[type="number"],
.field input[type="password"] {
	flex: 1;
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.4rem 0.6rem;
	color: var(--text);
	font-family: "SF Mono", "Fira Code", monospace;
	font-size: 0.85rem;
	outline: none;
}
.field input:focus { border-color: var(--accent); }
.field select {
	flex: 1;
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.4rem 0.6rem;
	color: var(--text);
	font-family: inherit;
	font-size: 0.85rem;
	outline: none;
}
.field textarea {
	flex: 1;
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.4rem 0.6rem;
	color: var(--text);
	font-family: "SF Mono", "Fira Code", monospace;
	font-size: 0.8rem;
	outline: none;
	resize: vertical;
	min-height: 60px;
}
.field textarea:focus { border-color: var(--accent); }
.toggle {
	position: relative;
	width: 40px;
	min-width: 40px;
	height: 22px;
	flex: none;
}
.field label.toggle {
	min-width: 40px;
	width: 40px;
}
.toggle input {
	opacity: 0;
	width: 0;
	height: 0;
}
.toggle .slider {
	position: absolute;
	cursor: pointer;
	top: 0; left: 0; right: 0; bottom: 0;
	background: var(--border);
	border-radius: 22px;
	transition: 0.3s;
}
.toggle .slider:before {
	content: "";
	position: absolute;
	height: 16px;
	width: 16px;
	left: 3px;
	bottom: 3px;
	background: var(--text);
	border-radius: 50%;
	transition: 0.3s;
}
.toggle input:checked + .slider { background: var(--accent); }
.toggle input:checked + .slider:before { transform: translateX(18px); }
.dynamic-list { margin-top: 0.5rem; }
.dynamic-item {
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 8px;
	padding: 0.75rem;
	margin-bottom: 0.5rem;
	position: relative;
}
.dynamic-item .field { margin-bottom: 0.5rem; }
.dynamic-item .field:last-child { margin-bottom: 0; }
.remove-btn {
	position: absolute;
	top: 0.5rem;
	right: 0.5rem;
	background: none;
	border: none;
	color: var(--error);
	cursor: pointer;
	font-size: 1.1rem;
	line-height: 1;
	padding: 0.2rem;
}
.remove-btn:hover { opacity: 0.7; }
.add-btn {
	background: none;
	border: 1px dashed var(--border);
	border-radius: 6px;
	padding: 0.4rem 0.75rem;
	color: var(--accent2);
	cursor: pointer;
	font-size: 0.8rem;
	width: 100%;
	margin-top: 0.25rem;
}
.add-btn:hover { border-color: var(--accent); color: var(--accent); }
</style>
</head>
<body>
<div id="header">
	<h1>Felix Settings</h1>
	<span class="spacer"></span>
	<span id="status-msg"></span>
	<button id="save-btn" disabled>Save</button>
	<button class="header-btn" id="theme-btn" title="Toggle light/dark mode">&#9790;</button>
</div>
<div id="content">
	<div id="loading" style="text-align:center;padding:3rem;color:var(--text-muted)">Loading configuration...</div>
</div>

<script>
(function() {
	var content = document.getElementById('content');
	var saveBtn = document.getElementById('save-btn');
	var statusMsg = document.getElementById('status-msg');
	var themeBtn = document.getElementById('theme-btn');
	var cfg = null;

	// Theme
	function setTheme(mode) {
		if (mode === 'light') {
			document.documentElement.classList.add('light');
			themeBtn.innerHTML = '&#9728;';
		} else {
			document.documentElement.classList.remove('light');
			themeBtn.innerHTML = '&#9790;';
		}
		localStorage.setItem('felix-theme', mode);
	}
	setTheme(localStorage.getItem('felix-theme') || 'dark');
	themeBtn.addEventListener('click', function() {
		var cur = document.documentElement.classList.contains('light') ? 'light' : 'dark';
		setTheme(cur === 'light' ? 'dark' : 'light');
	});

	function showStatus(msg, isError) {
		statusMsg.textContent = msg;
		statusMsg.style.color = isError ? 'var(--error)' : 'var(--success)';
		if (!isError) setTimeout(function() { statusMsg.textContent = ''; }, 3000);
	}

	// Load config
	fetch(location.pathname + '/api/config')
		.then(function(r) { return r.json(); })
		.then(function(data) {
			cfg = data;
			render();
			saveBtn.disabled = false;
		})
		.catch(function(err) {
			content.innerHTML = '<div style="color:var(--error);padding:2rem">Failed to load config: ' + err.message + '</div>';
		});

	// Save
	saveBtn.addEventListener('click', function() {
		collectFromForm();
		saveBtn.disabled = true;
		fetch(location.pathname + '/api/config', {
			method: 'POST',
			headers: {'Content-Type': 'application/json'},
			body: JSON.stringify(cfg)
		})
		.then(function(r) { return r.json().then(function(d) { return {ok: r.ok, data: d}; }); })
		.then(function(res) {
			saveBtn.disabled = false;
			if (res.data.ok) {
				showStatus('Saved', false);
			} else {
				showStatus('Error: ' + (res.data.error || 'unknown'), true);
			}
		})
		.catch(function(err) {
			saveBtn.disabled = false;
			showStatus('Error: ' + err.message, true);
		});
	});

	function render() {
		content.innerHTML = '';
		renderGateway();
		renderProviders();
		renderAgents();
		renderChannels();
		renderMemory();
		renderCortex();
		renderHeartbeat();
		renderSecurity();
	}

	function makeSection(title) {
		var s = document.createElement('div');
		s.className = 'section';
		var h = document.createElement('h2');
		h.textContent = title;
		s.appendChild(h);
		content.appendChild(s);
		return s;
	}

	function makeField(parent, label, type, value, onChange) {
		var f = document.createElement('div');
		f.className = 'field';
		var l = document.createElement('label');
		l.textContent = label;
		f.appendChild(l);

		if (type === 'toggle') {
			var t = document.createElement('label');
			t.className = 'toggle';
			var inp = document.createElement('input');
			inp.type = 'checkbox';
			inp.checked = !!value;
			inp.addEventListener('change', function() { onChange(inp.checked); });
			var sl = document.createElement('span');
			sl.className = 'slider';
			t.appendChild(inp);
			t.appendChild(sl);
			f.appendChild(t);
		} else if (type === 'select') {
			// value = {value, options}
			var sel = document.createElement('select');
			for (var i = 0; i < value.options.length; i++) {
				var opt = document.createElement('option');
				opt.value = value.options[i];
				opt.textContent = value.options[i];
				if (value.options[i] === value.value) opt.selected = true;
				sel.appendChild(opt);
			}
			sel.addEventListener('change', function() { onChange(sel.value); });
			f.appendChild(sel);
		} else if (type === 'textarea') {
			var ta = document.createElement('textarea');
			ta.value = value || '';
			ta.addEventListener('input', function() { onChange(ta.value); });
			f.appendChild(ta);
		} else {
			var inp = document.createElement('input');
			inp.type = type || 'text';
			inp.value = value != null ? value : '';
			inp.addEventListener('input', function() {
				onChange(type === 'number' ? parseInt(inp.value, 10) || 0 : inp.value);
			});
			f.appendChild(inp);
		}

		parent.appendChild(f);
		return f;
	}

	function renderGateway() {
		var s = makeSection('Gateway');
		var gw = cfg.gateway || {};
		makeField(s, 'Host', 'text', gw.host, function(v) { cfg.gateway.host = v; });
		makeField(s, 'Port', 'number', gw.port, function(v) { cfg.gateway.port = v; });
		makeField(s, 'Auth Token', 'text', (gw.auth || {}).token, function(v) {
			if (!cfg.gateway.auth) cfg.gateway.auth = {};
			cfg.gateway.auth.token = v;
		});
		makeField(s, 'Reload Mode', 'select', {value: (gw.reload || {}).mode || 'hybrid', options: ['hybrid', 'manual', 'auto-restart']}, function(v) {
			if (!cfg.gateway.reload) cfg.gateway.reload = {};
			cfg.gateway.reload.mode = v;
		});
	}

	function renderProviders() {
		var s = makeSection('Providers');
		var providers = cfg.providers || {};
		var names = Object.keys(providers);

		for (var i = 0; i < names.length; i++) {
			(function(name) {
				var p = providers[name];
				var item = document.createElement('div');
				item.className = 'dynamic-item';

				var title = document.createElement('div');
				title.style.cssText = 'font-weight:600;color:var(--text-strong);margin-bottom:0.5rem;font-size:0.9rem;';
				title.textContent = name;
				item.appendChild(title);

				var rm = document.createElement('button');
				rm.className = 'remove-btn';
				rm.innerHTML = '&times;';
				rm.onclick = function() { delete cfg.providers[name]; render(); };
				item.appendChild(rm);

				makeField(item, 'Kind', 'text', p.kind || '', function(v) { cfg.providers[name].kind = v; });
				makeField(item, 'API Key', 'text', p.api_key || '', function(v) { cfg.providers[name].api_key = v; });
				makeField(item, 'Base URL', 'text', p.base_url || '', function(v) { cfg.providers[name].base_url = v; });

				s.appendChild(item);
			})(names[i]);
		}

		var addBtn = document.createElement('button');
		addBtn.className = 'add-btn';
		addBtn.textContent = '+ Add Provider';
		addBtn.onclick = function() {
			var name = prompt('Provider name (e.g. openai, anthropic, ollama):');
			if (!name) return;
			if (!cfg.providers) cfg.providers = {};
			cfg.providers[name] = {kind: '', api_key: '', base_url: ''};
			render();
		};
		s.appendChild(addBtn);
	}

	function renderAgents() {
		var s = makeSection('Agents');
		var agents = (cfg.agents || {}).list || [];

		for (var i = 0; i < agents.length; i++) {
			(function(idx) {
				var a = agents[idx];
				var item = document.createElement('div');
				item.className = 'dynamic-item';

				var rm = document.createElement('button');
				rm.className = 'remove-btn';
				rm.innerHTML = '&times;';
				rm.onclick = function() { cfg.agents.list.splice(idx, 1); render(); };
				item.appendChild(rm);

				makeField(item, 'ID', 'text', a.id, function(v) { cfg.agents.list[idx].id = v; });
				makeField(item, 'Name', 'text', a.name, function(v) { cfg.agents.list[idx].name = v; });
				makeField(item, 'Model', 'text', a.model, function(v) { cfg.agents.list[idx].model = v; });
				makeField(item, 'Sandbox', 'select', {value: a.sandbox || 'none', options: ['none', 'docker', 'namespace']}, function(v) { cfg.agents.list[idx].sandbox = v; });
				makeField(item, 'Max Turns', 'number', a.maxTurns || 0, function(v) { cfg.agents.list[idx].maxTurns = v; });
				makeField(item, 'System Prompt', 'textarea', a.system_prompt || '', function(v) { cfg.agents.list[idx].system_prompt = v; });
				makeField(item, 'Allowed Tools', 'text', ((a.tools || {}).allow || []).join(', '), function(v) {
					if (!cfg.agents.list[idx].tools) cfg.agents.list[idx].tools = {};
					cfg.agents.list[idx].tools.allow = v.split(',').map(function(s) { return s.trim(); }).filter(Boolean);
				});
				makeField(item, 'Denied Tools', 'text', ((a.tools || {}).deny || []).join(', '), function(v) {
					if (!cfg.agents.list[idx].tools) cfg.agents.list[idx].tools = {};
					cfg.agents.list[idx].tools.deny = v.split(',').map(function(s) { return s.trim(); }).filter(Boolean);
				});

				s.appendChild(item);
			})(i);
		}

		var addBtn = document.createElement('button');
		addBtn.className = 'add-btn';
		addBtn.textContent = '+ Add Agent';
		addBtn.onclick = function() {
			if (!cfg.agents) cfg.agents = {list: []};
			if (!cfg.agents.list) cfg.agents.list = [];
			cfg.agents.list.push({id: '', name: '', model: '', sandbox: 'none', tools: {allow: []}});
			render();
		};
		s.appendChild(addBtn);
	}

	function renderChannels() {
		var s = makeSection('Channels');
		var ch = cfg.channels || {};
		var cli = ch.cli || {};
		var tg = ch.telegram || {};

		makeField(s, 'CLI Enabled', 'toggle', cli.enabled, function(v) {
			if (!cfg.channels) cfg.channels = {};
			if (!cfg.channels.cli) cfg.channels.cli = {};
			cfg.channels.cli.enabled = v;
		});
		makeField(s, 'Telegram Token', 'text', tg.token || '', function(v) {
			if (!cfg.channels) cfg.channels = {};
			if (!cfg.channels.telegram) cfg.channels.telegram = {};
			cfg.channels.telegram.token = v;
		});
		makeField(s, 'Telegram Mode', 'select', {value: tg.mode || 'polling', options: ['polling', 'webhook']}, function(v) {
			if (!cfg.channels) cfg.channels = {};
			if (!cfg.channels.telegram) cfg.channels.telegram = {};
			cfg.channels.telegram.mode = v;
		});
	}

	function renderMemory() {
		var s = makeSection('Memory');
		var m = cfg.memory || {};
		makeField(s, 'Enabled', 'toggle', m.enabled, function(v) {
			if (!cfg.memory) cfg.memory = {};
			cfg.memory.enabled = v;
		});
		makeField(s, 'Embedding Provider', 'text', m.embeddingProvider || '', function(v) {
			if (!cfg.memory) cfg.memory = {};
			cfg.memory.embeddingProvider = v;
		});
		makeField(s, 'Embedding Model', 'text', m.embeddingModel || '', function(v) {
			if (!cfg.memory) cfg.memory = {};
			cfg.memory.embeddingModel = v;
		});
	}

	function renderCortex() {
		var s = makeSection('Cortex');
		var cx = cfg.cortex || {};
		makeField(s, 'Enabled', 'toggle', cx.enabled, function(v) {
			if (!cfg.cortex) cfg.cortex = {};
			cfg.cortex.enabled = v;
		});
		makeField(s, 'DB Path', 'text', cx.dbPath || '', function(v) {
			if (!cfg.cortex) cfg.cortex = {};
			cfg.cortex.dbPath = v;
		});
		makeField(s, 'API Key', 'password', cx.apiKey || '', function(v) {
			if (!cfg.cortex) cfg.cortex = {};
			cfg.cortex.apiKey = v;
		});
		makeField(s, 'LLM Model', 'text', cx.llmModel || '', function(v) {
			if (!cfg.cortex) cfg.cortex = {};
			cfg.cortex.llmModel = v;
		});
	}

	function renderHeartbeat() {
		var s = makeSection('Heartbeat');
		var hb = cfg.heartbeat || {};
		makeField(s, 'Enabled', 'toggle', hb.enabled, function(v) {
			if (!cfg.heartbeat) cfg.heartbeat = {};
			cfg.heartbeat.enabled = v;
		});
		makeField(s, 'Interval', 'text', hb.interval || '30m', function(v) {
			if (!cfg.heartbeat) cfg.heartbeat = {};
			cfg.heartbeat.interval = v;
		});
	}

	function renderSecurity() {
		var s = makeSection('Security');
		var sec = cfg.security || {};
		var exec = sec.execApprovals || {};
		var dm = sec.dmPolicy || {};
		var grp = sec.groupPolicy || {};

		makeField(s, 'Exec Level', 'select', {value: exec.level || 'full', options: ['full', 'allowlist', 'deny']}, function(v) {
			if (!cfg.security) cfg.security = {};
			if (!cfg.security.execApprovals) cfg.security.execApprovals = {};
			cfg.security.execApprovals.level = v;
		});
		makeField(s, 'Exec Allowlist', 'text', (exec.allowlist || []).join(', '), function(v) {
			if (!cfg.security) cfg.security = {};
			if (!cfg.security.execApprovals) cfg.security.execApprovals = {};
			cfg.security.execApprovals.allowlist = v.split(',').map(function(s) { return s.trim(); }).filter(Boolean);
		});
		makeField(s, 'Unknown Senders', 'select', {value: dm.unknownSenders || 'ignore', options: ['ignore', 'respond', 'notify']}, function(v) {
			if (!cfg.security) cfg.security = {};
			if (!cfg.security.dmPolicy) cfg.security.dmPolicy = {};
			cfg.security.dmPolicy.unknownSenders = v;
		});
		makeField(s, 'Require Mention', 'toggle', grp.requireMention, function(v) {
			if (!cfg.security) cfg.security = {};
			if (!cfg.security.groupPolicy) cfg.security.groupPolicy = {};
			cfg.security.groupPolicy.requireMention = v;
		});
	}

	function collectFromForm() {
		// cfg is already updated in real-time via onChange callbacks
	}
})();
</script>
</body>
</html>`
