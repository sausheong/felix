package gateway

import (
	"fmt"
	"net/http"
)

// NewChatHandler returns an HTTP handler func that serves the chat web interface.
func NewChatHandler(port int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src ws: wss:; img-src 'self' data:")
		fmt.Fprintf(w, chatHTML, port)
	}
}

const chatHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Felix Chat</title>
<style>
:root {
	--bg: #1a1a2e;
	--bg-header: #16213e;
	--bg-msg-user: #0f3460;
	--bg-msg-asst: #16213e;
	--bg-code: #0d1b36;
	--bg-input: #0d1b36;
	--border: #0f3460;
	--text: #e0e0e0;
	--text-muted: #888;
	--text-strong: #fff;
	--text-em: #ccc;
	--accent: #16dbaa;
	--accent2: #53a8b6;
	--btn-text: #1a1a2e;
	--placeholder: #555;
	--error: #e74c3c;
	--tool-output: #aaa;
}
html.light {
	--bg: #f5f5f5;
	--bg-header: #ffffff;
	--bg-msg-user: #d1e7ff;
	--bg-msg-asst: #ffffff;
	--bg-code: #f0f0f0;
	--bg-input: #ffffff;
	--border: #ddd;
	--text: #1a1a1a;
	--text-muted: #777;
	--text-strong: #000;
	--text-em: #333;
	--accent: #0fa888;
	--accent2: #3a7f8c;
	--btn-text: #fff;
	--placeholder: #999;
	--error: #d32f2f;
	--tool-output: #555;
}
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
	font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, monospace;
	background: var(--bg);
	color: var(--text);
	height: 100vh;
	display: flex;
	flex-direction: column;
	transition: background 0.3s, color 0.3s;
}
#header {
	background: var(--bg-header);
	padding: 0.75rem 1.5rem;
	border-bottom: 1px solid var(--border);
	display: flex;
	align-items: center;
	gap: 0.75rem;
	flex-shrink: 0;
	transition: background 0.3s, border-color 0.3s;
}
#header .logo {
	width: 24px; height: 24px;
	filter: invert(1);
	transition: filter 0.3s;
}
html.light #header .logo {
	filter: none;
}
#header h1 { font-size: 1.1rem; color: var(--accent); }
#header .status { font-size: 0.8rem; color: var(--text-muted); }
#header .spacer { margin-left: auto; }
#theme-btn {
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.3rem 0.5rem;
	cursor: pointer;
	font-size: 1rem;
	line-height: 1;
	color: var(--text);
	transition: border-color 0.3s;
}
#theme-btn:hover, #clear-btn:hover, #toggle-tools-btn:hover, #agent-select:hover, #session-select:hover, #new-session-btn:hover { border-color: var(--accent); }
#toggle-tools-btn {
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
#toggle-tools-btn.active {
	border-color: var(--accent);
	color: var(--accent);
}
#toggle-trace-btn {
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
#toggle-trace-btn:hover { border-color: var(--accent); }
#toggle-trace-btn.active {
	border-color: var(--accent);
	color: var(--accent);
}
#trace-panel {
	border-top: 1px solid var(--border);
	background: var(--bg-msg-asst);
	max-height: 32vh;
	overflow-y: auto;
	font-family: "SF Mono", "Fira Code", monospace;
	font-size: 0.75rem;
	flex-shrink: 0;
}
#trace-header {
	display: flex;
	align-items: center;
	gap: 0.5rem;
	padding: 0.4rem 1.5rem;
	border-bottom: 1px solid var(--border);
	color: var(--text-muted);
	background: var(--bg-header);
	position: sticky;
	top: 0;
}
#trace-title { font-weight: 600; color: var(--text); }
#trace-clear-btn {
	margin-left: auto;
	background: none;
	border: 1px solid var(--border);
	border-radius: 4px;
	padding: 0.15rem 0.5rem;
	cursor: pointer;
	font-size: 0.7rem;
	color: var(--text-muted);
}
#trace-clear-btn:hover { border-color: var(--accent); color: var(--accent); }
#trace-list {
	padding: 0.4rem 1.5rem;
	display: flex;
	flex-direction: column;
	gap: 0.15rem;
}
.trace-row {
	display: grid;
	grid-template-columns: 5em 5em 1fr;
	gap: 0.6rem;
	color: var(--text-muted);
	white-space: nowrap;
	overflow: hidden;
	text-overflow: ellipsis;
}
.trace-row .t-at { color: var(--text); text-align: right; }
.trace-row .t-dur { color: var(--accent2); text-align: right; }
.trace-row .t-phase { color: var(--text); }
.trace-row .t-attrs { color: var(--text-muted); }
.trace-row.slow .t-dur { color: var(--error); }
.trace-row.run-divider {
	color: var(--accent);
	border-top: 1px dashed var(--border);
	padding-top: 0.25rem;
	margin-top: 0.25rem;
}
#token-chip {
	font-size: 0.75rem;
	color: var(--text-muted);
	font-family: "SF Mono","Fira Code",monospace;
	padding: 0.2rem 0.5rem;
	border: 1px solid var(--border);
	border-radius: 12px;
	white-space: nowrap;
	cursor: help;
}
#token-chip.warn { border-color: var(--accent2); color: var(--accent2); }
#token-chip.danger { border-color: var(--error); color: var(--error); }
#bootstrap-banner {
	display: none;
	background: var(--bg-msg-asst);
	border-bottom: 1px solid var(--border);
	padding: 0.85rem 1.5rem;
	flex-shrink: 0;
}
#bootstrap-banner .bb-header {
	font-size: 0.85rem;
	color: var(--text);
	margin-bottom: 0.45rem;
	display: flex;
	gap: 0.5rem;
	align-items: baseline;
}
#bootstrap-banner .bb-title { font-weight: 600; color: var(--accent); }
#bootstrap-banner .bb-summary { color: var(--text-muted); }
#bootstrap-banner .bb-models {
	display: flex;
	flex-direction: column;
	gap: 0.4rem;
}
#bootstrap-banner .bb-row {
	display: grid;
	grid-template-columns: 12em 1fr 6em;
	gap: 0.6rem;
	font-size: 0.78rem;
	align-items: center;
}
#bootstrap-banner .bb-name { color: var(--text); font-family: "SF Mono","Fira Code",monospace; }
#bootstrap-banner .bb-status { color: var(--text-muted); text-align: right; font-variant-numeric: tabular-nums; }
#bootstrap-banner .bb-bar {
	height: 6px;
	background: var(--border);
	border-radius: 3px;
	overflow: hidden;
}
#bootstrap-banner .bb-fill {
	height: 100%%;
	background: var(--accent);
	width: 0%%;
	transition: width 0.4s ease;
}
#bootstrap-banner .bb-row.done .bb-fill { background: var(--accent2); }
#bootstrap-banner .bb-row.error .bb-fill { background: var(--error); }
#input-area.bootstrapping textarea { opacity: 0.5; pointer-events: none; }
#input-area.bootstrapping textarea::placeholder { color: var(--accent); }
#agent-select {
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.3rem 0.5rem;
	font-size: 0.85rem;
	color: var(--text);
	font-family: inherit;
	outline: none;
	cursor: pointer;
	transition: background 0.3s, border-color 0.3s, color 0.3s;
}
#agent-select:focus, #session-select:focus { border-color: var(--accent); }
#session-select {
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.3rem 0.5rem;
	font-size: 0.85rem;
	color: var(--text);
	font-family: inherit;
	outline: none;
	cursor: pointer;
	transition: background 0.3s, border-color 0.3s, color 0.3s;
}
#new-session-btn {
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
#clear-btn {
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
#messages {
	flex: 1;
	overflow-y: auto;
	padding: 1rem 1.5rem;
	display: flex;
	flex-direction: column;
	gap: 1rem;
}
.msg {
	max-width: 85%%;
	padding: 0.75rem 1rem;
	border-radius: 12px;
	line-height: 1.5;
	word-wrap: break-word;
	overflow-wrap: break-word;
	transition: background 0.3s, border-color 0.3s;
}
.msg.user {
	background: var(--bg-msg-user);
	align-self: flex-end;
	border-bottom-right-radius: 4px;
}
.msg.assistant {
	background: var(--bg-msg-asst);
	align-self: flex-start;
	border-bottom-left-radius: 4px;
	border: 1px solid var(--border);
}
.msg.assistant .content p { margin-bottom: 0.5em; }
.msg.assistant .content p:last-child { margin-bottom: 0; }
.msg.assistant .content code {
	background: var(--bg-code);
	padding: 0.15em 0.4em;
	border-radius: 3px;
	font-size: 0.9em;
	font-family: "SF Mono", "Fira Code", monospace;
}
.msg.assistant .content pre {
	background: var(--bg-code);
	padding: 0.75rem;
	border-radius: 6px;
	overflow-x: auto;
	margin: 0.5em 0;
	border: 1px solid var(--border);
	transition: background 0.3s, border-color 0.3s;
}
.msg.assistant .content pre code {
	background: none;
	padding: 0;
	font-size: 0.85em;
}
.msg.assistant .content a { color: var(--accent2); }
.msg.assistant .content strong { color: var(--text-strong); }
.msg.assistant .content em { color: var(--text-em); }
.msg.assistant .content h1,
.msg.assistant .content h2,
.msg.assistant .content h3,
.msg.assistant .content h4,
.msg.assistant .content h5,
.msg.assistant .content h6 {
	margin: 0.75em 0 0.25em;
	color: var(--text-strong);
}
.msg.assistant .content h1 { font-size: 1.4em; }
.msg.assistant .content h2 { font-size: 1.2em; }
.msg.assistant .content h3 { font-size: 1.05em; }
.msg.assistant .content hr {
	border: none;
	border-top: 1px solid var(--border);
	margin: 0.75em 0;
}
.msg.assistant .content ul, .msg.assistant .content ol {
	margin: 0.5em 0 0.5em 1.5em;
}
.msg.assistant .content li { margin-bottom: 0.25em; }
.msg.assistant .content table {
	border-collapse: collapse;
	margin: 0.5em 0;
	display: block;
	overflow-x: auto;
	max-width: 100%%;
}
.msg.assistant .content th,
.msg.assistant .content td {
	border: 1px solid var(--border);
	padding: 0.4em 0.75em;
	text-align: left;
}
.msg.assistant .content th {
	background: var(--bg-code);
	color: var(--text-strong);
	font-weight: 600;
}
.msg.assistant .content tr:nth-child(even) td {
	background: rgba(128,128,128,0.07);
}
.tool-call {
	background: var(--bg-code);
	border: 1px solid var(--border);
	border-radius: 6px;
	margin: 0.5rem 0;
	font-size: 0.85rem;
	max-width: 85%%;
	align-self: flex-start;
	transition: background 0.3s, border-color 0.3s;
}
.tool-call-header {
	padding: 0.4rem 0.75rem;
	color: var(--accent2);
	cursor: pointer;
	display: flex;
	align-items: center;
	gap: 0.5rem;
	user-select: none;
}
.tool-call-header .arrow {
	font-size: 0.7em;
	transition: transform 0.2s;
}
.tool-call-header .arrow.open { transform: rotate(90deg); }
.tool-call-output {
	display: none;
	padding: 0.5rem 0.75rem;
	border-top: 1px solid var(--border);
	color: var(--tool-output);
	white-space: pre-wrap;
	max-height: 300px;
	overflow-y: auto;
	font-family: "SF Mono", "Fira Code", monospace;
	font-size: 0.8rem;
}
.tool-call-output.show { display: block; }
.tool-call-output.error { color: var(--error); }
.tool-call-output img {
	display: block;
	max-width: 100%%;
	max-height: 500px;
	border-radius: 6px;
	margin-top: 0.5rem;
	cursor: pointer;
}
.tool-call-output.has-image { max-height: none; }
.hide-tools .tool-call { display: none; }
.tool-call-header .tool-detail {
	color: var(--text-muted);
	font-family: "SF Mono", "Fira Code", monospace;
	font-size: 0.9em;
	max-width: 500px;
	overflow: hidden;
	text-overflow: ellipsis;
	white-space: nowrap;
	display: inline-block;
	vertical-align: bottom;
}
#input-area {
	background: var(--bg-header);
	padding: 0.75rem 1.5rem;
	border-top: 1px solid var(--border);
	display: flex;
	gap: 0.75rem;
	flex-shrink: 0;
	transition: background 0.3s, border-color 0.3s;
}
#input {
	flex: 1;
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 8px;
	padding: 0.6rem 1rem;
	color: var(--text);
	font-size: 0.95rem;
	font-family: inherit;
	outline: none;
	resize: none;
	min-height: 40px;
	max-height: 150px;
	transition: background 0.3s, border-color 0.3s, color 0.3s;
}
#input:focus { border-color: var(--accent); }
#input::placeholder { color: var(--placeholder); }
#send-btn {
	background: var(--accent);
	color: var(--btn-text);
	border: none;
	border-radius: 8px;
	padding: 0 1.25rem;
	font-size: 0.95rem;
	font-weight: 600;
	cursor: pointer;
	transition: opacity 0.2s, background 0.3s;
	align-self: flex-end;
	height: 40px;
}
#send-btn:hover { opacity: 0.85; }
#send-btn:disabled { opacity: 0.4; cursor: not-allowed; }
#stop-btn {
	background: var(--error);
	color: #fff;
	border: none;
	border-radius: 8px;
	padding: 0 1.25rem;
	font-size: 0.95rem;
	font-weight: 600;
	cursor: pointer;
	transition: opacity 0.2s, background 0.3s;
	align-self: flex-end;
	height: 40px;
	display: none;
}
#stop-btn:hover { opacity: 0.85; }
</style>
</head>
<body>
<div id="header">
	<h1>Felix</h1>
	<select id="agent-select" title="Select agent"></select>
	<select id="session-select" title="Select session"></select>
	<button id="new-session-btn" title="New session">+ New</button>
	<span class="spacer"></span>
	<span id="token-chip" title="Tokens used in last turn / context window" style="display:none;"></span>
	<button id="toggle-tools-btn" title="Hide/show tool calls">Tools</button>
	<button id="toggle-trace-btn" title="Hide/show live trace panel">Trace</button>
	<button id="clear-btn" title="Clear session">Clear</button>
	<button id="theme-btn" title="Toggle light/dark mode">&#9790;</button>
	<span class="status" id="conn-status">connecting...</span>
</div>
<div id="bootstrap-banner">
	<div class="bb-header">
		<span class="bb-title">Setting up your local AI</span>
		<span class="bb-summary" id="bb-summary"></span>
	</div>
	<div class="bb-models" id="bb-models"></div>
</div>
<div id="messages"></div>
<div id="trace-panel" style="display:none;">
	<div id="trace-header"><span id="trace-title">Live trace</span><button id="trace-clear-btn" title="Clear trace">clear</button></div>
	<div id="trace-list"></div>
</div>
<div id="input-area">
	<textarea id="input" rows="1" placeholder="Type a message..." autofocus></textarea>
	<button id="send-btn" disabled>Send</button>
	<button id="stop-btn">Stop</button>
</div>

<script>
(function() {
	var PORT = %d;
	var wsProto = (location.protocol === 'https:') ? 'wss://' : 'ws://';
	var wsBase = wsProto + location.host + location.pathname.replace(/\/chat\/?$/, '');
	var messagesEl = document.getElementById('messages');
	var inputEl = document.getElementById('input');
	var sendBtn = document.getElementById('send-btn');
	var connStatus = document.getElementById('conn-status');
	var themeBtn = document.getElementById('theme-btn');
	var clearBtn = document.getElementById('clear-btn');
	var stopBtn = document.getElementById('stop-btn');
	var agentSelect = document.getElementById('agent-select');
	var sessionSelect = document.getElementById('session-select');
	var newSessionBtn = document.getElementById('new-session-btn');
	var toggleToolsBtn = document.getElementById('toggle-tools-btn');

	// Tool visibility toggle
	var toolsHidden = localStorage.getItem('felix-hide-tools') === 'true';
	function applyToolVisibility() {
		if (toolsHidden) {
			messagesEl.classList.add('hide-tools');
			toggleToolsBtn.classList.remove('active');
		} else {
			messagesEl.classList.remove('hide-tools');
			toggleToolsBtn.classList.add('active');
		}
	}
	applyToolVisibility();
	toggleToolsBtn.addEventListener('click', function() {
		toolsHidden = !toolsHidden;
		localStorage.setItem('felix-hide-tools', toolsHidden);
		applyToolVisibility();
	});

	// Live trace panel
	var toggleTraceBtn = document.getElementById('toggle-trace-btn');
	var tracePanel = document.getElementById('trace-panel');
	var traceList = document.getElementById('trace-list');
	var traceClearBtn = document.getElementById('trace-clear-btn');
	var traceVisible = localStorage.getItem('felix-show-trace') === 'true';
	var traceFirstOfRun = true;
	function applyTraceVisibility() {
		tracePanel.style.display = traceVisible ? 'block' : 'none';
		toggleTraceBtn.classList.toggle('active', traceVisible);
	}
	applyTraceVisibility();
	toggleTraceBtn.addEventListener('click', function() {
		traceVisible = !traceVisible;
		localStorage.setItem('felix-show-trace', traceVisible);
		applyTraceVisibility();
	});
	traceClearBtn.addEventListener('click', function() {
		traceList.innerHTML = '';
	});

	function fmtMs(n) {
		if (n == null) return '';
		if (n < 1000) return n + 'ms';
		return (n / 1000).toFixed(1) + 's';
	}

	// Bootstrap banner — surfaces first-run model pulls from
	// /settings/api/bootstrap so a user landing on /chat sees real
	// progress instead of a chat box that ignores their messages while
	// 10 GB of gemma4 downloads in the background.
	var bootstrapBanner = document.getElementById('bootstrap-banner');
	var bbSummary = document.getElementById('bb-summary');
	var bbModels = document.getElementById('bb-models');
	var inputArea = document.getElementById('input-area');
	var bootstrapPollTimer = null;
	var bootstrapWasActive = false;

	function fmtBytes(n) {
		if (!n || n < 0) return '';
		if (n < 1024) return n + ' B';
		var u = ['KB','MB','GB','TB'];
		var i = -1;
		do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
		return n.toFixed(1) + ' ' + u[i];
	}

	function renderBootstrap(snap) {
		if (!snap || !snap.models) return false;
		var names = Object.keys(snap.models);
		if (names.length === 0) return false;
		names.sort();

		// The banner is shown only when a bootstrap is actually in flight
		// (snap.active === true). The tracker can carry stale per-model
		// state from before EnsureFirstRunModels' early-return path was
		// fixed, so we don't infer "active" from individual model statuses
		// alone — that produced false-positives where the chat input was
		// disabled long after downloads completed.
		if (!snap.active) {
			if (bootstrapWasActive) {
				// Just transitioned active→inactive. Fade after a short
				// victory display so the user sees "ready" briefly.
				setTimeout(function() {
					bootstrapBanner.style.display = 'none';
					inputArea.classList.remove('bootstrapping');
					inputEl.placeholder = 'Type a message...';
				}, 2500);
				bootstrapWasActive = false;
			}
			return false;
		}

		var doneCount = 0;
		bbModels.innerHTML = '';
		names.forEach(function(name) {
			var m = snap.models[name];
			var row = document.createElement('div');
			row.className = 'bb-row';
			if (m.status === 'done') row.classList.add('done');
			if (m.error) row.classList.add('error');

			var nm = document.createElement('div');
			nm.className = 'bb-name';
			nm.textContent = name;

			var bar = document.createElement('div');
			bar.className = 'bb-bar';
			var fill = document.createElement('div');
			fill.className = 'bb-fill';
			fill.style.width = (m.pct || (m.status === 'done' ? 100 : 0)) + '%%';
			bar.appendChild(fill);

			var st = document.createElement('div');
			st.className = 'bb-status';
			if (m.error) {
				st.textContent = 'error';
				st.title = m.error;
			} else if (m.status === 'done') {
				st.textContent = 'ready';
				doneCount++;
			} else if (m.completed && m.total) {
				st.textContent = fmtBytes(m.completed) + '/' + fmtBytes(m.total);
			} else if (m.status) {
				st.textContent = m.status;
			}

			row.appendChild(nm);
			row.appendChild(bar);
			row.appendChild(st);
			bbModels.appendChild(row);
		});

		bbSummary.textContent = doneCount + '/' + names.length + ' ready';
		bootstrapBanner.style.display = 'block';
		inputArea.classList.add('bootstrapping');
		inputEl.placeholder = 'Local AI is downloading… please wait';
		bootstrapWasActive = true;
		return true;
	}

	// Token chip: rendered in the header on every EventDone with usage.
	// Format: "INPUT/CTX_WINDOW (PCT%%) +OUT" — gives the user a feel for
	// how close they are to compaction and how much they spent this turn.
	var tokenChip = document.getElementById('token-chip');
	function fmtTokens(n) {
		if (n == null) return '?';
		if (n < 1000) return String(n);
		if (n < 1000000) return (n / 1000).toFixed(n < 10000 ? 1 : 0) + 'K';
		return (n / 1000000).toFixed(2) + 'M';
	}
	function updateTokenChip(usage, ctxWindow, model) {
		if (!usage || !tokenChip) return;
		var inTok = (usage.input_tokens || 0) + (usage.cache_creation_input_tokens || 0) + (usage.cache_read_input_tokens || 0);
		var outTok = usage.output_tokens || 0;
		var pct = ctxWindow > 0 ? (inTok / ctxWindow) * 100 : 0;
		tokenChip.textContent = fmtTokens(inTok) + (ctxWindow > 0 ? '/' + fmtTokens(ctxWindow) : '') +
			(ctxWindow > 0 ? ' (' + pct.toFixed(0) + '%%)' : '') +
			'  +' + fmtTokens(outTok);
		tokenChip.title = 'Last turn: input=' + inTok + ', output=' + outTok +
			(ctxWindow > 0 ? ', context window=' + ctxWindow : '') +
			(model ? ', model=' + model : '');
		tokenChip.classList.remove('warn', 'danger');
		if (pct >= 80) tokenChip.classList.add('danger');
		else if (pct >= 60) tokenChip.classList.add('warn');
		tokenChip.style.display = '';
	}

	function pollBootstrap() {
		fetch('/settings/api/bootstrap', { cache: 'no-store' })
			.then(function(r) { return r.ok ? r.json() : null; })
			.then(function(snap) {
				var stillActive = renderBootstrap(snap);
				if (bootstrapPollTimer) { clearTimeout(bootstrapPollTimer); bootstrapPollTimer = null; }
				if (stillActive) {
					bootstrapPollTimer = setTimeout(pollBootstrap, 1500);
				}
			})
			.catch(function() { /* endpoint absent / transient — ignore */ });
	}
	pollBootstrap();

	function summarizeAttrs(attrs) {
		if (!attrs) return '';
		var keys = Object.keys(attrs);
		if (keys.length === 0) return '';
		var parts = [];
		for (var i = 0; i < keys.length && parts.length < 3; i++) {
			var k = keys[i];
			var v = attrs[k];
			if (typeof v === 'string' && v.length > 40) v = v.slice(0, 40) + '…';
			parts.push(k + '=' + v);
		}
		return parts.join(' ');
	}

	function addTraceRow(r) {
		// Insert a divider when a new run starts (ws.received is the first
		// mark of every chat.send).
		if (r.phase === 'ws.received') {
			traceFirstOfRun = true;
		}
		var row = document.createElement('div');
		row.className = 'trace-row';
		if (traceFirstOfRun && r.phase === 'ws.received' && traceList.children.length > 0) {
			row.classList.add('run-divider');
		}
		traceFirstOfRun = false;
		if (r.dur_ms != null && r.dur_ms >= 1500) {
			row.classList.add('slow');
		}
		var at = document.createElement('span');
		at.className = 't-at';
		at.textContent = fmtMs(r.at_ms);
		var dur = document.createElement('span');
		dur.className = 't-dur';
		dur.textContent = '+' + fmtMs(r.dur_ms);
		var rest = document.createElement('span');
		rest.className = 't-phase';
		rest.textContent = r.phase;
		var attrText = summarizeAttrs(r.attrs);
		if (attrText) {
			rest.textContent += '  ';
			var a = document.createElement('span');
			a.className = 't-attrs';
			a.textContent = attrText;
			rest.appendChild(a);
		}
		row.appendChild(at);
		row.appendChild(dur);
		row.appendChild(rest);
		traceList.appendChild(row);
		// Keep at most ~500 rows so the panel doesn't grow unbounded.
		while (traceList.children.length > 500) {
			traceList.removeChild(traceList.firstChild);
		}
		tracePanel.scrollTop = tracePanel.scrollHeight;
	}

	// Theme toggle
	function setTheme(mode) {
		if (mode === 'light') {
			document.documentElement.classList.add('light');
			themeBtn.innerHTML = '&#9728;';
			themeBtn.title = 'Switch to dark mode';
		} else {
			document.documentElement.classList.remove('light');
			themeBtn.innerHTML = '&#9790;';
			themeBtn.title = 'Switch to light mode';
		}
		localStorage.setItem('felix-theme', mode);
	}

	var saved = localStorage.getItem('felix-theme') || 'dark';
	setTheme(saved);

	themeBtn.addEventListener('click', function() {
		var current = document.documentElement.classList.contains('light') ? 'light' : 'dark';
		setTheme(current === 'light' ? 'dark' : 'light');
	});

	clearBtn.addEventListener('click', function() {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'session.clear',
			params: { agentId: agentSelect.value, sessionKey: sessionSelect.value },
			id: 'clear'
		}));
		messagesEl.innerHTML = '';
		currentAssistant = null;
		toolEls = {};
		loadSessions();
	});

	agentSelect.addEventListener('change', function() {
		messagesEl.innerHTML = '';
		currentAssistant = null;
		toolEls = {};
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		// Load sessions for the new agent
		loadSessions();
	});

	sessionSelect.addEventListener('change', function() {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'session.switch',
			params: { agentId: agentSelect.value, sessionKey: sessionSelect.value },
			id: 'session-switch'
		}));
		messagesEl.innerHTML = '';
		currentAssistant = null;
		toolEls = {};
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'session.history',
			params: { agentId: agentSelect.value, sessionKey: sessionSelect.value },
			id: 'history'
		}));
	});

	newSessionBtn.addEventListener('click', function() {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		var name = prompt('Session name (leave empty for timestamp):');
		if (name === null) return; // cancelled
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'session.new',
			params: { agentId: agentSelect.value, name: name || '' },
			id: 'session-new'
		}));
	});

	function loadSessions() {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'session.list',
			params: { agentId: agentSelect.value },
			id: 'sessions'
		}));
	}

	var ws = null;
	var msgId = 0;
	var currentAssistant = null;
	var sending = false;
	var reconnectTimer = null;

	// Inline markdown: code, bold, italic, links
	function inlineMd(s) {
		// Extract inline code spans into placeholders (before HTML escaping)
		var codeSpans = [];
		s = s.replace(/` + "`" + `([^` + "`" + `]+)` + "`" + `/g, function(_, code) {
			var idx = codeSpans.length;
			codeSpans.push('<code>' + escHtml(code) + '</code>');
			return '\x00CS' + idx + '\x00';
		});
		// Escape HTML in all remaining text to prevent XSS
		s = escHtml(s);
		// Apply formatting on the now-safe text
		s = s.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
		s = s.replace(/\*(.+?)\*/g, '<em>$1</em>');
		// Validate link URLs — block javascript: and data: schemes
		s = s.replace(/\[([^\]]+)\]\(([^)]+)\)/g, function(m, text, url) {
			var lower = url.trim().toLowerCase();
			if (lower.indexOf('javascript:') === 0 || lower.indexOf('data:') === 0 || lower.indexOf('vbscript:') === 0) {
				return text;
			}
			return '<a href="' + url + '" target="_blank" rel="noopener">' + text + '</a>';
		});
		// Restore code spans
		for (var i = 0; i < codeSpans.length; i++) {
			s = s.replace('\x00CS' + i + '\x00', codeSpans[i]);
		}
		return s;
	}

	// Block-level markdown renderer
	function renderMd(text) {
		// 1. Extract code blocks into placeholders
		var codeBlocks = [];
		var s = text.replace(/` + "```" + `(\w*)\n([\s\S]*?)` + "```" + `/g, function(_, lang, code) {
			var idx = codeBlocks.length;
			codeBlocks.push('<pre><code>' + escHtml(code.trimEnd()) + '</code></pre>');
			return '__CB' + idx + '__';
		});

		// 2. Process lines
		var lines = s.split('\n');
		var html = '';
		var inUl = false, inOl = false, inP = false;

		function closeAll() {
			if (inP) { html += '</p>'; inP = false; }
			if (inUl) { html += '</ul>'; inUl = false; }
			if (inOl) { html += '</ol>'; inOl = false; }
		}

		for (var i = 0; i < lines.length; i++) {
			var t = lines[i].trim();

			// Code block placeholder
			if (/^__CB\d+__$/.test(t)) {
				closeAll();
				html += t;
				continue;
			}

			// Empty line — close paragraphs but keep lists open
			// (loose list items separated by blank lines stay in the same list)
			if (t === '') {
				if (inP) { html += '</p>'; inP = false; }
				continue;
			}

			// Horizontal rule (before list check so --- isn't a list item)
			if (/^[-*_]{3,}$/.test(t)) {
				closeAll();
				html += '<hr>';
				continue;
			}

			// Heading
			var hm = t.match(/^(#{1,6})\s+(.*)$/);
			if (hm) {
				closeAll();
				var lvl = hm[1].length;
				html += '<h' + lvl + '>' + inlineMd(hm[2]) + '</h' + lvl + '>';
				continue;
			}

			// Unordered list item
			var um = t.match(/^[-*]\s+(.*)$/);
			if (um) {
				if (inP) { html += '</p>'; inP = false; }
				if (inOl) { html += '</ol>'; inOl = false; }
				if (!inUl) { html += '<ul>'; inUl = true; }
				html += '<li>' + inlineMd(um[1]) + '</li>';
				continue;
			}

			// Ordered list item
			var om = t.match(/^\d+[.)]\s+(.*)$/);
			if (om) {
				if (inP) { html += '</p>'; inP = false; }
				if (inUl) { html += '</ul>'; inUl = false; }
				if (!inOl) { html += '<ol>'; inOl = true; }
				html += '<li>' + inlineMd(om[1]) + '</li>';
				continue;
			}

			// Table: line starts with | and next line is a separator row
			if (t.charAt(0) === '|' && i + 1 < lines.length) {
				var sepLine = lines[i + 1].trim();
				if (/^\|[\s\-:]+(\|[\s\-:]+)+\|?\s*$/.test(sepLine)) {
					closeAll();
					// Parse alignment from separator
					var sepCells = sepLine.replace(/^\||\|$/g, '').split('|');
					var aligns = [];
					for (var a = 0; a < sepCells.length; a++) {
						var sc = sepCells[a].trim();
						if (sc.charAt(0) === ':' && sc.charAt(sc.length - 1) === ':') aligns.push('center');
						else if (sc.charAt(sc.length - 1) === ':') aligns.push('right');
						else aligns.push('left');
					}
					// Parse header row
					var hdrs = t.replace(/^\||\|$/g, '').split('|');
					var tbl = '<table><thead><tr>';
					for (var h = 0; h < hdrs.length; h++) {
						var al = aligns[h] || 'left';
						tbl += '<th style="text-align:' + al + '">' + inlineMd(hdrs[h].trim()) + '</th>';
					}
					tbl += '</tr></thead><tbody>';
					// Skip separator line
					i += 2;
					// Parse body rows
					while (i < lines.length && lines[i].trim().charAt(0) === '|') {
						var cells = lines[i].trim().replace(/^\||\|$/g, '').split('|');
						tbl += '<tr>';
						for (var c = 0; c < cells.length; c++) {
							var cal = aligns[c] || 'left';
							tbl += '<td style="text-align:' + cal + '">' + inlineMd(cells[c].trim()) + '</td>';
						}
						tbl += '</tr>';
						i++;
					}
					tbl += '</tbody></table>';
					html += tbl;
					i--; // compensate for loop increment
					continue;
				}
			}

			// Regular text — close any open list first
			if (inUl) { html += '</ul>'; inUl = false; }
			if (inOl) { html += '</ol>'; inOl = false; }
			if (inP) {
				html += '<br>' + inlineMd(t);
			} else {
				html += '<p>' + inlineMd(t);
				inP = true;
			}
		}

		if (inP) html += '</p>';
		if (inUl) html += '</ul>';
		if (inOl) html += '</ol>';

		// 3. Restore code blocks
		for (var i = 0; i < codeBlocks.length; i++) {
			html = html.replace('__CB' + i + '__', codeBlocks[i]);
		}

		return html;
	}

	function escHtml(s) {
		return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
	}

	function scrollToBottom() {
		messagesEl.scrollTop = messagesEl.scrollHeight;
	}

	function connect() {
		ws = new WebSocket(wsBase + '/ws');

		ws.onopen = function() {
			connStatus.textContent = 'connected';
			sendBtn.disabled = false;
			if (reconnectTimer) {
				clearTimeout(reconnectTimer);
				reconnectTimer = null;
			}
			// Fetch available agents first, then load history
			ws.send(JSON.stringify({
				jsonrpc: '2.0',
				method: 'agent.status',
				params: {},
				id: 'agents'
			}));
		};

		ws.onclose = function() {
			connStatus.textContent = 'disconnected';
			sendBtn.disabled = true;
			sending = false;
			reconnectTimer = setTimeout(connect, 3000);
		};

		ws.onerror = function() {
			connStatus.textContent = 'error';
		};

		ws.onmessage = function(e) {
			try {
				var resp = JSON.parse(e.data);
				if (resp.error) {
					addError(typeof resp.error === 'string' ? resp.error : resp.error.message || JSON.stringify(resp.error));
					sending = false;
					updateSendBtn();
					return;
				}
				if (!resp.result) return;

				// Handle agent.status response
				if (resp.id === 'agents') {
					var agents = resp.result.agents || [];
					agentSelect.innerHTML = '';
					for (var i = 0; i < agents.length; i++) {
						var opt = document.createElement('option');
						opt.value = agents[i].id;
						opt.textContent = agents[i].name || agents[i].id;
						agentSelect.appendChild(opt);
					}
					if (agents.length === 0) {
						var opt = document.createElement('option');
						opt.value = 'default';
						opt.textContent = 'default';
						agentSelect.appendChild(opt);
					}
					// Load sessions for the selected agent
					loadSessions();
					return;
				}

				// Handle session.list response
				if (resp.id === 'sessions') {
					var sessions = resp.result.sessions || [];
					sessionSelect.innerHTML = '';
					for (var i = 0; i < sessions.length; i++) {
						var opt = document.createElement('option');
						opt.value = sessions[i].key;
						opt.textContent = sessions[i].key + ' (' + sessions[i].entryCount + ')';
						if (sessions[i].active) opt.selected = true;
						sessionSelect.appendChild(opt);
					}
					if (sessions.length === 0) {
						var opt = document.createElement('option');
						opt.value = 'ws_default';
						opt.textContent = 'ws_default (0)';
						sessionSelect.appendChild(opt);
					}
					// Load history for the active session
					messagesEl.innerHTML = '';
					currentAssistant = null;
					toolEls = {};
					ws.send(JSON.stringify({
						jsonrpc: '2.0',
						method: 'session.history',
						params: { agentId: agentSelect.value, sessionKey: sessionSelect.value },
						id: 'history'
					}));
					return;
				}

				// Handle session.new response
				if (resp.id === 'session-new') {
					if (resp.result && resp.result.sessionKey) {
						loadSessions();
					}
					return;
				}

				// Handle session.switch response
				if (resp.id === 'session-switch') {
					return;
				}

				// Handle history response
				if (resp.id === 'history') {
					var entries = resp.result.entries || [];
					for (var i = 0; i < entries.length; i++) {
						var entry = entries[i];
						if (entry.type === 'message' && entry.role === 'user') {
							addUserMsg(entry.text);
						} else if (entry.type === 'message' && entry.role === 'assistant') {
							var bubble = addAssistantMsg();
							bubble.raw = entry.text;
							bubble.content.innerHTML = renderMd(entry.text);
						} else if (entry.type === 'tool_call') {
							addToolCall(entry.tool, entry.id, entry.input);
						} else if (entry.type === 'tool_result') {
							updateToolResult(null, entry.tool_call_id, null, entry.output, entry.error, entry.images);
						}
					}
					scrollToBottom();
					return;
				}

				var r = resp.result;

				switch (r.type) {
				case 'text_delta':
					if (!currentAssistant) {
						currentAssistant = addAssistantMsg('');
					}
					appendToAssistant(r.text);
					break;
				case 'tool_call_start':
					if (currentAssistant) {
						finalizeAssistant();
						currentAssistant = null;
					}
					addToolCall(r.tool, r.id, r.input);
					break;
				case 'tool_result':
					updateToolResult(r.tool, r.id, r.input, r.output, r.error, r.images);
					break;
				case 'done':
					if (currentAssistant) {
						finalizeAssistant();
					}
					currentAssistant = null;
					sending = false;
					updateSendBtn();
					updateTokenChip(r.usage, r.context_window, r.model);
					break;
				case 'aborted':
					if (currentAssistant) {
						finalizeAssistant();
					}
					currentAssistant = null;
					sending = false;
					updateSendBtn();
					break;
				case 'error':
					addError(r.message);
					currentAssistant = null;
					sending = false;
					updateSendBtn();
					break;
				case 'trace':
					addTraceRow(r);
					break;
				}
			} catch(err) {
				console.error('parse error:', err);
			}
		};
	}

	function addUserMsg(text) {
		var div = document.createElement('div');
		div.className = 'msg user';
		div.textContent = text;
		messagesEl.appendChild(div);
		scrollToBottom();
	}

	function addAssistantMsg() {
		var div = document.createElement('div');
		div.className = 'msg assistant';
		var content = document.createElement('div');
		content.className = 'content';
		div.appendChild(content);
		messagesEl.appendChild(div);
		scrollToBottom();
		return { el: div, content: content, raw: '' };
	}

	function appendToAssistant(text) {
		if (!currentAssistant) return;
		currentAssistant.raw += text;
		currentAssistant.content.innerHTML = renderMd(currentAssistant.raw);
		scrollToBottom();
	}

	function finalizeAssistant() {
		if (!currentAssistant) return;
		currentAssistant.content.innerHTML = renderMd(currentAssistant.raw);
		scrollToBottom();
	}

	var toolEls = {};

	function toolSummary(toolName, input) {
		if (!input) return escHtml(toolName);
		try {
			var p = (typeof input === 'string') ? JSON.parse(input) : input;
			switch (toolName) {
			case 'bash':
				if (p.command) return escHtml(toolName) + ': <span class="tool-detail">' + escHtml(p.command) + '</span>';
				break;
			case 'read_file':
				if (p.path) return escHtml(toolName) + ': <span class="tool-detail">' + escHtml(p.path) + '</span>';
				break;
			case 'write_file':
				if (p.path) return escHtml(toolName) + ': <span class="tool-detail">' + escHtml(p.path) + '</span>';
				break;
			case 'edit_file':
				if (p.path) return escHtml(toolName) + ': <span class="tool-detail">' + escHtml(p.path) + '</span>';
				break;
			case 'web_fetch':
				if (p.url) return escHtml(toolName) + ': <span class="tool-detail">' + escHtml(p.url) + '</span>';
				break;
			case 'web_search':
				if (p.query) return escHtml(toolName) + ': <span class="tool-detail">' + escHtml(p.query) + '</span>';
				break;
			case 'browser':
				if (p.action) {
					var detail = p.action;
					if (p.url) detail += ' ' + p.url;
					else if (p.selector) detail += ' ' + p.selector;
					return escHtml(toolName) + ': <span class="tool-detail">' + escHtml(detail) + '</span>';
				}
				break;
			case 'send_message':
				if (p.channel) return escHtml(toolName) + ': <span class="tool-detail">' + escHtml(p.channel + ' → ' + (p.chat_id || '')) + '</span>';
				break;
			case 'cron':
				if (p.action) return escHtml(toolName) + ': <span class="tool-detail">' + escHtml(p.action + (p.name ? ' ' + p.name : '')) + '</span>';
				break;
			}
		} catch(e) {}
		return escHtml(toolName);
	}

	function addToolCall(toolName, toolId, input) {
		var div = document.createElement('div');
		div.className = 'tool-call';
		var id = toolId || toolName;
		div.dataset.toolId = id;

		var header = document.createElement('div');
		header.className = 'tool-call-header';
		header.innerHTML = '<span class="arrow">&#9654;</span> ' + toolSummary(toolName, input);
		header.onclick = function() {
			var arrow = header.querySelector('.arrow');
			var output = div.querySelector('.tool-call-output');
			if (output) {
				output.classList.toggle('show');
				arrow.classList.toggle('open');
			}
		};

		var output = document.createElement('div');
		output.className = 'tool-call-output';

		div.appendChild(header);
		div.appendChild(output);
		messagesEl.appendChild(div);
		toolEls[id] = div;
		scrollToBottom();
	}

	function updateToolResult(toolName, toolId, input, outputText, errorText, images) {
		var el = toolEls[toolId] || toolEls[toolName];
		if (!el) return;

		// Update header with input if we now have it
		if (input) {
			var header = el.querySelector('.tool-call-header');
			if (header) {
				var arrow = header.querySelector('.arrow');
				var isOpen = arrow && arrow.classList.contains('open');
				header.innerHTML = '<span class="arrow' + (isOpen ? ' open' : '') + '">&#9654;</span> ' + toolSummary(toolName, input);
				header.onclick = function() {
					var a = header.querySelector('.arrow');
					var o = el.querySelector('.tool-call-output');
					if (o) { o.classList.toggle('show'); a.classList.toggle('open'); }
				};
			}
		}

		var output = el.querySelector('.tool-call-output');
		if (!output) return;
		if (errorText) {
			output.textContent = errorText;
			output.classList.add('error');
		} else if (outputText) {
			var display = outputText.length > 2000 ? outputText.substring(0, 2000) + '\n...(truncated)' : outputText;
			output.textContent = display;
		} else {
			output.textContent = '(no output)';
		}

		// Render images (e.g. browser screenshots)
		if (images && images.length > 0) {
			output.classList.add('has-image');
			for (var i = 0; i < images.length; i++) {
				var img = document.createElement('img');
				img.src = 'data:' + images[i].mimeType + ';base64,' + images[i].data;
				img.title = 'Click to open full size';
				img.onclick = (function(src) {
					return function() { window.open(src, '_blank'); };
				})(img.src);
				output.appendChild(img);
			}
			// Auto-expand to show the image
			output.classList.add('show');
			var arrow = el.querySelector('.arrow');
			if (arrow) arrow.classList.add('open');
		}
	}

	// friendlyError maps a raw error string from the agent runtime / LLM
	// provider into a non-technical title + suggestion + (optional) link
	// to a settings tab. Falls back to the raw error so we never lose info.
	function friendlyError(raw) {
		var s = String(raw || '').toLowerCase();
		// Anthropic / OpenAI rate limits.
		if (s.indexOf('rate_limit') >= 0 || s.indexOf('429') >= 0) {
			return {
				title: 'Hit the provider rate limit',
				suggest: 'Wait a minute and try again. To switch models automatically next time, set a fallback model in Settings → Agents.',
				settings: 'agents'
			};
		}
		// Anthropic 529 / OpenAI 5xx — provider overloaded.
		if (s.indexOf('overloaded') >= 0 || s.indexOf('529') >= 0 || /\b5\d\d\b/.test(s)) {
			return {
				title: 'The model provider is overloaded',
				suggest: 'Try again in a minute. If this is persistent, switch to a different provider in Settings → Providers.',
				settings: 'providers'
			};
		}
		// Context overflow.
		if (s.indexOf('context length') >= 0 || s.indexOf('context_length') >= 0 || s.indexOf('too long') >= 0) {
			return {
				title: 'Conversation is too long for the model',
				suggest: 'Start a new session, or enable / lower the compaction threshold in Settings → Intelligence → Compaction.',
				settings: 'intelligence'
			};
		}
		// Missing API key / auth.
		if (s.indexOf('api key') >= 0 || s.indexOf('api_key') >= 0 || s.indexOf('unauthorized') >= 0 || s.indexOf('401') >= 0 || s.indexOf('403') >= 0) {
			return {
				title: 'Missing or invalid API key',
				suggest: 'Add your API key in Settings → Providers, then save and try again.',
				settings: 'providers'
			};
		}
		// LLM provider not configured at all.
		if (s.indexOf('llm provider not configured') >= 0 || s.indexOf('no api key') >= 0) {
			return {
				title: 'No LLM provider is configured',
				suggest: 'Add a provider in Settings → Providers, then point your agent at it in Settings → Agents.',
				settings: 'providers'
			};
		}
		// Model not found.
		if (s.indexOf('model not found') >= 0 || s.indexOf('does not exist') >= 0 || s.indexOf('unknown model') >= 0) {
			return {
				title: 'Model not available',
				suggest: 'Check the model name in Settings → Agents. For local models, install it under Settings → Models.',
				settings: 'agents'
			};
		}
		// Local Ollama not reachable.
		if (s.indexOf('connection refused') >= 0 || s.indexOf('ollama') >= 0 || s.indexOf('eof') >= 0) {
			return {
				title: 'Local model is unavailable',
				suggest: 'The bundled local model service may not be running. Check Settings → Models.',
				settings: 'models'
			};
		}
		// Tool denied by policy.
		if (s.indexOf('not allowed for agent') >= 0 || s.indexOf('not allowed') >= 0) {
			return {
				title: 'A tool the agent tried to use is denied',
				suggest: 'Adjust this agent\'s allowed tools in Settings → Agents.',
				settings: 'agents'
			};
		}
		// Aborted / cancelled (cosmetic only).
		if (s.indexOf('aborted by user') >= 0 || s.indexOf('canceled') >= 0 || s.indexOf('cancelled') >= 0) {
			return { title: 'Run was cancelled', suggest: '' };
		}
		// Default — show the raw text but tag it.
		return { title: 'Something went wrong', suggest: raw };
	}

	function addError(msg) {
		var f = friendlyError(msg);
		var div = document.createElement('div');
		div.className = 'msg assistant';
		div.style.borderColor = 'var(--error)';
		var html = '<div class="content" style="color:var(--error)">' +
			'<strong>' + escHtml(f.title) + '</strong>';
		if (f.suggest) {
			html += '<div style="margin-top:0.4rem; color:var(--text); font-size:0.85em;">' +
				escHtml(f.suggest) + '</div>';
		}
		if (f.settings) {
			html += '<div style="margin-top:0.4rem;">' +
				'<a href="/settings#' + escHtml(f.settings) + '" target="_blank" rel="noopener" style="color:var(--accent); text-decoration:none; font-size:0.8em;">' +
				'Open Settings &rarr;</a></div>';
		}
		// Always include the raw message in a folded details so power users
		// can still see what actually broke.
		html += '<details style="margin-top:0.5rem; color:var(--text-muted); font-size:0.75em;">' +
			'<summary style="cursor:pointer;">technical detail</summary>' +
			'<div style="margin-top:0.25rem; font-family:monospace; white-space:pre-wrap; word-break:break-all;">' +
			escHtml(msg) + '</div></details>';
		html += '</div>';
		div.innerHTML = html;
		messagesEl.appendChild(div);
		scrollToBottom();
	}

	function updateSendBtn() {
		if (sending) {
			sendBtn.style.display = 'none';
			stopBtn.style.display = 'block';
		} else {
			sendBtn.style.display = 'block';
			stopBtn.style.display = 'none';
			sendBtn.disabled = !ws || ws.readyState !== WebSocket.OPEN;
		}
	}

	function sendMessage() {
		var text = inputEl.value.trim();
		if (!text || sending) return;
		if (!ws || ws.readyState !== WebSocket.OPEN) return;

		addUserMsg(text);
		sending = true;
		updateSendBtn();
		msgId++;

		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'chat.send',
			params: { agentId: agentSelect.value, text: text, sessionKey: sessionSelect.value },
			id: msgId
		}));

		inputEl.value = '';
		inputEl.style.height = 'auto';
	}

	sendBtn.addEventListener('click', sendMessage);

	stopBtn.addEventListener('click', function() {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'chat.abort',
			params: {},
			id: 'abort'
		}));
	});

	inputEl.addEventListener('keydown', function(e) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			sendMessage();
		}
	});

	// Auto-resize textarea
	inputEl.addEventListener('input', function() {
		this.style.height = 'auto';
		this.style.height = Math.min(this.scrollHeight, 150) + 'px';
	});

	connect();
})();
</script>
</body>
</html>`
