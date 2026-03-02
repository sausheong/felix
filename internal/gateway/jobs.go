package gateway

import (
	"fmt"
	"net/http"
)

// NewJobsHandler returns an HTTP handler that serves the cron jobs management page.
func NewJobsHandler(port int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, jobsHTML, port)
	}
}

const jobsHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoClaw Jobs</title>
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
	--warning: #f39c12;
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
	--warning: #e67e22;
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
	transition: background 0.3s, border-color 0.3s;
}
#header h1 { font-size: 1.1rem; color: var(--accent); }
#header .spacer { margin-left: auto; }
#header .status { font-size: 0.8rem; color: var(--text-muted); }
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
#theme-btn:hover { border-color: var(--accent); }
#content {
	max-width: 900px;
	margin: 1.5rem auto;
	padding: 0 1.5rem;
}
.empty-state {
	text-align: center;
	padding: 3rem 1rem;
	color: var(--text-muted);
	font-size: 0.95rem;
}
.job-card {
	background: var(--bg-card);
	border: 1px solid var(--border);
	border-radius: 10px;
	padding: 1rem 1.25rem;
	margin-bottom: 0.75rem;
	transition: background 0.3s, border-color 0.3s;
}
.job-card.paused { opacity: 0.7; }
.job-header {
	display: flex;
	align-items: center;
	gap: 0.75rem;
	margin-bottom: 0.5rem;
}
.job-name {
	font-weight: 600;
	font-size: 1rem;
	color: var(--text-strong);
}
.job-status {
	font-size: 0.75rem;
	padding: 0.15rem 0.5rem;
	border-radius: 10px;
	font-weight: 600;
	text-transform: uppercase;
	letter-spacing: 0.05em;
}
.job-status.running {
	background: var(--success);
	color: #fff;
}
.job-status.paused-badge {
	background: var(--warning);
	color: #fff;
}
.job-schedule {
	font-size: 0.85rem;
	color: var(--accent2);
	margin-bottom: 0.25rem;
	font-family: "SF Mono", "Fira Code", monospace;
}
.job-prompt {
	font-size: 0.85rem;
	color: var(--text-muted);
	margin-bottom: 0.75rem;
	white-space: pre-wrap;
	word-break: break-word;
}
.job-actions {
	display: flex;
	gap: 0.5rem;
	align-items: center;
	flex-wrap: wrap;
}
.job-actions button {
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.3rem 0.7rem;
	cursor: pointer;
	font-size: 0.8rem;
	color: var(--text);
	transition: border-color 0.2s, color 0.2s;
}
.job-actions button:hover {
	border-color: var(--accent);
	color: var(--accent);
}
.job-actions button.danger:hover {
	border-color: var(--error);
	color: var(--error);
}
.edit-schedule {
	display: none;
	align-items: center;
	gap: 0.4rem;
}
.edit-schedule.show { display: flex; }
.edit-schedule input {
	background: var(--bg-input);
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.3rem 0.5rem;
	color: var(--text);
	font-size: 0.8rem;
	font-family: "SF Mono", "Fira Code", monospace;
	width: 100px;
	outline: none;
	transition: border-color 0.2s;
}
.edit-schedule input:focus { border-color: var(--accent); }
.edit-schedule button {
	background: none;
	border: 1px solid var(--border);
	border-radius: 6px;
	padding: 0.3rem 0.5rem;
	cursor: pointer;
	font-size: 0.75rem;
	color: var(--text);
	transition: border-color 0.2s;
}
.edit-schedule button:hover { border-color: var(--accent); }
.edit-schedule button.save-btn { color: var(--success); border-color: var(--success); }
.edit-schedule button.cancel-btn { color: var(--text-muted); }
</style>
</head>
<body>
<div id="header">
	<h1>GoClaw Jobs</h1>
	<span class="spacer"></span>
	<button id="theme-btn" title="Toggle light/dark mode">&#9790;</button>
	<span class="status" id="conn-status">connecting...</span>
</div>
<div id="content">
	<div id="jobs-list"></div>
</div>

<script>
(function() {
	var PORT = %d;
	var jobsList = document.getElementById('jobs-list');
	var connStatus = document.getElementById('conn-status');
	var themeBtn = document.getElementById('theme-btn');
	var ws = null;
	var msgId = 0;
	var reconnectTimer = null;

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
		localStorage.setItem('goclaw-theme', mode);
	}

	var saved = localStorage.getItem('goclaw-theme') || 'dark';
	setTheme(saved);

	themeBtn.addEventListener('click', function() {
		var current = document.documentElement.classList.contains('light') ? 'light' : 'dark';
		setTheme(current === 'light' ? 'dark' : 'light');
	});

	function escHtml(s) {
		return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
	}

	function sendRPC(method, params, callback) {
		if (!ws || ws.readyState !== WebSocket.OPEN) return;
		msgId++;
		var id = 'jobs-' + msgId;
		var handler = function(e) {
			try {
				var resp = JSON.parse(e.data);
				if (resp.id !== id) return;
				ws.removeEventListener('message', handler);
				if (resp.error) {
					alert(typeof resp.error === 'string' ? resp.error : resp.error.message || JSON.stringify(resp.error));
					return;
				}
				if (callback) callback(resp.result);
			} catch(err) {}
		};
		ws.addEventListener('message', handler);
		ws.send(JSON.stringify({
			jsonrpc: '2.0',
			method: method,
			params: params || {},
			id: id
		}));
	}

	function refreshJobs() {
		sendRPC('jobs.list', {}, function(result) {
			renderJobs(result.jobs || []);
		});
	}

	function renderJobs(jobs) {
		jobsList.innerHTML = '';
		if (jobs.length === 0) {
			jobsList.innerHTML = '<div class="empty-state">No cron jobs configured.</div>';
			return;
		}

		for (var i = 0; i < jobs.length; i++) {
			(function(job) {
				var card = document.createElement('div');
				card.className = 'job-card' + (job.paused ? ' paused' : '');

				var header = document.createElement('div');
				header.className = 'job-header';

				var name = document.createElement('span');
				name.className = 'job-name';
				name.textContent = job.name;

				var status = document.createElement('span');
				status.className = 'job-status ' + (job.paused ? 'paused-badge' : 'running');
				status.textContent = job.paused ? 'Paused' : 'Running';

				header.appendChild(name);
				header.appendChild(status);

				var schedule = document.createElement('div');
				schedule.className = 'job-schedule';
				schedule.textContent = 'Every ' + job.schedule;

				var prompt = document.createElement('div');
				prompt.className = 'job-prompt';
				prompt.textContent = job.prompt;

				var actions = document.createElement('div');
				actions.className = 'job-actions';

				// Pause/Resume button
				var toggleBtn = document.createElement('button');
				toggleBtn.textContent = job.paused ? 'Resume' : 'Pause';
				toggleBtn.addEventListener('click', function() {
					var method = job.paused ? 'jobs.resume' : 'jobs.pause';
					sendRPC(method, { name: job.name }, function() {
						refreshJobs();
					});
				});

				// Edit schedule button
				var editBtn = document.createElement('button');
				editBtn.textContent = 'Edit Schedule';
				editBtn.addEventListener('click', function() {
					editArea.classList.toggle('show');
				});

				// Remove button
				var removeBtn = document.createElement('button');
				removeBtn.className = 'danger';
				removeBtn.textContent = 'Remove';
				removeBtn.addEventListener('click', function() {
					if (!confirm('Remove job "' + job.name + '"?')) return;
					sendRPC('jobs.remove', { name: job.name }, function() {
						refreshJobs();
					});
				});

				actions.appendChild(toggleBtn);
				actions.appendChild(editBtn);
				actions.appendChild(removeBtn);

				// Edit schedule inline
				var editArea = document.createElement('div');
				editArea.className = 'edit-schedule';

				var input = document.createElement('input');
				input.type = 'text';
				input.value = job.schedule;
				input.placeholder = 'e.g. 30m, 1h';

				var saveBtn = document.createElement('button');
				saveBtn.className = 'save-btn';
				saveBtn.textContent = 'Save';
				saveBtn.addEventListener('click', function() {
					var newSchedule = input.value.trim();
					if (!newSchedule) return;
					sendRPC('jobs.update', { name: job.name, schedule: newSchedule }, function() {
						refreshJobs();
					});
				});

				var cancelBtn = document.createElement('button');
				cancelBtn.className = 'cancel-btn';
				cancelBtn.textContent = 'Cancel';
				cancelBtn.addEventListener('click', function() {
					editArea.classList.remove('show');
					input.value = job.schedule;
				});

				editArea.appendChild(input);
				editArea.appendChild(saveBtn);
				editArea.appendChild(cancelBtn);

				card.appendChild(header);
				card.appendChild(schedule);
				card.appendChild(prompt);
				card.appendChild(actions);
				card.appendChild(editArea);

				jobsList.appendChild(card);
			})(jobs[i]);
		}
	}

	function connect() {
		ws = new WebSocket('ws://localhost:' + PORT + '/ws');

		ws.onopen = function() {
			connStatus.textContent = 'connected';
			if (reconnectTimer) {
				clearTimeout(reconnectTimer);
				reconnectTimer = null;
			}
			refreshJobs();
		};

		ws.onclose = function() {
			connStatus.textContent = 'disconnected';
			reconnectTimer = setTimeout(connect, 3000);
		};

		ws.onerror = function() {
			connStatus.textContent = 'error';
		};
	}

	connect();
})();
</script>
</body>
</html>`
