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

			// Update the in-memory config so the GET handler returns fresh values.
			cfg.UpdateFrom(&newCfg)

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
/* === Custom Properties === */
:root {
	--color-primary: #2563eb;
	--color-primary-hover: #1d4ed8;
	--color-bg: #f8fafc;
	--color-surface: #ffffff;
	--color-text: #1e293b;
	--color-text-muted: #64748b;
	--color-border: #e2e8f0;
	--color-error: #dc2626;
	--color-success: #16a34a;
	--radius: 8px;
	--shadow: 0 1px 3px rgba(0,0,0,0.1);
	--shadow-md: 0 4px 6px rgba(0,0,0,0.1);
}
html.dark {
	--color-primary: #3b82f6;
	--color-primary-hover: #60a5fa;
	--color-bg: #0f172a;
	--color-surface: #1e293b;
	--color-text: #e2e8f0;
	--color-text-muted: #94a3b8;
	--color-border: #334155;
	--color-error: #ef4444;
	--color-success: #22c55e;
}

/* === Reset === */
*, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
body {
	font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
	background: var(--color-bg);
	color: var(--color-text);
	min-height: 100vh;
	line-height: 1.6;
}

/* === Header / Nav === */
#header {
	background: var(--color-surface);
	border-bottom: 1px solid var(--color-border);
	padding: 0.75rem 1.5rem;
	box-shadow: var(--shadow);
	display: flex;
	align-items: center;
	gap: 0.75rem;
	position: sticky;
	top: 0;
	z-index: 10;
}
#header h1 { font-size: 1.1rem; font-weight: 700; color: var(--color-text); }
.spacer { margin-left: auto; }
#status-msg { font-size: 0.85rem; }
#status-msg.success { color: var(--color-success); }
#status-msg.error { color: var(--color-error); }

/* === Buttons === */
.btn-primary {
	display: inline-flex;
	align-items: center;
	padding: 0.45rem 1rem;
	background: var(--color-primary);
	color: #fff;
	border: none;
	border-radius: var(--radius);
	font-size: 0.875rem;
	font-weight: 500;
	cursor: pointer;
	transition: background 0.15s;
}
.btn-primary:hover { background: var(--color-primary-hover); }
.btn-primary:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-icon {
	background: var(--color-surface);
	border: 1px solid var(--color-border);
	border-radius: var(--radius);
	padding: 0.3rem 0.55rem;
	cursor: pointer;
	font-size: 1rem;
	line-height: 1;
	color: var(--color-text);
	transition: border-color 0.15s;
}
.btn-icon:hover { border-color: var(--color-primary); }

/* === Main Layout === */
main { padding: 2rem 0 4rem; }
.container { max-width: 960px; margin: 0 auto; padding: 0 1.5rem; }

/* === Settings Card === */
.settings-wide {
	background: var(--color-surface);
	border: 1px solid var(--color-border);
	border-radius: var(--radius);
	padding: 2rem;
	box-shadow: var(--shadow-md);
}

/* === Finger Tabs === */
.finger-tabs {
	display: flex;
	gap: 0;
	border-bottom: 2px solid var(--color-border);
	margin-bottom: 1.75rem;
	overflow-x: auto;
	scrollbar-width: none;
	-ms-overflow-style: none;
}
.finger-tabs::-webkit-scrollbar { display: none; }
.finger-tab {
	padding: 0.6rem 1.25rem;
	font-size: 0.9rem;
	font-weight: 500;
	color: var(--color-text-muted);
	cursor: pointer;
	border: none;
	background: none;
	border-bottom: 2px solid transparent;
	margin-bottom: -2px;
	white-space: nowrap;
	transition: color 0.15s, border-color 0.15s;
}
.finger-tab:hover { color: var(--color-text); }
.finger-tab.active {
	color: var(--color-primary);
	border-bottom-color: var(--color-primary);
}
.finger-panel { display: none; }
.finger-panel.active { display: block; }

/* === Form Groups (label above input) === */
.form-group { margin-bottom: 1rem; }
.form-group > label {
	display: block;
	font-size: 0.875rem;
	font-weight: 500;
	margin-bottom: 0.3rem;
	color: var(--color-text);
}
.form-group input[type="text"],
.form-group input[type="password"],
.form-group input[type="number"],
.form-group select,
.form-group textarea {
	width: 100%;
	padding: 0.5rem 0.75rem;
	border: 1px solid var(--color-border);
	border-radius: var(--radius);
	font-size: 0.9rem;
	background: var(--color-surface);
	color: var(--color-text);
	font-family: inherit;
	transition: border-color 0.15s, box-shadow 0.15s;
}
.form-group input:focus,
.form-group select:focus,
.form-group textarea:focus {
	outline: none;
	border-color: var(--color-primary);
	box-shadow: 0 0 0 3px rgba(37,99,235,0.15);
}
.form-group textarea {
	min-height: 80px;
	resize: vertical;
	font-family: "SF Mono", "Fira Code", monospace;
	font-size: 0.85rem;
}
html.dark .form-group input,
html.dark .form-group select,
html.dark .form-group textarea { background: #0f172a; }

/* === 2-column Row === */
.form-row {
	display: grid;
	grid-template-columns: 1fr 1fr;
	gap: 1rem;
}

/* === Toggle Group === */
.toggle-group {
	display: flex;
	align-items: center;
	gap: 0.65rem;
	margin-bottom: 1rem;
}
.toggle-label {
	font-size: 0.875rem;
	font-weight: 500;
	color: var(--color-text);
}
.toggle {
	position: relative;
	width: 40px;
	height: 22px;
	flex-shrink: 0;
}
.toggle input { opacity: 0; width: 0; height: 0; position: absolute; }
.toggle .slider {
	position: absolute;
	cursor: pointer;
	top: 0; left: 0; right: 0; bottom: 0;
	background: var(--color-border);
	border-radius: 22px;
	transition: 0.25s;
}
.toggle .slider:before {
	content: "";
	position: absolute;
	height: 16px;
	width: 16px;
	left: 3px;
	bottom: 3px;
	background: #fff;
	border-radius: 50%;
	transition: 0.25s;
}
.toggle input:checked + .slider { background: var(--color-primary); }
.toggle input:checked + .slider:before { transform: translateX(18px); }

/* === Panel Sections (sub-headings within a panel) === */
.panel-section { margin-bottom: 0.25rem; }
.panel-section + .panel-section {
	margin-top: 1.5rem;
	padding-top: 1.25rem;
	border-top: 1px solid var(--color-border);
}
.panel-section h3 {
	font-size: 1rem;
	font-weight: 600;
	color: var(--color-text);
	margin-bottom: 1rem;
}

/* === Dynamic Cards (Providers / Agents) === */
.dynamic-list { display: flex; flex-direction: column; gap: 0.75rem; margin-bottom: 0.75rem; }
.dynamic-item {
	background: var(--color-bg);
	border: 1px solid var(--color-border);
	border-radius: var(--radius);
	padding: 1rem 1rem 0.25rem;
	position: relative;
}
.dynamic-item-title {
	font-weight: 600;
	font-size: 0.9rem;
	color: var(--color-text);
	margin-bottom: 0.75rem;
}
.remove-btn {
	position: absolute;
	top: 0.75rem;
	right: 0.75rem;
	background: none;
	border: none;
	color: var(--color-error);
	cursor: pointer;
	font-size: 1.1rem;
	line-height: 1;
	padding: 0.1rem 0.25rem;
	opacity: 0.6;
	border-radius: 4px;
}
.remove-btn:hover { opacity: 1; background: rgba(220,38,38,0.08); }
.add-btn {
	display: block;
	width: 100%;
	background: none;
	border: 1px dashed var(--color-border);
	border-radius: var(--radius);
	padding: 0.5rem;
	color: var(--color-text-muted);
	cursor: pointer;
	font-size: 0.875rem;
	transition: border-color 0.15s, color 0.15s;
}
.add-btn:hover { border-color: var(--color-primary); color: var(--color-primary); }

/* === WhatsApp QR Modal === */
#wa-qr-modal { display: none; position: fixed; inset: 0; z-index: 1000; }
.wa-qr-overlay {
	position: absolute; inset: 0;
	background: rgba(0,0,0,0.55);
	display: flex; align-items: center; justify-content: center;
}
.wa-qr-card {
	background: var(--color-surface);
	border-radius: var(--radius);
	padding: 2rem;
	max-width: 360px;
	text-align: center;
	box-shadow: 0 12px 40px rgba(0,0,0,0.3);
}
#wa-qr-modal[style*="flex"] { display: flex !important; }

/* === Loading / Error === */
.loading-state {
	text-align: center;
	padding: 3rem;
	color: var(--color-text-muted);
}
.error-state {
	padding: 1rem;
	background: #fee2e2;
	color: var(--color-error);
	border-radius: var(--radius);
}
html.dark .error-state { background: #450a0a; }

/* === Responsive === */
@media (max-width: 600px) {
	.form-row { grid-template-columns: 1fr; }
	.finger-tab { padding: 0.5rem 0.75rem; font-size: 0.8rem; }
	.settings-wide { padding: 1rem; }
}
</style>
</head>
<body>
<div id="header">
	<h1>Felix Settings</h1>
	<span class="spacer"></span>
	<span id="status-msg"></span>
	<button class="btn-primary" id="save-btn" disabled>Save</button>
	<button class="btn-icon" id="theme-btn" title="Toggle light/dark mode">&#9790;</button>
</div>
<main>
<div class="container">
	<div id="loading" class="loading-state">Loading configuration&#8230;</div>
	<div id="settings-root" style="display:none">
		<div class="settings-wide">
			<div class="finger-tabs" id="tabs">
				<button class="finger-tab active" data-tab="gateway">Gateway</button>
				<button class="finger-tab" data-tab="providers">Providers</button>
				<button class="finger-tab" data-tab="models">Models</button>
				<button class="finger-tab" data-tab="agents">Agents</button>
				<button class="finger-tab" data-tab="channels">Channels</button>
				<button class="finger-tab" data-tab="intelligence">Intelligence</button>
				<button class="finger-tab" data-tab="security">Security</button>
			</div>
			<div class="finger-panel active" id="panel-gateway"></div>
			<div class="finger-panel" id="panel-providers"></div>
			<div class="finger-panel" id="panel-models"></div>
			<div class="finger-panel" id="panel-agents"></div>
			<div class="finger-panel" id="panel-channels"></div>
			<div class="finger-panel" id="panel-intelligence"></div>
			<div class="finger-panel" id="panel-security"></div>
		</div>
	</div>
</div>
</main>

<script>
(function() {
	var saveBtn = document.getElementById('save-btn');
	var statusMsg = document.getElementById('status-msg');
	var themeBtn = document.getElementById('theme-btn');
	var loading = document.getElementById('loading');
	var settingsRoot = document.getElementById('settings-root');
	var cfg = null;

	// === Theme ===
	function setTheme(mode) {
		if (mode === 'dark') {
			document.documentElement.classList.add('dark');
			themeBtn.innerHTML = '&#9728;';
		} else {
			document.documentElement.classList.remove('dark');
			themeBtn.innerHTML = '&#9790;';
		}
		localStorage.setItem('felix-theme', mode);
	}
	setTheme(localStorage.getItem('felix-theme') || 'light');
	themeBtn.addEventListener('click', function() {
		setTheme(document.documentElement.classList.contains('dark') ? 'light' : 'dark');
	});

	// === Tab switching ===
	var tabBtns = document.querySelectorAll('.finger-tab');
	tabBtns.forEach(function(btn) {
		btn.addEventListener('click', function() {
			tabBtns.forEach(function(b) { b.classList.remove('active'); });
			document.querySelectorAll('.finger-panel').forEach(function(p) { p.classList.remove('active'); });
			btn.classList.add('active');
			var panel = document.getElementById('panel-' + btn.dataset.tab);
			if (panel) panel.classList.add('active');
		});
	});

	// === Status message ===
	function showStatus(msg, isError) {
		statusMsg.textContent = msg;
		statusMsg.className = isError ? 'error' : 'success';
		if (!isError) setTimeout(function() { statusMsg.textContent = ''; statusMsg.className = ''; }, 3000);
	}

	// === Load config ===
	fetch(location.pathname + '/api/config')
		.then(function(r) { return r.json(); })
		.then(function(data) {
			cfg = data;
			loading.style.display = 'none';
			settingsRoot.style.display = 'block';
			render();
			saveBtn.disabled = false;
		})
		.catch(function(err) {
			loading.className = 'error-state';
			loading.textContent = 'Failed to load config: ' + err.message;
		});

	// === Save ===
	saveBtn.addEventListener('click', function() {
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

	// === Render all panels ===
	function render() {
		renderGateway();
		renderProviders();
		renderModels();
		renderAgents();
		renderChannels();
		renderIntelligence();
		renderSecurity();
	}

	// === Models tab — talks directly to bundled Ollama via providers.local.base_url ===
	var CURATED_MODELS = [
		{name: 'gemma4:latest', label: 'Gemma 4 (multimodal)', size: '~9.6 GB', note: 'recommended — vision + general agent'},
		{name: 'qwen3.5:9b',    label: 'Qwen 3.5 9B',          size: '~5.0 GB', note: 'lighter, text-only'}
	];
	var pullState = {}; // name -> {pct, status, err}
	var pollTimer = null;

	function ollamaBase() {
		var base = (cfg.providers && cfg.providers.local && cfg.providers.local.base_url) || 'http://127.0.0.1:18790';
		return base.replace(/\/v1\/?$/, '').replace(/\/$/, '');
	}

	function fmtBytes(n) {
		if (!n || n < 0) return '';
		if (n < 1024) return n + ' B';
		var u = ['KB','MB','GB','TB'];
		var i = -1;
		do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
		return n.toFixed(1) + ' ' + u[i];
	}

	function renderModels() {
		var panel = document.getElementById('panel-models');
		panel.innerHTML = '';

		var section = document.createElement('div');
		section.className = 'panel-section';
		var h = document.createElement('h3');
		h.textContent = 'Local models (via bundled Ollama)';
		section.appendChild(h);

		var p = document.createElement('p');
		p.style.cssText = 'color:var(--color-text-muted); font-size:0.85rem; margin:0.25rem 0 1rem 0;';
		p.textContent = 'Endpoint: ' + ollamaBase();
		section.appendChild(p);

		// Installed list
		var installedHdr = document.createElement('div');
		installedHdr.style.cssText = 'font-weight:600; font-size:0.9rem; margin-bottom:0.5rem;';
		installedHdr.textContent = 'Installed';
		section.appendChild(installedHdr);

		var installedBox = document.createElement('div');
		installedBox.id = 'models-installed';
		installedBox.style.cssText = 'border:1px solid var(--color-border); border-radius:var(--radius); padding:0.5rem; margin-bottom:1.5rem; min-height:2.5rem;';
		installedBox.textContent = 'Loading…';
		section.appendChild(installedBox);

		// Curated download list
		var availHdr = document.createElement('div');
		availHdr.style.cssText = 'font-weight:600; font-size:0.9rem; margin-bottom:0.5rem;';
		availHdr.textContent = 'Available to download';
		section.appendChild(availHdr);

		var grid = document.createElement('div');
		grid.style.cssText = 'display:grid; grid-template-columns:1fr; gap:0.75rem;';
		CURATED_MODELS.forEach(function(m) {
			var card = document.createElement('div');
			card.style.cssText = 'border:1px solid var(--color-border); border-radius:var(--radius); padding:0.75rem;';

			var top = document.createElement('div');
			top.style.cssText = 'display:flex; justify-content:space-between; align-items:center; gap:0.5rem;';
			var info = document.createElement('div');
			var nameLine = document.createElement('div');
			nameLine.style.cssText = 'font-weight:600;';
			nameLine.textContent = m.label + ' (' + m.name + ')';
			var sub = document.createElement('div');
			sub.style.cssText = 'color:var(--color-text-muted); font-size:0.8rem;';
			sub.textContent = m.size + ' • ' + m.note;
			info.appendChild(nameLine);
			info.appendChild(sub);

			var btn = document.createElement('button');
			btn.className = 'btn';
			btn.dataset.model = m.name;
			btn.textContent = 'Download';
			btn.addEventListener('click', function() { startPull(m.name); });

			top.appendChild(info);
			top.appendChild(btn);
			card.appendChild(top);

			var prog = document.createElement('div');
			prog.id = 'pull-progress-' + m.name;
			prog.style.cssText = 'margin-top:0.5rem; display:none;';
			prog.innerHTML = '<div style="font-size:0.8rem; color:var(--color-text-muted); margin-bottom:0.25rem;" class="progress-text">Starting…</div>' +
				'<div style="height:6px; background:var(--color-border); border-radius:3px; overflow:hidden;"><div class="progress-bar" style="height:100%; width:0%; background:var(--color-accent, #3b82f6); transition:width 0.3s;"></div></div>';
			card.appendChild(prog);

			grid.appendChild(card);
		});
		section.appendChild(grid);

		panel.appendChild(section);

		refreshInstalled();
		// Apply any in-flight pull state in case the user switched tabs and back.
		Object.keys(pullState).forEach(function(name) { applyPullState(name); });
	}

	function refreshInstalled() {
		var box = document.getElementById('models-installed');
		if (!box) return;
		fetch(ollamaBase() + '/api/tags')
			.then(function(r) { return r.json(); })
			.then(function(data) {
				var models = (data && data.models) || [];
				if (models.length === 0) {
					box.textContent = 'No models installed yet.';
					return;
				}
				box.innerHTML = '';
				models.forEach(function(m) {
					var row = document.createElement('div');
					row.style.cssText = 'display:flex; justify-content:space-between; padding:0.4rem 0.25rem; border-bottom:1px solid var(--color-border);';
					var nm = document.createElement('div');
					nm.textContent = m.name;
					var sz = document.createElement('div');
					sz.style.cssText = 'color:var(--color-text-muted); font-size:0.85rem;';
					sz.textContent = fmtBytes(m.size);
					row.appendChild(nm);
					row.appendChild(sz);
					box.appendChild(row);
				});
				box.lastChild.style.borderBottom = 'none';
			})
			.catch(function(err) {
				box.textContent = 'Error: ' + err.message + ' — is the bundled Ollama running?';
			});
	}

	function applyPullState(name) {
		var st = pullState[name];
		var prog = document.getElementById('pull-progress-' + name);
		var btn = document.querySelector('button[data-model="' + name + '"]');
		if (!prog || !btn) return;
		if (!st) {
			prog.style.display = 'none';
			btn.disabled = false;
			btn.textContent = 'Download';
			return;
		}
		prog.style.display = 'block';
		btn.disabled = true;
		btn.textContent = 'Downloading…';
		var bar = prog.querySelector('.progress-bar');
		var txt = prog.querySelector('.progress-text');
		if (bar) bar.style.width = (st.pct || 0) + '%';
		if (txt) {
			var label = st.status || 'pulling';
			if (st.completed && st.total) {
				label += ' — ' + fmtBytes(st.completed) + ' / ' + fmtBytes(st.total) + ' (' + (st.pct || 0).toFixed(1) + '%)';
			} else if (st.pct != null) {
				label += ' — ' + st.pct.toFixed(1) + '%';
			}
			if (st.err) label = 'Error: ' + st.err;
			txt.textContent = label;
		}
	}

	function startPull(name) {
		if (pullState[name] && !pullState[name].err && !pullState[name].done) return;
		pullState[name] = {pct: 0, status: 'starting'};
		applyPullState(name);

		fetch(ollamaBase() + '/api/pull', {
			method: 'POST',
			headers: {'Content-Type': 'application/json'},
			body: JSON.stringify({name: name, stream: true})
		}).then(function(resp) {
			if (!resp.ok || !resp.body) {
				pullState[name].err = 'HTTP ' + resp.status;
				applyPullState(name);
				return;
			}
			var reader = resp.body.getReader();
			var decoder = new TextDecoder();
			var buf = '';
			function read() {
				return reader.read().then(function(chunk) {
					if (chunk.done) {
						pullState[name].done = true;
						pullState[name].pct = 100;
						pullState[name].status = 'done';
						applyPullState(name);
						refreshInstalled();
						setTimeout(function() { delete pullState[name]; applyPullState(name); }, 3000);
						return;
					}
					buf += decoder.decode(chunk.value, {stream: true});
					var lines = buf.split('\n');
					buf = lines.pop();
					lines.forEach(function(line) {
						if (!line.trim()) return;
						try {
							var ev = JSON.parse(line);
							var st = pullState[name];
							st.status = ev.status || st.status;
							if (typeof ev.total === 'number') st.total = ev.total;
							if (typeof ev.completed === 'number') st.completed = ev.completed;
							if (st.total > 0) st.pct = (st.completed || 0) * 100 / st.total;
							if (ev.error) st.err = ev.error;
							applyPullState(name);
						} catch (e) { /* ignore unparsable line */ }
					});
					return read();
				});
			}
			return read();
		}).catch(function(err) {
			pullState[name].err = err.message;
			applyPullState(name);
		});
	}

	// === Helper: toggle-group ===
	function makeToggle(parent, label, checked, onChange) {
		var g = document.createElement('div');
		g.className = 'toggle-group';
		var t = document.createElement('label');
		t.className = 'toggle';
		var inp = document.createElement('input');
		inp.type = 'checkbox';
		inp.checked = !!checked;
		inp.addEventListener('change', function() { onChange(inp.checked); });
		var sl = document.createElement('span');
		sl.className = 'slider';
		t.appendChild(inp);
		t.appendChild(sl);
		var lbl = document.createElement('span');
		lbl.className = 'toggle-label';
		lbl.textContent = label;
		g.appendChild(t);
		g.appendChild(lbl);
		parent.appendChild(g);
		return g;
	}

	// === Helper: form-group (label above input) ===
	function makeField(parent, label, type, value, onChange) {
		if (type === 'toggle') {
			return makeToggle(parent, label, value, onChange);
		}
		var g = document.createElement('div');
		g.className = 'form-group';
		var l = document.createElement('label');
		l.textContent = label;
		g.appendChild(l);

		if (type === 'select') {
			var sel = document.createElement('select');
			var opts = (value && value.options) ? value.options : [];
			var cur = (value && value.value != null) ? value.value : '';
			for (var i = 0; i < opts.length; i++) {
				var opt = document.createElement('option');
				var ov, ol;
				if (opts[i] && typeof opts[i] === 'object') {
					ov = opts[i].value; ol = opts[i].label || opts[i].value;
				} else {
					ov = opts[i]; ol = opts[i];
				}
				opt.value = ov;
				opt.textContent = ol;
				if (ov === cur) opt.selected = true;
				sel.appendChild(opt);
			}
			sel.addEventListener('change', function() { onChange(sel.value); });
			g.appendChild(sel);
		} else if (type === 'textarea') {
			var ta = document.createElement('textarea');
			ta.value = value || '';
			ta.addEventListener('input', function() { onChange(ta.value); });
			g.appendChild(ta);
		} else {
			var inp = document.createElement('input');
			inp.type = type || 'text';
			inp.value = value != null ? value : '';
			if (type === 'password') inp.placeholder = '(leave blank to keep)';
			inp.addEventListener('input', function() {
				onChange(type === 'number' ? (parseInt(inp.value, 10) || 0) : inp.value);
			});
			g.appendChild(inp);
		}

		parent.appendChild(g);
		return g;
	}

	// === Helper: read-only display field (no input — shows a value with id) ===
	function makeReadOnlyField(parent, label, valueElemId, placeholder) {
		var g = document.createElement('div');
		g.className = 'form-group';
		var l = document.createElement('label');
		l.textContent = label;
		g.appendChild(l);
		var v = document.createElement('div');
		v.id = valueElemId;
		v.style.cssText = 'padding:0.5rem 0.75rem; border:1px solid var(--color-border); border-radius:var(--radius); background:var(--color-bg); font-size:0.9rem; font-family:inherit; color:var(--color-text-muted); user-select:text; min-height:1.2em; word-break:break-all;';
		v.textContent = placeholder || '';
		g.appendChild(v);
		parent.appendChild(g);
		return g;
	}

	// === Helper: 2-column row ===
	function makeRow(parent) {
		var row = document.createElement('div');
		row.className = 'form-row';
		parent.appendChild(row);
		return row;
	}

	// === Helper: DM policy options (label/value) ===
	function dmPolicyOptions() {
		return [
			{value: 'ignore', label: 'Ignore'},
			{value: 'respond', label: 'Respond'},
			{value: 'process', label: 'Process (no reply)'},
			{value: 'notify', label: 'Notify'}
		];
	}

	// === Helper: read peer IDs from bindings for a channel ===
	function peerIDsFromBindings(channelName) {
		var ids = [];
		var bindings = cfg.bindings || [];
		for (var i = 0; i < bindings.length; i++) {
			var m = bindings[i].match || {};
			if (m.channel === channelName && m.peer && m.peer.id) {
				ids.push(m.peer.id);
			}
		}
		return ids.join(', ');
	}

	// === Helper: replace peer-specific bindings for a channel ===
	function setPeerIDsInBindings(channelName, csv) {
		if (!cfg.bindings) cfg.bindings = [];
		// Remove existing bindings whose match has channel + peer.id for this channel
		cfg.bindings = cfg.bindings.filter(function(b) {
			var m = b.match || {};
			return !(m.channel === channelName && m.peer && m.peer.id);
		});
		// Pick a default agentId (first agent, or 'default')
		var defaultAgent = 'default';
		var agents = (cfg.agents || {}).list || [];
		if (agents.length > 0 && agents[0].id) defaultAgent = agents[0].id;
		// Add a binding per peer ID
		var ids = csv.split(',').map(function(s) { return s.trim(); }).filter(Boolean);
		for (var i = 0; i < ids.length; i++) {
			cfg.bindings.push({
				agentId: defaultAgent,
				match: {channel: channelName, peer: {id: ids[i]}}
			});
		}
	}

	// === Helper: panel section with optional heading ===
	function makeSection(panel, title) {
		var sec = document.createElement('div');
		sec.className = 'panel-section';
		if (title) {
			var h = document.createElement('h3');
			h.textContent = title;
			sec.appendChild(h);
		}
		panel.appendChild(sec);
		return sec;
	}

	// === Gateway Panel ===
	function renderGateway() {
		var p = document.getElementById('panel-gateway');
		p.innerHTML = '';
		var sec = makeSection(p, null);
		var gw = cfg.gateway || {};
		var row = makeRow(sec);
		makeField(row, 'Host', 'text', gw.host || '', function(v) {
			if (!cfg.gateway) cfg.gateway = {};
			cfg.gateway.host = v;
		});
		makeField(row, 'Port', 'number', gw.port || 18789, function(v) {
			if (!cfg.gateway) cfg.gateway = {};
			cfg.gateway.port = v;
		});
		makeField(sec, 'Auth Token', 'text', (gw.auth || {}).token || '', function(v) {
			if (!cfg.gateway) cfg.gateway = {};
			if (!cfg.gateway.auth) cfg.gateway.auth = {};
			cfg.gateway.auth.token = v;
		});
		makeField(sec, 'Reload Mode', 'select', {
			value: (gw.reload || {}).mode || 'hybrid',
			options: ['hybrid', 'manual', 'auto-restart']
		}, function(v) {
			if (!cfg.gateway) cfg.gateway = {};
			if (!cfg.gateway.reload) cfg.gateway.reload = {};
			cfg.gateway.reload.mode = v;
		});
	}

	// === Providers Panel ===
	function renderProviders() {
		var p = document.getElementById('panel-providers');
		p.innerHTML = '';
		var sec = makeSection(p, null);
		var providers = cfg.providers || {};
		var names = Object.keys(providers);
		var list = document.createElement('div');
		list.className = 'dynamic-list';
		sec.appendChild(list);

		for (var i = 0; i < names.length; i++) {
			(function(name) {
				var prov = providers[name];
				var item = document.createElement('div');
				item.className = 'dynamic-item';

				var title = document.createElement('div');
				title.className = 'dynamic-item-title';
				title.textContent = name;
				item.appendChild(title);

				var rm = document.createElement('button');
				rm.className = 'remove-btn';
				rm.innerHTML = '&times;';
				rm.onclick = function() { delete cfg.providers[name]; render(); };
				item.appendChild(rm);

				var row = makeRow(item);
				makeField(row, 'Kind', 'text', prov.kind || '', function(v) { cfg.providers[name].kind = v; });
				makeField(row, 'Base URL', 'text', prov.base_url || '', function(v) { cfg.providers[name].base_url = v; });
				makeField(item, 'API Key', 'password', '', function(v) { if (v) cfg.providers[name].api_key = v; });

				list.appendChild(item);
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
		sec.appendChild(addBtn);
	}

	// === Agents Panel ===
	function renderAgents() {
		var p = document.getElementById('panel-agents');
		p.innerHTML = '';
		var sec = makeSection(p, null);
		var agents = (cfg.agents || {}).list || [];
		var list = document.createElement('div');
		list.className = 'dynamic-list';
		sec.appendChild(list);

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

				var row1 = makeRow(item);
				makeField(row1, 'ID', 'text', a.id || '', function(v) { cfg.agents.list[idx].id = v; });
				makeField(row1, 'Name', 'text', a.name || '', function(v) { cfg.agents.list[idx].name = v; });

				var row2 = makeRow(item);
				makeField(row2, 'Model', 'text', a.model || '', function(v) { cfg.agents.list[idx].model = v; });
				makeField(row2, 'Max Turns', 'number', a.maxTurns || 0, function(v) { cfg.agents.list[idx].maxTurns = v; });

				makeField(item, 'Sandbox', 'select', {
					value: a.sandbox || 'none',
					options: ['none', 'docker', 'namespace']
				}, function(v) { cfg.agents.list[idx].sandbox = v; });

				makeField(item, 'System Prompt', 'textarea', a.system_prompt || '', function(v) {
					cfg.agents.list[idx].system_prompt = v;
				});

				var row3 = makeRow(item);
				makeField(row3, 'Allowed Tools', 'text', ((a.tools || {}).allow || []).join(', '), function(v) {
					if (!cfg.agents.list[idx].tools) cfg.agents.list[idx].tools = {};
					cfg.agents.list[idx].tools.allow = v.split(',').map(function(s) { return s.trim(); }).filter(Boolean);
				});
				makeField(row3, 'Denied Tools', 'text', ((a.tools || {}).deny || []).join(', '), function(v) {
					if (!cfg.agents.list[idx].tools) cfg.agents.list[idx].tools = {};
					cfg.agents.list[idx].tools.deny = v.split(',').map(function(s) { return s.trim(); }).filter(Boolean);
				});

				list.appendChild(item);
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
		sec.appendChild(addBtn);
	}

	// === Channels Panel ===
	function renderChannels() {
		var p = document.getElementById('panel-channels');
		p.innerHTML = '';
		var ch = cfg.channels || {};

		// CLI
		var cliSec = makeSection(p, 'CLI');
		makeField(cliSec, 'Enabled', 'toggle', (ch.cli || {}).enabled, function(v) {
			if (!cfg.channels) cfg.channels = {};
			if (!cfg.channels.cli) cfg.channels.cli = {};
			cfg.channels.cli.enabled = v;
		});

		// Telegram
		var tg = ch.telegram || {};
		var tgSec = makeSection(p, 'Telegram');
		makeField(tgSec, 'Bot Token', 'password', '', function(v) {
			if (!cfg.channels) cfg.channels = {};
			if (!cfg.channels.telegram) cfg.channels.telegram = {};
			cfg.channels.telegram.token = v;
		});
		var tgRow = makeRow(tgSec);
		makeField(tgRow, 'Mode', 'select', {
			value: tg.mode || 'polling',
			options: ['polling', 'webhook']
		}, function(v) {
			if (!cfg.channels) cfg.channels = {};
			if (!cfg.channels.telegram) cfg.channels.telegram = {};
			cfg.channels.telegram.mode = v;
		});
		makeField(tgRow, 'Require Mention in Groups', 'toggle',
			((cfg.security || {}).groupPolicy || {}).requireMention,
			function(v) {
				if (!cfg.security) cfg.security = {};
				if (!cfg.security.groupPolicy) cfg.security.groupPolicy = {};
				cfg.security.groupPolicy.requireMention = v;
			}
		);
		var tgRow2 = makeRow(tgSec);
		makeField(tgRow2, 'Peer IDs (comma-separated — known senders for DM policy)', 'text',
			peerIDsFromBindings('telegram'),
			function(v) { setPeerIDsInBindings('telegram', v); }
		);
		makeField(tgRow2, 'DM Policy: Unknown Senders', 'select', {
			value: tg.dm_policy || 'ignore',
			options: dmPolicyOptions()
		}, function(v) {
			if (!cfg.channels) cfg.channels = {};
			if (!cfg.channels.telegram) cfg.channels.telegram = {};
			cfg.channels.telegram.dm_policy = v;
		});
		makeField(tgSec, 'Processing Prompt (prepended to system prompt for Telegram messages)', 'textarea',
			tg.processing_prompt || '',
			function(v) {
				if (!cfg.channels) cfg.channels = {};
				if (!cfg.channels.telegram) cfg.channels.telegram = {};
				cfg.channels.telegram.processing_prompt = v;
			}
		);

		// WhatsApp
		var wa = ch.whatsapp || {};
		var waSec = makeSection(p, 'WhatsApp');

		// Status indicator + connect/disconnect controls
		var statusBar = document.createElement('div');
		statusBar.id = 'wa-status-bar';
		statusBar.style.cssText = 'display:flex;align-items:center;gap:0.75rem;margin-bottom:1rem;';
		statusBar.innerHTML = '<span style="color:var(--color-text-muted);font-size:0.85rem;">Loading status&#8230;</span>';
		waSec.appendChild(statusBar);

		var waRow1 = makeRow(waSec);
		makeReadOnlyField(waRow1, 'Phone Number', 'wa-phone-display', '—');
		makeReadOnlyField(waRow1, 'DB Path', 'wa-dbpath-display', '—');
		makeField(waSec, 'Allowed Senders (comma-separated phone numbers/JIDs, empty = allow all)', 'textarea',
			(wa.allowed_senders || []).join(', '),
			function(v) {
				if (!cfg.channels) cfg.channels = {};
				if (!cfg.channels.whatsapp) cfg.channels.whatsapp = {};
				cfg.channels.whatsapp.allowed_senders = v.split(',').map(function(s) { return s.trim(); }).filter(Boolean);
			}
		);
		var waRow2 = makeRow(waSec);
		makeField(waRow2, 'Peer IDs (comma-separated — known senders for DM policy)', 'text',
			peerIDsFromBindings('whatsapp'),
			function(v) { setPeerIDsInBindings('whatsapp', v); }
		);
		makeField(waRow2, 'DM Policy: Unknown Senders', 'select', {
			value: wa.dm_policy || 'ignore',
			options: dmPolicyOptions()
		}, function(v) {
			if (!cfg.channels) cfg.channels = {};
			if (!cfg.channels.whatsapp) cfg.channels.whatsapp = {};
			cfg.channels.whatsapp.dm_policy = v;
		});
		makeField(waSec, 'Processing Prompt (prepended to system prompt for WhatsApp messages)', 'textarea',
			wa.processing_prompt || '',
			function(v) {
				if (!cfg.channels) cfg.channels = {};
				if (!cfg.channels.whatsapp) cfg.channels.whatsapp = {};
				cfg.channels.whatsapp.processing_prompt = v;
			}
		);

		// Hidden modal markup (single instance per render).
		if (!document.getElementById('wa-qr-modal')) {
			var modal = document.createElement('div');
			modal.id = 'wa-qr-modal';
			modal.innerHTML = '<div class="wa-qr-overlay">' +
				'<div class="wa-qr-card">' +
				'<h3 style="margin-bottom:0.5rem;">Scan QR with WhatsApp</h3>' +
				'<p style="color:var(--color-text-muted);font-size:0.85rem;margin-bottom:1rem;">Open WhatsApp &rarr; Settings &rarr; Linked Devices &rarr; Link a Device.</p>' +
				'<div id="wa-qr-img" style="margin:0 auto 1rem;min-height:256px;display:flex;align-items:center;justify-content:center;color:var(--color-text-muted);">Waiting for QR&#8230;</div>' +
				'<div id="wa-qr-error" style="color:var(--color-error);margin-bottom:0.75rem;display:none;font-size:0.85rem;"></div>' +
				'<button id="wa-qr-cancel" class="btn-primary" style="background:var(--color-text-muted);">Cancel</button>' +
				'</div></div>';
			document.body.appendChild(modal);
		}

		refreshWhatsAppStatus();

		// Google Workspace
		var gSec = makeSection(p, 'Google Workspace');
		gSec.id = 'google';
		var gContainer = document.createElement('div');
		gContainer.id = 'google-container';
		gSec.appendChild(gContainer);
		refreshGoogleStatus();
	}

	// === Google Workspace ===
	function refreshGoogleStatus() {
		fetch('/google/status').then(function(r) { return r.json(); }).then(function(d) {
			renderGoogle(d);
		}).catch(function() {
			renderGoogle({configured: false, connected: false});
		});
	}

	function renderGoogle(s) {
		var c = document.getElementById('google-container');
		if (!c) return;
		c.innerHTML = '';

		// Surface query-string error/success from the OAuth callback redirect
		var qs = new URLSearchParams(location.search);
		var qsErr = qs.get('google_error');
		var qsOk = qs.get('google_connected');
		if (qsErr) {
			var bar = document.createElement('div');
			bar.style.cssText = 'background:var(--color-error);color:#fff;padding:0.5rem 0.75rem;border-radius:var(--radius);margin-bottom:0.75rem;font-size:0.85rem;';
			bar.textContent = 'Google: ' + qsErr;
			c.appendChild(bar);
		}
		if (qsOk) {
			var ok = document.createElement('div');
			ok.style.cssText = 'background:var(--color-success);color:#fff;padding:0.5rem 0.75rem;border-radius:var(--radius);margin-bottom:0.75rem;font-size:0.85rem;';
			ok.textContent = 'Google account connected.';
			c.appendChild(ok);
		}

		if (s.connected) {
			renderGoogleConnected(c, s);
			return;
		}
		if (s.configured) {
			renderGoogleAuthorize(c);
			return;
		}
		renderGoogleWizard(c);
	}

	function renderGoogleConnected(c, s) {
		var bar = document.createElement('div');
		bar.style.cssText = 'display:flex;align-items:center;gap:0.75rem;';
		bar.innerHTML = '<span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--color-success);"></span>' +
			'<span style="font-size:0.875rem;">Connected as <strong>' + (s.email || 'Google account') + '</strong></span>' +
			'<span style="margin-left:auto;"><button id="google-disconnect" class="btn-primary" style="background:var(--color-error);">Disconnect</button></span>';
		c.appendChild(bar);
		document.getElementById('google-disconnect').addEventListener('click', function() {
			if (!confirm('Disconnect Google? Felix will lose access to Gmail/Drive/Calendar until you reconnect.')) return;
			fetch('/google/disconnect', {method: 'POST'}).then(function() { refreshGoogleStatus(); });
		});
	}

	function renderGoogleAuthorize(c) {
		var p = document.createElement('p');
		p.style.cssText = 'color:var(--color-text-muted);font-size:0.9rem;margin-bottom:0.75rem;';
		p.textContent = 'Credentials saved. Click Connect to grant Felix access to your Google account.';
		c.appendChild(p);
		var btn = document.createElement('button');
		btn.className = 'btn-primary';
		btn.textContent = 'Connect with Google';
		btn.addEventListener('click', function() { location.href = '/google/oauth/start'; });
		c.appendChild(btn);
		var reset = document.createElement('button');
		reset.className = 'btn-primary';
		reset.style.cssText = 'margin-left:0.5rem;background:var(--color-text-muted);';
		reset.textContent = 'Re-enter Credentials';
		reset.addEventListener('click', function() { renderGoogleWizard(c); });
		c.appendChild(reset);
	}

	function renderGoogleWizard(c) {
		c.innerHTML = '';
		var intro = document.createElement('p');
		intro.style.cssText = 'color:var(--color-text-muted);font-size:0.9rem;margin-bottom:1rem;';
		intro.innerHTML = 'Felix needs its own Google "app" to ask for permission to access your Gmail, Drive, and Calendar. ' +
			'This is a one-time setup that takes about 5 minutes. Tokens are stored encrypted on this machine and never leave it.';
		c.appendChild(intro);

		var steps = [
			{ n: 1, title: 'Create a project',
			  body: 'In the page that opens, click <strong>NEW PROJECT</strong>, name it <code>Felix</code>, then click <strong>CREATE</strong>. Wait for it to finish, then come back here.',
			  href: 'https://console.cloud.google.com/projectcreate', linkText: 'Open Google Cloud Console' },
			{ n: 2, title: 'Enable the APIs',
			  body: 'Click <strong>ENABLE</strong> on each of these three pages.',
			  multiHref: [
				  ['Enable Gmail API', 'https://console.cloud.google.com/apis/library/gmail.googleapis.com'],
				  ['Enable Drive API', 'https://console.cloud.google.com/apis/library/drive.googleapis.com'],
				  ['Enable Calendar API', 'https://console.cloud.google.com/apis/library/calendar-json.googleapis.com'],
			  ] },
			{ n: 3, title: 'Set up consent screen',
			  body: 'Pick <strong>External</strong>, click <strong>CREATE</strong>. Enter <code>Felix</code> as App name, your email for both <em>User support email</em> and <em>Developer contact</em>, and click <strong>SAVE AND CONTINUE</strong> through the rest. On the <em>Test users</em> step, add your own Google account.',
			  href: 'https://console.cloud.google.com/apis/credentials/consent', linkText: 'Open Consent Screen' },
			{ n: 4, title: 'Create credentials',
			  body: 'Click <strong>+ CREATE CREDENTIALS → OAuth client ID</strong>. Application type: <strong>Desktop app</strong>. Name: <code>Felix</code>. Click <strong>CREATE</strong>. A popup will show your Client ID and Client Secret — paste both below.',
			  href: 'https://console.cloud.google.com/apis/credentials', linkText: 'Open Credentials Page' },
		];

		steps.forEach(function(step) {
			var div = document.createElement('div');
			div.style.cssText = 'margin-bottom:1rem;padding:0.75rem;border:1px solid var(--color-border);border-radius:var(--radius);background:var(--color-bg);';
			var h = document.createElement('div');
			h.style.cssText = 'font-weight:600;margin-bottom:0.25rem;font-size:0.9rem;';
			h.textContent = 'Step ' + step.n + ' of 4 — ' + step.title;
			div.appendChild(h);
			var body = document.createElement('div');
			body.style.cssText = 'font-size:0.85rem;color:var(--color-text);margin-bottom:0.5rem;';
			body.innerHTML = step.body;
			div.appendChild(body);
			if (step.href) {
				var link = document.createElement('a');
				link.href = step.href;
				link.target = '_blank';
				link.rel = 'noopener noreferrer';
				link.className = 'btn-primary';
				link.style.cssText = 'display:inline-block;text-decoration:none;font-size:0.85rem;';
				link.textContent = step.linkText + ' →';
				div.appendChild(link);
			}
			if (step.multiHref) {
				step.multiHref.forEach(function(pair) {
					var a = document.createElement('a');
					a.href = pair[1]; a.target = '_blank'; a.rel = 'noopener noreferrer';
					a.className = 'btn-primary';
					a.style.cssText = 'display:inline-block;text-decoration:none;font-size:0.85rem;margin-right:0.5rem;margin-bottom:0.25rem;';
					a.textContent = pair[0] + ' →';
					div.appendChild(a);
				});
			}
			c.appendChild(div);
		});

		// Paste form
		var formWrap = document.createElement('div');
		formWrap.style.cssText = 'padding:0.75rem;border:1px solid var(--color-border);border-radius:var(--radius);';
		var fh = document.createElement('div');
		fh.style.cssText = 'font-weight:600;margin-bottom:0.5rem;font-size:0.9rem;';
		fh.textContent = 'Paste your credentials';
		formWrap.appendChild(fh);

		var idField = document.createElement('div');
		idField.className = 'form-group';
		idField.innerHTML = '<label>Client ID</label><input type="text" id="g-client-id" placeholder="123-abc.apps.googleusercontent.com">';
		formWrap.appendChild(idField);

		var secField = document.createElement('div');
		secField.className = 'form-group';
		secField.innerHTML = '<label>Client Secret</label><input type="text" id="g-client-secret" placeholder="GOCSPX-…">';
		formWrap.appendChild(secField);

		var err = document.createElement('div');
		err.id = 'g-error';
		err.style.cssText = 'color:var(--color-error);font-size:0.85rem;margin-bottom:0.5rem;display:none;';
		formWrap.appendChild(err);

		var btn = document.createElement('button');
		btn.className = 'btn-primary';
		btn.textContent = 'Save & Connect';
		btn.addEventListener('click', function() {
			var id = (document.getElementById('g-client-id').value || '').trim();
			var sec = (document.getElementById('g-client-secret').value || '').trim();
			err.style.display = 'none';
			fetch('/google/credentials', {
				method: 'POST',
				headers: {'Content-Type': 'application/json'},
				body: JSON.stringify({client_id: id, client_secret: sec})
			})
			.then(function(r) {
				if (!r.ok) {
					return r.text().then(function(t) {
						err.textContent = t.replace(/^[^"]*"error":"|"\}.*$/g, '');
						err.style.display = 'block';
						throw new Error('save failed');
					});
				}
				location.href = '/google/oauth/start';
			})
			.catch(function() {});
		});
		formWrap.appendChild(btn);
		c.appendChild(formWrap);
	}

	// === WhatsApp pairing flow ===
	var waEvtSource = null;
	function refreshWhatsAppStatus() {
		var bar = document.getElementById('wa-status-bar');
		if (!bar) return;
		fetch('/whatsapp/status').then(function(r) { return r.json(); }).then(function(d) {
			renderWhatsAppStatusBar(d.status || 'not_configured');
			var phoneEl = document.getElementById('wa-phone-display');
			if (phoneEl) {
				if (d.jid) {
					var num = d.jid.split('@')[0];
					phoneEl.textContent = '+' + num;
				} else {
					phoneEl.textContent = '—';
				}
			}
			var dbEl = document.getElementById('wa-dbpath-display');
			if (dbEl) dbEl.textContent = d.db_path || '—';
		}).catch(function() {
			renderWhatsAppStatusBar('not_configured');
		});
	}
	function renderWhatsAppStatusBar(status) {
		var bar = document.getElementById('wa-status-bar');
		if (!bar) return;
		var badgeColor = status === 'connected' ? 'var(--color-success)'
			: status === 'pairing' ? 'var(--color-primary)'
			: status === 'disconnected' ? 'var(--color-text-muted)'
			: 'var(--color-error)';
		var label = {
			'connected': 'Connected',
			'pairing': 'Pairing&#8230;',
			'disconnected': 'Disconnected',
			'not_paired': 'Not paired',
			'not_configured': 'Not configured'
		}[status] || status;
		var actions = '';
		if (status === 'connected') {
			actions = '<button id="wa-disconnect" class="btn-primary" style="background:var(--color-error);">Unpair</button>';
		} else if (status === 'disconnected') {
			actions = '<button id="wa-pair" class="btn-primary">Reconnect</button>' +
				' <button id="wa-disconnect" class="btn-primary" style="background:var(--color-error);">Unpair</button>';
		} else if (status === 'not_paired') {
			actions = '<button id="wa-pair" class="btn-primary">Pair Device</button>';
		}
		bar.innerHTML = '<span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:' + badgeColor + ';"></span>' +
			'<span style="font-size:0.875rem;">' + label + '</span>' +
			'<span style="margin-left:auto;">' + actions + '</span>';

		var pair = document.getElementById('wa-pair');
		if (pair) pair.addEventListener('click', startWhatsAppPairing);
		var disc = document.getElementById('wa-disconnect');
		if (disc) disc.addEventListener('click', disconnectWhatsApp);
	}
	function startWhatsAppPairing() {
		var modal = document.getElementById('wa-qr-modal');
		var img = document.getElementById('wa-qr-img');
		var err = document.getElementById('wa-qr-error');
		var cancel = document.getElementById('wa-qr-cancel');
		modal.style.display = 'flex';
		err.style.display = 'none';
		img.innerHTML = 'Waiting for QR&#8230;';

		if (waEvtSource) { waEvtSource.close(); }
		waEvtSource = new EventSource('/whatsapp/pair');
		waEvtSource.addEventListener('qr', function(e) {
			var data = JSON.parse(e.data);
			img.innerHTML = '<img src="data:image/png;base64,' + data.png_b64 + '" alt="QR" style="width:256px;height:256px;display:block;" />';
		});
		waEvtSource.addEventListener('connected', function() {
			img.innerHTML = '<div style="color:var(--color-success);font-weight:600;">Connected!</div>';
			setTimeout(function() {
				modal.style.display = 'none';
				if (waEvtSource) { waEvtSource.close(); waEvtSource = null; }
				refreshWhatsAppStatus();
			}, 800);
		});
		waEvtSource.addEventListener('error', function(e) {
			if (e.data) {
				var data = JSON.parse(e.data);
				err.textContent = data.message || 'Pairing failed';
				err.style.display = 'block';
			}
			if (waEvtSource) { waEvtSource.close(); waEvtSource = null; }
		});
		cancel.onclick = function() {
			if (waEvtSource) { waEvtSource.close(); waEvtSource = null; }
			modal.style.display = 'none';
		};
	}
	function disconnectWhatsApp() {
		if (!confirm('Unpair WhatsApp? You will need to scan the QR again to reconnect.')) return;
		fetch('/whatsapp/disconnect', {method: 'POST'}).then(function() {
			refreshWhatsAppStatus();
		});
	}

	// === Intelligence Panel (Memory + Cortex + Heartbeat) ===
	function renderIntelligence() {
		var p = document.getElementById('panel-intelligence');
		p.innerHTML = '';

		// Memory
		var m = cfg.memory || {};
		var memSec = makeSection(p, 'Memory');
		makeField(memSec, 'Enabled', 'toggle', m.enabled, function(v) {
			if (!cfg.memory) cfg.memory = {};
			cfg.memory.enabled = v;
		});
		var memRow = makeRow(memSec);
		makeField(memRow, 'Embedding Provider', 'select', {
			value: m.embeddingProvider || '',
			options: Object.keys(cfg.providers || {})
		}, function(v) {
			if (!cfg.memory) cfg.memory = {};
			cfg.memory.embeddingProvider = v;
		});
		makeField(memRow, 'Embedding Model', 'text', m.embeddingModel || '', function(v) {
			if (!cfg.memory) cfg.memory = {};
			cfg.memory.embeddingModel = v;
		});

		// Cortex
		var cx = cfg.cortex || {};
		var cxSec = makeSection(p, 'Cortex');
		makeField(cxSec, 'Enabled', 'toggle', cx.enabled, function(v) {
			if (!cfg.cortex) cfg.cortex = {};
			cfg.cortex.enabled = v;
		});
		makeField(cxSec, 'DB Path', 'text', cx.dbPath || '', function(v) {
			if (!cfg.cortex) cfg.cortex = {};
			cfg.cortex.dbPath = v;
		});
		var cxRow = makeRow(cxSec);
		makeField(cxRow, 'Provider', 'select', {
			value: cx.provider || '',
			options: Object.keys(cfg.providers || {})
		}, function(v) {
			if (!cfg.cortex) cfg.cortex = {};
			cfg.cortex.provider = v;
		});
		makeField(cxRow, 'LLM Model', 'text', cx.llmModel || '', function(v) {
			if (!cfg.cortex) cfg.cortex = {};
			cfg.cortex.llmModel = v;
		});

	}

	// === Security Panel ===
	function renderSecurity() {
		var p = document.getElementById('panel-security');
		p.innerHTML = '';
		var sec = makeSection(p, null);
		var security = cfg.security || {};
		var exec = security.execApprovals || {};

		makeField(sec, 'Exec Approvals Level', 'select', {
			value: exec.level || 'full',
			options: ['full', 'allowlist', 'deny']
		}, function(v) {
			if (!cfg.security) cfg.security = {};
			if (!cfg.security.execApprovals) cfg.security.execApprovals = {};
			cfg.security.execApprovals.level = v;
		});
		makeField(sec, 'Exec Allowlist (comma-separated commands)', 'text',
			(exec.allowlist || []).join(', '),
			function(v) {
				if (!cfg.security) cfg.security = {};
				if (!cfg.security.execApprovals) cfg.security.execApprovals = {};
				cfg.security.execApprovals.allowlist = v.split(',').map(function(s) { return s.trim(); }).filter(Boolean);
			}
		);
	}
})();
</script>
</body>
</html>`
