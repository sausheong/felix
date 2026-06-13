package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// secretScrubbers are applied to every log message + attribute before the
// record enters the ring buffer and SSE fan-out. Best-effort defense in depth:
// the canonical fix is to not log secrets, but these close the known leak
// shapes (provider auth errors, MCP stderr, Telegram bot-token URLs).
var secretScrubbers = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)bearer\s+\S+`), "Bearer [REDACTED]"},
	{regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)(\s*[=:]\s*)\S+`), "${1}${2}[REDACTED]"},
	{regexp.MustCompile(`sk-[A-Za-z0-9]{16,}`), "[REDACTED]"},
	{regexp.MustCompile(`bot\d+:[A-Za-z0-9_-]{30,}`), "bot[REDACTED]"},
	{regexp.MustCompile(`//[^/@\s]+:[^/@\s]+@`), "//[REDACTED]@"},
}

// scrubSecrets returns s with known secret shapes masked.
func scrubSecrets(s string) string {
	for _, sc := range secretScrubbers {
		s = sc.re.ReplaceAllString(s, sc.repl)
	}
	return s
}

// maxSSESubscribers caps concurrent /logs/stream subscribers so an attacker
// can't exhaust memory/goroutines by opening unbounded SSE connections. (N2)
const maxSSESubscribers = 16

// LogEntry is a single captured log record.
type LogEntry struct {
	Time    time.Time
	Level   slog.Level
	Message string
	Attrs   string // pre-formatted key=value pairs
}

// logStore holds the shared mutable state for a LogBuffer and any
// handlers derived from it via WithAttrs/WithGroup.
type logStore struct {
	mu      sync.RWMutex
	entries []LogEntry
	head    int // next write position (circular buffer)
	count   int // number of entries currently stored
	max     int
	subs    map[chan LogEntry]struct{}
}

// LogBuffer captures slog records into a ring buffer and supports
// streaming new entries to subscribers via Server-Sent Events.
type LogBuffer struct {
	store *logStore
	inner slog.Handler
}

// NewLogBuffer creates a log buffer with the given capacity that wraps
// an existing slog handler.
func NewLogBuffer(capacity int, inner slog.Handler) *LogBuffer {
	return &LogBuffer{
		store: &logStore{
			entries: make([]LogEntry, capacity),
			max:     capacity,
			subs:    make(map[chan LogEntry]struct{}),
		},
		inner: inner,
	}
}

// Enabled implements slog.Handler.
func (b *LogBuffer) Enabled(ctx context.Context, level slog.Level) bool {
	return b.inner.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (b *LogBuffer) Handle(ctx context.Context, r slog.Record) error {
	// Format attributes
	attrs := ""
	r.Attrs(func(a slog.Attr) bool {
		if attrs != "" {
			attrs += " "
		}
		attrs += a.Key + "=" + scrubSecrets(a.Value.String())
		return true
	})

	entry := LogEntry{
		Time:    r.Time,
		Level:   r.Level,
		Message: scrubSecrets(r.Message),
		Attrs:   attrs,
	}

	s := b.store
	s.mu.Lock()
	s.entries[s.head] = entry
	s.head = (s.head + 1) % s.max
	if s.count < s.max {
		s.count++
	}
	// Snapshot subscriber channels under the lock; send AFTER releasing so a
	// large/slow subscriber set can't serialize every slog call. (N2)
	subs := make([]chan LogEntry, 0, len(s.subs))
	for ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- entry:
		default:
		}
	}

	// Forward to the original handler
	return b.inner.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (b *LogBuffer) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogBuffer{
		store: b.store,
		inner: b.inner.WithAttrs(attrs),
	}
}

// WithGroup implements slog.Handler.
func (b *LogBuffer) WithGroup(name string) slog.Handler {
	return &LogBuffer{
		store: b.store,
		inner: b.inner.WithGroup(name),
	}
}

// Snapshot returns a copy of all current entries in chronological order.
func (b *LogBuffer) Snapshot() []LogEntry {
	s := b.store
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]LogEntry, s.count)
	start := (s.head - s.count + s.max) % s.max
	for i := 0; i < s.count; i++ {
		out[i] = s.entries[(start+i)%s.max]
	}
	return out
}

// Subscribe returns a channel that receives new log entries, or nil if the
// subscriber cap is reached (caller must handle nil). Call Unsubscribe when done.
func (b *LogBuffer) Subscribe() chan LogEntry {
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subs) >= maxSSESubscribers {
		return nil
	}
	ch := make(chan LogEntry, 64)
	s.subs[ch] = struct{}{}
	return ch
}

// Unsubscribe removes a subscriber channel from the fan-out set.
//
// It deliberately does NOT close(ch). Handle fans out off the lock (it
// snapshots the subscriber channels under s.mu, releases the lock, then does a
// non-blocking send on each), so there are multiple concurrent producers and no
// single owner that can safely close the channel — closing here would race with
// an in-flight send in Handle and panic with "send on closed channel". The
// correct Go pattern with multiple producers is to not close. The channel is
// simply dropped from the map and garbage-collected once the consumer goroutine
// returns. The only consumer (NewLogsStreamHandler) exits via r.Context().Done()
// when the client disconnects, not via channel close, so close is unnecessary.
func (b *LogBuffer) Unsubscribe(ch chan LogEntry) {
	s := b.store
	s.mu.Lock()
	delete(s.subs, ch)
	s.mu.Unlock()
}

// formatEntry formats a log entry as a single text line.
func formatEntry(e LogEntry) string {
	ts := e.Time.Format("15:04:05.000")
	level := e.Level.String()
	line := ts + " " + level + " " + e.Message
	if e.Attrs != "" {
		line += " " + e.Attrs
	}
	return line
}

// NewLogsHandler returns an HTTP handler for the /logs page.
func NewLogsHandler(buf *LogBuffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, logsHTML)
	}
}

// NewLogsStreamHandler returns an SSE handler that streams log entries.
func NewLogsStreamHandler(buf *LogBuffer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Send existing entries
		for _, e := range buf.Snapshot() {
			fmt.Fprintf(w, "data: %s\n\n", formatEntry(e))
		}
		flusher.Flush()

		// Stream new entries
		ch := buf.Subscribe()
		if ch == nil {
			fmt.Fprint(w, ": too many log subscribers, try again later\n\n")
			flusher.Flush()
			return
		}
		defer buf.Unsubscribe(ch)

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case entry, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", formatEntry(entry))
				flusher.Flush()
			}
		}
	}
}

const logsHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Felix Logs</title>
<link rel="icon" type="image/png" href="/favicon.png">
<style>
:root {
	--bg: #1a1a2e;
	--bg-header: #16213e;
	--border: #0f3460;
	--text: #ccc;
	--accent: #16dbaa;
	--accent2: #53a8b6;
	--level-info: #16dbaa;
	--level-warn: #f0ad4e;
	--level-error: #e74c3c;
	--level-debug: #888;
	--ts: #666;
}
html.light {
	--bg: #f5f5f5;
	--bg-header: #ffffff;
	--border: #ddd;
	--text: #333;
	--accent: #0fa888;
	--accent2: #3a7f8c;
	--level-info: #0fa888;
	--level-warn: #c68a00;
	--level-error: #d32f2f;
	--level-debug: #999;
	--ts: #999;
}
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
	font-family: "SF Mono", "Fira Code", Menlo, Consolas, monospace;
	background: var(--bg);
	color: var(--text);
	height: 100vh;
	display: flex;
	flex-direction: column;
	font-size: 0.82rem;
	transition: background 0.3s, color 0.3s;
}
#header {
	background: var(--bg-header);
	padding: 0.6rem 1.25rem;
	border-bottom: 1px solid var(--border);
	display: flex;
	align-items: center;
	gap: 0.75rem;
	flex-shrink: 0;
	transition: background 0.3s, border-color 0.3s;
}
#header h1 { font-size: 1rem; color: var(--accent); font-weight: 600; }
#header .spacer { margin-left: auto; }
#header .status { font-size: 0.75rem; color: var(--ts); }
#theme-btn, #clear-btn, #scroll-btn {
	background: none;
	border: 1px solid var(--border);
	border-radius: 5px;
	padding: 0.25rem 0.5rem;
	cursor: pointer;
	font-size: 0.85rem;
	line-height: 1;
	color: var(--text);
	transition: border-color 0.3s;
}
#theme-btn:hover, #clear-btn:hover, #scroll-btn:hover { border-color: var(--accent); }
#scroll-btn.paused { border-color: var(--level-warn); color: var(--level-warn); }
#logs {
	flex: 1;
	overflow-y: auto;
	padding: 0.5rem 1.25rem;
}
.log-line {
	white-space: pre-wrap;
	word-break: break-all;
	padding: 1px 0;
	line-height: 1.5;
}
.log-line .ts { color: var(--ts); }
.log-line .lvl-INFO { color: var(--level-info); font-weight: 600; }
.log-line .lvl-WARN { color: var(--level-warn); font-weight: 600; }
.log-line .lvl-ERROR { color: var(--level-error); font-weight: 600; }
.log-line .lvl-DEBUG { color: var(--level-debug); }
.log-line .attrs { color: var(--ts); }
#filter-bar {
	background: var(--bg-header);
	padding: 0.4rem 1.25rem;
	border-bottom: 1px solid var(--border);
	display: flex;
	gap: 0.5rem;
	align-items: center;
	flex-shrink: 0;
	transition: background 0.3s, border-color 0.3s;
}
#filter {
	flex: 1;
	background: var(--bg);
	border: 1px solid var(--border);
	border-radius: 5px;
	padding: 0.3rem 0.6rem;
	color: var(--text);
	font-family: inherit;
	font-size: 0.82rem;
	outline: none;
	transition: background 0.3s, border-color 0.3s, color 0.3s;
}
#filter:focus { border-color: var(--accent); }
#filter::placeholder { color: var(--ts); }
.filter-label { color: var(--ts); font-size: 0.78rem; }
</style>
</head>
<body>
<div id="header">
	<h1>Felix Logs</h1>
	<span class="spacer"></span>
	<button id="scroll-btn" title="Toggle auto-scroll">&#8615; Auto</button>
	<button id="clear-btn" title="Clear logs display">Clear</button>
	<button id="theme-btn" title="Toggle light/dark mode">&#9790;</button>
	<span class="status" id="status">connecting...</span>
</div>
<div id="filter-bar">
	<span class="filter-label">Filter:</span>
	<input id="filter" type="text" placeholder="Type to filter log lines...">
</div>
<div id="logs"></div>

<script>
(function() {
	var logsEl = document.getElementById('logs');
	var statusEl = document.getElementById('status');
	var themeBtn = document.getElementById('theme-btn');
	var clearBtn = document.getElementById('clear-btn');
	var scrollBtn = document.getElementById('scroll-btn');
	var filterEl = document.getElementById('filter');
	var autoScroll = true;
	var allLines = [];
	var filterText = '';
	var lineCount = 0;

	// Theme
	function setTheme(mode) {
		if (mode === 'light') {
			document.documentElement.classList.add('light');
			themeBtn.innerHTML = '&#9728;';
		} else {
			document.documentElement.classList.remove('light');
			themeBtn.innerHTML = '&#9790;';
		}
		localStorage.setItem('felix-logs-theme', mode);
	}
	setTheme(localStorage.getItem('felix-logs-theme') || 'dark');
	themeBtn.addEventListener('click', function() {
		var current = document.documentElement.classList.contains('light') ? 'light' : 'dark';
		setTheme(current === 'light' ? 'dark' : 'light');
	});

	// Auto-scroll toggle
	scrollBtn.addEventListener('click', function() {
		autoScroll = !autoScroll;
		scrollBtn.classList.toggle('paused', !autoScroll);
		if (autoScroll) logsEl.scrollTop = logsEl.scrollHeight;
	});

	// Pause auto-scroll on manual scroll up
	logsEl.addEventListener('scroll', function() {
		var atBottom = logsEl.scrollTop + logsEl.clientHeight >= logsEl.scrollHeight - 20;
		if (!atBottom && autoScroll) {
			autoScroll = false;
			scrollBtn.classList.add('paused');
		}
	});

	clearBtn.addEventListener('click', function() {
		allLines = [];
		logsEl.innerHTML = '';
		lineCount = 0;
	});

	// Filter
	filterEl.addEventListener('input', function() {
		filterText = this.value.toLowerCase();
		applyFilter();
	});

	function applyFilter() {
		var els = logsEl.querySelectorAll('.log-line');
		for (var i = 0; i < els.length; i++) {
			var show = !filterText || allLines[i].toLowerCase().indexOf(filterText) !== -1;
			els[i].style.display = show ? '' : 'none';
		}
	}

	function escHtml(s) {
		return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
	}

	function parseLine(raw) {
		// Format: "15:04:05.000 LEVEL message attrs"
		var m = raw.match(/^(\S+)\s+(INFO|WARN|ERROR|DEBUG)\s+(.*)/);
		if (!m) return '<span>' + escHtml(raw) + '</span>';
		var ts = m[1], level = m[2], rest = m[3];
		// Split message from attrs (attrs contain = signs)
		var parts = rest.split(/\s+/);
		var msg = [], attrs = [];
		var inAttrs = false;
		for (var i = 0; i < parts.length; i++) {
			if (!inAttrs && parts[i].indexOf('=') !== -1) inAttrs = true;
			if (inAttrs) attrs.push(parts[i]);
			else msg.push(parts[i]);
		}
		var html = '<span class="ts">' + escHtml(ts) + '</span> ';
		html += '<span class="lvl-' + level + '">' + level + '</span> ';
		html += escHtml(msg.join(' '));
		if (attrs.length > 0) {
			html += ' <span class="attrs">' + escHtml(attrs.join(' ')) + '</span>';
		}
		return html;
	}

	function addLine(raw) {
		allLines.push(raw);
		var div = document.createElement('div');
		div.className = 'log-line';
		div.innerHTML = parseLine(raw);
		if (filterText && raw.toLowerCase().indexOf(filterText) === -1) {
			div.style.display = 'none';
		}
		logsEl.appendChild(div);
		lineCount++;
		// Cap at 5000 visible lines
		if (lineCount > 5000) {
			logsEl.removeChild(logsEl.firstChild);
			allLines.shift();
			lineCount--;
		}
		if (autoScroll) logsEl.scrollTop = logsEl.scrollHeight;
	}

	// SSE connection
	function connect() {
		var es = new EventSource('/logs/stream');
		es.onopen = function() { statusEl.textContent = 'streaming'; };
		es.onmessage = function(e) { addLine(e.data); };
		es.onerror = function() {
			statusEl.textContent = 'disconnected';
			es.close();
			setTimeout(connect, 3000);
		};
	}
	connect();
})();
</script>
</body>
</html>`
