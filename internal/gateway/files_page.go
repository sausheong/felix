package gateway

import (
	"fmt"
	"net/http"
)

// NewFilesPageHandler serves the operator-facing Files page. It is a
// self-contained HTML page that wraps the existing /files/* JSON APIs
// in a file-explorer UI styled to match the chat shell. The page is
// embedded into the chat sidebar via iframe (like Settings, Jobs, Logs).
func NewFilesPageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, filesPageHTML)
	}
}

const filesPageHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Files · Felix</title>
<link rel="icon" type="image/png" href="/favicon.png">
<style>
:root {
	--bg: oklch(0.985 0.005 95);
	--bg-header: oklch(0.99 0.005 95);
	--bg-card: oklch(0.99 0.005 95);
	--bg-input: oklch(0.97 0.005 95);
	--border: oklch(0.9 0.008 95);
	--text: oklch(0.22 0.01 95);
	--text-muted: oklch(0.5 0.01 95);
	--accent: oklch(0.55 0.13 162);
	--accent2: oklch(0.55 0.12 200);
	--btn-text: oklch(0.99 0.005 95);
	--error: oklch(0.55 0.18 27);
}
html.dark {
	--bg: oklch(0.18 0.01 162);
	--bg-header: oklch(0.21 0.01 162);
	--bg-card: oklch(0.22 0.01 162);
	--bg-input: oklch(0.25 0.01 162);
	--border: oklch(0.32 0.015 162);
	--text: oklch(0.92 0.005 95);
	--text-muted: oklch(0.68 0.01 95);
	--accent: oklch(0.78 0.13 162);
	--accent2: oklch(0.75 0.12 200);
	--btn-text: oklch(0.15 0.01 95);
	--error: oklch(0.72 0.18 27);
}
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
	font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
	background: var(--bg);
	color: var(--text);
	min-height: 100vh;
	display: flex;
	flex-direction: column;
	transition: background 0.3s, color 0.3s;
}
.icon {
	width: 1.1em; height: 1.1em;
	vertical-align: -0.15em;
	flex-shrink: 0;
	stroke: currentColor;
	fill: none;
	stroke-width: 1.5;
	stroke-linecap: round;
	stroke-linejoin: round;
}
#header {
	background: var(--bg-header);
	border-bottom: 1px solid var(--border);
	padding: 0.95rem 1.5rem;
	display: flex;
	align-items: center;
	gap: 0.75rem;
	flex-shrink: 0;
}
#header h1 { font-size: 1.05rem; color: var(--text); font-weight: 600; }
#header .spacer { margin-left: auto; }
#agent-label {
	padding: 0.35rem 0.65rem;
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 6px;
	color: var(--text-muted);
	font: inherit;
	font-size: 0.85rem;
	font-variant: tabular-nums;
}
#toolbar {
	background: var(--bg-header);
	border-bottom: 1px solid var(--border);
	padding: 0.55rem 1.5rem;
	display: flex;
	align-items: center;
	gap: 0.5rem;
	flex-shrink: 0;
}
.tb-btn, #files-upload-label {
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 8px;
	padding: 0.4rem 0.7rem;
	cursor: pointer;
	color: var(--text);
	font-family: inherit;
	font-size: 0.78rem;
	display: inline-flex;
	align-items: center;
	gap: 0.35rem;
	transition: border-color 0.15s, color 0.15s;
}
.tb-btn:hover, #files-upload-label:hover { border-color: var(--accent); color: var(--accent); }
.tb-btn[disabled] { opacity: 0.4; cursor: not-allowed; }
.tb-btn.danger { color: var(--error); border-color: color-mix(in oklch, var(--error) 30%, var(--border)); }
.tb-btn.danger:hover { background: var(--error); color: var(--btn-text); border-color: var(--error); }
#breadcrumbs {
	padding: 0.6rem 1.5rem;
	font-size: 0.8rem;
	color: var(--text-muted);
	border-bottom: 1px solid var(--border);
	word-break: break-all;
}
#breadcrumbs a { color: var(--accent2); text-decoration: none; cursor: pointer; }
#breadcrumbs a:hover { text-decoration: underline; }
#error-bar {
	background: color-mix(in oklch, var(--error) 12%, transparent);
	border-bottom: 1px solid var(--error);
	color: var(--error);
	font-size: 0.8rem;
	padding: 0.5rem 1.5rem;
}
#error-bar[hidden] { display: none; }
#files-list {
	flex: 1;
	overflow-y: auto;
	padding: 0.5rem 0;
}
.inline-filter {
	display: block;
	width: calc(100% - 3rem);
	margin: 0.4rem 1.5rem 0.6rem;
	padding: 0.4rem 0.6rem;
	font-size: 0.85rem;
	background: var(--bg);
	border: 1px solid var(--border);
	border-radius: 6px;
	color: var(--text);
}
.inline-filter:focus { border-color: var(--accent); outline: none; }
.file-row {
	display: flex;
	align-items: center;
	gap: 0.65rem;
	padding: 0.5rem 1.5rem;
	font-size: 0.88rem;
	color: var(--text);
	cursor: pointer;
	user-select: none;
}
.file-row:hover { background: var(--bg-card); }
.file-row.selected {
	background: color-mix(in oklch, var(--accent) 14%, transparent);
	color: var(--accent);
	font-weight: 600;
}
.file-row .file-icon { width: 1.2em; flex-shrink: 0; }
.file-row .file-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-row .file-size { color: var(--text-muted); font-size: 0.78rem; font-variant-numeric: tabular-nums; }
.files-empty { padding: 3rem 1.5rem; text-align: center; color: var(--text-muted); font-size: 0.9rem; }
.selection-count {
	margin-left: 0.6rem;
	color: var(--text-muted);
	font-size: 0.78rem;
	font-variant-numeric: tabular-nums;
}
.selection-count:empty { display: none; }

/* Modal (lifted from chat.go for prompt/confirm) */
.modal-backdrop {
	position: fixed; inset: 0;
	background: oklch(0 0 0 / 0.4);
	z-index: 1000;
	display: flex; align-items: center; justify-content: center;
	padding: 1rem;
	animation: modal-fade 0.15s ease-out;
}
@keyframes modal-fade { from { opacity: 0; } to { opacity: 1; } }
.modal {
	background: var(--bg-header);
	border: 1px solid var(--border);
	border-radius: 12px;
	padding: 1.25rem 1.5rem 1rem;
	width: 100%;
	max-width: 380px;
	box-shadow: 0 16px 48px oklch(0 0 0 / 0.25);
}
.modal-title { font-size: 1rem; font-weight: 600; color: var(--text); margin-bottom: 0.5rem; }
.modal-body { font-size: 0.88rem; color: var(--text-muted); line-height: 1.5; margin-bottom: 1rem; word-break: break-word; }
.modal-input {
	width: 100%;
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.5rem 0.75rem;
	font-family: inherit;
	font-size: 0.88rem;
	color: var(--text);
	margin-bottom: 1rem;
	outline: none;
	box-sizing: border-box;
}
.modal-input:focus { border-color: var(--accent); }
.modal-actions { display: flex; justify-content: flex-end; gap: 0.5rem; }
.modal-btn {
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.4rem 0.9rem;
	font-family: inherit;
	font-size: 0.85rem;
	color: var(--text);
	cursor: pointer;
}
.modal-btn:hover { background: var(--bg-card); }
.modal-btn-primary { background: var(--accent); color: var(--btn-text); border-color: var(--accent); font-weight: 600; }
.modal-btn-danger { background: var(--error); color: var(--btn-text); border-color: var(--error); font-weight: 600; }
</style>
</head>
<body>
<div id="header">
	<h1>Files</h1>
	<span class="spacer"></span>
	<span id="agent-label" aria-label="Current agent" title="Current agent"></span>
</div>
<div id="toolbar">
	<button class="tb-btn" id="refresh-btn" title="Refresh">
		<svg class="icon" viewBox="0 0 24 24"><path d="M21 12a9 9 0 1 1-3-6.7M21 4v5h-5"/></svg> Refresh
	</button>
	<button class="tb-btn" id="mkdir-btn" title="New folder">
		<svg class="icon" viewBox="0 0 24 24"><path d="M3 6.5C3 5.67 3.67 5 4.5 5H9l2 2h8.5c.83 0 1.5.67 1.5 1.5v9c0 .83-.67 1.5-1.5 1.5h-15c-.83 0-1.5-.67-1.5-1.5z"/><path d="M12 11v6M9 14h6"/></svg> New folder
	</button>
	<label id="files-upload-label" title="Upload">
		<input type="file" id="files-upload-input" hidden>
		<svg class="icon" viewBox="0 0 24 24"><path d="M12 16V4M7 9l5-5 5 5M5 18v2a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-2"/></svg> Upload
	</label>
	<span class="spacer" style="margin-left:auto;"></span>
	<button class="tb-btn" data-fileaction="download" disabled>
		<svg class="icon" viewBox="0 0 24 24"><path d="M12 4v12M7 11l5 5 5-5M5 18v2a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-2"/></svg> Download
	</button>
	<button class="tb-btn" data-fileaction="rename" disabled>
		<svg class="icon" viewBox="0 0 24 24"><path d="M16 4l4 4-11 11H5v-4z"/></svg> Rename
	</button>
	<button class="tb-btn" data-fileaction="move" disabled>
		<svg class="icon" viewBox="0 0 24 24"><path d="M7 17L17 7M9 7h8v8"/></svg> Move
	</button>
	<button class="tb-btn danger" data-fileaction="delete" disabled>
		<svg class="icon" viewBox="0 0 24 24"><path d="M4 7h16M10 7V5a2 2 0 0 1 2-2h0a2 2 0 0 1 2 2v2M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12"/></svg> Delete
	</button>
	<span id="selection-count" class="selection-count" aria-live="polite"></span>
</div>
<div id="breadcrumbs"></div>
<div id="error-bar" hidden></div>
<input id="files-filter" class="inline-filter" type="search" placeholder="Filter files in this folder...">
<div id="files-list"></div>

<script>
(function() {
	// Theme — inherit from felix-theme set by chat shell.
	if (localStorage.getItem('felix-theme') === 'dark') {
		document.documentElement.classList.add('dark');
	}

	var filesList = document.getElementById('files-list');
	var breadcrumbs = document.getElementById('breadcrumbs');
	var errorBar = document.getElementById('error-bar');
	var uploadInput = document.getElementById('files-upload-input');
	var toolbarBtns = document.querySelectorAll('[data-fileaction]');
	var filesFilter = document.getElementById('files-filter');
	if (filesFilter) {
		filesFilter.addEventListener('input', function() {
			var q = filesFilter.value.toLowerCase();
			var rows = filesList.querySelectorAll('.file-row');
			for (var i = 0; i < rows.length; i++) {
				var name = (rows[i].querySelector('.file-name') || {}).textContent || '';
				rows[i].style.display = (!q || name.toLowerCase().indexOf(q) !== -1) ? '' : 'none';
			}
		});
	}

	var cwd = '';
	// Multi-select state — set of entry names within the current cwd.
	// entriesByName holds the entry object for cheap lookup in toolbar actions.
	var selected = new Set();
	var entriesByName = {};
	var lastClickedName = null; // for shift-click range

	function fmtSize(n) {
		if (n == null) return '';
		if (n < 1024) return n + ' B';
		var u = ['KB','MB','GB'];
		var i = -1;
		do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
		return n.toFixed(1) + ' ' + u[i];
	}
	function escHtml(s) {
		return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
	}
	function showError(msg) {
		errorBar.textContent = msg;
		errorBar.hidden = false;
		setTimeout(function() { errorBar.hidden = true; }, 5000);
	}
	function setToolbarEnabled(any) {
		var multi = selected.size > 1;
		for (var i = 0; i < toolbarBtns.length; i++) {
			var btn = toolbarBtns[i];
			var act = btn.dataset.fileaction;
			// Single-only actions: rename, move, download (raw open is single-file)
			if (act === 'rename' || act === 'move' || act === 'download') {
				btn.disabled = !any || multi;
			} else {
				btn.disabled = !any;
			}
		}
		var counter = document.getElementById('selection-count');
		if (counter) {
			counter.textContent = selected.size > 1 ? selected.size + ' selected' : '';
		}
	}
	function clearSelection() {
		selected.clear();
		lastClickedName = null;
		var prevs = filesList.querySelectorAll('.file-row.selected');
		for (var i = 0; i < prevs.length; i++) prevs[i].classList.remove('selected');
		setToolbarEnabled(false);
	}
	function syncRowSelectedClasses() {
		var rows = filesList.querySelectorAll('.file-row[data-name]');
		for (var i = 0; i < rows.length; i++) {
			if (selected.has(rows[i].dataset.name)) rows[i].classList.add('selected');
			else rows[i].classList.remove('selected');
		}
	}

	function renderBreadcrumbs() {
		breadcrumbs.innerHTML = '';
		var parts = cwd === '' ? [] : cwd.split('/').filter(Boolean);
		var root = document.createElement('a');
		root.textContent = 'workspace';
		root.onclick = function() { cwd = ''; clearSelection(); loadFiles(); };
		breadcrumbs.appendChild(root);
		var acc = '';
		for (var i = 0; i < parts.length; i++) {
			breadcrumbs.appendChild(document.createTextNode(' / '));
			acc = acc ? acc + '/' + parts[i] : parts[i];
			var a = document.createElement('a');
			a.textContent = parts[i];
			(function(p) { a.onclick = function() { cwd = p; clearSelection(); loadFiles(); }; })(acc);
			breadcrumbs.appendChild(a);
		}
	}

	function renderList(entries) {
		filesList.innerHTML = '';
		if (cwd !== '') {
			var up = document.createElement('div');
			up.className = 'file-row';
			up.innerHTML = '<span class="file-icon"><svg class="icon" viewBox="0 0 24 24"><path d="M5 12l7-7 7 7M12 5v14"/></svg></span><span class="file-name">..</span>';
			up.onclick = function() {
				var parts = cwd.split('/').filter(Boolean);
				parts.pop();
				cwd = parts.join('/');
				clearSelection();
				loadFiles();
			};
			filesList.appendChild(up);
		}
		if (!entries || entries.length === 0) {
			if (cwd === '') {
				var empty = document.createElement('div');
				empty.className = 'files-empty';
				empty.textContent = 'No files yet. Upload one or have the agent create one.';
				filesList.appendChild(empty);
			}
			return;
		}
		entriesByName = {};
		for (var k = 0; k < entries.length; k++) entriesByName[entries[k].name] = entries[k];
		for (var i = 0; i < entries.length; i++) {
			var e = entries[i];
			var row = document.createElement('div');
			row.className = 'file-row';
			row.dataset.name = e.name;
			row.dataset.type = e.type;
			var iconSvg = e.type === 'dir'
				? '<svg class="icon" viewBox="0 0 24 24"><path d="M3 6.5C3 5.67 3.67 5 4.5 5H9l2 2h8.5c.83 0 1.5.67 1.5 1.5v9c0 .83-.67 1.5-1.5 1.5h-15c-.83 0-1.5-.67-1.5-1.5z"/></svg>'
				: '<svg class="icon" viewBox="0 0 24 24"><path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/></svg>';
			row.innerHTML = '<span class="file-icon">' + iconSvg + '</span>' +
				'<span class="file-name">' + escHtml(e.name) + '</span>' +
				(e.type === 'file' ? '<span class="file-size">' + fmtSize(e.size) + '</span>' : '');
			(function(entry, rowEl) {
				rowEl.addEventListener('click', function(ev) {
					if (ev.detail === 2 && entry.type === 'dir') {
						cwd = cwd ? cwd + '/' + entry.name : entry.name;
						clearSelection();
						loadFiles();
						return;
					}
					if (ev.detail === 2 && entry.type === 'file') {
						window.open('/files/raw?agent=' + encodeURIComponent(agentId) +
							'&path=' + encodeURIComponent((cwd ? cwd + '/' : '') + entry.name), '_blank');
						return;
					}
					// single click — selection logic
					var name = entry.name;
					if (ev.shiftKey && lastClickedName) {
						// Range select from lastClickedName to name (inclusive).
						var names = Array.prototype.map.call(
							filesList.querySelectorAll('.file-row[data-name]'),
							function(r) { return r.dataset.name; }
						);
						var a = names.indexOf(lastClickedName);
						var b = names.indexOf(name);
						if (a !== -1 && b !== -1) {
							var lo = Math.min(a, b), hi = Math.max(a, b);
							for (var i = lo; i <= hi; i++) selected.add(names[i]);
						}
					} else if (ev.metaKey || ev.ctrlKey) {
						// Toggle this row in/out of the selection.
						if (selected.has(name)) selected.delete(name);
						else selected.add(name);
						lastClickedName = name;
					} else {
						// Plain click — replace selection.
						selected.clear();
						selected.add(name);
						lastClickedName = name;
					}
					syncRowSelectedClasses();
					setToolbarEnabled(selected.size > 0);
				});
			})(e, row);
			filesList.appendChild(row);
		}
	}

	function loadFiles() {
		if (!agentId) return;
		var url = '/files/list?agent=' + encodeURIComponent(agentId) +
			'&dir=' + encodeURIComponent(cwd);
		fetch(url).then(function(r) {
			if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
			return r.json();
		}).then(function(data) {
			renderBreadcrumbs();
			renderList(data.entries || []);
		}).catch(function(e) {
			showError('Could not load files: ' + e.message);
		});
	}

	// Modal helpers (slim, no focus trap — page is single-purpose).
	function modal(opts) {
		return new Promise(function(resolve) {
			var bd = document.createElement('div');
			bd.className = 'modal-backdrop';
			var m = document.createElement('div');
			m.className = 'modal';
			if (opts.title) {
				var t = document.createElement('div');
				t.className = 'modal-title';
				t.textContent = opts.title;
				m.appendChild(t);
			}
			if (opts.body) {
				var b = document.createElement('div');
				b.className = 'modal-body';
				b.textContent = opts.body;
				m.appendChild(b);
			}
			var input = null;
			if (opts.kind === 'prompt') {
				input = document.createElement('input');
				input.type = 'text';
				input.className = 'modal-input';
				if (opts.defaultValue) input.value = opts.defaultValue;
				m.appendChild(input);
			}
			var actions = document.createElement('div');
			actions.className = 'modal-actions';
			function done(v) {
				document.removeEventListener('keydown', onKey);
				if (bd.parentNode) bd.parentNode.removeChild(bd);
				resolve(v);
			}
			function cancelV() {
				if (opts.kind === 'prompt') return null;
				if (opts.kind === 'alert') return undefined;
				return false;
			}
			if (opts.kind !== 'alert') {
				var c = document.createElement('button');
				c.className = 'modal-btn';
				c.textContent = 'Cancel';
				c.onclick = function() { done(cancelV()); };
				actions.appendChild(c);
			}
			var ok = document.createElement('button');
			ok.className = 'modal-btn modal-btn-primary' + (opts.danger ? ' modal-btn-danger' : '');
			ok.textContent = opts.confirmLabel || (opts.kind === 'alert' ? 'OK' : 'Confirm');
			ok.onclick = function() {
				if (opts.kind === 'prompt') done(input ? input.value : '');
				else if (opts.kind === 'alert') done();
				else done(true);
			};
			actions.appendChild(ok);
			m.appendChild(actions);
			bd.appendChild(m);
			bd.addEventListener('mousedown', function(e) { if (e.target === bd) done(cancelV()); });
			function onKey(e) {
				if (e.key === 'Escape') { e.preventDefault(); done(cancelV()); }
				else if (e.key === 'Enter' && input) { e.preventDefault(); done(input.value); }
			}
			document.addEventListener('keydown', onKey);
			document.body.appendChild(bd);
			setTimeout(function() { (input || ok).focus(); }, 0);
		});
	}

	// Toolbar handlers.
	document.getElementById('refresh-btn').addEventListener('click', function() { clearSelection(); loadFiles(); });
	document.getElementById('mkdir-btn').addEventListener('click', function() {
		modal({ kind: 'prompt', title: 'New folder', body: 'Folder name', confirmLabel: 'Create' }).then(function(name) {
			if (!name) return;
			var target = (cwd ? cwd + '/' : '') + name;
			fetch('/files/mkdir', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ agent: agentId, path: target })
			}).then(function(r) {
				if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
				clearSelection();
				loadFiles();
			}).catch(function(e) { showError('mkdir failed: ' + e.message); });
		});
	});
	uploadInput.addEventListener('change', function() {
		var file = uploadInput.files[0];
		if (!file) return;
		var fd = new FormData();
		fd.append('file', file);
		var url = '/files/upload?agent=' + encodeURIComponent(agentId) +
			'&dir=' + encodeURIComponent(cwd);
		fetch(url, { method: 'POST', body: fd }).then(function(r) {
			if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
			uploadInput.value = '';
			clearSelection();
			loadFiles();
		}).catch(function(e) { showError('Upload failed: ' + e.message); });
	});
	document.querySelector('[data-fileaction="download"]').addEventListener('click', function() {
		if (selected.size !== 1) return; // disabled in toolbar, defensive
		var only = Array.from(selected)[0];
		window.open('/files/raw?agent=' + encodeURIComponent(agentId) +
			'&path=' + encodeURIComponent((cwd ? cwd + '/' : '') + only), '_blank');
	});
	document.querySelector('[data-fileaction="rename"]').addEventListener('click', function() {
		if (selected.size !== 1) return; // disabled in toolbar, defensive
		var only = Array.from(selected)[0];
		var path = (cwd ? cwd + '/' : '') + only;
		modal({ kind: 'prompt', title: 'Rename', body: 'New name', defaultValue: only, confirmLabel: 'Rename' }).then(function(name) {
			if (!name || name === only) return;
			fetch('/files/rename', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ agent: agentId, path: path, newName: name })
			}).then(function(r) {
				if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
				clearSelection();
				loadFiles();
			}).catch(function(e) { showError('Rename failed: ' + e.message); });
		});
	});
	document.querySelector('[data-fileaction="move"]').addEventListener('click', function() {
		if (selected.size !== 1) return; // disabled in toolbar, defensive
		var only = Array.from(selected)[0];
		var path = (cwd ? cwd + '/' : '') + only;
		modal({ kind: 'prompt', title: 'Move', body: 'Destination path (relative, includes filename)', defaultValue: path, confirmLabel: 'Move' }).then(function(dest) {
			if (!dest || dest === path) return;
			fetch('/files/move', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ agent: agentId, from: path, to: dest })
			}).then(function(r) {
				if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
				clearSelection();
				loadFiles();
			}).catch(function(e) { showError('Move failed: ' + e.message); });
		});
	});
	document.querySelector('[data-fileaction="delete"]').addEventListener('click', function() {
		var names = Array.from(selected);
		if (names.length === 0) return;
		var hasDir = false;
		for (var i = 0; i < names.length; i++) {
			var ent = entriesByName[names[i]];
			if (ent && ent.type === 'dir') { hasDir = true; break; }
		}
		var label;
		if (names.length === 1) {
			var only = entriesByName[names[0]];
			label = (only && only.type === 'dir')
				? 'Delete folder "' + names[0] + '" and everything inside it? This cannot be undone.'
				: 'Delete "' + names[0] + '"? This cannot be undone.';
		} else {
			label = 'Delete ' + names.length + ' items' + (hasDir ? ' (including folders)' : '') + '? This cannot be undone.';
		}
		modal({ kind: 'confirm', title: 'Delete', body: label, confirmLabel: 'Delete', danger: true }).then(function(ok) {
			if (!ok) return;
			Promise.all(names.map(function(n) {
				var ent = entriesByName[n];
				var p = (cwd ? cwd + '/' : '') + n;
				var url = '/files?agent=' + encodeURIComponent(agentId) +
					'&path=' + encodeURIComponent(p) +
					(ent && ent.type === 'dir' ? '&recursive=true' : '');
				return fetch(url, { method: 'DELETE' }).then(function(r) {
					if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
				});
			})).then(function() {
				clearSelection();
				loadFiles();
			}).catch(function(err) { showError('Delete failed: ' + err.message); });
		});
	});

	// Agent comes from ?agent=<id> query param (the chat shell passes
	// the currently-selected agent when navigating to /files). Falls
	// back to the first configured agent if the page is opened directly.
	var qs = new URLSearchParams(window.location.search);
	var agentId = qs.get('agent') || '';
	var agentLabel = document.getElementById('agent-label');

	function setAgent(id) {
		agentId = id;
		if (agentLabel) agentLabel.textContent = id || '(no agent)';
		if (id) loadFiles();
	}

	if (agentId) {
		setAgent(agentId);
	} else {
		// Fallback: deep-link without ?agent= → use the first configured agent.
		fetch('/settings/api/config').then(function(r) { return r.ok ? r.json() : null; })
			.then(function(cfg) {
				var agents = (cfg && cfg.agents && cfg.agents.list) || [];
				if (agents.length === 0) {
					if (agentLabel) agentLabel.textContent = '(no agent)';
					showError('No agents configured. Add one in Settings → Agents first.');
					return;
				}
				console.warn('[files] no ?agent= query param; defaulting to', agents[0].id);
				setAgent(agents[0].id);
			})
			.catch(function(e) { showError('Could not load agents: ' + e.message); });
	}

	// Keyboard shortcuts: Esc clears, Delete batch-deletes, Cmd/Ctrl-A selects visible.
	document.addEventListener('keydown', function(ev) {
		// Don't interfere with text inputs / modals.
		var t = ev.target;
		if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
		if (document.querySelector('.modal-backdrop')) return;

		if (ev.key === 'Escape' && selected.size > 0) {
			ev.preventDefault();
			clearSelection();
			return;
		}
		if (ev.key === 'Delete' || ev.key === 'Backspace') {
			if (selected.size === 0) return;
			ev.preventDefault();
			var deleteBtn = document.querySelector('[data-fileaction="delete"]');
			if (deleteBtn && !deleteBtn.disabled) deleteBtn.click();
			return;
		}
		if ((ev.metaKey || ev.ctrlKey) && (ev.key === 'a' || ev.key === 'A')) {
			ev.preventDefault();
			// Select all visible (post-filter) rows.
			var rows = filesList.querySelectorAll('.file-row[data-name]');
			selected.clear();
			for (var i = 0; i < rows.length; i++) {
				if (rows[i].style.display !== 'none') selected.add(rows[i].dataset.name);
			}
			syncRowSelectedClasses();
			setToolbarEnabled(selected.size > 0);
		}
	});
})();
</script>
</body>
</html>`
