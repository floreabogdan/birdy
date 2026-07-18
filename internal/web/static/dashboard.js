(function () {
	var POLL_MS = 5000;
	var sessionFilters = { state: "all", family: "all", model: "all", search: "", sort: "name" };
	var lastProtocols = [], lastHistory = {};
	var KIND_BADGE = {
		session_up: ["badge-success", "up"],
		session_down: ["badge-danger", "down"],
		flap: ["badge-warning", "flap"],
		limit_hit: ["badge-warning", "limit"],
		prefix_drop: ["badge-danger", "drop"],
		config_apply: ["badge-info", "config"],
		config_revert: ["badge-info", "revert"],
		config_drift: ["badge-warning", "drift"],
		irr_refresh: ["badge-info", "irr"],
		model_change: ["badge", "change"],
		bird_unreachable: ["badge-danger", "bird down"],
		bird_reachable: ["badge-success", "bird up"],
	};

	function esc(s) {
		var d = document.createElement("div");
		d.textContent = s == null ? "" : String(s);
		return d.innerHTML;
	}

	function fmtTime(iso) {
		var d = new Date(iso);
		if (isNaN(d.getTime())) return "-";
		return d.toLocaleString();
	}

	function progressClass(pct) {
		if (pct >= 90) return "p-danger";
		if (pct >= 70) return "p-warning";
		return "";
	}

	// Twin of the comma template func in templates.go.
	function comma(n) {
		return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
	}

	function ratio(part, total) {
		if (!total || total <= 0) return 0;
		return Math.min((part / total) * 100, 100);
	}

	function setText(id, text) {
		var el = document.getElementById(id);
		if (el) el.textContent = text;
	}

	function setWidth(id, pct) {
		var el = document.getElementById(id);
		if (el) el.style.width = Math.round(pct) + "%";
	}

	function countCells(p) {
		if (!p.hasCounts) {
			return '<td class="num text-muted">—</td><td class="num text-muted" data-col="rejected">—</td>' +
				'<td class="num text-muted">—</td><td class="text-muted">—</td>';
		}
		var limit;
		if (p.limitPct >= 0) {
			var w = Math.round(p.limitPct);
			limit =
				'<div class="progress-track"><div class="progress-fill ' + progressClass(p.limitPct) + '" style="width:' + w + '%"></div></div>' +
				'<div class="progress-label">' + esc(p.limitText) + "</div>";
		} else {
			limit = '<span class="text-muted">no limit</span>';
		}
		var filtered = p.filtered
			? esc(comma(p.filtered))
			: '<span class="text-muted">0</span>';
		return (
			'<td class="num mono">' + esc(comma(p.imported)) + "</td>" +
			'<td class="num mono" data-col="rejected">' + filtered + "</td>" +
			'<td class="num mono">' + esc(comma(p.exported)) + "</td>" +
			"<td>" + limit + "</td>"
		);
	}

	// Twin of sparklineHTML in sparkline.go: a route-count series as an SVG line,
	// so the trend cell survives the live table rebuild that server-rendered HTML
	// would not.
	// The twin of sparklineHTML in sparkline.go: same shape, same data attributes.
	// The series is {t, v} points — the timestamps are what spark-hover.js names
	// under the cursor, so a rebuilt row must carry them or hovering it goes dead
	// on the first poll.
	function sparkline(series, w, h) {
		if (!series || series.length < 2) return '<span class="spark-empty text-muted">—</span>';
		var lo = series[0].v, hi = series[0].v;
		for (var i = 0; i < series.length; i++) {
			if (series[i].v < lo) lo = series[i].v;
			if (series[i].v > hi) hi = series[i].v;
		}
		var span = hi - lo, pad = 2, n = series.length, pts = [];
		for (var j = 0; j < n; j++) {
			var x = pad + (j / (n - 1)) * (w - 2 * pad);
			var y = span === 0 ? h / 2 : h - pad - ((series[j].v - lo) / span) * (h - 2 * pad);
			pts.push(x.toFixed(1) + "," + y.toFixed(1));
		}
		return '<svg class="sparkline" viewBox="0 0 ' + w + " " + h + '" preserveAspectRatio="none" role="img" aria-label="route-count history"' +
			' data-spark="' + esc(JSON.stringify(series)) + '" data-spark-w="' + w + '" data-spark-h="' + h + '" data-spark-pad="' + pad + '">' +
			'<polyline fill="none" stroke="currentColor" stroke-width="1.5" vector-effect="non-scaling-stroke" points="' + pts.join(" ") + '"/></svg>';
	}

	// Only BGP belongs in the sessions table. Device/kernel/static are rendered
	// once, server-side, in the collapsed infrastructure card.
	function isBGP(p) {
		return String(p.proto).toUpperCase() === "BGP";
	}

	function familyOf(p) {
		var name = String(p.name || "").toLowerCase();
		if (name.indexOf("_v4") !== -1 || name.indexOf("ipv4") !== -1) return "v4";
		if (name.indexOf("_v6") !== -1 || name.indexOf("ipv6") !== -1) return "v6";
		return "all";
	}

	function applySessionFilters() {
		var body = document.getElementById("proto-table-body");
		if (!body) return;
		Array.prototype.forEach.call(body.querySelectorAll("tr[data-state]"), function (row) {
			var stateOK = sessionFilters.state === "all" || row.dataset.state === sessionFilters.state;
			var familyOK = sessionFilters.family === "all" || row.dataset.family === sessionFilters.family;
			var modelOK = sessionFilters.model === "all" || row.dataset.model === sessionFilters.model;
			var searchOK = !sessionFilters.search || row.dataset.name.indexOf(sessionFilters.search) >= 0;
			row.hidden = !(stateOK && familyOK && modelOK && searchOK);
		});
	}

	function renderRows(protocols, history) {
		var body = document.getElementById("proto-table-body");
		if (!body) return;
		var sessions = (protocols || []).filter(isBGP);
		lastProtocols = protocols || [];
		lastHistory = history || {};
		sessions.sort(function (a, b) {
			if (sessionFilters.sort === "imported" || sessionFilters.sort === "exported") return (Number(b[sessionFilters.sort]) || 0) - (Number(a[sessionFilters.sort]) || 0);
			if (sessionFilters.sort === "state") return String(b.info || b.state).localeCompare(String(a.info || a.state));
			return String(a.name).localeCompare(String(b.name));
		});
		if (sessions.length === 0) {
			body.innerHTML = '<tr><td colspan="10"><div class="empty-state"><strong>No BGP sessions are running</strong><p>Add a peer directly, or import sessions already present in BIRD.</p><div><a class="btn btn-primary" href="/peers/new">Add peer</a><a class="btn" href="/peers/seed">Import from BIRD</a></div></div></td></tr>';
			return;
		}
		var hist = history || {};
		var remote = document.body.getAttribute("data-remote") === "true";
		body.innerHTML = sessions.map(function (p) {
			var badgeClass = p.up ? "badge-success" : "badge-danger";
			// BIRD's own vocabulary: Established, Active, Connect, Idle.
			var state = p.info || p.state;
			// A peer switched off in birdy is down on purpose. BIRD reports it as
			// plain "down", same as a session that failed — only the model knows
			// which is which, so it rides along in the JSON.
			if (p.disabled && !p.up) {
				badgeClass = "badge";
				state = "disabled";
			}
			var managed = p.configured
				? '<span class="badge badge-success">configured</span>'
				: '<span class="badge badge-warning">unmanaged</span>';
			var state = p.up ? "up" : "down";
			var model = p.configured ? "managed" : "unmanaged";
			var rowStart = remote ? '<tr data-name="' + esc(String(p.name).toLowerCase()) + '" data-state="' + state + '" data-family="' + familyOf(p) + '" data-model="' + model + '">' : '<tr data-name="' + esc(String(p.name).toLowerCase()) + '" data-state="' + state + '" data-family="' + familyOf(p) + '" data-model="' + model + '" class="row-link" data-href="/peers/' + encodeURIComponent(p.name) + '" tabindex="0">';
			return (
				rowStart +
				'<td class="mono">' + esc(p.name) + "</td>" +
				'<td class="mono" data-col="table">' + esc(p.table) + "</td>" +
				'<td><span class="badge ' + badgeClass + '"><span class="dot"></span>' + esc(state) + "</span></td>" +
				'<td class="mono" data-col="since">' + esc(p.since) + "</td>" +
				countCells(p) +
				'<td class="spark-cell" data-col="trend">' + sparkline(hist[p.name], 108, 26) + "</td>" +
				'<td data-col="model">' + managed + "</td>" +
				"</tr>"
			);
		}).join("");
		applyColumnVisibility();
		applySessionFilters();
	}

	var hiddenColumns = {};
	try { hiddenColumns = JSON.parse(localStorage.getItem("birdy-session-columns") || "{}"); } catch (_) { hiddenColumns = {}; }
	function applyColumnVisibility() {
		Object.keys(hiddenColumns).forEach(function (name) {
			document.querySelectorAll('#session-table [data-col="' + name + '"]').forEach(function (cell) {
				cell.hidden = !!hiddenColumns[name];
			});
		});
		document.querySelectorAll("[data-session-column]").forEach(function (box) {
			box.checked = !hiddenColumns[box.getAttribute("data-session-column")];
		});
	}
	document.querySelectorAll("[data-session-column]").forEach(function (box) {
		box.addEventListener("change", function () {
			hiddenColumns[box.getAttribute("data-session-column")] = !box.checked;
			try { localStorage.setItem("birdy-session-columns", JSON.stringify(hiddenColumns)); } catch (_) {}
			applyColumnVisibility();
		});
	});
	applyColumnVisibility();

	function renderEvents(events) {
		var el = document.getElementById("recent-events");
		if (!el) return;
		if (!events || events.length === 0) {
			el.innerHTML = '<div class="empty">No events yet.</div>';
			return;
		}
		el.innerHTML = events.map(function (e) {
			var b = KIND_BADGE[e.kind] || ["", e.kind];
			var who = e.actor ? ' <span class="text-muted">by ' + esc(e.actor) + "</span>" : "";
			return (
				'<div class="timeline-item">' +
				'<div class="timeline-time" data-ts="' + esc(e.ts) + '" title="' + esc(fmtTime(e.ts)) + '">' + esc(fmtTime(e.ts)) + "</div>" +
				'<div><span class="badge ' + b[0] + '">' + esc(b[1]) + '</span> <span class="mono">' + esc(e.protocol) + "</span> — " + esc(e.message) + who + "</div>" +
				"</div>"
			);
		}).join("");
	}

	function poll() {
		fetch("/api/dashboard", { credentials: "same-origin" })
			.then(function (r) {
				if (r.status === 401 || r.redirected) { window.location.reload(); return null; }
				return r.json();
			})
			.then(function (data) {
				if (!data) return;
				document.body.setAttribute("data-remote", data.instanceRemote ? "true" : "false");
				renderRows(data.protocols, data.history);
				renderEvents(data.recentEvents);

				// Session stats count BGP only; infrastructure protocols
				// (device/kernel/static/RPKI) are not sessions.
				var sessions = (data.protocols || []).filter(isBGP).length;
				setText("stat-total", sessions);
				setText("stat-up", data.sessionUp);
				setText("stat-up-total", sessions);
				setText("stat-down", data.sessionDown);
				setText("stat-routes", comma(data.totalRoutes));
				setText("stat-managed", data.sessionManaged);
				setText("stat-model-total", sessions);
				setText("model-note", data.sessionUnmanaged ? data.sessionUnmanaged + " unmanaged" : "all live sessions represented");
				setWidth("bar-up", ratio(data.sessionUp, sessions));
				setWidth("bar-down", ratio(data.sessionDown, sessions));
				setWidth("bar-managed", ratio(data.sessionManaged, sessions));

				var hero = document.getElementById("hero-status");
				if (hero) {
					hero.classList.toggle("ok", !!data.statusOK);
					hero.classList.toggle("bad", !data.statusOK);
				}
				setText("hero-status-text", data.statusText);

				var updated = document.getElementById("updated-at");
				if (updated) {
					updated.setAttribute("data-ts", data.updatedAt);
					updated.setAttribute("title", fmtTime(data.updatedAt));
				}
				if (window.birdyRefreshTimes) window.birdyRefreshTimes();
				if (window.birdyApplyFilter) window.birdyApplyFilter();
			})
			.catch(function () { /* transient network hiccup, next poll will retry */ });
	}
	var search = document.getElementById("session-search");
	if (search) search.addEventListener("input", function () { sessionFilters.search = search.value.trim().toLowerCase(); applySessionFilters(); });
	var sort = document.getElementById("session-sort");
	if (sort) sort.addEventListener("change", function () { sessionFilters.sort = sort.value; renderRows(lastProtocols, lastHistory); });

	["session-state-filter", "session-family-filter", "session-model-filter"].forEach(function (id) {
		var select = document.getElementById(id);
		if (!select) return;
		select.addEventListener("change", function () {
			if (id === "session-state-filter") sessionFilters.state = select.value;
			if (id === "session-family-filter") sessionFilters.family = select.value;
			if (id === "session-model-filter") sessionFilters.model = select.value;
			applySessionFilters();
		});
	});

	setInterval(poll, POLL_MS);
	if (window.birdyRefreshTimes) window.birdyRefreshTimes();
})();
