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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:")
		fmt.Fprintf(w, chatHTML, port)
	}
}

const chatHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Felix</title>
<style>
/* Default: light "warm cream + forest green" matches the Felix
   auth pages so signing in flows visually into the app. Tinted
   neutrals around hue 95 (warm cream), accent around hue 162
   (forest green ≈ auth's #1a7f5a). No #fff / #000. */
:root {
	/* Type scale: 5-step fixed rem ladder at ~1.2× ratio. Replaces
	   the prior 19 ad-hoc sizes; theme-independent so it lives here
	   and not in html.dark. */
	--fs-xs:     0.75rem;   /* captions, status, breadcrumbs */
	--fs-sm:     0.875rem;  /* UI rows, menu items, selects */
	--fs-md:     1rem;      /* body, input, primary buttons */
	--fs-lg:     1.25rem;   /* secondary headings (h2 in markdown) */
	--fs-xl:     1.5rem;    /* primary headings (h1 in markdown) */
	--lh-tight:  1.25;      /* headings */
	--lh-snug:   1.4;       /* code blocks */
	--lh-normal: 1.6;       /* body prose */

	--bg:            oklch(0.98 0.005 95);
	--bg-header:     oklch(0.995 0.003 95);
	--bg-msg-user:   oklch(0.95 0.04 162);
	--bg-msg-asst:   oklch(0.995 0.003 95);
	--bg-code:       oklch(0.96 0.006 95);
	--bg-input:      oklch(0.995 0.003 95);
	--border:        oklch(0.92 0.005 95);
	--text:          oklch(0.22 0.01 95);
	--text-muted:    oklch(0.55 0.01 95);
	--text-strong:   oklch(0.18 0.01 95);
	--text-em:       oklch(0.35 0.01 95);
	--accent:        oklch(0.51 0.10 162);
	--accent2:       oklch(0.58 0.08 200);
	--btn-text:      oklch(0.99 0.003 95);
	--placeholder:   oklch(0.65 0.01 95);
	--error:         oklch(0.55 0.18 27);
	--tool-output:   oklch(0.45 0.01 95);
}
/* Opt-in dark theme. Rebuilt off the same green hue family rather
   than the old navy IDE look so brand stays coherent across modes. */
html.dark {
	--bg:            oklch(0.20 0.01 95);
	--bg-header:     oklch(0.24 0.01 95);
	--bg-msg-user:   oklch(0.32 0.05 162);
	--bg-msg-asst:   oklch(0.26 0.008 95);
	--bg-code:       oklch(0.18 0.008 95);
	--bg-input:      oklch(0.22 0.008 95);
	--border:        oklch(0.32 0.008 95);
	--text:          oklch(0.92 0.005 95);
	--text-muted:    oklch(0.60 0.008 95);
	--text-strong:   oklch(0.98 0.005 95);
	--text-em:       oklch(0.82 0.005 95);
	--accent:        oklch(0.68 0.13 162);
	--accent2:       oklch(0.70 0.08 200);
	--btn-text:      oklch(0.18 0.01 95);
	--placeholder:   oklch(0.45 0.008 95);
	--error:         oklch(0.65 0.18 27);
	--tool-output:   oklch(0.65 0.005 95);
}
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
	font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
	font-size: var(--fs-md);
	line-height: var(--lh-normal);
	background: var(--bg);
	color: var(--text);
	height: 100vh;
	display: flex;
	flex-direction: column;
	transition: background 0.3s, color 0.3s;
}
#header {
	background: var(--bg-header);
	padding: 0.5rem 1rem;
	border-bottom: 1px solid var(--border);
	display: flex;
	align-items: center;
	position: relative;
	gap: 0.75rem;
	flex-shrink: 0;
	transition: background 0.3s, border-color 0.3s;
}
/* Inline-SVG icon convention. Every icon is 24×24 with stroke-based
   art so it inherits color via currentColor and scales cleanly against
   surrounding text. To swap in literal HugeIcons SVGs, replace the
   path content; keep the .icon class. */
.icon {
	width: 1.1em;
	height: 1.1em;
	vertical-align: -0.15em;
	flex-shrink: 0;
	stroke: currentColor;
	fill: none;
	stroke-width: 1.5;
	stroke-linecap: round;
	stroke-linejoin: round;
}
#brand {
	display: flex;
	align-items: center;
	gap: 0.4rem;
	text-decoration: none;
	color: var(--text-muted);
	flex-shrink: 0;
}
#brand:hover { color: var(--accent); }
#brand .brand-text {
	font-size: var(--fs-sm);
	font-weight: 600;
	letter-spacing: 0.2px;
}
.header-divider {
	width: 1px;
	height: 1.5rem;
	background: var(--border);
	flex-shrink: 0;
}
#session-context {
	display: flex;
	align-items: center;
	gap: 0.4rem;
	min-width: 0; /* allow ellipsis on narrow */
}
.context-label {
	font-size: var(--fs-xs);
	color: var(--text-muted);
	font-weight: 500;
}
#header .spacer { margin-left: auto; }
#view-toggles {
	display: flex;
	gap: 0.15rem;
	padding: 0.15rem;
	background: var(--bg-code);
	border-radius: 8px;
	flex-shrink: 0;
}
.view-toggle {
	background: none;
	border: none;
	border-radius: 6px;
	padding: 0.3rem 0.45rem;
	cursor: pointer;
	font-size: var(--fs-sm);
	line-height: 1;
	color: var(--text-muted);
	transition: background 0.15s, color 0.15s;
}
.view-toggle:hover { color: var(--text); }
.view-toggle.on {
	background: var(--bg-msg-asst);
	color: var(--accent);
	box-shadow: 0 1px 2px oklch(0 0 0 / 0.06);
}
.status-dot {
	width: 0.6rem;
	height: 0.6rem;
	border-radius: 50%%;
	background: var(--text-muted);
	flex-shrink: 0;
	transition: background 0.2s;
	cursor: help;
}
.status-dot.ok      { background: var(--accent); }
.status-dot.connecting { background: oklch(0.75 0.13 80); /* amber */ }
.status-dot.error   { background: var(--error); }
#agent-select:hover, #session-select:hover { border-color: var(--accent); }
#trace-panel {
	border-top: 1px solid var(--border);
	background: var(--bg-msg-asst);
	max-height: 32vh;
	overflow-y: auto;
	font-family: "SF Mono", "Fira Code", monospace;
	font-size: var(--fs-xs);
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
	font-size: var(--fs-xs);
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
	font-size: var(--fs-xs);
	color: var(--text-muted);
	font-family: "SF Mono","Fira Code",monospace;
	padding: 0.2rem 0.5rem;
	border: 1px solid var(--border);
	border-radius: 12px;
	white-space: nowrap;
	cursor: help;
}
#token-chip.warn, #topbar-token-chip.warn { border-color: var(--accent2); color: var(--accent2); }
#token-chip.danger, #topbar-token-chip.danger { border-color: var(--error); color: var(--error); }
#topbar-token-chip {
	font-size: var(--fs-xs);
	color: var(--text-muted);
	font-family: "SF Mono", "Fira Code", monospace;
	padding: 0.2rem 0.5rem;
	border: 1px solid var(--border);
	border-radius: 12px;
	white-space: nowrap;
	cursor: help;
}
.mcp-reauth {
	margin: 0.5rem 0;
	padding: 0.5rem 0.75rem;
	background: var(--bg-msg-asst);
	border: 1px solid var(--accent2);
	border-radius: 6px;
	color: var(--accent2);
	font-size: var(--fs-sm);
	display: flex;
	align-items: center;
	gap: 0.5rem;
	flex-wrap: wrap;
}
.mcp-reauth.ok { border-color: var(--accent); color: var(--accent); }
.mcp-reauth.error { border-color: var(--error); color: var(--error); }
.mcp-reauth button {
	background: var(--accent2);
	color: var(--btn-text);
	border: none;
	padding: 0.3rem 0.7rem;
	border-radius: 4px;
	cursor: pointer;
	font-size: var(--fs-sm);
}
.mcp-reauth button:disabled { opacity: 0.6; cursor: wait; }
.mcp-reauth button:hover:not(:disabled) { filter: brightness(1.1); }
#main-row {
	flex: 1;
	display: flex;
	overflow: hidden;
}
#main-row #messages { flex: 1; }
#files-panel {
	width: 320px;
	flex-shrink: 0;
	display: flex;
	flex-direction: column;
	border-left: 1px solid var(--border);
	background: var(--bg-header);
	overflow: hidden;
}
#files-panel[hidden] { display: none; }
#files-head {
	display: flex;
	align-items: center;
	gap: 0.5rem;
	padding: 0.5rem 0.75rem;
	border-bottom: 1px solid var(--border);
}
#files-title { font-weight: 600; flex: 1; color: var(--text); }
#files-head button, #files-upload-label {
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.2rem 0.45rem;
	cursor: pointer;
	color: var(--text-muted);
	font-size: var(--fs-md);
	line-height: 1;
}
#files-head button:hover, #files-upload-label:hover { color: var(--accent); border-color: var(--accent); }
#files-breadcrumbs {
	padding: 0.4rem 0.75rem;
	font-size: var(--fs-xs);
	color: var(--text-muted);
	border-bottom: 1px solid var(--border);
	word-break: break-all;
}
#files-breadcrumbs a { color: var(--accent2); text-decoration: none; cursor: pointer; }
#files-breadcrumbs a:hover { text-decoration: underline; }
#files-error {
	background: color-mix(in oklch, var(--error) 12%%, transparent);
	border-bottom: 1px solid var(--error);
	color: var(--error);
	font-size: var(--fs-xs);
	padding: 0.4rem 0.75rem;
}
#files-list {
	flex: 1;
	overflow-y: auto;
	padding: 0.25rem 0;
}
.file-row {
	display: flex;
	align-items: center;
	gap: 0.5rem;
	padding: 0.35rem 0.75rem;
	font-size: var(--fs-sm);
	color: var(--text);
	cursor: pointer;
	user-select: none;
}
.file-row:hover { background: var(--bg-msg-asst); }
/* Selection uses a tinted accent wash rather than a left-stripe; warm tone,
   no decorative side-border (impeccable absolute-ban). */
.file-row.selected {
	background: color-mix(in oklch, var(--accent) 14%%, transparent);
	color: var(--accent);
	font-weight: 600;
}
.file-row .file-icon { width: 1.2em; flex-shrink: 0; }
.file-row .file-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-row .file-size { color: var(--text-muted); font-size: var(--fs-xs); font-variant-numeric: tabular-nums; }
#files-toolbar {
	display: flex;
	flex-wrap: wrap;
	gap: 0.3rem;
	padding: 0.5rem 0.75rem;
	border-top: 1px solid var(--border);
}
#files-toolbar[hidden] { display: none; }
#files-toolbar button {
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.3rem 0.55rem;
	cursor: pointer;
	font-size: var(--fs-xs);
	color: var(--text);
}
#files-toolbar button[data-fileaction="delete"] { color: var(--error); border-color: var(--error); }
#files-toolbar button:hover { background: var(--bg-msg-asst); }
.files-empty { padding: 2rem 0.75rem; text-align: center; color: var(--text-muted); font-size: var(--fs-sm); }
#sessions-panel {
	width: 220px;
	flex-shrink: 0;
	display: flex;
	flex-direction: column;
	border-right: 1px solid var(--border);
	background: var(--bg-header);
	overflow: hidden;
}
#sessions-panel[hidden] { display: none; }
#sessions-head {
	padding: 0.5rem 0.75rem;
	border-bottom: 1px solid var(--border);
	font-weight: 600;
	color: var(--text);
	font-size: var(--fs-sm);
	display: flex;
	align-items: center;
	gap: 0.5rem;
}
#sessions-head button {
	margin-left: auto;
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.15rem 0.45rem;
	cursor: pointer;
	color: var(--text-muted);
	font-size: var(--fs-md);
	line-height: 1;
}
#sessions-head button:hover { color: var(--accent); border-color: var(--accent); }
#sessions-list {
	flex: 1;
	overflow-y: auto;
	padding: 0.25rem 0;
}
.sb-list-filter {
	margin: 0 0.75rem 0.4rem;
	padding: 0.4rem 0.55rem;
	font-size: var(--fs-sm);
	background: var(--bg);
	border: 1px solid var(--border);
	border-radius: 6px;
	color: var(--text);
}
.sb-list-filter:focus { border-color: var(--accent); outline: none; }
#sidebar.collapsed .sb-list-filter { display: none; }
.session-row {
	display: flex;
	align-items: center;
	gap: 0.5rem;
	padding: 0.5rem 0.75rem;
	font-size: var(--fs-sm);
	color: var(--text);
	cursor: pointer;
	user-select: none;
}
.session-row:hover { background: var(--bg-msg-asst); }
.session-row.active {
	background: color-mix(in oklch, var(--accent) 14%%, transparent);
	color: var(--accent);
	font-weight: 600;
}
.session-row .ses-icon { width: 1.2em; flex-shrink: 0; opacity: 0.8; }
.session-row .ses-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.session-row .ses-count { color: var(--text-muted); font-size: var(--fs-xs); font-variant-numeric: tabular-nums; }
.session-row .ses-clear {
	background: none;
	border: none;
	color: var(--text-muted);
	padding: 0.2rem;
	margin-left: 0.15rem;
	border-radius: 4px;
	cursor: pointer;
	display: flex;
	align-items: center;
	justify-content: center;
	opacity: 0;
	transition: opacity 0.12s, color 0.12s, background 0.12s;
}
.session-row:hover .ses-clear, .session-row.active .ses-clear { opacity: 0.7; }
.session-row .ses-clear:hover { opacity: 1; color: var(--error); background: color-mix(in oklch, var(--error) 12%%, transparent); }
.session-row .ses-clear .icon { width: 14px; height: 14px; }

/* Per-session chevron expands a sub-list of past runs (Wave 2). */
.session-row .ses-chevron {
	width: 14px; height: 14px; flex-shrink: 0;
	cursor: pointer; opacity: 0.5;
	transition: transform 0.15s, opacity 0.15s;
}
.session-row .ses-chevron:hover { opacity: 1; }
.session-row .ses-chevron.expanded { transform: rotate(90deg); }

.runs-sublist {
	margin: 0.25rem 0 0.5rem 1.75rem;
	padding-left: 0.5rem;
	border-left: 1px solid var(--border);
	display: none;
}
.runs-sublist.expanded { display: block; }
.run-row {
	display: flex; align-items: center; gap: 0.5rem;
	padding: 0.3rem 0.4rem;
	font-size: var(--fs-xs);
	color: var(--text-muted);
	border-radius: 4px;
	cursor: pointer;
}
.run-row:hover { background: var(--bg-msg-asst); color: var(--text); }
.run-row .run-time { font-variant-numeric: tabular-nums; }
.run-row .run-status {
	padding: 0 0.4em; border-radius: 3px;
	font-size: 0.85em;
	background: var(--bg-msg-user);
}
.run-row .run-status.completed   { background: rgba(59, 156, 93, 0.18); color: #5ad17e; }
.run-row .run-status.cancelled   { background: rgba(140, 140, 140, 0.18); color: var(--text-muted); }
.run-row .run-status.failed      { background: rgba(220, 80, 80, 0.18); color: #ff7a7a; }
.run-row .run-status.interrupted { background: rgba(220, 140, 40, 0.18); color: #ffa040; }
.run-row .run-status.running     { background: rgba(80, 140, 220, 0.18); color: #6fa6ff; }
.run-row .run-count { margin-left: auto; }
.run-row .run-delete {
	background: none; border: none; cursor: pointer;
	opacity: 0; padding: 0.1rem;
	color: var(--text-muted);
}
.run-row:hover .run-delete { opacity: 0.7; }
.run-row .run-delete:hover { opacity: 1; color: var(--error); }
.run-row .run-delete svg { width: 12px; height: 12px; }

/* Read-only replay mode banner + composer-hiding (Task 3.3). */
.replay-banner {
	background: rgba(220, 140, 40, 0.25);
	border-bottom: 1px solid rgba(220, 140, 40, 0.5);
	color: var(--text);
	padding: 0.5rem 1rem;
	font-size: var(--fs-sm);
	display: flex; align-items: center; gap: 0.5rem;
	cursor: pointer;
}
.replay-banner:hover { background: rgba(220, 140, 40, 0.35); }
.replay-banner .label { flex: 1; }
body.replay-mode #input-shell { display: none !important; }

.sb-session-edit {
	flex: 1;
	min-width: 0;
	padding: 0.15rem 0.35rem;
	border: 1px solid var(--accent);
	border-radius: 4px;
	background: var(--bg-input);
	color: var(--text);
	font: inherit;
	font-size: var(--fs-sm);
	outline: none;
}
.sessions-empty { padding: 1rem 0.75rem; text-align: center; color: var(--text-muted); font-size: var(--fs-xs); }
/* On narrow screens the chat transcript is always the primary surface.
   Panels become slide-overs anchored to the row edges with a shadow,
   layered over the messages instead of stealing their space. */
@media (max-width: 900px) {
	#main-row { position: relative; }
	#sessions-panel, #files-panel {
		position: absolute;
		top: 0;
		bottom: 0;
		z-index: 20;
		width: 82%%;
		max-width: 320px;
		box-shadow: 0 4px 24px oklch(0.2 0.01 95 / 0.18);
	}
	#sessions-panel { left: 0; border-right: 1px solid var(--border); }
	#files-panel { right: 0; border-left: 1px solid var(--border); }
	#view-toggles { padding: 0.1rem; }
	.view-toggle { padding: 0.3rem; }
	#brand .brand-text { display: none; }
	.context-label { display: none; }
}
#agent-select {
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.3rem 0.5rem;
	font-size: var(--fs-sm);
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
	font-size: var(--fs-sm);
	color: var(--text);
	font-family: inherit;
	outline: none;
	cursor: pointer;
	transition: background 0.3s, border-color 0.3s, color 0.3s;
}
#hamburger-btn {
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.25rem 0.5rem;
	cursor: pointer;
	font-size: var(--fs-lg);
	line-height: 1;
	color: var(--text);
	transition: border-color 0.2s, color 0.2s;
}
#hamburger-btn:hover, #hamburger-btn[aria-expanded="true"] { border-color: var(--accent); color: var(--accent); }
#hamburger-menu {
	position: absolute;
	top: 3rem;
	left: 1rem;
	z-index: 100;
	min-width: 14rem;
	background: var(--bg-header);
	border: 1px solid var(--border);
	border-radius: 8px;
	box-shadow: 0 10px 24px oklch(0 0 0 / 0.35);
	padding: 0.35rem;
	display: flex;
	flex-direction: column;
	gap: 0.1rem;
}
#hamburger-menu[hidden] { display: none; }
.menu-item {
	display: block;
	width: 100%%;
	text-align: left;
	background: none;
	border: none;
	color: var(--text);
	font: inherit;
	font-size: var(--fs-sm);
	padding: 0.5rem 0.65rem;
	border-radius: 5px;
	cursor: pointer;
	text-decoration: none;
	transition: background 0.15s, color 0.15s;
}
.menu-item:hover, .menu-item:focus { background: var(--bg-msg-asst); color: var(--accent); outline: none; }
.menu-item.menu-danger { color: var(--error); }
.menu-item.menu-danger:hover { background: var(--error); color: var(--btn-text); }
.menu-icon {
	display: inline-block;
	width: 1.5em;
	text-align: center;
	margin-right: 0.5rem;
	opacity: 0.85;
}
.menu-sep {
	margin: 0.25rem 0;
	border: none;
	border-top: 1px solid var(--border);
}
#messages {
	flex: 1;
	overflow-y: auto;
	padding: 1rem 1.5rem;
	display: flex;
	flex-direction: column;
	gap: 1rem;
}
/* First-run / no-agent-configured surface. Centered card with a single
   primary CTA pointing at the right Settings tab. Same visual language
   as friendlyError but a warmer, welcoming tone. */
.empty-state {
	align-self: center;
	margin: auto 0;
	max-width: 420px;
	text-align: center;
	padding: 2rem 1.5rem;
	color: var(--text-muted);
}
.empty-state h2 {
	font-size: var(--fs-lg);
	color: var(--text);
	margin-bottom: 0.5rem;
	font-weight: 600;
	line-height: var(--lh-tight);
}
.empty-state p {
	font-size: var(--fs-md);
	line-height: var(--lh-normal);
	margin-bottom: 1.25rem;
}
.empty-cta {
	display: inline-block;
	padding: 0.5rem 1.1rem;
	background: var(--accent);
	color: var(--btn-text);
	border-radius: 8px;
	text-decoration: none;
	font-size: var(--fs-md);
	font-weight: 600;
	transition: opacity 0.2s;
}
.empty-cta:hover { opacity: 0.9; }
/* Inline modal: one small component replaces native prompt/confirm/
   alert across the file so the visual system stays consistent and
   we can validate input, show previews, or attach progress affordances
   on top of dialog flows. */
.modal-backdrop {
	position: fixed;
	inset: 0;
	background: oklch(0 0 0 / 0.4);
	z-index: 1000;
	display: flex;
	align-items: center;
	justify-content: center;
	padding: 1rem;
	animation: modal-fade 0.15s ease-out;
}
@keyframes modal-fade { from { opacity: 0; } to { opacity: 1; } }
.modal {
	background: var(--bg-msg-asst);
	border: 1px solid var(--border);
	border-radius: 12px;
	padding: 1.25rem 1.5rem 1rem;
	width: 100%%;
	max-width: 380px;
	box-shadow: 0 16px 48px oklch(0 0 0 / 0.25);
}
.modal-title {
	font-size: var(--fs-lg);
	font-weight: 600;
	color: var(--text);
	margin-bottom: 0.5rem;
	line-height: var(--lh-tight);
}
.modal-body {
	font-size: var(--fs-md);
	color: var(--text-muted);
	line-height: var(--lh-normal);
	margin-bottom: 1rem;
	word-break: break-word;
}
.modal-input {
	width: 100%%;
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.5rem 0.75rem;
	font-family: inherit;
	font-size: var(--fs-md);
	color: var(--text);
	margin-bottom: 1rem;
	outline: none;
	box-sizing: border-box;
}
.modal-input:focus { border-color: var(--accent); }
.modal-actions {
	display: flex;
	justify-content: flex-end;
	gap: 0.5rem;
}
.modal-btn {
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.4rem 0.9rem;
	font-family: inherit;
	font-size: var(--fs-sm);
	color: var(--text);
	cursor: pointer;
	transition: background 0.15s, border-color 0.15s;
}
.modal-btn:hover { background: var(--bg-code); }
.modal-btn-primary {
	background: var(--accent);
	color: var(--btn-text);
	border-color: var(--accent);
	font-weight: 600;
}
.modal-btn-primary:hover { background: var(--accent); opacity: 0.9; }
.modal-btn-danger {
	background: var(--error);
	color: var(--btn-text);
	border-color: var(--error);
	font-weight: 600;
}
.modal-btn-danger:hover { background: var(--error); opacity: 0.9; }
/* Command palette: shares .modal-backdrop, but anchors near the top of
   the viewport (like Cmd+K / Cmd+P palettes in code editors) so it
   doesn't move when the result list grows or shrinks. */
.palette-backdrop { align-items: flex-start; padding-top: 12vh; }
.palette {
	background: var(--bg-msg-asst);
	border: 1px solid var(--border);
	border-radius: 12px;
	width: 100%%;
	max-width: 560px;
	box-shadow: 0 20px 60px oklch(0 0 0 / 0.3);
	display: flex;
	flex-direction: column;
	overflow: hidden;
}
.palette-input {
	background: none;
	border: none;
	border-bottom: 1px solid var(--border);
	padding: 0.85rem 1rem;
	font-family: inherit;
	font-size: var(--fs-md);
	color: var(--text);
	outline: none;
}
.palette-input::placeholder { color: var(--text-muted); }
.palette-list {
	max-height: 50vh;
	overflow-y: auto;
	padding: 0.35rem 0;
}
.palette-section {
	padding: 0.4rem 1rem 0.2rem;
	font-size: var(--fs-xs);
	color: var(--text-muted);
	text-transform: uppercase;
	letter-spacing: 0.05em;
}
.palette-item {
	display: flex;
	align-items: center;
	gap: 0.65rem;
	padding: 0.45rem 1rem;
	cursor: pointer;
	font-size: var(--fs-md);
	color: var(--text);
	transition: background 0.1s;
}
.palette-item .icon { width: 16px; height: 16px; flex-shrink: 0; color: var(--text-muted); }
.palette-item .palette-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.palette-item .palette-hint {
	font-size: var(--fs-xs);
	color: var(--text-muted);
	font-family: "SF Mono", "Fira Code", monospace;
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 4px;
	padding: 1px 6px;
	flex-shrink: 0;
}
.palette-item.active { background: color-mix(in oklch, var(--accent) 14%%, var(--bg-msg-asst)); color: var(--accent); }
.palette-item.active .icon { color: var(--accent); }
.palette-empty {
	padding: 1.25rem 1rem;
	text-align: center;
	color: var(--text-muted);
	font-size: var(--fs-sm);
}
.palette-footer {
	border-top: 1px solid var(--border);
	padding: 0.45rem 1rem;
	display: flex;
	justify-content: space-between;
	gap: 1rem;
	font-size: var(--fs-xs);
	color: var(--text-muted);
}
.palette-footer kbd {
	font-family: "SF Mono", "Fira Code", monospace;
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 3px;
	padding: 0 4px;
}
.cheat {
	background: var(--bg-msg-asst);
	border: 1px solid var(--border);
	border-radius: 12px;
	width: 100%%;
	max-width: 480px;
	padding: 1.25rem 1.5rem;
}
.cheat-title { font-size: var(--fs-lg); font-weight: 600; color: var(--text); margin-bottom: 0.75rem; }
.cheat table { width: 100%%; border-collapse: collapse; font-size: var(--fs-sm); }
.cheat td { padding: 0.35rem 0; color: var(--text); vertical-align: top; }
.cheat td:first-child { color: var(--text-muted); width: 40%%; }
.cheat kbd {
	font-family: "SF Mono", "Fira Code", monospace;
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 4px;
	padding: 1px 6px;
	font-size: var(--fs-xs);
}
.msg {
	max-width: 85%%;
	padding: 0.75rem 1rem;
	border-radius: 12px;
	line-height: var(--lh-normal);
	word-wrap: break-word;
	overflow-wrap: break-word;
	transition: background 0.3s, border-color 0.3s;
}
.system-marker {
	color: var(--text-muted);
	font-size: var(--fs-sm);
	padding: 4px 12px;
	font-style: italic;
	align-self: center;
}
.chat-empty {
	display: flex;
	flex-direction: column;
	align-items: center;
	justify-content: center;
	gap: 0.4rem;
	padding: 4rem 2rem;
	color: var(--text-muted);
	text-align: center;
}
.chat-empty-title {
	font-size: 1.1rem;
	font-weight: 600;
	color: var(--text);
}
#messages .msg ~ .chat-empty { display: none; }
.msg.user {
	background: var(--bg-msg-user);
	align-self: flex-end;
	border-bottom-right-radius: 4px;
}
.msg.assistant {
	background: var(--bg-msg-asst);
	align-self: flex-start;
	border-bottom-left-radius: 4px;
	/* Shrink the bubble to its content (which itself caps at 70ch) so
	   wide viewports don't leave an empty gutter inside the bubble. */
	width: fit-content;
}
/* Cap reading width so long passages don't sprawl on wide viewports;
   code blocks and tables can still spill to the bubble edge so they
   stay scannable. */
.msg.assistant .content { max-width: 70ch; }
.msg.assistant .content pre,
.msg.assistant .content table,
.msg.assistant .content img { max-width: 100%%; }
.msg.assistant .content p { margin-bottom: 0.5em; }
.msg.assistant .content p:last-child { margin-bottom: 0; }
.msg.assistant .content code {
	background: var(--bg-code);
	padding: 0.15em 0.4em;
	border-radius: 3px;
	font-size: var(--fs-sm);
	font-family: "SF Mono", "Fira Code", monospace;
}
.msg.assistant .content pre {
	background: var(--bg-code);
	padding: 0.75rem;
	border-radius: 6px;
	overflow-x: auto;
	margin: 0.5em 0;
	line-height: var(--lh-snug);
	border: 1px solid var(--border);
	transition: background 0.3s, border-color 0.3s;
}
.msg.assistant .content pre code {
	background: none;
	padding: 0;
	font-size: var(--fs-sm);
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
.msg.assistant .content h1 { font-size: var(--fs-xl); line-height: var(--lh-tight); }
.msg.assistant .content h2 { font-size: var(--fs-lg); line-height: var(--lh-tight); }
.msg.assistant .content h3 { font-size: var(--fs-md); line-height: var(--lh-tight); }
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
	background: color-mix(in oklch, var(--text) 7%%, transparent);
}
.tool-call {
	background: var(--bg-code);
	border: 1px solid var(--border);
	border-radius: 6px;
	margin: 0.5rem 0;
	font-size: var(--fs-sm);
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
	font-size: var(--fs-xs);
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
	font-size: var(--fs-xs);
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
	font-size: var(--fs-sm);
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
	font-size: var(--fs-md);
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
	font-size: var(--fs-md);
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
	color: var(--btn-text);
	border: none;
	border-radius: 8px;
	padding: 0 1.25rem;
	font-size: var(--fs-md);
	font-weight: 600;
	cursor: pointer;
	transition: opacity 0.2s, background 0.3s;
	align-self: flex-end;
	height: 40px;
	display: none;
}
#stop-btn:hover { opacity: 0.85; }

/* --- Sidebar shell (replaces hamburger menu + header view-toggles) ---
   Persistent left rail with Sessions + Tools + user footer. Expand /
   collapse via the head toggle; the .collapsed modifier hides the
   labels and shrinks the rail to icon-only. Width transition is the
   only animated layout property (everything else uses opacity). */
#app-shell {
	flex: 1;
	display: flex;
	min-height: 0;
	overflow: hidden;
}
#sidebar {
	width: 260px;
	flex-shrink: 0;
	background: var(--bg-header);
	border-right: 1px solid var(--border);
	display: flex;
	flex-direction: column;
	overflow: hidden;
	transition: width 0.18s cubic-bezier(0.22, 1, 0.36, 1);
}
#sidebar.collapsed { width: 64px; }
#sidebar.collapsed .sb-label { display: none; }
#sidebar.collapsed #sb-head .brand-text { display: none; }
#sidebar.collapsed #sb-head { padding: 0.75rem 0.5rem; justify-content: center; }
#sidebar.collapsed #sb-head #brand { display: none; }
#sidebar.collapsed #sb-new { padding: 0.5rem; justify-content: center; }
#sidebar.collapsed .sb-section-label { display: none; }
#sidebar.collapsed .sb-item, #sidebar.collapsed #sb-new { justify-content: center; }
#sidebar.collapsed #user-row { justify-content: center; }
#sidebar.collapsed .session-row { padding: 0.5rem; justify-content: center; }
#sidebar.collapsed .session-row .ses-name, #sidebar.collapsed .session-row .ses-count, #sidebar.collapsed .session-row .ses-clear { display: none; }
#sidebar.collapsed #sb-collapse svg { transform: scaleX(-1); }
#sb-head {
	display: flex;
	align-items: center;
	gap: 0.6rem;
	padding: 0.75rem 0.75rem;
	border-bottom: 1px solid transparent;
}
#sb-head #brand {
	display: flex;
	align-items: center;
	gap: 0.55rem;
	text-decoration: none;
	color: var(--text);
	flex: 1;
	min-width: 0;
}
#sb-head .brand-text {
	font-size: var(--fs-md);
	font-weight: 600;
	color: var(--text);
	letter-spacing: 0.2px;
}
#sb-collapse {
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.3rem;
	cursor: pointer;
	color: var(--text-muted);
	flex-shrink: 0;
	display: flex;
	align-items: center;
	justify-content: center;
	transition: color 0.15s, border-color 0.15s;
}
#sb-collapse:hover { color: var(--accent); border-color: var(--accent); }
#sb-collapse svg { width: 16px; height: 16px; transition: transform 0.18s; }
#sb-new {
	margin: 0.5rem 0.75rem 0.75rem;
	background: var(--accent);
	color: var(--btn-text);
	border: none;
	border-radius: 10px;
	padding: 0.6rem 0.85rem;
	font-family: inherit;
	font-size: var(--fs-sm);
	font-weight: 600;
	cursor: pointer;
	display: flex;
	align-items: center;
	gap: 0.5rem;
	transition: opacity 0.15s;
}
#sb-new:hover { opacity: 0.92; }
#sb-new .icon { width: 16px; height: 16px; }
.sb-section-label {
	padding: 0.6rem 0.75rem 0.25rem;
	font-size: var(--fs-xs);
	color: var(--text-muted);
	text-transform: uppercase;
	letter-spacing: 0.08em;
	font-weight: 600;
}
.sb-agent {
	margin: 0 0.5rem 0.6rem 0.5rem;
	padding: 0.4rem 0.55rem;
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 6px;
	color: var(--text);
	font: inherit;
	font-size: 0.85rem;
	cursor: pointer;
	transition: border-color 0.15s;
}
.sb-agent:hover, .sb-agent:focus { border-color: var(--accent); outline: none; }
#sidebar.collapsed .sb-agent { display: none; }
.sb-list { overflow-y: auto; flex: 1 1 auto; min-height: 0; }
#sb-tools { display: flex; flex-direction: column; padding: 0.15rem 0.4rem; }
.sb-item {
	display: flex;
	align-items: center;
	gap: 0.65rem;
	padding: 0.5rem 0.6rem;
	margin: 0.05rem 0;
	background: none;
	border: none;
	border-radius: 8px;
	color: var(--text);
	font-family: inherit;
	font-size: var(--fs-sm);
	cursor: pointer;
	text-align: left;
	transition: background 0.1s, color 0.1s;
}
.sb-item:hover { background: var(--bg-msg-asst); }
.sb-item.active { background: color-mix(in oklch, var(--accent) 14%%, transparent); color: var(--accent); font-weight: 600; }
.sb-item .icon { width: 18px; height: 18px; flex-shrink: 0; color: var(--text-muted); }
.sb-item.active .icon, .sb-item:hover .icon { color: var(--accent); }
#sb-foot {
	margin-top: auto;
	padding: 0.6rem 0.6rem 0.75rem;
	border-top: 1px solid var(--border);
	position: relative;
}
#user-row {
	display: flex;
	align-items: center;
	gap: 0.55rem;
}
#theme-toggle {
	background: none;
	border: 1px solid var(--border);
	border-radius: 8px;
	padding: 0.4rem;
	cursor: pointer;
	color: var(--text-muted);
	display: flex;
	align-items: center;
	justify-content: center;
	flex-shrink: 0;
	transition: color 0.15s, border-color 0.15s;
}
#theme-toggle:hover { color: var(--accent); border-color: var(--accent); }
#theme-toggle .icon { width: 16px; height: 16px; }
/* Main column: top bar + content pane + (optional) right-side files
   panel. Replaces the previous #header + #main-row vertical stack. */
#main-col {
	flex: 1;
	display: flex;
	flex-direction: column;
	min-width: 0;
	background: var(--bg);
	position: relative;
}
#topbar {
	display: flex;
	align-items: center;
	gap: 0.6rem;
	padding: 0.55rem 1.25rem;
	border-bottom: 1px solid var(--border);
	background: var(--bg-header);
	flex-shrink: 0;
}
#topbar .spacer { margin-left: auto; }
#status-pill {
	display: flex;
	align-items: center;
	gap: 0.4rem;
	font-size: var(--fs-xs);
	color: var(--text-muted);
}
#status-pill #status-text { font-weight: 500; }
/* Top-right view toggles: small icon buttons that flip the visibility
   of in-chat affordances (tool calls in transcript, live perf trace
   panel under the chat). Active state mirrors the accent wash used
   on sidebar items so they read as the same control language. */
#topbar-toggles {
	display: flex;
	gap: 0.2rem;
	margin-right: 0.5rem;
}
.tb-toggle {
	background: none;
	border: 1px solid transparent;
	border-radius: 8px;
	padding: 0.3rem 0.4rem;
	cursor: pointer;
	color: var(--text-muted);
	display: flex;
	align-items: center;
	justify-content: center;
	transition: background 0.12s, color 0.12s, border-color 0.12s;
}
.tb-toggle:hover { color: var(--accent); border-color: var(--border); }
.tb-toggle.active {
	background: color-mix(in oklch, var(--accent) 14%%, transparent);
	color: var(--accent);
	border-color: color-mix(in oklch, var(--accent) 24%%, transparent);
}
.tb-toggle .icon { width: 16px; height: 16px; }
#main-pane {
	flex: 1;
	display: flex;
	flex-direction: row;
	min-height: 0;
	overflow: hidden;
}
#chat-view {
	flex: 1;
	display: flex;
	flex-direction: column;
	min-width: 0;
	overflow: hidden;
}
#chat-view[hidden] { display: none; }
#chat-view #messages { flex: 1; }
#input-shell {
	background: var(--bg);
	padding: 0.5rem 1.25rem 0.85rem;
	border-top: 1px solid var(--border);
	flex-shrink: 0;
}
#input-helper {
	margin-top: 0.35rem;
	font-size: var(--fs-xs);
	color: var(--text-muted);
}
/* Embed view (iframes /settings, /jobs, /logs into the main pane so
   navigation away from chat doesn't unmount the WebSocket / lose
   session state). Iframe inherits scroll from its own document. */
#embed-view {
	flex: 1;
	display: flex;
	flex-direction: column;
	min-width: 0;
	overflow: hidden;
}
#embed-view[hidden] { display: none; }
#embed-head {
	display: flex;
	align-items: center;
	gap: 0.5rem;
	padding: 0.55rem 1.25rem;
	border-bottom: 1px solid var(--border);
	background: var(--bg-header);
}
#embed-title { flex: 1; font-weight: 600; color: var(--text); font-size: var(--fs-sm); }
#embed-close {
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.3rem;
	cursor: pointer;
	color: var(--text-muted);
	display: flex;
	align-items: center;
	justify-content: center;
	transition: color 0.15s, border-color 0.15s;
}
#embed-close:hover { color: var(--accent); border-color: var(--accent); }
#embed-close .icon { width: 16px; height: 16px; }
#embed-frame {
	flex: 1;
	width: 100%%;
	border: 0;
	background: var(--bg);
}

/* Restyle the input bar to match the screenshot:
   circular send button with up-arrow icon, helper-text under input,
   no visible "Stop" pill (replaced with a circular square icon). */
#input-area {
	background: transparent;
	border-top: 0;
	padding: 0;
	gap: 0.5rem;
	align-items: flex-end;
}
#input {
	border-radius: 10px;
	padding: 0.7rem 0.95rem;
	background: var(--bg-msg-asst);
	border-color: var(--border);
}
#send-btn {
	background: var(--accent);
	color: var(--btn-text);
	border: none;
	border-radius: 50%%;
	width: 38px;
	height: 38px;
	padding: 0;
	display: flex;
	align-items: center;
	justify-content: center;
	cursor: pointer;
	transition: opacity 0.15s, background 0.3s;
}
#send-btn .icon { width: 18px; height: 18px; }
#send-btn:hover { opacity: 0.9; }
#send-btn:disabled { opacity: 0.45; cursor: not-allowed; }
#stop-btn {
	background: var(--error);
	color: var(--btn-text);
	border: none;
	border-radius: 50%%;
	width: 38px;
	height: 38px;
	padding: 0;
	display: none;
	align-items: center;
	justify-content: center;
	cursor: pointer;
	position: relative;
	overflow: hidden;
	/* The button gets a halo of expanding red rings via box-shadow on the
	   button itself, while ::before keeps a bright inner ripple. Combined
	   with a gentle scale on the button, this reads unmistakably as "still
	   working". Loops only run while display != none, so the existing
	   send→stop toggle drives them implicitly. */
	animation: stop-halo 1.1s ease-out infinite;
}
#stop-btn::before {
	content: '';
	position: absolute;
	inset: 0;
	border-radius: 50%%;
	background: oklch(100%% 0 0 / 0.55);
	transform: scale(0);
	animation: stop-pulse-inner 1.1s cubic-bezier(0.22, 1, 0.36, 1) infinite;
	pointer-events: none;
}
#stop-btn .icon { width: 14px; height: 14px; position: relative; z-index: 1; }
@keyframes stop-halo {
	0%%   { box-shadow: 0 0 0 0   oklch(0.55 0.18 27 / 0.65),
	                    0 0 0 0   oklch(0.55 0.18 27 / 0.45);
	        transform: scale(1); }
	60%%  { box-shadow: 0 0 0 10px oklch(0.55 0.18 27 / 0),
	                    0 0 0 18px oklch(0.55 0.18 27 / 0);
	        transform: scale(1.06); }
	100%% { box-shadow: 0 0 0 14px oklch(0.55 0.18 27 / 0),
	                    0 0 0 22px oklch(0.55 0.18 27 / 0);
	        transform: scale(1); }
}
@keyframes stop-pulse-inner {
	0%%   { transform: scale(0);    opacity: 0.85; }
	70%%  { transform: scale(1.2);  opacity: 0.15; }
	100%% { transform: scale(1.4);  opacity: 0; }
}
html.dark #stop-btn {
	animation-name: stop-halo-dark;
}
@keyframes stop-halo-dark {
	0%%   { box-shadow: 0 0 0 0   oklch(0.65 0.18 27 / 0.7),
	                    0 0 0 0   oklch(0.65 0.18 27 / 0.5);
	        transform: scale(1); }
	60%%  { box-shadow: 0 0 0 10px oklch(0.65 0.18 27 / 0),
	                    0 0 0 18px oklch(0.65 0.18 27 / 0);
	        transform: scale(1.06); }
	100%% { box-shadow: 0 0 0 14px oklch(0.65 0.18 27 / 0),
	                    0 0 0 22px oklch(0.65 0.18 27 / 0);
	        transform: scale(1); }
}
@media (prefers-reduced-motion: reduce) {
	#stop-btn { animation: none; box-shadow: 0 0 0 4px oklch(0.55 0.18 27 / 0.3); }
	#stop-btn::before { animation: none; opacity: 0.3; transform: scale(0.85); }
}

#attach-btn {
	background: transparent;
	color: var(--text-muted);
	border: 1px solid var(--border);
	border-radius: 50%%;
	width: 38px;
	height: 38px;
	padding: 0;
	display: flex;
	align-items: center;
	justify-content: center;
	cursor: pointer;
	flex-shrink: 0;
	transition: color 0.15s, border-color 0.15s, background 0.15s;
}
#attach-btn:hover { color: var(--accent); border-color: var(--accent); background: color-mix(in oklch, var(--accent) 8%%, transparent); }
#attach-btn .icon { width: 18px; height: 18px; }

#attach-row {
	display: flex;
	flex-wrap: wrap;
	gap: 0.35rem;
	margin-bottom: 0.4rem;
}
.attach-chip {
	display: inline-flex;
	align-items: center;
	gap: 0.4rem;
	padding: 0.3rem 0.55rem;
	background: var(--bg-msg-asst);
	border: 1px solid var(--border);
	border-radius: 999px;
	font-size: 0.8rem;
	color: var(--text);
	max-width: 24ch;
}
.attach-chip.uploading { opacity: 0.65; }
.attach-chip.failed { border-color: var(--error); color: var(--error); }
.attach-chip .chip-name {
	overflow: hidden;
	text-overflow: ellipsis;
	white-space: nowrap;
}
.attach-chip .chip-size { color: var(--text-muted); font-variant-numeric: tabular-nums; }
.attach-chip .chip-x {
	background: none;
	border: none;
	cursor: pointer;
	padding: 0;
	color: var(--text-muted);
	font-size: 1rem;
	line-height: 1;
	display: flex;
	align-items: center;
}
.attach-chip .chip-x:hover { color: var(--error); }
.attach-chip .chip-warn {
	margin-left: 0.3rem;
	font-size: var(--fs-xs);
	color: var(--warning, oklch(0.6 0.16 65));
}

#drop-overlay {
	position: fixed;
	inset: 0;
	background: color-mix(in oklch, var(--accent) 10%%, var(--bg) 60%%);
	z-index: 9999;
	display: flex;
	align-items: center;
	justify-content: center;
	pointer-events: none;
}
/* display:flex above wins over the hidden attribute's default
   display:none; need an explicit override or the overlay shows
   on every page load. */
#drop-overlay[hidden] { display: none; }
#drop-overlay-inner {
	border: 2px dashed var(--accent);
	border-radius: 14px;
	padding: 2rem 3rem;
	color: var(--accent);
	font-weight: 600;
	font-size: 1.1rem;
	background: var(--bg);
}

/* Old shell elements: hide. Kept in DOM where JS still references
   them (e.g. select#session-select for state) but visually removed. */
#header { display: none !important; }
#hamburger-menu { display: none !important; }
#main-row { display: contents; } /* legacy selector that we want neutralised */

/* Files panel now overlays from the right edge of the main column
   when the sidebar Files button is toggled. Same content as before. */
#files-panel {
	position: absolute;
	top: 49px; /* topbar height */
	right: 0;
	bottom: 0;
	width: 340px;
	z-index: 10;
	box-shadow: -8px 0 24px oklch(0 0 0 / 0.08);
}

/* Mobile-only menu button in the topbar. Hidden on desktop because
   the sidebar is always visible there (collapsed = icon rail). On
   mobile the sidebar slides off-screen entirely when collapsed, so
   this button is the only way back. */
#topbar-menu {
	display: none;
	background: none;
	border: 1px solid transparent;
	border-radius: 8px;
	padding: 0.3rem 0.4rem;
	cursor: pointer;
	color: var(--text-muted);
	align-items: center;
	justify-content: center;
	transition: background 0.12s, color 0.12s, border-color 0.12s;
}
#topbar-menu:hover { color: var(--accent); border-color: var(--border); }
#topbar-menu .icon { width: 18px; height: 18px; }
/* Backdrop captures taps outside the open sidebar on mobile so users
   can dismiss it without hunting for the collapse button. Hidden on
   desktop and when the sidebar is collapsed (off-screen). */
#sidebar-backdrop {
	display: none;
	position: absolute;
	inset: 0;
	background: oklch(0 0 0 / 0.32);
	z-index: 40;
}

@media (max-width: 820px) {
	#sidebar {
		position: absolute;
		top: 0; bottom: 0; left: 0;
		z-index: 50;
		box-shadow: 8px 0 24px oklch(0 0 0 / 0.18);
	}
	#sidebar.collapsed {
		transform: translateX(-100%%);
		width: 260px;
	}
	#sidebar.collapsed .sb-label, #sidebar.collapsed #sb-head .brand-text { display: inline; }
	#topbar-menu { display: inline-flex; }
	#sidebar.expanded ~ #sidebar-backdrop { display: block; }
}
</style>
</head>
<body>
<!-- Legacy header + hamburger are CSS-hidden but kept in the DOM so
     a few JS sites that look them up don't crash. The real shell is
     #app-shell below. -->
<div id="header">
	<button id="hamburger-btn" aria-label="Menu" aria-expanded="false" hidden></button>
	<select id="agent-select" aria-label="Agent" hidden></select>
	<select id="session-select" aria-label="Session" hidden></select>
	<div id="view-toggles" hidden></div>
	<span id="token-chip" hidden></span>
	<span class="status-dot" id="conn-status" hidden></span>
</div>
<div id="hamburger-menu" hidden></div>

<div id="app-shell">
	<aside id="sidebar" class="expanded">
		<div id="sb-head">
			<a id="brand" href="/" title="Felix">
				<span class="brand-text">Felix</span>
			</a>
			<button id="sb-collapse" aria-label="Collapse sidebar" title="Collapse sidebar">
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M15 6l-6 6 6 6"/></svg>
			</button>
		</div>

		<button id="sb-new" title="New session (⌘N)">
			<svg class="icon" viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></svg>
			<span class="sb-label">New session</span>
		</button>

		<div class="sb-section-label sb-label">Agent</div>
		<select id="sb-agent-select" class="sb-agent sb-label" aria-label="Agent"></select>

		<div class="sb-section-label sb-label">Sessions</div>
		<input id="sessions-filter" class="sb-list-filter sb-label" type="search" placeholder="Filter sessions...">
		<div id="sessions-list" class="sb-list"></div>

		<div class="sb-section-label sb-label">Tools</div>
		<nav id="sb-tools">
			<button class="sb-item" data-view="files" title="Files">
				<svg class="icon" viewBox="0 0 24 24"><path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/></svg>
				<span class="sb-label">Files</span>
			</button>
			<button class="sb-item" data-view="settings" title="Settings">
				<svg class="icon" viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06A1.65 1.65 0 0 0 15 19.4a1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.6 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.6a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09A1.65 1.65 0 0 0 15 4.6a1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9c.36.13.74.2 1.11.21H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
				<span class="sb-label">Settings</span>
			</button>
			<button class="sb-item" data-view="jobs" title="Jobs">
				<svg class="icon" viewBox="0 0 24 24"><rect x="3" y="7" width="18" height="13" rx="2"/><path d="M8 7V5a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
				<span class="sb-label">Jobs</span>
			</button>
			<button class="sb-item" data-view="logs" title="Logs">
				<svg class="icon" viewBox="0 0 24 24"><path d="M5 4h11a3 3 0 0 1 3 3v13a2 2 0 0 1-2 2H8a3 3 0 0 1-3-3z"/><path d="M9 8h6M9 12h6M9 16h4"/></svg>
				<span class="sb-label">Logs</span>
			</button>
		</nav>

		<div id="sb-foot">
			<div id="user-row">
				<button id="theme-toggle" aria-label="Toggle theme" title="Toggle theme">
					<svg class="icon" id="theme-icon" viewBox="0 0 24 24"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>
				</button>
			</div>
		</div>
	</aside>
	<div id="sidebar-backdrop"></div>

	<div id="main-col">
		<div id="topbar">
			<button id="topbar-menu" aria-label="Open menu" title="Menu">
				<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><line x1="3" y1="6" x2="21" y2="6"/><line x1="3" y1="12" x2="21" y2="12"/><line x1="3" y1="18" x2="21" y2="18"/></svg>
			</button>
			<span class="spacer"></span>
			<div id="topbar-toggles" role="group" aria-label="View toggles">
				<button class="tb-toggle" data-toggle="tools" aria-label="Toggle tool calls" title="Tool calls">
					<svg class="icon" viewBox="0 0 24 24"><path d="M14.7 6.3a4 4 0 0 0 5 5l-9 9a2.83 2.83 0 0 1-4-4z"/></svg>
				</button>
				<button class="tb-toggle" data-toggle="trace" aria-label="Toggle live trace" title="Live trace">
					<svg class="icon" viewBox="0 0 24 24"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>
				</button>
			</div>
			<div id="status-pill" title="connecting">
				<span class="status-dot connecting" id="topbar-status-dot"></span>
				<span id="status-text">Connecting</span>
			</div>
			<span id="topbar-token-chip" title="Tokens used / context window">&#183;</span>
		</div>

		<div id="main-pane">
			<div id="chat-view">
				<div id="messages">
					<div id="chat-empty-state" class="chat-empty">
						<div class="chat-empty-title">Felix is ready</div>
						<div class="chat-empty-body">Ask anything, or drop files anywhere to attach.</div>
					</div>
				</div>
				<div id="trace-panel" style="display:none;">
					<div id="trace-header"><span id="trace-title">Live trace</span><button id="trace-clear-btn" title="Clear trace">clear</button></div>
					<div id="trace-list"></div>
				</div>
				<div id="input-shell">
					<div id="attach-row" hidden></div>
					<div id="input-area">
						<button id="attach-btn" aria-label="Attach files" title="Attach files (or drop into chat)">
							<svg class="icon" viewBox="0 0 24 24"><path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48"/></svg>
						</button>
						<input type="file" id="attach-input" multiple hidden accept="image/*,application/pdf,text/*,application/json,application/x-yaml,.yaml,.yml,.csv,.md,.log,.go,.js,.ts,.py">
						<textarea id="input" rows="1" placeholder="Message Felix..." autofocus></textarea>
						<button id="send-btn" disabled aria-label="Send" title="Send">
							<svg class="icon" viewBox="0 0 24 24"><path d="M12 19V5M5 12l7-7 7 7"/></svg>
						</button>
						<button id="stop-btn" aria-label="Stop" title="Stop">
							<svg class="icon" viewBox="0 0 24 24"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>
						</button>
					</div>
					<div id="input-helper">Enter to send · Shift+Enter for newline · Drop files anywhere to attach</div>
				</div>
				<div id="drop-overlay" hidden><div id="drop-overlay-inner">Drop to attach</div></div>
			</div>
			<div id="embed-view" hidden>
				<!-- Embedded pages provide their own header (title + actions),
				     so no wrapper chrome here. Re-clicking the active sidebar
				     item or clicking any session row returns to chat. -->
				<iframe id="embed-frame" title=""></iframe>
			</div>
			<aside id="files-panel" hidden>
				<div id="files-head">
					<span id="files-title">Files</span>
					<button id="files-refresh" aria-label="Refresh" title="Refresh">
						<svg class="icon" viewBox="0 0 24 24"><path d="M21 12a9 9 0 1 1-3-6.7M21 4v5h-5"/></svg>
					</button>
					<button id="files-mkdir" aria-label="New folder" title="New folder">
						<svg class="icon" viewBox="0 0 24 24"><path d="M3 6.5C3 5.67 3.67 5 4.5 5H9l2 2h8.5c.83 0 1.5.67 1.5 1.5v9c0 .83-.67 1.5-1.5 1.5h-15c-.83 0-1.5-.67-1.5-1.5z"/><path d="M12 11v6M9 14h6"/></svg>
					</button>
					<label id="files-upload-label" aria-label="Upload" title="Upload">
						<input type="file" id="files-upload-input" hidden>
						<svg class="icon" viewBox="0 0 24 24"><path d="M12 16V4M7 9l5-5 5 5M5 18v2a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-2"/></svg>
					</label>
				</div>
				<div id="files-breadcrumbs"></div>
				<div id="files-error" hidden></div>
				<div id="files-list"></div>
				<div id="files-toolbar" hidden>
					<button data-fileaction="download" title="Download">
						<svg class="icon" viewBox="0 0 24 24"><path d="M12 4v12M7 11l5 5 5-5M5 18v2a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-2"/></svg> Download
					</button>
					<button data-fileaction="rename" title="Rename">
						<svg class="icon" viewBox="0 0 24 24"><path d="M16 4l4 4-11 11H5v-4z"/></svg> Rename
					</button>
					<button data-fileaction="move" title="Move">
						<svg class="icon" viewBox="0 0 24 24"><path d="M7 17L17 7M9 7h8v8"/></svg> Move
					</button>
					<button data-fileaction="delete" title="Delete">
						<svg class="icon" viewBox="0 0 24 24"><path d="M4 7h16M10 7V5a2 2 0 0 1 2-2h0a2 2 0 0 1 2 2v2M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12"/></svg> Delete
					</button>
				</div>
			</aside>
		</div>
	</div>
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
	// Start in the "connecting" amber state so the dot is meaningful
	// even before the first WebSocket frame lands.
	connStatus.className = 'status-dot connecting';
	var stopBtn = document.getElementById('stop-btn');
	var agentSelect = document.getElementById('agent-select');
	var sessionSelect = document.getElementById('session-select');
	var hamburgerBtn = document.getElementById('hamburger-btn');
	var hamburgerMenu = document.getElementById('hamburger-menu');
	var menuTheme = document.getElementById('menu-theme');
	var sessionsPanel = document.getElementById('sessions-panel');
	// View toggles live in the header now; keep references so the
	// apply*Visibility() helpers can mark them on/off.
	var viewToggles = document.getElementById('view-toggles');
	function setToggleState(view, on) {
		// Legacy hidden header still gets the class (kept so any future
		// re-introduction of the header strip works automatically).
		if (viewToggles) {
			var btn = viewToggles.querySelector('[data-view="' + view + '"]');
			if (btn) btn.classList.toggle('on', !!on);
		}
		// Mirror onto the topbar toggle buttons so the user sees the
		// current on/off state without leaving the chat surface.
		var tbToggles = document.getElementById('topbar-toggles');
		if (tbToggles) {
			var tbBtn = tbToggles.querySelector('[data-toggle="' + view + '"]');
			if (tbBtn) tbBtn.classList.toggle('active', !!on);
		}
	}

	// Inline SVG icons used in dynamically-rendered rows (file list,
	// session list, breadcrumbs). Same stroke style as the static
	// icons in the header. Swap any of these with literal HugeIcons
	// SVG markup if you license the pack; the .icon CSS class handles
	// sizing and color.
	var ICONS = {
		folder: '<svg class="icon" viewBox="0 0 24 24"><path d="M3 6.5C3 5.67 3.67 5 4.5 5H9l2 2h8.5c.83 0 1.5.67 1.5 1.5v9c0 .83-.67 1.5-1.5 1.5h-15c-.83 0-1.5-.67-1.5-1.5z"/></svg>',
		file:   '<svg class="icon" viewBox="0 0 24 24"><path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/></svg>',
		chat:   '<svg class="icon" viewBox="0 0 24 24"><path d="M21 12a8 8 0 0 1-12.8 6.4L3 20l1.6-5.2A8 8 0 1 1 21 12z"/></svg>',
		up:     '<svg class="icon" viewBox="0 0 24 24"><path d="M5 12l7-7 7 7M12 5v14"/></svg>',
		chevron: '<svg class="icon" viewBox="0 0 24 24"><path d="M9 6l6 6-6 6"/></svg>',
		plus:    '<svg class="icon" viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></svg>',
		broom:   '<svg class="icon" viewBox="0 0 24 24"><path d="M14 4l6 6-9 9-6-1-1-6z"/><path d="M11 7l6 6"/></svg>',
		gear:    '<svg class="icon" viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06A1.65 1.65 0 0 0 15 19.4a1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.6 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.6a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09A1.65 1.65 0 0 0 15 4.6a1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9c.36.13.74.2 1.11.21H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>',
		sun:     '<svg class="icon" viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/></svg>',
		eye:     '<svg class="icon" viewBox="0 0 24 24"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>',
		wrench:  '<svg class="icon" viewBox="0 0 24 24"><path d="M14.7 6.3a4 4 0 0 0 5 5l-9 9a2.83 2.83 0 0 1-4-4z"/></svg>',
		pulse:   '<svg class="icon" viewBox="0 0 24 24"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>',
		keyboard:'<svg class="icon" viewBox="0 0 24 24"><rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 10h.01M10 10h.01M14 10h.01M18 10h.01M7 14h10"/></svg>',
		command: '<svg class="icon" viewBox="0 0 24 24"><path d="M18 3a3 3 0 0 0-3 3v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3V6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3"/></svg>'
	};
	function icon(name) { return ICONS[name] || ''; }
	var sessionsList = document.getElementById('sessions-list');
	var sessionsNewBtn = document.getElementById('sessions-new');

	// --- Wave 2: per-session run history state ---
	// runsBySession: cache of past run summaries keyed by agentId+"::"+sessionKey.
	// Each value: { runs: [RunSummary], expanded: bool, loading: bool }. Populated
	// lazily on chevron expand via chat.runs RPC.
	var runsBySession = new Map();
	function runsKey(agentId, sessionKey) { return agentId + '::' + sessionKey; }
	// liveRunIdBySession: tracks the in-flight runId per scope so the runs
	// sublist can hide the delete button next to the active run. Populated on
	// the run_attached response to chat.send, cleared on the run_terminal event.
	var liveRunIdBySession = new Map();
	// replayState: read-only replay mode. null when viewing live; otherwise
	// { agentId, sessionKey, runId, events: [] } while a past run is loaded.
	var replayState = null;

	var sessionsFilter = document.getElementById('sessions-filter');
	if (sessionsFilter) {
		sessionsFilter.addEventListener('input', function() {
			var q = this.value.toLowerCase();
			var rows = sessionsList.querySelectorAll('.session-row');
			for (var i = 0; i < rows.length; i++) {
				var name = (rows[i].querySelector('.ses-name') || {}).textContent || '';
				rows[i].style.display = (!q || name.toLowerCase().indexOf(q) !== -1) ? '' : 'none';
			}
		});
	}

	// === Hamburger menu: open/close + action dispatch ===
	function openMenu() {
		hamburgerMenu.hidden = false;
		hamburgerBtn.setAttribute('aria-expanded', 'true');
	}
	function closeMenu() {
		hamburgerMenu.hidden = true;
		hamburgerBtn.setAttribute('aria-expanded', 'false');
	}
	function toggleMenu() { hamburgerMenu.hidden ? openMenu() : closeMenu(); }
	hamburgerBtn.addEventListener('click', function(e) { e.stopPropagation(); toggleMenu(); });
	document.addEventListener('click', function(e) {
		if (!hamburgerMenu.hidden && !hamburgerMenu.contains(e.target) && e.target !== hamburgerBtn) {
			closeMenu();
		}
	});
	document.addEventListener('keydown', function(e) {
		if (e.key === 'Escape' && !hamburgerMenu.hidden) closeMenu();
	});
	hamburgerMenu.addEventListener('click', function(e) {
		var item = e.target.closest('[data-action]');
		if (!item) return; // anchor links (Settings/Jobs/Logs) just navigate
		closeMenu();
		menuDispatch(item.dataset.action);
	});

	function menuDispatch(action) {
		switch (action) {
		case 'new':         doNewSession(); break;
		case 'clear':       doClearSession(); break;
		case 'theme':       toggleTheme(); break;
		}
	}

	// Header view-toggle strip: same toggle functions as the old menu items,
	// just promoted to a one-click control. Button .on class reflects state.
	if (viewToggles) {
		viewToggles.addEventListener('click', function(e) {
			var btn = e.target.closest('[data-view]');
			if (!btn) return;
			switch (btn.dataset.view) {
			case 'sessions': toggleSessions(); break;
			case 'files':      toggleFiles(); break;
			case 'tools':      toggleTools(); break;
			case 'trace':      toggleTrace(); break;
			}
		});
	}



	// --- Inline modal helpers ---
	// Replaces native prompt/confirm/alert across the file. All three
	// return a Promise:
	//   modalAlert(title, body)              -> Promise<void>
	//   modalConfirm(title, body, opts)      -> Promise<boolean>
	//   modalPrompt(title, body, opts)       -> Promise<string|null>
	// ESC or backdrop click dismisses with the cancel value (false / null).
	// Enter inside a prompt submits. The input field (or primary action
	// when there's no input) auto-focuses.
	var _modalSeq = 0;
	function _modalShow(opts) {
		return new Promise(function(resolve) {
			var seq = ++_modalSeq;
			var previouslyFocused = document.activeElement;
			var backdrop = document.createElement('div');
			backdrop.className = 'modal-backdrop';
			var modal = document.createElement('div');
			modal.className = 'modal';
			modal.setAttribute('role', opts.kind === 'alert' ? 'alertdialog' : 'dialog');
			modal.setAttribute('aria-modal', 'true');
			modal.tabIndex = -1;

			if (opts.title) {
				var t = document.createElement('div');
				t.className = 'modal-title';
				t.id = 'modal-title-' + seq;
				t.textContent = opts.title;
				modal.appendChild(t);
				modal.setAttribute('aria-labelledby', t.id);
			}
			if (opts.body) {
				var b = document.createElement('div');
				b.className = 'modal-body';
				b.id = 'modal-body-' + seq;
				b.textContent = opts.body;
				modal.appendChild(b);
				modal.setAttribute('aria-describedby', b.id);
			}

			var input = null;
			if (opts.kind === 'prompt') {
				input = document.createElement('input');
				input.type = 'text';
				input.className = 'modal-input';
				if (opts.defaultValue) input.value = opts.defaultValue;
				if (opts.placeholder)  input.placeholder = opts.placeholder;
				modal.appendChild(input);
			}

			var actions = document.createElement('div');
			actions.className = 'modal-actions';

			var resolved = false;
			function done(value) {
				if (resolved) return;
				resolved = true;
				document.removeEventListener('keydown', onKey);
				if (backdrop.parentNode) backdrop.parentNode.removeChild(backdrop);
				if (previouslyFocused && typeof previouslyFocused.focus === 'function') {
					try { previouslyFocused.focus(); } catch (e) { /* element gone */ }
				}
				resolve(value);
			}
			function cancelValue() {
				if (opts.kind === 'prompt') return null;
				if (opts.kind === 'alert')  return undefined;
				return false;
			}

			if (opts.kind !== 'alert') {
				var cancelBtn = document.createElement('button');
				cancelBtn.type = 'button';
				cancelBtn.className = 'modal-btn';
				cancelBtn.textContent = opts.cancelLabel || 'Cancel';
				cancelBtn.onclick = function() { done(cancelValue()); };
				actions.appendChild(cancelBtn);
			}

			var confirmBtn = document.createElement('button');
			confirmBtn.type = 'button';
			confirmBtn.className = 'modal-btn modal-btn-primary' +
				(opts.danger ? ' modal-btn-danger' : '');
			confirmBtn.textContent = opts.confirmLabel ||
				(opts.kind === 'alert' ? 'OK' : 'Confirm');
			confirmBtn.onclick = function() {
				if (opts.kind === 'prompt')      done(input ? input.value : '');
				else if (opts.kind === 'alert')  done();
				else                             done(true);
			};
			actions.appendChild(confirmBtn);
			modal.appendChild(actions);
			backdrop.appendChild(modal);

			backdrop.addEventListener('mousedown', function(e) {
				if (e.target === backdrop) done(cancelValue());
			});
			function getFocusable() {
				var nodes = modal.querySelectorAll(
					'a[href], button:not([disabled]), input:not([disabled]),' +
					' select:not([disabled]), textarea:not([disabled]),' +
					' [tabindex]:not([tabindex="-1"])'
				);
				return Array.prototype.filter.call(nodes, function(el) {
					return el.offsetParent !== null || el === document.activeElement;
				});
			}
			function onKey(e) {
				if (e.key === 'Escape') {
					e.preventDefault();
					done(cancelValue());
				} else if (e.key === 'Enter' && input) {
					e.preventDefault();
					done(input.value);
				} else if (e.key === 'Tab') {
					var focusable = getFocusable();
					if (focusable.length === 0) {
						e.preventDefault();
						modal.focus();
						return;
					}
					var first = focusable[0];
					var last = focusable[focusable.length - 1];
					var active = document.activeElement;
					if (!modal.contains(active)) {
						e.preventDefault();
						(e.shiftKey ? last : first).focus();
					} else if (e.shiftKey && active === first) {
						e.preventDefault();
						last.focus();
					} else if (!e.shiftKey && active === last) {
						e.preventDefault();
						first.focus();
					}
				}
			}
			document.addEventListener('keydown', onKey);

			document.body.appendChild(backdrop);
			setTimeout(function() {
				if (input) input.focus();
				else confirmBtn.focus();
			}, 0);
		});
	}
	function modalAlert(title, body) {
		return _modalShow({ kind: 'alert', title: title, body: body });
	}
	function modalConfirm(title, body, opts) {
		opts = opts || {};
		return _modalShow({
			kind: 'confirm', title: title, body: body,
			confirmLabel: opts.confirmLabel, cancelLabel: opts.cancelLabel,
			danger: opts.danger
		});
	}
	function modalPrompt(title, body, opts) {
		opts = opts || {};
		return _modalShow({
			kind: 'prompt', title: title, body: body,
			defaultValue: opts.defaultValue, placeholder: opts.placeholder,
			confirmLabel: opts.confirmLabel || 'Save',
			cancelLabel: opts.cancelLabel
		});
	}

	// --- Command palette ---
	// Cmd+K (or Ctrl+K) opens a searchable list of every action in the
	// surface: switching agents/sessions, panel toggles, settings deep
	// links, theme, restart, sign out. Other tools the operator uses all
	// day (VS Code, Linear, Slack) have one — Felix now does too.
	var META = (navigator.platform || '').toLowerCase().indexOf('mac') >= 0;
	var MOD_LABEL = META ? '⌘' : 'Ctrl';
	var paletteOpen = false;
	function buildCommands() {
		var cmds = [];
		// Agent switching — only shows if there's more than one configured.
		var agentOpts = agentSelect ? agentSelect.options : [];
		if (agentOpts.length > 1) {
			for (var i = 0; i < agentOpts.length; i++) (function(opt) {
				if (opt.value === agentSelect.value) return; // skip current
				cmds.push({
					section: 'Switch agent', icon: 'chat',
					label: opt.textContent || opt.value,
					run: function() {
						agentSelect.value = opt.value;
						agentSelect.dispatchEvent(new Event('change'));
					}
				});
			})(agentOpts[i]);
		}
		// Session switching — pulled from the same <select> the dropdown drives.
		var sesOpts = sessionSelect ? sessionSelect.options : [];
		for (var j = 0; j < sesOpts.length; j++) (function(opt) {
			if (opt.value === sessionSelect.value) return;
			cmds.push({
				section: 'Switch session', icon: 'chat',
				label: opt.textContent || opt.value,
				run: function() {
					sessionSelect.value = opt.value;
					sessionSelect.dispatchEvent(new Event('change'));
				}
			});
		})(sesOpts[j]);
		// Actions.
		cmds.push(
			{ section: 'Session', icon: 'plus', label: 'New session', hint: MOD_LABEL + ' N', run: doNewSession },
			{ section: 'Session', icon: 'broom', label: 'Clear current session', run: doClearSession },
			{ section: 'View',    icon: 'folder', label: 'Toggle sessions panel', run: toggleSessions },
			{ section: 'Open',    icon: 'file',   label: 'Files',                 run: function() { window.location.href = '/files' + (agentSelect.value ? '?agent=' + encodeURIComponent(agentSelect.value) : ''); } },
			{ section: 'View',    icon: 'wrench', label: 'Toggle tool calls',     run: toggleTools },
			{ section: 'View',    icon: 'pulse',  label: 'Toggle trace panel',    run: toggleTrace },
			{ section: 'View',    icon: 'sun',    label: 'Toggle theme',          run: toggleTheme },
			{ section: 'Open',    icon: 'gear',   label: 'Settings',              run: function() { window.location.href = '/settings'; } },
			{ section: 'Open',    icon: 'gear',   label: 'Settings: Agents',      run: function() { window.location.href = '/settings#agents'; } },
			{ section: 'Open',    icon: 'gear',   label: 'Settings: Models',      run: function() { window.location.href = '/settings#models'; } },
			{ section: 'Open',    icon: 'gear',   label: 'Settings: MCP',         run: function() { window.location.href = '/settings#mcp'; } },
			{ section: 'Open',    icon: 'eye',    label: 'Logs',                  run: function() { window.location.href = '/logs'; } },
			{ section: 'Open',    icon: 'eye',    label: 'Jobs',                  run: function() { window.location.href = '/jobs'; } },
			{ section: 'Help',    icon: 'keyboard', label: 'Keyboard shortcuts',  hint: '?', run: showCheatSheet }
		);
		return cmds;
	}
	// Lightweight fuzzy: characters of the query must appear in order in
	// the label. Word-boundary matches score higher than mid-token ones
	// so "ns" finds "New session" before "Settings".
	function fuzzyScore(query, label) {
		if (!query) return 1;
		var q = query.toLowerCase(), l = label.toLowerCase();
		var qi = 0, score = 0, prevBoundary = true;
		for (var li = 0; li < l.length && qi < q.length; li++) {
			if (l[li] === q[qi]) {
				score += prevBoundary ? 2 : 1;
				qi++;
				prevBoundary = false;
			} else {
				prevBoundary = (l[li] === ' ' || l[li] === ':' || l[li] === '-');
			}
		}
		return qi === q.length ? score : 0;
	}
	function showPalette() {
		if (paletteOpen) return;
		paletteOpen = true;
		var previouslyFocused = document.activeElement;
		var commands = buildCommands();
		var filtered = commands.slice();
		var activeIdx = 0;

		var backdrop = document.createElement('div');
		backdrop.className = 'modal-backdrop palette-backdrop';
		var palette = document.createElement('div');
		palette.className = 'palette';
		palette.setAttribute('role', 'dialog');
		palette.setAttribute('aria-modal', 'true');
		palette.setAttribute('aria-label', 'Command palette');

		var input = document.createElement('input');
		input.type = 'text';
		input.className = 'palette-input';
		input.placeholder = 'Type a command, agent, or session...';
		input.setAttribute('aria-label', 'Command palette search');
		input.setAttribute('autocomplete', 'off');
		input.setAttribute('spellcheck', 'false');

		var list = document.createElement('div');
		list.className = 'palette-list';
		list.setAttribute('role', 'listbox');

		var footer = document.createElement('div');
		footer.className = 'palette-footer';
		footer.innerHTML = '<span><kbd>↑</kbd><kbd>↓</kbd> navigate <kbd>Enter</kbd> run</span>' +
			'<span><kbd>Esc</kbd> close</span>';

		palette.appendChild(input);
		palette.appendChild(list);
		palette.appendChild(footer);
		backdrop.appendChild(palette);

		function render() {
			list.innerHTML = '';
			if (filtered.length === 0) {
				var empty = document.createElement('div');
				empty.className = 'palette-empty';
				empty.textContent = 'No matches';
				list.appendChild(empty);
				return;
			}
			var lastSection = null;
			for (var i = 0; i < filtered.length; i++) {
				var cmd = filtered[i];
				if (cmd.section !== lastSection) {
					lastSection = cmd.section;
					var sec = document.createElement('div');
					sec.className = 'palette-section';
					sec.textContent = lastSection;
					list.appendChild(sec);
				}
				var row = document.createElement('div');
				row.className = 'palette-item' + (i === activeIdx ? ' active' : '');
				row.setAttribute('role', 'option');
				row.dataset.idx = i;
				row.innerHTML =
					(cmd.icon ? icon(cmd.icon) : '<span class="icon"></span>') +
					'<span class="palette-label"></span>' +
					(cmd.hint ? '<span class="palette-hint"></span>' : '');
				row.querySelector('.palette-label').textContent = cmd.label;
				if (cmd.hint) row.querySelector('.palette-hint').textContent = cmd.hint;
				list.appendChild(row);
			}
		}
		function recompute() {
			var q = input.value.trim();
			if (!q) { filtered = commands.slice(); }
			else {
				var scored = [];
				for (var i = 0; i < commands.length; i++) {
					var s = fuzzyScore(q, commands[i].label);
					if (s > 0) scored.push({ s: s, c: commands[i] });
				}
				scored.sort(function(a, b) { return b.s - a.s; });
				filtered = scored.map(function(x) { return x.c; });
			}
			activeIdx = 0;
			render();
		}
		function scrollIntoView() {
			var rows = list.querySelectorAll('.palette-item');
			if (rows[activeIdx]) rows[activeIdx].scrollIntoView({ block: 'nearest' });
		}
		function move(d) {
			if (filtered.length === 0) return;
			activeIdx = (activeIdx + d + filtered.length) %% filtered.length;
			var rows = list.querySelectorAll('.palette-item');
			for (var i = 0; i < rows.length; i++) rows[i].classList.toggle('active', i === activeIdx);
			scrollIntoView();
		}
		function close() {
			if (!paletteOpen) return;
			paletteOpen = false;
			document.removeEventListener('keydown', onKey, true);
			if (backdrop.parentNode) backdrop.parentNode.removeChild(backdrop);
			if (previouslyFocused && typeof previouslyFocused.focus === 'function') {
				try { previouslyFocused.focus(); } catch (e) { /* gone */ }
			}
		}
		function execute() {
			var cmd = filtered[activeIdx];
			if (!cmd) return;
			close();
			try { cmd.run(); } catch (e) { console.error('palette command failed', e); }
		}
		function onKey(e) {
			if (e.key === 'Escape') { e.preventDefault(); close(); }
			else if (e.key === 'ArrowDown') { e.preventDefault(); move(1); }
			else if (e.key === 'ArrowUp')   { e.preventDefault(); move(-1); }
			else if (e.key === 'Enter')     { e.preventDefault(); execute(); }
		}
		input.addEventListener('input', recompute);
		list.addEventListener('click', function(e) {
			var row = e.target.closest('.palette-item');
			if (!row) return;
			activeIdx = parseInt(row.dataset.idx, 10) || 0;
			execute();
		});
		backdrop.addEventListener('mousedown', function(e) {
			if (e.target === backdrop) close();
		});
		document.addEventListener('keydown', onKey, true);

		document.body.appendChild(backdrop);
		render();
		setTimeout(function() { input.focus(); }, 0);
	}
	function showCheatSheet() {
		var rows = [
			['Open command palette', MOD_LABEL + ' K'],
			['New session',          MOD_LABEL + ' N'],
			['Show this cheat sheet', '?'],
			['Close dialog / menu',  'Esc'],
			['Send message',         'Enter'],
			['Newline in message',   'Shift Enter']
		];
		var backdrop = document.createElement('div');
		backdrop.className = 'modal-backdrop';
		var sheet = document.createElement('div');
		sheet.className = 'cheat';
		sheet.setAttribute('role', 'dialog');
		sheet.setAttribute('aria-modal', 'true');
		sheet.setAttribute('aria-label', 'Keyboard shortcuts');
		var title = document.createElement('div');
		title.className = 'cheat-title';
		title.textContent = 'Keyboard shortcuts';
		sheet.appendChild(title);
		var table = document.createElement('table');
		for (var i = 0; i < rows.length; i++) {
			var tr = document.createElement('tr');
			var td1 = document.createElement('td');
			td1.textContent = rows[i][0];
			var td2 = document.createElement('td');
			var parts = rows[i][1].split(' ');
			for (var j = 0; j < parts.length; j++) {
				var kbd = document.createElement('kbd');
				kbd.textContent = parts[j];
				td2.appendChild(kbd);
				if (j < parts.length - 1) td2.appendChild(document.createTextNode(' '));
			}
			tr.appendChild(td1); tr.appendChild(td2);
			table.appendChild(tr);
		}
		sheet.appendChild(table);
		backdrop.appendChild(sheet);
		var prev = document.activeElement;
		function close() {
			document.removeEventListener('keydown', onKey, true);
			if (backdrop.parentNode) backdrop.parentNode.removeChild(backdrop);
			if (prev && typeof prev.focus === 'function') try { prev.focus(); } catch (e) {}
		}
		function onKey(e) { if (e.key === 'Escape') { e.preventDefault(); close(); } }
		backdrop.addEventListener('mousedown', function(e) { if (e.target === backdrop) close(); });
		document.addEventListener('keydown', onKey, true);
		document.body.appendChild(backdrop);
		setTimeout(function() { sheet.focus && sheet.focus(); }, 0);
	}
	// Global key bindings. Cmd/Ctrl+K opens the palette from anywhere
	// (including the textarea — operators expect this). Cmd/Ctrl+N is
	// new-session. "?" opens the cheat sheet, but only when the user
	// isn't typing into an input or contenteditable.
	document.addEventListener('keydown', function(e) {
		var mod = e.metaKey || e.ctrlKey;
		if (mod && e.key.toLowerCase() === 'k') {
			e.preventDefault();
			if (paletteOpen) return;
			showPalette();
			return;
		}
		if (mod && e.key.toLowerCase() === 'n') {
			e.preventDefault();
			doNewSession();
			return;
		}
		if (e.key === '?' && !e.metaKey && !e.ctrlKey && !e.altKey) {
			var t = e.target;
			var typing = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable);
			if (!typing) {
				e.preventDefault();
				showCheatSheet();
			}
		}
	});

	// Tool visibility toggle — state persists, menu label reflects action
	var toolsHidden = localStorage.getItem('felix-hide-tools') === 'true';
	function applyToolVisibility() {
		messagesEl.classList.toggle('hide-tools', toolsHidden);
		setToggleState('tools', !toolsHidden);
	}
	applyToolVisibility();
	function toggleTools() {
		toolsHidden = !toolsHidden;
		localStorage.setItem('felix-hide-tools', toolsHidden);
		applyToolVisibility();
	}

	// Live trace panel
	var tracePanel = document.getElementById('trace-panel');
	var traceList = document.getElementById('trace-list');
	var traceClearBtn = document.getElementById('trace-clear-btn');
	var traceVisible = localStorage.getItem('felix-show-trace') === 'true';
	var traceFirstOfRun = true;
	function applyTraceVisibility() {
		tracePanel.style.display = traceVisible ? 'block' : 'none';
		setToggleState('trace', traceVisible);
	}
	applyTraceVisibility();
	function toggleTrace() {
		traceVisible = !traceVisible;
		localStorage.setItem('felix-show-trace', traceVisible);
		applyTraceVisibility();
	}
	traceClearBtn.addEventListener('click', function() {
		traceList.innerHTML = '';
	});

	// === File explorer panel ===
	var filesPanel = document.getElementById('files-panel');
	var filesList = document.getElementById('files-list');
	var filesBreadcrumbs = document.getElementById('files-breadcrumbs');
	var filesError = document.getElementById('files-error');
	var filesToolbar = document.getElementById('files-toolbar');
	var filesRefresh = document.getElementById('files-refresh');
	var filesUploadInput = document.getElementById('files-upload-input');
	var mainRow = document.getElementById('main-row');

	// Files now lives at /files as a full page (sidebar Files item
	// routes there). Keep the legacy panel JS in place but force it
	// off so stale localStorage from earlier builds doesn't re-show
	// the old right-side overlay.
	var filesVisible = false;
	localStorage.removeItem('felix-show-files');
	var filesCwd = '';
	var filesSelected = null; // {name, type}

	function applyFilesVisibility() {
		if (filesPanel) filesPanel.hidden = !filesVisible;
		setToggleState('files', filesVisible);
		if (filesVisible) loadFiles();
	}
	function toggleFiles() {
		filesVisible = !filesVisible;
		localStorage.setItem('felix-show-files', filesVisible);
		applyFilesVisibility();
	}

	function fmtSize(n) {
		if (n == null) return '';
		if (n < 1024) return n + ' B';
		var u = ['KB','MB','GB'];
		var i = -1;
		do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
		return n.toFixed(n < 10 ? 1 : 0) + ' ' + u[i];
	}

	function showFilesError(msg) {
		filesError.textContent = msg;
		filesError.hidden = false;
		setTimeout(function() { filesError.hidden = true; }, 4000);
	}

	function clearSelection() {
		filesSelected = null;
		filesToolbar.hidden = true;
		var sel = filesList.querySelector('.file-row.selected');
		if (sel) sel.classList.remove('selected');
	}

	function renderBreadcrumbs(crumbs) {
		filesBreadcrumbs.innerHTML = '';
		var rootLink = document.createElement('a');
		rootLink.textContent = 'workspace';
		rootLink.addEventListener('click', function() { filesCwd = ''; clearSelection(); loadFiles(); });
		filesBreadcrumbs.appendChild(rootLink);
		for (var i = 0; i < crumbs.length; i++) {
			var sep = document.createElement('span'); sep.textContent = ' › ';
			filesBreadcrumbs.appendChild(sep);
			var a = document.createElement('a');
			a.textContent = crumbs[i].name;
			(function(p) {
				a.addEventListener('click', function() { filesCwd = p; clearSelection(); loadFiles(); });
			})(crumbs[i].path);
			filesBreadcrumbs.appendChild(a);
		}
	}

	function renderFilesList(entries) {
		filesList.innerHTML = '';
		if (filesCwd) {
			var up = document.createElement('div');
			up.className = 'file-row';
			up.innerHTML = '<span class="file-icon">' + icon('up') + '</span><span class="file-name">..</span>';
			up.addEventListener('click', function() {
				filesCwd = filesCwd.split('/').slice(0, -1).join('/');
				clearSelection();
				loadFiles();
			});
			filesList.appendChild(up);
		}
		if (entries.length === 0 && !filesCwd) {
			var empty = document.createElement('div');
			empty.className = 'files-empty';
			empty.textContent = 'No files yet. Use ⬇ to upload, or ask the agent to create something.';
			filesList.appendChild(empty);
			return;
		}
		entries.forEach(function(e) {
			var row = document.createElement('div');
			row.className = 'file-row';
			row.dataset.name = e.name;
			row.dataset.type = e.type;
			row.innerHTML = '<span class="file-icon">' + icon(e.type === 'dir' ? 'folder' : 'file') + '</span>' +
				'<span class="file-name"></span>' +
				(e.type === 'file' ? '<span class="file-size">' + fmtSize(e.size) + '</span>' : '');
			row.querySelector('.file-name').textContent = e.name;
			// Click: select OR descend (for dir)
			row.addEventListener('click', function() {
				if (e.type === 'dir') {
					filesCwd = filesCwd ? filesCwd + '/' + e.name : e.name;
					clearSelection();
					loadFiles();
				} else {
					if (filesSelected && filesSelected.name === e.name) {
						clearSelection();
					} else {
						clearSelection();
						row.classList.add('selected');
						filesSelected = { name: e.name, type: e.type };
						filesToolbar.hidden = false;
					}
				}
			});
			// Double-click on file: open raw in new tab
			row.addEventListener('dblclick', function() {
				if (e.type !== 'file') return;
				var path = filesCwd ? filesCwd + '/' + e.name : e.name;
				window.open('/files/raw?agent=' + encodeURIComponent(agentSelect.value) +
					'&path=' + encodeURIComponent(path), '_blank');
			});
			filesList.appendChild(row);
		});
	}

	function loadFiles() {
		if (!agentSelect.value) return;
		var url = '/files/list?agent=' + encodeURIComponent(agentSelect.value) +
			'&dir=' + encodeURIComponent(filesCwd);
		fetch(url)
			.then(function(r) {
				if (!r.ok) return r.json().then(function(j) { throw new Error(j.error || ('HTTP ' + r.status)); });
				return r.json();
			})
			.then(function(data) {
				renderBreadcrumbs(data.breadcrumbs || []);
				renderFilesList(data.entries || []);
			})
			.catch(function(err) { showFilesError('Load failed: ' + err.message); });
	}

	filesRefresh.addEventListener('click', function() { clearSelection(); loadFiles(); });

	// New folder button — prompts for a name, creates inside current cwd.
	var filesMkdir = document.getElementById('files-mkdir');
	filesMkdir.addEventListener('click', function() {
		modalPrompt('New folder', 'Folder name', { confirmLabel: 'Create' }).then(function(raw) {
			if (raw === null) return; // cancelled
			var name = String(raw).trim();
			if (!name) return;
			var target = filesCwd ? filesCwd + '/' + name : name;
			fetch('/files/mkdir', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ agent: agentSelect.value, path: target })
			})
				.then(function(r) {
					if (!r.ok && r.status !== 204) return r.json().then(function(j) { throw new Error(j.error || ('HTTP ' + r.status)); });
				})
				.then(function() { loadFiles(); })
				.catch(function(err) { showFilesError('Create folder failed: ' + err.message); });
		});
	});

	// Upload via the hidden file input.
	filesUploadInput.addEventListener('change', function() {
		var file = filesUploadInput.files[0];
		if (!file) return;
		var fd = new FormData();
		fd.append('file', file);
		var url = '/files/upload?agent=' + encodeURIComponent(agentSelect.value) +
			'&dir=' + encodeURIComponent(filesCwd);
		fetch(url, { method: 'POST', body: fd })
			.then(function(r) {
				if (!r.ok) return r.json().then(function(j) { throw new Error(j.error || ('HTTP ' + r.status)); });
				return r.json();
			})
			.then(function() { filesUploadInput.value = ''; loadFiles(); })
			.catch(function(err) { showFilesError('Upload failed: ' + err.message); filesUploadInput.value = ''; });
	});

	// Toolbar action dispatch.
	filesToolbar.addEventListener('click', function(e) {
		var btn = e.target.closest('[data-fileaction]');
		if (!btn || !filesSelected) return;
		var act = btn.dataset.fileaction;
		var path = filesCwd ? filesCwd + '/' + filesSelected.name : filesSelected.name;
		if (act === 'download') {
			window.open('/files/raw?agent=' + encodeURIComponent(agentSelect.value) +
				'&path=' + encodeURIComponent(path) + '&download=1', '_blank');
		} else if (act === 'rename') {
			modalPrompt('Rename', 'New name', {
				defaultValue: filesSelected.name,
				confirmLabel: 'Rename'
			}).then(function(name) {
				if (!name || name === filesSelected.name) return;
				fetch('/files/rename', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ agent: agentSelect.value, path: path, newName: name })
				})
					.then(function(r) {
						if (!r.ok && r.status !== 204) return r.json().then(function(j) { throw new Error(j.error || ('HTTP ' + r.status)); });
					})
					.then(function() { clearSelection(); loadFiles(); })
					.catch(function(err) { showFilesError('Rename failed: ' + err.message); });
			});
		} else if (act === 'move') {
			modalPrompt('Move file', 'Destination path (relative, includes the new filename)', {
				defaultValue: path,
				confirmLabel: 'Move'
			}).then(function(dest) {
				if (!dest || dest === path) return;
				fetch('/files/move', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ agent: agentSelect.value, from: path, to: dest })
				})
					.then(function(r) {
						if (!r.ok && r.status !== 204) return r.json().then(function(j) { throw new Error(j.error || ('HTTP ' + r.status)); });
					})
					.then(function() { clearSelection(); loadFiles(); })
					.catch(function(err) { showFilesError('Move failed: ' + err.message); });
			});
		} else if (act === 'delete') {
			modalConfirm(
				'Delete "' + filesSelected.name + '"?',
				'',
				{ confirmLabel: 'Delete', danger: true }
			).then(function(ok) {
				if (ok) deleteSelected(false);
			});
		}
	});

	function deleteSelected(recursive) {
		var path = filesCwd ? filesCwd + '/' + filesSelected.name : filesSelected.name;
		var url = '/files?agent=' + encodeURIComponent(agentSelect.value) +
			'&path=' + encodeURIComponent(path) +
			(recursive ? '&recursive=1' : '');
		fetch(url, { method: 'DELETE' })
			.then(function(r) {
				if (r.status === 409 && !recursive && filesSelected.type === 'dir') {
					return modalConfirm(
						'Folder not empty',
						'"' + filesSelected.name + '" contains files. Delete it and all its contents?',
						{ confirmLabel: 'Delete all', danger: true }
					).then(function(ok) {
						if (ok) return deleteSelected(true);
					});
				}
				if (!r.ok && r.status !== 204) return r.json().then(function(j) { throw new Error(j.error || ('HTTP ' + r.status)); });
				clearSelection();
				loadFiles();
			})
			.catch(function(err) { showFilesError('Delete failed: ' + err.message); });
	}

	applyFilesVisibility();

	// === Sessions (sessions) panel (left) ===
	var sessionsVisible = localStorage.getItem('felix-show-sessions');
	if (sessionsVisible === null) {
		// First visit: open on desktop, closed on mobile so the slide-over
		// doesn't land on top of the chat as soon as the page paints.
		sessionsVisible = !window.matchMedia('(max-width: 900px)').matches;
	} else {
		sessionsVisible = (sessionsVisible === 'true');
	}

	function applySessionsVisibility() {
		if (sessionsPanel) sessionsPanel.hidden = !sessionsVisible;
		setToggleState('sessions', sessionsVisible);
	}
	function toggleSessions() {
		sessionsVisible = !sessionsVisible;
		localStorage.setItem('felix-show-sessions', sessionsVisible);
		applySessionsVisibility();
	}

	function renderSessions(sessions) {
		sessionsList.innerHTML = '';
		if (!sessions || sessions.length === 0) {
			var empty = document.createElement('div');
			empty.className = 'sessions-empty';
			empty.textContent = 'No sessions yet. Click + to create one.';
			sessionsList.appendChild(empty);
			return;
		}
		var aid = agentSelect.value;
		for (var i = 0; i < sessions.length; i++) {
			var s = sessions[i];
			var row = document.createElement('div');
			row.className = 'session-row';
			row.dataset.sessionKey = s.key;
			if (s.key === sessionSelect.value) row.classList.add('active');
			// Chevron expands the per-session runs sublist; the inline SVG
			// stays in flow so CSS rotation works (a wrapping span would
			// also work but the SVG carries the .ses-chevron hook directly).
			row.innerHTML = '<svg class="ses-chevron" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 6l6 6-6 6"/></svg>' +
				'<span class="ses-icon">' + icon('chat') + '</span>' +
				'<span class="ses-name" title="Double-click to rename"></span>' +
				'<span class="ses-count"></span>' +
				'<button class="ses-clear" type="button" aria-label="Clear session" title="Clear session">' +
					'<svg viewBox="0 0 24 24" class="icon"><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M6 6l1 14a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-14"/></svg>' +
				'</button>';
			var nameEl = row.querySelector('.ses-name');
			nameEl.textContent = s.title || s.key;
			nameEl.dataset.title = s.title || '';
			row.querySelector('.ses-count').textContent = s.entryCount;
			var clearBtn = row.querySelector('.ses-clear');
			var chevEl = row.querySelector('.ses-chevron');
			(function(key, name, rowEl, nameSpan, chev) {
				// detail===2 means the first click of a double-click —
				// short-circuit so we never switch sessions when the user
				// is starting an inline rename.
				rowEl.addEventListener('click', function(ev) {
					if (ev.detail === 2) {
						startSessionRename(rowEl, nameSpan, key);
						return;
					}
					if (key === sessionSelect.value) return;
					sessionSelect.value = key;
					highlightActiveSession();
					sessionSelect.dispatchEvent(new Event('change'));
				});
				clearBtn.addEventListener('click', function(e) {
					e.stopPropagation();
					modalConfirm('Clear session?',
						'Wipe all messages in "' + name + '". The session itself stays. This cannot be undone.',
						{ confirmLabel: 'Clear', danger: true }
					).then(function(ok) {
						if (!ok) return;
						clearSessionByKey(key);
					});
				});
				// Chevron toggles the per-session runs sublist. First expand
				// triggers a chat.runs RPC; subsequent toggles re-render from
				// the local cache.
				chev.addEventListener('click', function(ev) {
					ev.stopPropagation(); // don't switch sessions
					var k = runsKey(aid, key);
					var entry = runsBySession.get(k) || { runs: [], expanded: false, loading: false };
					entry.expanded = !entry.expanded;
					runsBySession.set(k, entry);
					chev.classList.toggle('expanded', entry.expanded);
					var sl = document.querySelector('.runs-sublist[data-key="' + cssEscape(k) + '"]');
					if (sl) sl.classList.toggle('expanded', entry.expanded);
					if (entry.expanded && entry.runs.length === 0 && !entry.loading) {
						fetchRuns(aid, key);
					} else if (entry.expanded) {
						renderRunsSublistFor(k);
					}
				});
			})(s.key, s.title || s.key, row, nameEl, chevEl);
			sessionsList.appendChild(row);
			// Sibling sublist directly under the row; CSS .runs-sublist
			// stays hidden until .expanded is toggled by the chevron.
			var sublist = document.createElement('div');
			sublist.className = 'runs-sublist';
			sublist.dataset.key = runsKey(aid, s.key);
			sessionsList.appendChild(sublist);
		}
	}

	// cssEscape — CSS.escape isn't safe to assume in older Safari shipped
	// with some macOS minor versions; this fallback handles the characters
	// we put in run-sublist data-key (alphanumerics + ":" + slug chars).
	function cssEscape(s) {
		if (window.CSS && typeof window.CSS.escape === 'function') return window.CSS.escape(s);
		return String(s).replace(/[^a-zA-Z0-9_-]/g, function(c) { return '\\' + c; });
	}

	// fetchRuns sends chat.runs for (agentId, sessionKey). The onmessage
	// dispatcher routes runs-... IDs back to the sublist renderer. Marks
	// the entry as loading so the placeholder shows immediately.
	function fetchRuns(agentId, sessionKey) {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		var k = runsKey(agentId, sessionKey);
		var entry = runsBySession.get(k) || { runs: [], expanded: false, loading: false };
		entry.loading = true;
		runsBySession.set(k, entry);
		renderRunsSublistFor(k);
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'chat.runs',
			params: { agentId: agentId, sessionKey: sessionKey },
			id: 'runs-' + k
		}));
	}

	// renderRunsSublistFor paints the cached entry for the given key into
	// the corresponding .runs-sublist node. Wires per-row click + delete
	// handlers after the innerHTML refresh.
	function renderRunsSublistFor(key) {
		var sublist = document.querySelector('.runs-sublist[data-key="' + cssEscape(key) + '"]');
		if (!sublist) return;
		var entry = runsBySession.get(key);
		if (!entry) { sublist.innerHTML = ''; return; }
		if (entry.loading) {
			sublist.innerHTML = '<div class="run-row" style="opacity:0.5">Loading…</div>';
			return;
		}
		if (!entry.runs || entry.runs.length === 0) {
			sublist.innerHTML = '<div class="run-row" style="opacity:0.5">No past runs</div>';
			return;
		}
		var html = '';
		for (var i = 0; i < entry.runs.length; i++) {
			html += formatRunRow(key, entry.runs[i]);
		}
		sublist.innerHTML = html;
		// Wire row click (loads replay) + delete click (chat.deleteRun).
		var rows = sublist.querySelectorAll('.run-row[data-run-id]');
		for (var r = 0; r < rows.length; r++) {
			(function(rowEl) {
				var runId = rowEl.dataset.runId;
				rowEl.addEventListener('click', function(ev) {
					if (ev.target.closest && ev.target.closest('.run-delete')) return;
					var parts = key.split('::');
					loadRunReadOnly(parts[0], parts[1], runId);
				});
				var delBtn = rowEl.querySelector('.run-delete');
				if (delBtn) {
					delBtn.addEventListener('click', function(ev) {
						ev.stopPropagation();
						if (!confirm('Delete this run? The conversation history stays; only the per-turn event log is removed.')) return;
						var parts = key.split('::');
						deleteRun(parts[0], parts[1], runId);
					});
				}
			})(rows[r]);
		}
	}

	// formatRunRow builds the HTML for one row in the runs sublist. The
	// delete affordance is omitted for the currently-in-flight run so the
	// user can't try to remove a log file the writer still owns.
	function formatRunRow(key, r) {
		var t = (r.started_at || '').slice(11, 19); // HH:MM:SS from RFC3339
		var status = (r.status || 'unknown');
		var count = (r.last_seq || 0) + ' events';
		var isLive = (liveRunIdBySession.get(key) === r.id);
		var delHTML = isLive ? '' :
			'<button class="run-delete" title="Delete run">' +
				'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">' +
					'<polyline points="3 6 5 6 21 6"/>' +
					'<path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>' +
				'</svg></button>';
		// ULIDs are alphanumeric so escaping is a no-op in practice, but
		// defense in depth: never trust server strings inside attribute values.
		var safeId = String(r.id || '').replace(/"/g, '&quot;');
		var safeStatus = escHtml(status);
		return '<div class="run-row" data-run-id="' + safeId + '">' +
			'<span class="run-time">' + escHtml(t) + '</span>' +
			'<span class="run-status ' + safeStatus + '">' + safeStatus + '</span>' +
			'<span class="run-count">' + escHtml(count) + '</span>' +
			delHTML +
			'</div>';
	}

	// deleteRun fires chat.deleteRun. The response (success or error) is
	// handled by the del-... branch in the onmessage dispatcher.
	function deleteRun(agentId, sessionKey, runId) {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'chat.deleteRun',
			params: { agentId: agentId, sessionKey: sessionKey, runId: runId },
			id: 'del-' + runId
		}));
	}

	// loadRunReadOnly fires chat.replay. The replay-... branch in the
	// onmessage dispatcher invokes renderReplayMode() when results land.
	function loadRunReadOnly(agentId, sessionKey, runId) {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		replayState = { agentId: agentId, sessionKey: sessionKey, runId: runId, events: [] };
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'chat.replay',
			params: { agentId: agentId, sessionKey: sessionKey, runId: runId, fromSeq: 0 },
			id: 'replay-' + runId
		}));
	}

	// renderReplayMode paints replayState.events into the chat pane in
	// read-only mode. The composer/stop button are hidden via the
	// body.replay-mode class (see CSS). A yellow banner anchors the
	// top of #chat-view; clicking it returns to live.
	//
	// Past-run events come from eventToResult and use the same shapes the
	// live dispatcher already knows how to render (text_delta, tool_call_start,
	// tool_result, compaction.start/done, trace, done, run_terminal). User
	// messages are NOT in the run log — they live in session history — so
	// the replay shows only the assistant side of one turn.
	var renderReplayMode = function() {
		document.body.classList.add('replay-mode');
		var chatView = document.getElementById('chat-view');
		var banner = document.getElementById('replay-banner');
		if (!banner) {
			banner = document.createElement('div');
			banner.id = 'replay-banner';
			banner.className = 'replay-banner';
			banner.innerHTML = '<span class="label">← <strong>Back to live</strong> · Viewing past run (read-only)</span>';
			banner.addEventListener('click', function() { exitReplayMode(); });
			if (chatView) chatView.insertBefore(banner, chatView.firstChild);
		}
		// Reset transcript state and repaint.
		messagesEl.innerHTML = '';
		currentAssistant = null;
		toolEls = {};
		if (!replayState || !replayState.events || replayState.events.length === 0) {
			var empty = document.createElement('div');
			empty.style.cssText = 'opacity:0.5;padding:1rem';
			empty.textContent = 'Run is empty or no longer available.';
			messagesEl.appendChild(empty);
			return;
		}
		var localAssistant = null;
		for (var i = 0; i < replayState.events.length; i++) {
			var ev = replayState.events[i];
			if (!ev || !ev.type) continue;
			switch (ev.type) {
			case 'text_delta':
				if (!localAssistant) {
					localAssistant = addAssistantMsg();
					currentAssistant = localAssistant;
				}
				appendToAssistant(ev.text || '');
				break;
			case 'tool_call_start':
				if (localAssistant) {
					finalizeAssistant();
					localAssistant = null;
					currentAssistant = null;
				}
				addToolCall(ev.tool, ev.id, ev.input);
				break;
			case 'tool_result':
				updateToolResult(ev.tool, ev.id, ev.input, ev.output, ev.error, ev.images, ev.auth_required);
				break;
			case 'compaction.start':
				if (!localAssistant) {
					localAssistant = addAssistantMsg('');
					currentAssistant = localAssistant;
				}
				appendToAssistant('\n*[Compacting context…]*\n');
				break;
			case 'compaction.done':
				if (localAssistant) appendToAssistant('\n*[Context compacted.]*\n');
				break;
			case 'trace':
				addTraceRow(ev);
				break;
			case 'run_terminal':
				if (localAssistant) {
					finalizeAssistant();
					localAssistant = null;
					currentAssistant = null;
				}
				if (ev.status && ev.status !== 'completed') {
					var marker = '';
					switch (ev.status) {
					case 'cancelled':   marker = ev.reason === 'superseded' ? '↳ replaced by next turn' : '⏹ cancelled'; break;
					case 'interrupted': marker = '↯ interrupted by server restart'; break;
					case 'failed':      marker = '⚠ failed' + (ev.error ? ': ' + ev.error : ''); break;
					default:            marker = '— ' + ev.status;
					}
					addSystemMarker(marker);
				}
				break;
			default:
				// Unknown event types (e.g. 'done') don't need a visual; the
				// transcript above them already captures the substance.
				break;
			}
		}
		if (localAssistant) finalizeAssistant();
		currentAssistant = null;
		scrollToBottom();
	};

	// exitReplayMode tears down the banner, drops the replay-mode body
	// class, and rehydrates the live session transcript from server history.
	var exitReplayMode = function() {
		document.body.classList.remove('replay-mode');
		var banner = document.getElementById('replay-banner');
		if (banner) banner.remove();
		replayState = null;
		// Reset transcript state.
		messagesEl.innerHTML = '';
		currentAssistant = null;
		toolEls = {};
		refreshEmptyState();
		// Re-request the live session's persisted history. This mirrors
		// what the session.switch handler does on session change.
		if (ws && ws.readyState === WebSocket.OPEN && agentSelect.value && sessionSelect.value) {
			ws.send(JSON.stringify({
				jsonrpc: '2.0',
				method: 'session.history',
				params: { agentId: agentSelect.value, sessionKey: sessionSelect.value },
				id: 'history'
			}));
		}
	};

	// startSessionRename swaps the .ses-name span for an inline input.
	// Enter and blur commit; Esc cancels. The label is updated locally only.
	function startSessionRename(rowEl, nameEl, sessionKey) {
		var current = nameEl.dataset.title || nameEl.textContent || '';
		var input = document.createElement('input');
		input.className = 'sb-session-edit';
		input.type = 'text';
		input.maxLength = 100;
		input.value = current;
		rowEl.replaceChild(input, nameEl);
		input.focus();
		input.select();

		var settled = false;
		function commit(save) {
			if (settled) return;
			settled = true;
			var newTitle = input.value.trim();
			// Always swap back to a span first so a failed save still has a label to show.
			rowEl.replaceChild(nameEl, input);
			if (save && newTitle !== current) {
				nameEl.textContent = newTitle || sessionKey;
				nameEl.dataset.title = newTitle;
			}
		}
		input.addEventListener('keydown', function(ev) {
			if (ev.key === 'Enter') { ev.preventDefault(); commit(true); }
			else if (ev.key === 'Escape') { ev.preventDefault(); commit(false); }
		});
		// stopPropagation so the parent row's click handler doesn't fire
		// a switchSession on the same gesture.
		input.addEventListener('click', function(ev) { ev.stopPropagation(); });
		input.addEventListener('blur', function() { commit(true); });
	}
	function clearSessionByKey(key) {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'session.clear',
			params: { agentId: agentSelect.value, sessionKey: key },
			id: 'clear-' + key
		}));
		// If we just cleared the active session, also clear the
		// transcript view + token chip locally so the user sees the
		// effect immediately (the server reply will also refresh).
		if (key === sessionSelect.value) {
			messagesEl.innerHTML = '';
			currentAssistant = null;
			toolEls = {};
			resetTokenChip();
			refreshEmptyState();
		}
		loadSessions();
	}

	function highlightActiveSession() {
		var rows = sessionsList.querySelectorAll('.session-row');
		for (var i = 0; i < rows.length; i++) {
			rows[i].classList.toggle('active', rows[i].dataset.sessionKey === sessionSelect.value);
		}
	}

	if (sessionsNewBtn) sessionsNewBtn.addEventListener('click', function() { doNewSession(); });

	applySessionsVisibility();

	function fmtMs(n) {
		if (n == null) return '';
		if (n < 1000) return n + 'ms';
		return (n / 1000).toFixed(1) + 's';
	}


	// Token chip: always visible in the header. Shows the most recent
	// turn's input usage vs the configured context window (so the user
	// always knows how much room they have left), plus output tokens
	// from the last turn. Before any turn fires we show "—/window +—"
	// as a stable baseline. agentWindows maps agentId → context window
	// in tokens; populated from agent.status so switching agents
	// updates the chip even before the first turn on that agent runs.
	var tokenChip = document.getElementById('token-chip');
	var agentWindows = {};
	var lastUsage = null;
	function fmtTokens(n) {
		if (n == null) return '·'; /* middle dot, not em dash */
		if (n < 1000) return String(n);
		if (n < 1000000) return (n / 1000).toFixed(n < 10000 ? 1 : 0) + 'K';
		return (n / 1000000).toFixed(2) + 'M';
	}
	function currentWindow() {
		var id = agentSelect && agentSelect.value;
		return (id && agentWindows[id]) || 0;
	}
	function renderTokenChip() {
		if (!tokenChip) return;
		var ctxWindow = currentWindow();
		if (lastUsage) {
			var inTok = (lastUsage.input_tokens || 0) +
				(lastUsage.cache_creation_input_tokens || 0) +
				(lastUsage.cache_read_input_tokens || 0);
			var outTok = lastUsage.output_tokens || 0;
			var pct = ctxWindow > 0 ? (inTok / ctxWindow) * 100 : 0;
			var remaining = ctxWindow > 0 ? Math.max(0, ctxWindow - inTok) : 0;
			tokenChip.textContent = fmtTokens(inTok) +
				(ctxWindow > 0 ? '/' + fmtTokens(ctxWindow) : '') +
				(ctxWindow > 0 ? ' (' + pct.toFixed(0) + '%%)' : '') +
				'  +' + fmtTokens(outTok);
			tokenChip.title = 'Last turn input=' + inTok + ', output=' + outTok +
				(ctxWindow > 0 ? ', remaining=' + remaining + ' / window=' + ctxWindow : '');
			tokenChip.classList.remove('warn', 'danger');
			if (pct >= 80) tokenChip.classList.add('danger');
			else if (pct >= 60) tokenChip.classList.add('warn');
		} else {
			// No turn data yet: show baseline so the user still sees
			// the context window of the selected agent.
			tokenChip.textContent = '·' +
				(ctxWindow > 0 ? '/' + fmtTokens(ctxWindow) : '') +
				'  +·';
			tokenChip.title = ctxWindow > 0
				? 'Context window: ' + ctxWindow + ' tokens (no turns yet)'
				: 'Context window unknown';
			tokenChip.classList.remove('warn', 'danger');
		}
	}
	function updateTokenChip(usage /*ctxWindow,model unused; we read from agentWindows*/) {
		if (!usage) return;
		lastUsage = usage;
		renderTokenChip();
	}
	function resetTokenChip() {
		// Called on session switch / new / clear — last-turn usage no
		// longer reflects this session, so drop back to the baseline.
		lastUsage = null;
		renderTokenChip();
	}
	function refreshEmptyState() {
		if (!messagesEl) return;
		var hasMsg = messagesEl.querySelector('.msg') != null;
		var emp = document.getElementById('chat-empty-state');
		if (hasMsg) {
			if (emp) emp.style.display = 'none';
			return;
		}
		if (!emp) {
			emp = document.createElement('div');
			emp.id = 'chat-empty-state';
			emp.className = 'chat-empty';
			var t = document.createElement('div');
			t.className = 'chat-empty-title';
			t.textContent = 'Felix is ready';
			var b = document.createElement('div');
			b.className = 'chat-empty-body';
			b.textContent = 'Ask anything, or drop files anywhere to attach.';
			emp.appendChild(t);
			emp.appendChild(b);
			messagesEl.appendChild(emp);
		} else {
			emp.style.display = '';
		}
	}
	// Initial paint so the chip isn't blank before agent.status arrives.
	renderTokenChip();


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

	// Theme — light is the default (matches the auth pages); dark is opt-in
	// via the .dark class. Menu label shows what clicking will switch TO.
	function setTheme(mode) {
		if (mode === 'dark') {
			document.documentElement.classList.add('dark');
		} else {
			document.documentElement.classList.remove('dark');
		}
		if (menuTheme) menuTheme.textContent = (mode === 'dark') ? 'Light theme' : 'Dark theme';
		localStorage.setItem('felix-theme', mode);
	}

	var saved = localStorage.getItem('felix-theme') || 'light';
	setTheme(saved);

	function toggleTheme() {
		var current = document.documentElement.classList.contains('dark') ? 'dark' : 'light';
		setTheme(current === 'dark' ? 'light' : 'dark');
	}

	function doClearSession() {
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
		resetTokenChip();
		refreshEmptyState();
		loadSessions();
	}

	agentSelect.addEventListener('change', function() {
		messagesEl.innerHTML = '';
		currentAssistant = null;
		toolEls = {};
		resetTokenChip();
		refreshEmptyState();
		// If the Files iframe panel is currently visible, reload it so it
		// shows the new agent's workspace.
		var filesIframe = document.getElementById('embed-frame');
		if (filesIframe && filesIframe.src && filesIframe.src.indexOf('/files') !== -1) {
			filesIframe.src = '/files?agent=' + encodeURIComponent(agentSelect.value);
		}
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		// Load sessions for the new agent
		loadSessions();
		filesCwd = '';
		clearSelection();
		if (filesVisible) loadFiles();
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
		resetTokenChip();
		refreshEmptyState();
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'session.history',
			params: { agentId: agentSelect.value, sessionKey: sessionSelect.value },
			id: 'history'
		}));
	});

	function doNewSession() {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'session.new',
			params: { agentId: agentSelect.value, name: '' },
			id: 'session-new'
		}));
	}

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
			connStatus.className = 'status-dot ok';
			connStatus.title = 'connected';
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
			connStatus.className = 'status-dot connecting';
			connStatus.title = 'reconnecting...';
			sendBtn.disabled = true;
			sending = false;
			reconnectTimer = setTimeout(connect, 3000);
		};

		ws.onerror = function() {
			connStatus.className = 'status-dot error';
			connStatus.title = 'connection error';
		};

		ws.onmessage = function(e) {
			try {
				var resp = JSON.parse(e.data);
				if (resp.error) {
					// Errors on Wave 2 RPCs (runs-/del-/replay-) are scoped to
					// the sublist or replay flow and must not lock the chat
					// composer. Surface them in-place; the chat send path is
					// unaffected.
					if (typeof resp.id === 'string' &&
						(resp.id.indexOf('runs-') === 0 ||
						 resp.id.indexOf('del-') === 0 ||
						 resp.id.indexOf('replay-') === 0)) {
						var emsg = (typeof resp.error === 'string') ? resp.error :
							(resp.error.message || JSON.stringify(resp.error));
						if (resp.id.indexOf('runs-') === 0) {
							var ekey = resp.id.slice('runs-'.length);
							var eentry = runsBySession.get(ekey) || { runs: [], expanded: false, loading: false };
							eentry.loading = false;
							runsBySession.set(ekey, eentry);
							renderRunsSublistFor(ekey);
							console.error('chat.runs error:', emsg);
						} else if (resp.id.indexOf('del-') === 0) {
							alert('Delete failed: ' + emsg);
						} else {
							alert('Replay failed: ' + emsg);
							replayState = null;
						}
						return;
					}
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
					if (window.__syncSbAgentOptions) window.__syncSbAgentOptions();
					renderTokenChip();
					if (agents.length === 0) {
						// Most common first-run failure: gateway is reachable
						// but no agent is configured. Show a guided empty
						// state instead of silently appending a "default"
						// option that has no provider behind it.
						showNoAgentEmptyState();
						return;
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
						opt.value = 'default';
						opt.textContent = 'default (0)';
						sessionSelect.appendChild(opt);
						sessions = [{ key: 'default', entryCount: 0, active: true }];
					}
					renderSessions(sessions);
					// Load history for the active session
					messagesEl.innerHTML = '';
					currentAssistant = null;
					toolEls = {};
					refreshEmptyState();
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

				// Handle chat.runs response (Wave 2). Result shape: { runs: [RunSummary] }.
				if (typeof resp.id === 'string' && resp.id.indexOf('runs-') === 0) {
					var rkey = resp.id.slice('runs-'.length);
					var rentry = runsBySession.get(rkey) || { runs: [], expanded: false, loading: false };
					rentry.loading = false;
					if (resp.result && Array.isArray(resp.result.runs)) {
						rentry.runs = resp.result.runs;
					}
					runsBySession.set(rkey, rentry);
					renderRunsSublistFor(rkey);
					return;
				}

				// Handle chat.deleteRun response (Wave 2). On success, optimistically
				// remove the row from every cached sublist that contains it; on
				// failure the dispatcher's top-level error branch already alerted.
				if (typeof resp.id === 'string' && resp.id.indexOf('del-') === 0) {
					var delRunId = resp.id.slice('del-'.length);
					var iter = runsBySession.entries();
					var step = iter.next();
					while (!step.done) {
						var pair = step.value;
						var pkey = pair[0], pentry = pair[1];
						var idx = -1;
						for (var p = 0; p < pentry.runs.length; p++) {
							if (pentry.runs[p].id === delRunId) { idx = p; break; }
						}
						if (idx >= 0) {
							pentry.runs.splice(idx, 1);
							renderRunsSublistFor(pkey);
						}
						step = iter.next();
					}
					return;
				}

				// Handle chat.replay response (Wave 2). Result shape: { runId, past: [event] }.
				if (typeof resp.id === 'string' && resp.id.indexOf('replay-') === 0) {
					if (!replayState) return;
					var past = (resp.result && Array.isArray(resp.result.past)) ? resp.result.past : [];
					replayState.events = past;
					renderReplayMode();
					return;
				}

				var r = resp.result;

				switch (r.type) {
				case 'run_attached':
					// Track the in-flight runId for the current scope so the
					// runs sublist hides the delete button next to this row.
					if (r.runID && agentSelect && agentSelect.value && sessionSelect && sessionSelect.value) {
						liveRunIdBySession.set(runsKey(agentSelect.value, sessionSelect.value), r.runID);
					}
					break;
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
					updateToolResult(r.tool, r.id, r.input, r.output, r.error, r.images, r.auth_required);
					break;
				case 'done':
					if (currentAssistant) {
						finalizeAssistant();
					}
					currentAssistant = null;
					sending = false;
					updateSendBtn();
					if (r.context_window && agentSelect && agentSelect.value) {
					// Per-turn context_window is the freshest server value
					// (config hot-reload could have changed it since
					// agent.status fired). Cache it back so subsequent
					// renders stay consistent.
					agentWindows[agentSelect.value] = r.context_window;
				}
				updateTokenChip(r.usage);
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
				case 'compaction.start':
					if (!currentAssistant) {
						currentAssistant = addAssistantMsg('');
					}
					appendToAssistant('\n*[Compacting context…]*\n');
					break;
				case 'compaction.done':
					if (currentAssistant) {
						appendToAssistant('\n*[Context compacted.]*\n');
					}
					break;
				case 'compaction.skipped':
					break;
				case 'trace':
					addTraceRow(r);
					break;
				case 'replay_done':
					// Boundary marker between gap-fill and (if live:true) live stream.
					// Nothing visual — the message rendering already handled the
					// historical events. If r.live is false, the run is no longer
					// in flight, so release the sending lock.
					if (!r.live) {
						sending = false;
						updateSendBtn();
					}
					break;
				case 'run_terminal':
					// Final lifecycle marker from the runs subsystem. Distinguish
					// from the agent's per-turn 'done' event which has already been
					// processed above. Render a visible marker for non-happy states.
					if (currentAssistant) {
						finalizeAssistant();
						currentAssistant = null;
					}
					sending = false;
					updateSendBtn();
					// Clear the live runId for this scope so the delete button
					// reappears in the runs sublist on next render.
					if (agentSelect && agentSelect.value && sessionSelect && sessionSelect.value) {
						liveRunIdBySession.delete(runsKey(agentSelect.value, sessionSelect.value));
					}
					if (r.status === 'completed') {
						// Happy path — no marker needed (the 'done' event already
						// finalized the assistant message).
						break;
					}
					var marker = '';
					switch (r.status) {
					case 'cancelled':
						marker = r.reason === 'superseded' ? '↳ replaced by next turn' : '⏹ cancelled';
						break;
					case 'interrupted':
						marker = '↯ interrupted by server restart';
						break;
					case 'failed':
						marker = '⚠ failed' + (r.error ? ': ' + r.error : '');
						break;
					default:
						marker = '— ' + r.status;
					}
					addSystemMarker(marker);
					break;
				}
			} catch(err) {
				console.error('parse error:', err);
			}
		};
	}

	function showNoAgentEmptyState() {
		messagesEl.innerHTML = '';
		var card = document.createElement('div');
		card.className = 'empty-state';
		var h = document.createElement('h2');
		h.textContent = 'Welcome to Felix';
		var p = document.createElement('p');
		p.textContent = 'No agent configured yet. Set one up in Settings to start chatting.';
		var cta = document.createElement('a');
		cta.className = 'empty-cta';
		cta.href = '/settings#agents';
		cta.textContent = 'Configure an agent →';
		card.appendChild(h);
		card.appendChild(p);
		card.appendChild(cta);
		messagesEl.appendChild(card);
		sendBtn.disabled = true;
		inputEl.placeholder = 'Configure an agent first';
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
		header.innerHTML = '<span class="arrow">' + icon('chevron') + '</span> ' + toolSummary(toolName, input);
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

	function updateToolResult(toolName, toolId, input, outputText, errorText, images, authRequired) {
		var el = toolEls[toolId] || toolEls[toolName];
		if (!el) return;

		// Update header with input if we now have it
		if (input) {
			var header = el.querySelector('.tool-call-header');
			if (header) {
				var arrow = header.querySelector('.arrow');
				var isOpen = arrow && arrow.classList.contains('open');
				header.innerHTML = '<span class="arrow' + (isOpen ? ' open' : '') + '">' + icon('chevron') + '</span> ' + toolSummary(toolName, input);
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
				img.alt = 'Tool screenshot';
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

		// Inline "Re-authenticate" button when the MCP adapter flagged
		// this result as auth-failure. POSTs to /api/mcp/reauth/{id};
		// the user's browser opens the IdP, the gateway loopback
		// listener catches the callback, the manager reconnects, and
		// the button is replaced with a status line. No restart needed.
		if (authRequired) {
			var existing = el.querySelector('.mcp-reauth');
			if (existing) existing.remove();
			var box = document.createElement('div');
			box.className = 'mcp-reauth';
			var msg = document.createElement('span');
			msg.textContent = 'MCP server "' + authRequired + '" needs re-authentication.';
			var btn = document.createElement('button');
			btn.type = 'button';
			btn.textContent = 'Re-authenticate';
			btn.onclick = function() {
				btn.disabled = true;
				btn.textContent = 'Opening browser…';
				fetch('/api/mcp/reauth/' + encodeURIComponent(authRequired), { method: 'POST' })
					.then(function(r) { return r.json().catch(function() { return {ok: false, error: 'invalid response'}; }); })
					.then(function(j) {
						if (j && j.ok) {
							msg.textContent = 'Re-authenticated. Retry your last message.';
							if (j.warning) {
								msg.textContent += ' Note: ' + j.warning;
							}
							btn.remove();
							box.classList.add('ok');
						} else {
							btn.disabled = false;
							btn.textContent = 'Re-authenticate';
							msg.textContent = 'Re-auth failed: ' + (j && j.error ? j.error : 'unknown error');
							box.classList.add('error');
						}
					})
					.catch(function(e) {
						btn.disabled = false;
						btn.textContent = 'Re-authenticate';
						msg.textContent = 'Re-auth request failed: ' + e;
						box.classList.add('error');
					});
			};
			box.appendChild(msg);
			box.appendChild(btn);
			// Append after the output area, inside the tool element so
			// it appears right under the failed call.
			el.appendChild(box);
			// Auto-expand the output so the user sees the button.
			output.classList.add('show');
			var arrow2 = el.querySelector('.arrow');
			if (arrow2) arrow2.classList.add('open');
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
				suggest: 'Wait a minute and try again.',
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
		// Upstream provider unreachable.
		if (s.indexOf('connection refused') >= 0 || s.indexOf('eof') >= 0) {
			return {
				title: 'Model provider is unreachable',
				suggest: 'The configured LLM provider did not respond. Check Settings → Providers and verify the gateway URL.',
				settings: 'providers'
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

	function addSystemMarker(text) {
		var el = document.createElement('div');
		el.className = 'system-marker';
		el.textContent = text;
		messagesEl.appendChild(el);
		scrollToBottom();
	}

	function addError(msg) {
		var f = friendlyError(msg);
		var div = document.createElement('div');
		div.className = 'msg assistant';
		div.style.borderColor = 'var(--error)';
		var html = '<div class="content" style="color:var(--error)">' +
			'<strong>' + escHtml(f.title) + '</strong>';
		if (f.suggest) {
			html += '<div style="margin-top:0.4rem; color:var(--text); font-size:var(--fs-sm);">' +
				escHtml(f.suggest) + '</div>';
		}
		if (f.settings) {
			html += '<div style="margin-top:0.4rem;">' +
				'<a href="/settings#' + escHtml(f.settings) + '" style="color:var(--accent); text-decoration:none; font-size:var(--fs-sm);">' +
				'Open Settings &rarr;</a></div>';
		}
		// Always include the raw message in a folded details so power users
		// can still see what actually broke.
		html += '<details style="margin-top:0.5rem; color:var(--text-muted); font-size:var(--fs-xs);">' +
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
			stopBtn.style.display = 'flex';
		} else {
			sendBtn.style.display = 'flex';
			stopBtn.style.display = 'none';
			sendBtn.disabled = !ws || ws.readyState !== WebSocket.OPEN;
		}
	}

	// ===== Inline attachments =====
	// Files queued via attach button or drag-drop. Each entry: {file, name, status, path}.
	// status: 'pending' | 'uploading' | 'done' | 'failed'. path is set on 'done'.
	var attachments = [];
	var attachBtn = document.getElementById('attach-btn');
	var attachInput = document.getElementById('attach-input');
	var attachRow = document.getElementById('attach-row');
	var dropOverlay = document.getElementById('drop-overlay');

	// Size thresholds. Hard limit mirrors internal/gateway/files.go
	// maxUploadBytes — bump both together if the server cap changes.
	var ATTACH_SOFT_WARN_BYTES = 5 * 1024 * 1024;
	var ATTACH_HARD_LIMIT_BYTES = 100 * 1024 * 1024;

	function renderAttachments() {
		if (attachments.length === 0) {
			attachRow.hidden = true;
			attachRow.innerHTML = '';
			updateSendBtn();
			return;
		}
		attachRow.hidden = false;
		attachRow.innerHTML = '';
		attachments.forEach(function(a) {
			var isEmpty = a.file.size === 0;
			var isLarge = a.file.size > ATTACH_SOFT_WARN_BYTES;
			var chip = document.createElement('span');
			chip.className = 'attach-chip' +
				(a.status === 'uploading' ? ' uploading' : '') +
				(a.status === 'failed' ? ' failed' : '') +
				(isEmpty ? ' empty' : '') +
				(isLarge ? ' large' : '');
			var label = a.status === 'uploading' ? '↑ ' : (a.status === 'failed' ? '! ' : '');
			chip.innerHTML = '<span class="chip-name" title="' + escapeHtml(a.name) + '">' + escapeHtml(label + a.name) + '</span>' +
				'<span class="chip-size">' + fmtBytes(a.file.size) + '</span>' +
				(isEmpty ? '<span class="chip-warn" title="This file is empty.">⚠ empty</span>' : '') +
				(isLarge ? '<span class="chip-warn" title="Large file — slower upload.">⚠ large</span>' : '') +
				'<button class="chip-x" type="button" aria-label="Remove">&times;</button>';
			// Identity-based removal: each attachment has a stable id so
			// concurrent × clicks don't race on array indices.
			var thisId = a.id;
			chip.querySelector('.chip-x').addEventListener('click', function() {
				attachments = attachments.filter(function(x) { return x.id !== thisId; });
				renderAttachments();
			});
			attachRow.appendChild(chip);
		});
		updateSendBtn();
	}

	function fmtBytes(n) {
		if (n == null) return '';
		if (n < 1024) return n + ' B';
		var u = ['KB','MB','GB'];
		var i = -1;
		do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
		return n.toFixed(1) + ' ' + u[i];
	}
	function escapeHtml(s) {
		return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
	}
	// Slug filenames so they survive resolveAgentPath's checks and don't
	// collide on retry — prefix with millisecond timestamp inside uploads/.
	function sanitizeName(name) {
		var base = name.replace(/[^A-Za-z0-9._-]+/g, '_');
		// Strip leading dots (dotfiles rejected by backend).
		base = base.replace(/^\.+/, '');
		if (!base) base = 'file';
		return base;
	}

	var attachmentSeq = 0;
	var riskyExts = ['sh', 'exe', 'bat', 'cmd', 'ps1', 'zip', 'rar', '7z', 'tar', 'gz', 'jar'];
	function isRiskyExt(name) {
		var m = /\.([^.]+)$/.exec(name || '');
		if (!m) return false;
		return riskyExts.indexOf(m[1].toLowerCase()) !== -1;
	}

	function queueFiles(fileList) {
		for (var i = 0; i < fileList.length; i++) {
			var f = fileList[i];
			if (f.size > ATTACH_HARD_LIMIT_BYTES) {
				alert('"' + f.name + '" is ' + fmtBytes(f.size) +
					', above the 100 MB upload limit. Skipped.');
				continue;
			}
			if (isRiskyExt(f.name)) {
				// Drag-drop bypasses accept= so confirm before adding
				// executable/archive types to the agent workspace.
				if (!window.confirm('Attaching "' + f.name +
					'" — its contents will be readable by the agent. Continue?')) {
					continue;
				}
			}
			attachmentSeq++;
			attachments.push({
				id: 'att-' + Date.now() + '-' + attachmentSeq,
				file: f,
				name: f.name,
				status: 'pending',
				path: null,
			});
		}
		renderAttachments();
	}

	attachBtn.addEventListener('click', function() { attachInput.click(); });
	attachInput.addEventListener('change', function() {
		if (attachInput.files && attachInput.files.length) {
			queueFiles(attachInput.files);
			attachInput.value = '';
		}
	});

	// Drag-drop anywhere on the document. The dragenter/dragleave counter
	// pattern desyncs when the drag exits the window without a drop (events
	// arrive for unrelated child elements). Instead, debounce against
	// dragover: the browser fires it continuously while the drag is over
	// the window; the moment it stops firing for ~150ms we hide.
	var dragHideTimer = null;
	function isFileDrag(e) {
		return e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types, 'Files') >= 0;
	}
	function hideOverlay() {
		dropOverlay.hidden = true;
		if (dragHideTimer) { clearTimeout(dragHideTimer); dragHideTimer = null; }
	}
	document.addEventListener('dragover', function(e) {
		if (!isFileDrag(e)) return;
		e.preventDefault();
		dropOverlay.hidden = false;
		if (dragHideTimer) clearTimeout(dragHideTimer);
		dragHideTimer = setTimeout(hideOverlay, 150);
	});
	document.addEventListener('drop', function(e) {
		if (!isFileDrag(e)) return;
		e.preventDefault();
		hideOverlay();
		if (e.dataTransfer.files && e.dataTransfer.files.length) {
			queueFiles(e.dataTransfer.files);
		}
	});
	// Belt-and-suspenders: clicking or pressing any key should also kill a
	// stuck overlay (covers tab-switch-during-drag and other browser oddities).
	document.addEventListener('keydown', function() { if (!dropOverlay.hidden) hideOverlay(); });
	dropOverlay.addEventListener('click', hideOverlay);

	function uploadOne(a, dir) {
		var fd = new FormData();
		// Send with the sanitized name so the on-disk path is predictable
		// and matches what we'll cite in the message body.
		var safe = sanitizeName(a.name);
		fd.append('file', a.file, safe);
		a.path = dir + '/' + safe;
		a.status = 'uploading';
		renderAttachments();
		return fetch('/files/upload?agent=' + encodeURIComponent(agentSelect.value) +
				'&dir=' + encodeURIComponent(dir), { method: 'POST', body: fd })
			.then(function(r) {
				if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
				a.status = 'done';
			})
			.catch(function(err) {
				a.status = 'failed';
				a.error = err.message;
				throw err;
			});
	}

	function uploadAllAttachments() {
		var pending = attachments.filter(function(a) { return a.status === 'pending' || a.status === 'failed'; });
		if (pending.length === 0) return Promise.resolve();
		// Single timestamped dir per send so file paths in the prompt are tidy.
		var stamp = new Date().toISOString().replace(/[:.]/g, '-');
		var dir = 'uploads/' + stamp;
		return Promise.all(pending.map(function(a) {
			return uploadOne(a, dir).catch(function() { /* swallow; status already 'failed' */ });
		})).then(function() {
			renderAttachments();
			var failures = attachments.filter(function(a) { return a.status === 'failed'; });
			if (failures.length) {
				throw new Error(failures.length + ' upload(s) failed: ' + failures.map(function(a) { return a.name; }).join(', '));
			}
		});
	}

	function sendMessage() {
		var text = inputEl.value.trim();
		if (sending) return;
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		if (!text && attachments.length === 0) return;

		sending = true;
		updateSendBtn();

		uploadAllAttachments().then(function() {
			// Prepend attachment paths so the agent knows where to find them.
			var preamble = '';
			if (attachments.length) {
				preamble = '[Attached files (read with read_file):\n' +
					attachments.map(function(a) { return '- ' + a.path; }).join('\n') +
					'\n]\n\n';
			}
			var fullText = preamble + text;

			addUserMsg(fullText);
			msgId++;
			ws.send(JSON.stringify({
				jsonrpc: '2.0',
				method: 'chat.send',
				params: { agentId: agentSelect.value, text: fullText, sessionKey: sessionSelect.value },
				id: msgId
			}));

			attachments = [];
			renderAttachments();
			inputEl.value = '';
			inputEl.style.height = 'auto';
		}).catch(function(err) {
			sending = false;
			updateSendBtn();
			alert('Upload failed: ' + err.message + '\n\nClick the × on any failed chip to remove it, then try again.');
		});
	}

	sendBtn.addEventListener('click', sendMessage);

	stopBtn.addEventListener('click', function() {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: 'chat.abort',
			params: { agentId: agentSelect.value, sessionKey: sessionSelect.value },
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

	// --- New shell wiring (sidebar, topbar mirrors, view router) ---
	// The agent-select, status-dot, and token-chip in the legacy hidden
	// header remain the source of truth for app state. We mirror their
	// state into the new #topbar* elements so the rest of the JS doesn't
	// need to know about the new shell. MutationObservers keep the copy
	// in sync without touching the render code paths.
	(function() {
		var sbAgentSelect   = document.getElementById('sb-agent-select');
		var topbarStatusDot = document.getElementById('topbar-status-dot');
		var topbarTokenChip = document.getElementById('topbar-token-chip');
		var statusText      = document.getElementById('status-text');
		var statusPill      = document.getElementById('status-pill');

		// Mirror status dot class + label.
		function syncStatus() {
			var cls = connStatus ? connStatus.className : 'status-dot connecting';
			var state = /\bok\b/.test(cls) ? 'ok' : /\berror\b/.test(cls) ? 'error' : 'connecting';
			if (topbarStatusDot) topbarStatusDot.className = 'status-dot ' + state;
			if (statusText) {
				statusText.textContent = state === 'ok' ? 'Connected'
					: state === 'error' ? 'Disconnected' : 'Connecting';
			}
			if (statusPill && connStatus) statusPill.title = connStatus.title || '';
		}
		new MutationObserver(syncStatus).observe(connStatus, { attributes: true, attributeFilter: ['class', 'title'] });
		syncStatus();

		// Mirror token chip.
		var srcChip = document.getElementById('token-chip');
		function syncToken() {
			if (!srcChip || !topbarTokenChip) return;
			topbarTokenChip.textContent = srcChip.textContent;
			topbarTokenChip.className = srcChip.className.replace('token-chip', '');
			topbarTokenChip.id = 'topbar-token-chip';
			topbarTokenChip.classList.remove('token-chip');
			// Re-apply token-chip styling tokens via class so it matches.
			['warn', 'danger'].forEach(function(c) {
				topbarTokenChip.classList.toggle(c, srcChip.classList.contains(c));
			});
		}
		new MutationObserver(syncToken).observe(srcChip, { childList: true, characterData: true, subtree: true, attributes: true });
		syncToken();

		// Mirror agent-select options + bidirectional value sync into the
		// sidebar #sb-agent-select. The hidden #agent-select stays the JS
		// source of truth so existing consumer code keeps working.
		function syncSbAgentOptions() {
			if (!sbAgentSelect) return;
			sbAgentSelect.innerHTML = '';
			for (var i = 0; i < agentSelect.options.length; i++) {
				var src = agentSelect.options[i];
				var opt = document.createElement('option');
				opt.value = src.value;
				opt.textContent = src.textContent;
				if (src.value === agentSelect.value) opt.selected = true;
				sbAgentSelect.appendChild(opt);
			}
			sbAgentSelect.value = agentSelect.value;
		}
		// Expose so populate sites can refresh after rewriting the hidden select.
		window.__syncSbAgentOptions = syncSbAgentOptions;
		new MutationObserver(syncSbAgentOptions).observe(agentSelect, { childList: true, attributes: true });
		if (sbAgentSelect) {
			sbAgentSelect.addEventListener('change', function() {
				if (agentSelect.value !== sbAgentSelect.value) {
					agentSelect.value = sbAgentSelect.value;
					agentSelect.dispatchEvent(new Event('change'));
				}
			});
		}
		agentSelect.addEventListener('change', function() {
			if (sbAgentSelect && sbAgentSelect.value !== agentSelect.value) sbAgentSelect.value = agentSelect.value;
		});
		syncSbAgentOptions();

		// Sidebar collapse, persisted. On mobile (≤820px) .collapsed slides
		// the rail off-screen entirely, so the topbar menu button and the
		// backdrop are the only ways back. On desktop they're hidden via
		// CSS and the in-sidebar collapse button does all the work.
		var sidebar = document.getElementById('sidebar');
		var sbCollapse = document.getElementById('sb-collapse');
		var topbarMenu = document.getElementById('topbar-menu');
		var sidebarBackdrop = document.getElementById('sidebar-backdrop');
		var collapsed = localStorage.getItem('felix-sb-collapsed') === 'true';
		function applyCollapsed() {
			sidebar.classList.toggle('collapsed', collapsed);
			sidebar.classList.toggle('expanded', !collapsed);
			sbCollapse.setAttribute('aria-label', collapsed ? 'Expand sidebar' : 'Collapse sidebar');
			sbCollapse.setAttribute('title', collapsed ? 'Expand sidebar' : 'Collapse sidebar');
		}
		function setCollapsed(v, persist) {
			collapsed = v;
			if (persist) localStorage.setItem('felix-sb-collapsed', String(collapsed));
			applyCollapsed();
		}
		applyCollapsed();
		sbCollapse.addEventListener('click', function() { setCollapsed(!collapsed, true); });
		if (topbarMenu) {
			topbarMenu.addEventListener('click', function() { setCollapsed(!collapsed, false); });
		}
		if (sidebarBackdrop) {
			sidebarBackdrop.addEventListener('click', function() { setCollapsed(true, false); });
		}

		// "+ New session" in sidebar = doNewSession.
		document.getElementById('sb-new').addEventListener('click', function() { doNewSession(); });

		// View router: chat (default) vs embed (settings/jobs/logs).
		var chatView = document.getElementById('chat-view');
		var embedView = document.getElementById('embed-view');
		var embedFrame = document.getElementById('embed-frame');
		var currentView = 'chat';
		function setView(name) {
			currentView = name;
			var sidebarItems = document.querySelectorAll('#sb-tools .sb-item[data-view]');
			for (var i = 0; i < sidebarItems.length; i++) {
				sidebarItems[i].classList.toggle('active', sidebarItems[i].dataset.view === name);
			}
			if (name === 'chat') {
				chatView.hidden = false;
				embedView.hidden = true;
				embedFrame.src = 'about:blank';
				document.title = 'Felix';
			} else {
				var routes = { files: '/files', settings: '/settings', jobs: '/jobs', logs: '/logs' };
				var labels = { files: 'Files', settings: 'Settings', jobs: 'Jobs', logs: 'Logs' };
				var url = routes[name];
				if (!url) return;
				if (name === 'files' && agentSelect.value) {
					url += '?agent=' + encodeURIComponent(agentSelect.value);
				}
				embedFrame.src = url;
				embedFrame.title = labels[name];
				chatView.hidden = true;
				embedView.hidden = false;
				document.title = labels[name] + ' · Felix';
			}
		}

		// Sidebar tools: re-clicking the active item toggles back to chat;
		// otherwise route into the embed pane. Selecting a session returns
		// to chat (the sessions-list capture handler below).
		document.getElementById('sb-tools').addEventListener('click', function(e) {
			var btn = e.target.closest('.sb-item');
			if (!btn || !btn.dataset.view) return;
			setView(btn.dataset.view === currentView ? 'chat' : btn.dataset.view);
		});
		// Top-right view toggles (Tool calls, Live trace). Both flip the
		// visibility of an inline section of the chat surface, so route
		// back to chat view first if the user is on a Settings/Jobs/Logs
		// embed.
		document.getElementById('topbar-toggles').addEventListener('click', function(e) {
			var btn = e.target.closest('.tb-toggle[data-toggle]');
			if (!btn) return;
			if (currentView !== 'chat') setView('chat');
			switch (btn.dataset.toggle) {
				case 'tools': toggleTools(); break;
				case 'trace': toggleTrace(); break;
			}
		});
		document.getElementById('sessions-list').addEventListener('click', function() {
			// renderSessions installs its own click handler per row; here
			// we only need to route the view back to chat when a session
			// is selected while the user is on settings/jobs/logs.
			if (currentView !== 'chat') setView('chat');
		}, true);

		// Theme toggle button (replaces the menu item).
		var themeIcon = document.getElementById('theme-icon');
		function updateThemeIcon() {
			var isDark = document.documentElement.classList.contains('dark');
			// Show sun in dark mode (click to go light), moon in light mode.
			themeIcon.innerHTML = isDark
				? '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/>'
				: '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>';
		}
		updateThemeIcon();
		document.getElementById('theme-toggle').addEventListener('click', function() {
			toggleTheme();
			updateThemeIcon();
		});
		new MutationObserver(updateThemeIcon).observe(document.documentElement, { attributes: true, attributeFilter: ['class'] });

		// Stop button: keep it inside the input area, just toggle its
		// display when sending. The old code did this via .style.display
		// on the legacy pill; the new circular button still respects the
		// same property.
		var stopBtn = document.getElementById('stop-btn');
		var sendBtn = document.getElementById('send-btn');
		var sendObserver = new MutationObserver(function() {
			// When sendBtn is disabled (an active turn is in flight), the
			// app shows Stop instead of Send.
			var sending = sendBtn.disabled && document.querySelector('.msg.assistant:last-child');
			if (sending) {
				sendBtn.style.display = 'none';
				stopBtn.style.display = 'flex';
			} else {
				sendBtn.style.display = 'flex';
				stopBtn.style.display = 'none';
			}
		});
		sendObserver.observe(sendBtn, { attributes: true, attributeFilter: ['disabled'] });
	})();

	connect();
})();
</script>
</body>
</html>`
