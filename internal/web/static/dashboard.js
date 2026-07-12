(function () {
	var POLL_MS = 4000;
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
			return '<td class="num text-muted">—</td><td class="num text-muted">—</td>' +
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
			'<td class="num mono">' + filtered + "</td>" +
			'<td class="num mono">' + esc(comma(p.exported)) + "</td>" +
			"<td>" + limit + "</td>"
		);
	}

	// Twin of sparklineHTML in sparkline.go: a route-count series as an SVG line,
	// so the trend cell survives the live table rebuild that server-rendered HTML
	// would not.
	function sparkline(vals, w, h) {
		if (!vals || vals.length < 2) return '<span class="spark-empty text-muted">—</span>';
		var lo = vals[0], hi = vals[0];
		for (var i = 0; i < vals.length; i++) {
			if (vals[i] < lo) lo = vals[i];
			if (vals[i] > hi) hi = vals[i];
		}
		var span = hi - lo, pad = 2, n = vals.length, pts = [];
		for (var j = 0; j < n; j++) {
			var x = pad + (j / (n - 1)) * (w - 2 * pad);
			var y = span === 0 ? h / 2 : h - pad - ((vals[j] - lo) / span) * (h - 2 * pad);
			pts.push(x.toFixed(1) + "," + y.toFixed(1));
		}
		return '<svg class="sparkline" viewBox="0 0 ' + w + " " + h + '" preserveAspectRatio="none" role="img" aria-label="route-count history">' +
			'<polyline fill="none" stroke="currentColor" stroke-width="1.5" vector-effect="non-scaling-stroke" points="' + pts.join(" ") + '"/></svg>';
	}

	// Only BGP belongs in the sessions table. Device/kernel/static are rendered
	// once, server-side, in the collapsed infrastructure card.
	function isBGP(p) {
		return String(p.proto).toUpperCase() === "BGP";
	}

	function renderRows(protocols, history) {
		var body = document.getElementById("proto-table-body");
		if (!body) return;
		var sessions = (protocols || []).filter(isBGP);
		if (sessions.length === 0) {
			body.innerHTML = '<tr><td colspan="10" class="empty">BIRD is running no BGP sessions.</td></tr>';
			return;
		}
		var hist = history || {};
		body.innerHTML = sessions.map(function (p) {
			var badgeClass = p.up ? "badge-success" : "badge-danger";
			// BIRD's own vocabulary: Established, Active, Connect, Idle.
			var state = p.info || p.state;
			var managed = p.configured
				? '<span class="badge badge-success">configured</span>'
				: '<span class="badge badge-warning">unmanaged</span>';
			return (
				'<tr class="row-link" onclick="location.href=\'/peers/' + encodeURIComponent(p.name) + '\'">' +
				'<td class="mono">' + esc(p.name) + "</td>" +
				'<td class="mono">' + esc(p.table) + "</td>" +
				'<td><span class="badge ' + badgeClass + '"><span class="dot"></span>' + esc(state) + "</span></td>" +
				'<td class="mono">' + esc(p.since) + "</td>" +
				countCells(p) +
				'<td class="spark-cell">' + sparkline(hist[p.name], 108, 26) + "</td>" +
				"<td>" + managed + "</td>" +
				"</tr>"
			);
		}).join("");
	}

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
				renderRows(data.protocols, data.history);
				renderEvents(data.recentEvents);

				var total = (data.protocols || []).length;
				setText("stat-total", total);
				setText("stat-up", data.upCount);
				setText("stat-up-total", total);
				setText("stat-down", data.downCount);
				setText("stat-routes", comma(data.totalRoutes));
				setWidth("bar-up", ratio(data.upCount, total));
				setWidth("bar-down", ratio(data.downCount, total));

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

	setInterval(poll, POLL_MS);
	if (window.birdyRefreshTimes) window.birdyRefreshTimes();
})();
