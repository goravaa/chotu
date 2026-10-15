package handlers

import (
	"log"
	"net/http"
)

func Page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Keep the status page self-contained: the machine may not have a route to a CDN.
	page := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="color-scheme" content="dark">
    <title>Chotu</title>
    <style>
        :root {
            color-scheme: dark;
            --bg: #0b0f14;
            --surface: #111820;
            --surface-raised: #151e27;
            --border: #293541;
            --text: #edf3f8;
            --muted: #91a0af;
            --green: #35d49a;
            --red: #ff6b72;
            --amber: #f4bd50;
            --blue: #52b8ff;
            --orange: #f29b57;
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
        .wrap { max-width: 1400px; margin: 0 auto; padding: 30px clamp(16px, 3vw, 40px) 44px; }
        header { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
        .brand { display: flex; align-items: center; gap: 12px; }
        h1 { margin: 0; font-size: 25px; line-height: 1.2; letter-spacing: -0.035em; }
        .subtitle { margin: 5px 0 0; color: var(--muted); font-size: 13px; }
        .meta { color: var(--muted); font-size: 12px; margin: 12px 0 22px; }
        .badge, .pill {
            display: inline-flex; align-items: center; justify-content: center;
            border: 1px solid var(--border); border-radius: 999px;
            padding: 3px 10px; font-size: 11px; font-weight: 700;
            letter-spacing: .035em; white-space: nowrap;
        }
        .badge.ready { color: var(--green); border-color: rgba(53,212,154,.55); background: rgba(53,212,154,.07); }
        .badge.failed { color: var(--red); border-color: rgba(255,107,114,.55); background: rgba(255,107,114,.07); }
        .badge.working { color: var(--amber); border-color: rgba(244,189,80,.55); background: rgba(244,189,80,.07); animation: pulse 1.8s ease-in-out infinite; }
        .badge.info { color: var(--blue); border-color: rgba(82,184,255,.55); background: rgba(82,184,255,.07); }
        @keyframes pulse { 50% { opacity: .65; } }
        .alert {
            border: 1px solid rgba(255,107,114,.5); background: rgba(255,107,114,.07);
            border-radius: 10px; padding: 14px 16px; margin: 0 0 20px;
        }
        .alert strong { color: var(--red); display: block; margin-bottom: 5px; }
        .alert-item { color: var(--muted); margin-top: 4px; overflow-wrap: anywhere; }
        .conn {
            display: none; border: 1px solid rgba(244,189,80,.5); background: rgba(244,189,80,.07);
            color: var(--amber); border-radius: 9px; padding: 10px 14px; margin: 0 0 18px;
        }
        .section { margin-top: 26px; }
        .columns { display: grid; grid-template-columns: 1.15fr 1fr; gap: 20px; align-items: start; }
        .columns .section { margin-top: 26px; }
        .section-heading { display: flex; align-items: end; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
        h2 { margin: 0; font-size: 16px; font-weight: 650; letter-spacing: -.015em; }
        .section-note { color: var(--muted); font-size: 12px; }
        .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 215px), 1fr)); gap: 12px; }
        .card {
            min-width: 0; background: var(--surface); border: 1px solid var(--border);
            border-radius: 11px; padding: 15px 16px;
        }
        .card h3 { margin: 0 0 7px; font-size: 11px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .075em; }
        .card .big { font-size: 27px; font-weight: 680; line-height: 1.2; letter-spacing: -.035em; font-variant-numeric: tabular-nums; }
        .card .sub { color: var(--muted); font-size: 12px; margin-top: 5px; overflow-wrap: anywhere; }
        .bar { height: 5px; background: #28333e; border-radius: 999px; margin-top: 12px; overflow: hidden; }
        .bar > div { height: 100%; background: var(--blue); border-radius: 999px; transition: width .35s ease; }
        .bar.hot > div { background: var(--amber); }
        .bar.full > div { background: var(--red); }
        .sync-card { display: flex; flex-direction: column; }
        .sync-card .big { font-size: 19px; letter-spacing: 0; }
        .sync-card .sub { line-height: 1.65; }
        .task-summary { color: var(--muted); font-size: 12px; }
        .task-groups { display: grid; grid-template-columns: 1fr; gap: 10px; }
        details.task-details { border: 1px solid var(--border); background: var(--surface); border-radius: 11px; overflow: hidden; }
        details.task-details summary { display: flex; align-items: center; justify-content: space-between; gap: 12px; cursor: pointer; padding: 14px 16px; font-weight: 600; list-style-position: inside; }
        details.task-details summary::marker { color: var(--muted); }
        .task-summary-count { color: var(--muted); font-size: 12px; font-weight: 500; }
        table { width: 100%; border-collapse: collapse; table-layout: auto; }
        th, td { text-align: left; padding: 10px 14px; border-top: 1px solid var(--border); vertical-align: top; }
        th { color: var(--muted); font-size: 10px; font-weight: 700; text-transform: uppercase; letter-spacing: .075em; }
        td.name { font-weight: 550; }
        td.msg { color: var(--muted); font-size: 12px; overflow-wrap: anywhere; }
        td.took { color: var(--muted); font-size: 12px; white-space: nowrap; font-variant-numeric: tabular-nums; }
        .pill { padding: 2px 9px; font-size: 10px; }
        .pill.completed { color: var(--green); border-color: rgba(53,212,154,.5); }
        .pill.failed { color: var(--red); border-color: rgba(255,107,114,.5); }
        .pill.running { color: var(--blue); border-color: rgba(82,184,255,.5); animation: pulse 1.8s ease-in-out infinite; }
        .pill.pending { color: var(--muted); }
        .pill.blocked { color: var(--orange); border-color: rgba(242,155,87,.5); }
        .empty { color: var(--muted); padding: 24px; text-align: center; border: 1px dashed var(--border); border-radius: 10px; }
        footer { margin-top: 24px; padding-top: 14px; border-top: 1px solid var(--border); color: var(--muted); font-size: 11px; }
        @media (max-width: 600px) {
            .wrap { padding: 22px 14px 32px; }
            .columns { grid-template-columns: 1fr; }
            h1 { font-size: 22px; }
            .section-heading { align-items: start; flex-direction: column; gap: 3px; }
            th, td { padding: 9px 10px; }
            th:last-child, td.msg { min-width: 120px; }
        }
        @media (prefers-reduced-motion: reduce) { *, *::before, *::after { animation: none !important; transition: none !important; } }
    </style>
</head>
<body>
    <main class="wrap">
        <header>
            <div class="brand">
                <div>
                    <h1>Chotu</h1>
                </div>
            </div>
            <span id="state" class="badge working">INITIALIZING</span>
        </header>
        <p class="meta"><span id="meta">Waiting for status…</span><span id="up"></span></p>
        <div id="conn" class="conn" role="status">Connection lost — retrying…</div>
        <div id="failed" aria-live="polite"></div>

        <div class="columns">
        <section class="section" aria-labelledby="setup-heading">
            <div class="section-heading">
                <h2 id="setup-heading">Setup progress</h2>
                <span id="task-summary" class="task-summary">Waiting for setup details…</span>
            </div>
            <div id="tasks"><div class="empty">Waiting for setup details…</div></div>
        </section>

        <section class="section" aria-labelledby="resources-heading">
            <div class="section-heading">
                <h2 id="resources-heading">System Resources</h2>
                <span class="section-note">Live resource usage</span>
            </div>
            <div id="mon"><div class="empty">Waiting for system metrics…</div></div>
        </section>
        </div>

        <footer>Chotu status · <span id="ver"></span> · Updates automatically</footer>
    </main>
    <script>
        var stateEl = document.getElementById("state");
        var metaEl = document.getElementById("meta");
        var connEl = document.getElementById("conn");
        var failedEl = document.getElementById("failed");
        var monEl = document.getElementById("mon");
        var tasksEl = document.getElementById("tasks");
        var taskSummaryEl = document.getElementById("task-summary");
        var lastTimestamp = "";
        var lastVersion = "";
        var lastData = null;
        var metaCache = {tasks: {}};

        function displayState(s) {
            s = String(s == null || s === "" ? "unknown" : s);
            return s.charAt(0).toUpperCase() + s.slice(1);
        }

        function esc(value) {
            return String(value == null ? "" : value)
                .replace(/&/g, "&amp;")
                .replace(/</g, "&lt;")
                .replace(/>/g, "&gt;")
                .replace(/"/g, "&quot;")
                .replace(/'/g, "&#39;");
        }

        function gib(bytes) {
            return (Number(bytes || 0) / 1073741824).toFixed(1) + " GiB";
        }

        function pct(value) {
            var n = Number(value || 0);
            return (Math.round(n * 10) / 10) + "%";
        }

        function durStr(totalSeconds) {
            if (totalSeconds == null) return "—";
            var s = Math.max(0, Math.round(Number(totalSeconds)));
            if (s < 60) return s + "s";
            var m = Math.floor(s / 60);
            if (m < 60) {
                var rs = s % 60;
                return m + "m" + (rs ? rs + "s" : "");
            }
            var h = Math.floor(m / 60);
            var rm = m % 60;
            return h + "h" + (rm ? rm + "m" : "");
        }

        function tookText(name) {
            var entry = (metaCache.tasks || {})[name];
            if (entry && entry.took_seconds != null) return durStr(entry.took_seconds);
            return "—";
        }

        function renderMetaUptime() {
            var upEl = document.getElementById("up");
            if (upEl) upEl.textContent = (metaCache.service_uptime_seconds != null)
                ? " · up " + durStr(metaCache.service_uptime_seconds)
                : "";
        }

        function fetchMeta() {
            fetch("/api/v1/meta").then(function(r) { return r.json(); }).then(function(meta) {
                metaCache = meta || {tasks: {}};
                renderMetaUptime();
                if (lastData) renderTasks(lastData);
            }).catch(function() {});
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
            if (s < 5) return "just now";
            if (s < 60) return s + " seconds ago";
            var m = Math.floor(s / 60);
            if (m === 1) return "1 minute ago";
            if (m < 60) return m + " minutes ago";
            var h = Math.floor(m / 60);
            if (h === 1) return "1 hour ago";
            if (h < 24) return h + " hours ago";
            var d = Math.floor(h / 24);
            return d === 1 ? "1 day ago" : d + " days ago";
        }

        function renderMeta(data) {
            lastVersion = data.version || lastVersion;
            metaEl.textContent = "Updated " + (ago(data.timestamp) || "just now") + " · Automatically refreshes" + (lastVersion ? " · " + lastVersion : "");
            var verEl = document.getElementById("ver");
            if (verEl) verEl.textContent = lastVersion || "";
        }

        function taskLabel(name) {
            var labels = {
                "machine": "Machine Check",
                "storage": "Storage",
                "docker": "Docker",
                "docker-gpu": "GPU support for containers",
                "gpu": "GPU drivers",
                "opencode": "OpenCode",
                "rclone": "Workspace sync service",
                "s3": "Cloud storage connection",
                "status": "Status reporting",
                "sync": "Workspace sync",
                "tooling": "Development tools",
                "vllm": "vLLM",
                "vllm-pull": "Download vLLM",
                "workspace": "S3 Sync"
            };
            return labels[name] || name.replace(/[-_]+/g, " ").replace(/\b\w/g, function(ch) { return ch.toUpperCase(); });
        }

        function renderFailed(data) {
            var failed = (data.setup && data.setup.failed) || [];
            if (!failed.length) {
                failedEl.innerHTML = "";
                return;
            }
            var items = failed.map(function(name) {
                var task = ((data.setup.tasks || {})[name]) || {};
                var msg = task.message ? " — " + esc(task.message) : "";
                return '<div class="alert-item"><strong style="display:inline;color:var(--text)">' + esc(taskLabel(name)) + '</strong>' + msg + '</div>';
            }).join("");
            failedEl.innerHTML = '<div class="alert"><strong>Some parts need attention (' + failed.length + ')</strong>' + items + '</div>';
        }

        function diskLabel(mount) {
            if (mount === "/") return "Root Disk";
            if (mount === "/data") return "Instance Store";
            return "Disk " + mount;
        }

        function diskDescription(disk) {
            var desc = esc(gib(disk.used_bytes)) + " of " + esc(gib(disk.total_bytes)) + " used";
            if (disk.mount === "/data") desc += " · Mounted at /data";
            return desc;
        }

        function diskCard(disk) {
            return '<div class="card"><h3>' + esc(diskLabel(disk.mount)) + '</h3>' +
                '<div class="big">' + esc(pct(disk.usage_percent)) + '</div>' +
                '<div class="sub">' + diskDescription(disk) + '</div>' +
                '<div class="' + barClass(disk.usage_percent) + '"><div style="width:' +
                esc(Math.min(100, Math.max(0, Number(disk.usage_percent) || 0))) + '%"></div></div></div>';
        }

        function metricCard(title, value, sub, usage) {
            var html = '<div class="card"><h3>' + esc(title) + '</h3><div class="big">' + esc(value) + '</div>';
            if (sub) html += '<div class="sub">' + sub + '</div>';
            if (usage != null) html += '<div class="' + barClass(usage) + '"><div style="width:' + esc(Math.min(100, Math.max(0, Number(usage) || 0))) + '%"></div></div>';
            return html + '</div>';
        }

        function renderMon(data) {
            var mon = data.monitoring || {};
            var cards = [];
            var cpu = mon.cpu || {};
            cards.push(metricCard("CPU", pct(cpu.usage_percent), "", cpu.usage_percent));

            var ram = mon.ram || {};
            cards.push(metricCard("RAM", pct(ram.usage_percent), esc(gib(ram.used_bytes)) + " of " + esc(gib(ram.total_bytes)), ram.usage_percent));

            var gpu = mon.gpu || {};
            if (Number(gpu.memory_total_bytes) > 0) {
                var gpuSub = "";
                if (gpu.temperature_celsius != null) gpuSub = esc(gpu.temperature_celsius) + " °C";
                cards.push(metricCard("GPU", pct(gpu.usage_percent), gpuSub, gpu.usage_percent));
                var vramTotal = Number(gpu.memory_total_bytes) || 0;
                var vramUsage = vramTotal > 0 ? (Number(gpu.memory_used_bytes) || 0) / vramTotal * 100 : 0;
                cards.push(metricCard("VRAM", pct(vramUsage), esc(gib(gpu.memory_used_bytes)) + " of " + esc(gib(gpu.memory_total_bytes)), vramUsage));
            } else {
                cards.push(metricCard("GPU", "—", "No GPU metrics available", null));
            }

            (mon.disks || []).forEach(function(disk) { cards.push(diskCard(disk)); });

            var sync = mon.sync || {};
            var syncState = displayState(sync.state);
            var syncSub = "Status: " + esc(displayState(sync.state));
            if (sync.last_success) syncSub += "<br>Last successful sync: " + esc(ago(sync.last_success) || sync.last_success);
            else if (sync.last_attempt) syncSub += "<br>Last attempt: " + esc(ago(sync.last_attempt) || sync.last_attempt);
            if (sync.message) syncSub += "<br>" + esc(sync.message);
            cards.push('<div class="card sync-card"><h3>Workspace sync</h3><div class="big">' + esc(syncState) + '</div><div class="sub">' + syncSub + '</div></div>');

            monEl.innerHTML = '<div class="grid">' + cards.join("") + '</div>';
        }

        function renderTasks(data) {
            var tasks = (data.setup && data.setup.tasks) || {};
            var names = Object.keys(tasks).sort();
            if (!names.length) {
                tasksEl.innerHTML = '<div class="empty">No setup details available yet.</div>';
                taskSummaryEl.textContent = "No setup details available";
                return;
            }

            var completed = 0, running = 0, failed = 0;
            names.forEach(function(name) {
                var state = String((tasks[name] || {}).state || "").toLowerCase();
                if (state === "completed") completed++;
                else if (state === "running") running++;
                else if (state === "failed") failed++;
            });
            var summary = completed + " of " + names.length + " finished";
            if (running) summary += " · " + running + " in progress";
            if (failed) summary += " · " + failed + " failed";
            taskSummaryEl.textContent = summary;

            var rows = names.map(function(name) {
                var task = tasks[name] || {};
                return '<tr><td class="name">' + esc(taskLabel(name)) + '</td>' +
                    '<td><span class="pill ' + taskPill(task.state) + '">' + esc(task.state || "?") + '</span></td>' +
                    '<td class="took">' + esc(tookText(name)) + '</td>' +
                    '<td class="msg">' + esc(task.message || "—") + '</td></tr>';
            }).join("");

            tasksEl.innerHTML = '<div class="task-groups"><details class="task-details" open><summary><span>Setup details</span><span class="task-summary-count">' + names.length + ' components</span></summary>' +
                '<table><thead><tr><th>Component</th><th>Status</th><th>Took</th><th>Details</th></tr></thead><tbody>' + rows + '</tbody></table></details></div>';
        }

        function render(data) {
            var badge = stateBadge(data.state);
            stateEl.className = "badge " + badge[0];
            stateEl.textContent = badge[1];
            renderMeta(data);
            renderFailed(data);
            renderMon(data);
            renderTasks(data);
        }

        setInterval(function() {
            if (lastTimestamp) metaEl.textContent = "Updated " + (ago(lastTimestamp) || "just now") + " · Automatically refreshes" + (lastVersion ? " · " + lastVersion : "");
        }, 1000);

        var events = new EventSource("/api/v1/events");
        events.onmessage = function(event) {
            connEl.style.display = "none";
            try {
                var data = JSON.parse(event.data);
                lastTimestamp = data.timestamp || "";
                lastData = data;
                render(data);
            } catch (err) {
                console.error("Could not parse a status update:", err);
            }
        };
        events.onerror = function() {
            connEl.style.display = "block";
        };

        fetchMeta();
        setInterval(fetchMeta, 5000);
    </script>
</body>
</html>`

	// A write failure means the browser went away before the page finished arriving.
	if _, err := w.Write([]byte(page)); err != nil {
		log.Printf("[page] could not write the page: %v", err)
	}
}
