package handlers

import (
	"log"
	"net/http"
)

func Page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Self-contained on purpose: no external CSS, fonts, or JS. The machine
	// may have no route to a CDN, and a status page that needs one to render
	// is a status page that is blank exactly when it is needed most.
	page := `
<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<title>Chotu Status</title>
	<style>
		:root {
			color-scheme: dark;
			--bg: #0d1117;
			--card: #161b22;
			--border: #30363d;
			--text: #e6edf3;
			--muted: #8b949e;
			--green: #3fb950;
			--red: #f85149;
			--amber: #d29922;
			--blue: #58a6ff;
			--orange: #db6d28;
		}
		* { box-sizing: border-box; }
		body {
			margin: 0;
			background: var(--bg);
			color: var(--text);
			font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
			font-size: 14px;
			line-height: 1.5;
		}
		.wrap { max-width: 1080px; margin: 0 auto; padding: 24px 16px 48px; }
		header { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 4px; }
		h1 { font-size: 22px; margin: 0; }
		.meta { color: var(--muted); font-size: 13px; margin: 0 0 16px; }
		.meta code { color: var(--text); }
		.badge {
			display: inline-block;
			padding: 2px 12px;
			border-radius: 999px;
			font-size: 13px;
			font-weight: 600;
			border: 1px solid var(--border);
			background: var(--card);
		}
		.badge.ready { color: var(--green); border-color: var(--green); }
		.badge.failed { color: var(--red); border-color: var(--red); }
		.badge.working { color: var(--amber); border-color: var(--amber); animation: pulse 1.6s ease-in-out infinite; }
		.badge.info { color: var(--blue); border-color: var(--blue); }
		@keyframes pulse { 50% { opacity: 0.55; } }
		.alert {
			border: 1px solid var(--red);
			background: rgba(248, 81, 73, 0.08);
			border-radius: 8px;
			padding: 12px 16px;
			margin: 12px 0 16px;
		}
		.alert strong { color: var(--red); }
		.alert code { color: var(--text); }
		.conn {
			display: none;
			border: 1px solid var(--amber);
			background: rgba(210, 153, 34, 0.08);
			color: var(--amber);
			border-radius: 8px;
			padding: 8px 16px;
			margin: 0 0 16px;
		}
		h2 { font-size: 15px; margin: 24px 0 8px; color: var(--muted); text-transform: uppercase; letter-spacing: 0.06em; }
		.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; }
		.card {
			background: var(--card);
			border: 1px solid var(--border);
			border-radius: 8px;
			padding: 12px 14px;
		}
		.card h3 { margin: 0 0 6px; font-size: 12px; color: var(--muted); text-transform: uppercase; letter-spacing: 0.06em; }
		.card .big { font-size: 26px; font-weight: 600; }
		.card .sub { color: var(--muted); font-size: 12.5px; margin-top: 2px; word-break: break-word; }
		.bar { height: 6px; background: var(--border); border-radius: 3px; margin-top: 8px; overflow: hidden; }
		.bar > div { height: 100%; background: var(--blue); border-radius: 3px; }
		.bar.hot > div { background: var(--amber); }
		.bar.full > div { background: var(--red); }
		table { width: 100%; border-collapse: collapse; background: var(--card); border: 1px solid var(--border); border-radius: 8px; overflow: hidden; }
		th, td { text-align: left; padding: 8px 12px; border-bottom: 1px solid var(--border); vertical-align: top; }
		th { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: 0.06em; }
		tr:last-child td { border-bottom: none; }
		td.name { font-family: ui-monospace, monospace; white-space: nowrap; }
		td.msg { color: var(--muted); font-size: 12.5px; word-break: break-word; }
		.pill {
			display: inline-block;
			padding: 1px 10px;
			border-radius: 999px;
			font-size: 12px;
			font-weight: 600;
			border: 1px solid var(--border);
			white-space: nowrap;
		}
		.pill.completed { color: var(--green); border-color: var(--green); }
		.pill.failed { color: var(--red); border-color: var(--red); }
		.pill.running { color: var(--blue); border-color: var(--blue); animation: pulse 1.6s ease-in-out infinite; }
		.pill.pending { color: var(--muted); }
		.pill.blocked { color: var(--orange); border-color: var(--orange); }
		details { margin-top: 24px; }
		details summary { cursor: pointer; color: var(--muted); }
		pre {
			background: var(--card);
			border: 1px solid var(--border);
			border-radius: 8px;
			padding: 12px;
			overflow: auto;
			font-size: 12px;
		}
		.empty { color: var(--muted); padding: 24px; text-align: center; }
	</style>
</head>
<body>
	<div class="wrap">
		<header>
			<h1>Chotu Status</h1>
			<span id="state" class="badge">waiting</span>
		</header>
		<p id="meta" class="meta">Waiting for status...</p>
		<div id="conn" class="conn">Connection lost &mdash; retrying&hellip;</div>
		<div id="failed"></div>

		<h2>Monitoring</h2>
		<div id="mon"><div class="empty">No metrics yet.</div></div>

		<h2>Setup tasks</h2>
		<div id="tasks"><div class="empty">No task data yet.</div></div>

		<details>
			<summary>Raw JSON</summary>
			<pre id="raw">Waiting for status...</pre>
		</details>
	</div>

	<script>
		var stateEl = document.getElementById("state");
		var metaEl = document.getElementById("meta");
		var connEl = document.getElementById("conn");
		var failedEl = document.getElementById("failed");
		var monEl = document.getElementById("mon");
		var tasksEl = document.getElementById("tasks");
		var rawEl = document.getElementById("raw");
		var lastTimestamp = "";

		function esc(value) {
			return String(value == null ? "" : value)
				.replace(/&/g, "&amp;")
				.replace(/</g, "&lt;")
				.replace(/>/g, "&gt;")
				.replace(/"/g, "&quot;");
		}

		function gib(bytes) {
			return (Number(bytes || 0) / 1073741824).toFixed(1) + " GiB";
		}

		function pct(value) {
			var n = Number(value || 0);
			return (Math.round(n * 10) / 10) + "%";
		}

		function barClass(usage) {
			if (Number(usage) >= 90) return "bar full";
			if (Number(usage) >= 75) return "bar hot";
			return "bar";
		}

		function stateBadge(state) {
			if (state === "READY") return ["ready", "READY"];
			if (state === "FAILED") return ["failed", "FAILED"];
			if (state === "INITIALIZING") return ["working", "INITIALIZING"];
			if (state === "REBOOT_REQUIRED") return ["info", "REBOOT REQUIRED"];
			return ["", esc(state || "unknown")];
		}

		function taskPill(state) {
			var s = String(state || "").toLowerCase();
			if (s === "completed") return "completed";
			if (s === "failed") return "failed";
			if (s === "running") return "running";
			if (s === "blocked") return "blocked";
			return "pending";
		}

		function ago(iso) {
			var t = Date.parse(iso);
			if (isNaN(t)) return "";
			var s = Math.max(0, Math.round((Date.now() - t) / 1000));
			if (s < 60) return s + "s ago";
			var m = Math.floor(s / 60);
			if (m < 60) return m + "m ago";
			return Math.floor(m / 60) + "h ago";
		}

		function renderMeta(data) {
			var secs = "";
			if (data.setup && data.setup.last_cycle_duration_ms != null) {
				secs = (Number(data.setup.last_cycle_duration_ms) / 1000).toFixed(1) + "s";
			}
			metaEl.innerHTML = "cycle <code>" + esc(data.cycle) + "</code>" +
				" &middot; version <code>" + esc(data.version) + "</code>" +
				" &middot; updated <code>" + esc(data.timestamp) + "</code>" +
				" (<span id='ago'>" + esc(ago(data.timestamp)) + "</span>)" +
				(secs ? " &middot; last cycle <code>" + esc(secs) + "</code>" : "");
		}

		function renderFailed(data) {
			var failed = (data.setup && data.setup.failed) || [];
			if (!failed.length) {
				failedEl.innerHTML = "";
				return;
			}
			var items = failed.map(function(name) {
				var task = ((data.setup.tasks || {})[name]) || {};
				var msg = task.message ? " &mdash; " + esc(task.message) : "";
				return "<div><code>" + esc(name) + "</code>" + msg + "</div>";
			}).join("");
			failedEl.innerHTML = '<div class="alert"><strong>Failing: ' +
				failed.length + " task" + (failed.length === 1 ? "" : "s") +
				"</strong>" + items + "</div>";
		}

		function diskCard(disk) {
			return '<div class="card"><h3>Disk ' + esc(disk.mount) + "</h3>" +
				'<div class="big">' + esc(pct(disk.usage_percent)) + "</div>" +
				'<div class="sub">' + esc(gib(disk.used_bytes)) + " of " + esc(gib(disk.total_bytes)) + "</div>" +
				'<div class="' + barClass(disk.usage_percent) + '"><div style="width:' +
				esc(Math.min(100, Number(disk.usage_percent) || 0)) + '%"></div></div></div>';
		}

		function renderMon(data) {
			var mon = data.monitoring || {};
			var cards = [];
			var cpu = mon.cpu || {};
			cards.push('<div class="card"><h3>CPU</h3><div class="big">' + esc(pct(cpu.usage_percent)) + "</div>" +
				'<div class="' + barClass(cpu.usage_percent) + '"><div style="width:' +
				esc(Math.min(100, Number(cpu.usage_percent) || 0)) + '%"></div></div></div>');
			var ram = mon.ram || {};
			cards.push('<div class="card"><h3>RAM</h3><div class="big">' + esc(pct(ram.usage_percent)) + "</div>" +
				'<div class="sub">' + esc(gib(ram.used_bytes)) + " of " + esc(gib(ram.total_bytes)) + "</div>" +
				'<div class="' + barClass(ram.usage_percent) + '"><div style="width:' +
				esc(Math.min(100, Number(ram.usage_percent) || 0)) + '%"></div></div></div>');
			var gpu = mon.gpu || {};
			if (Number(gpu.memory_total_bytes) > 0) {
				cards.push('<div class="card"><h3>GPU</h3><div class="big">' + esc(pct(gpu.usage_percent)) + "</div>" +
					'<div class="sub">' + esc(gib(gpu.memory_used_bytes)) + " of " + esc(gib(gpu.memory_total_bytes)) +
					" &middot; " + esc(gpu.temperature_celsius) + " &deg;C</div>" +
					'<div class="' + barClass(gpu.usage_percent) + '"><div style="width:' +
					esc(Math.min(100, Number(gpu.usage_percent) || 0)) + '%"></div></div></div>');
			} else {
				cards.push('<div class="card"><h3>GPU</h3><div class="big">&mdash;</div>' +
					'<div class="sub">no GPU reporting (see gpu task below)</div></div>');
			}
			(mon.disks || []).forEach(function(disk) { cards.push(diskCard(disk)); });
			var sync = mon.sync || {};
			var syncSub = "state: " + esc(sync.state || "?");
			if (sync.last_success) syncSub += "<br>last success: " + esc(sync.last_success);
			else if (sync.last_attempt) syncSub += "<br>last attempt: " + esc(sync.last_attempt);
			if (sync.message) syncSub += "<br>" + esc(sync.message);
			cards.push('<div class="card"><h3>Workspace sync</h3><div class="big" style="font-size:18px">' +
				esc(sync.state || "?") + '</div><div class="sub">' + syncSub + "</div></div>");
			monEl.innerHTML = '<div class="grid">' + cards.join("") + "</div>";
		}

		function renderTasks(data) {
			var tasks = (data.setup && data.setup.tasks) || {};
			var names = Object.keys(tasks).sort();
			if (!names.length) {
				tasksEl.innerHTML = '<div class="empty">No task data yet.</div>';
				return;
			}
			var rows = names.map(function(name) {
				var task = tasks[name] || {};
				return "<tr><td class='name'>" + esc(name) + "</td>" +
					"<td><span class='pill " + taskPill(task.state) + "'>" + esc(task.state || "?") + "</span></td>" +
					"<td class='msg'>" + esc(task.message || "") + "</td></tr>";
			}).join("");
			tasksEl.innerHTML = "<table><thead><tr><th>Task</th><th>State</th><th>Message</th></tr></thead>" +
				"<tbody>" + rows + "</tbody></table>";
		}

		function render(data) {
			var badge = stateBadge(data.state);
			stateEl.className = "badge " + badge[0];
			stateEl.textContent = badge[1];
			renderMeta(data);
			renderFailed(data);
			renderMon(data);
			renderTasks(data);
			rawEl.textContent = JSON.stringify(data, null, 2);
		}

		setInterval(function() {
			var agoEl = document.getElementById("ago");
			if (agoEl && lastTimestamp) agoEl.textContent = ago(lastTimestamp);
		}, 1000);

		var events = new EventSource("/api/v1/events");

		events.onmessage = function(event) {
			connEl.style.display = "none";
			try {
				var data = JSON.parse(event.data);
				lastTimestamp = data.timestamp || "";
				render(data);
			} catch (err) {
				rawEl.textContent = "Could not parse a status update: " + err;
			}
		};

		events.onerror = function() {
			connEl.style.display = "block";
		};
	</script>
</body>
</html>
`

	// A write failure here means the browser went away before the page finished
	// arriving, which is not worth failing over.
	if _, err := w.Write([]byte(page)); err != nil {
		log.Printf("[page] could not write the page: %v", err)
	}
}
