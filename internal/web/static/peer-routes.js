(function () {
	var card = document.getElementById("routes-card");
	if (!card) return;

	var peer = card.getAttribute("data-peer");
	var body = document.getElementById("routes-body");
	var prevBtn = document.getElementById("routes-prev");
	var nextBtn = document.getElementById("routes-next");
	var status = document.getElementById("routes-status");
	var pages = document.getElementById("routes-pages");
	var tabs = card.querySelectorAll(".tab-btn");

	var LIMIT = 50;
	var state = { dir: "protocol", offset: 0, hasMore: false };

	function esc(s) {
		var d = document.createElement("div");
		d.textContent = s == null ? "" : String(s);
		return d.innerHTML;
	}

	var EMPTY_TEXT = {
		protocol: "No routes imported from this peer.",
		export: "No routes exported to this peer.",
		noexport: "Nothing is rejected on export — everything eligible is announced.",
	};

	// renderRows returns the number of route rows drawn so the caller can
	// report an accurate "rows X–Y" range.
	function renderRows(tables) {
		var rows = [];
		(tables || []).forEach(function (t) {
			(t.Routes || []).forEach(function (r) {
				rows.push(
					"<tr>" +
					'<td class="mono">' + esc(r.Network) + "</td>" +
					"<td>" + esc(r.Type) + "</td>" +
					'<td class="mono">' + esc(r.Protocol) + "</td>" +
					'<td class="mono">' + esc(r.Since) + "</td>" +
					"<td>" + (r.Primary ? '<span class="badge badge-success">best</span>' : "") + "</td>" +
					'<td class="num mono">' + esc(r.Preference) + "</td>" +
					'<td class="mono">' + esc(r.ASPath) + "</td>" +
					'<td class="mono">' + esc(r.NextHop) + (r.From ? " (from " + esc(r.From) + ")" : "") + "</td>" +
					"</tr>"
				);
			});
		});
		var emptyText = state.offset > 0 ? "No more routes." : (EMPTY_TEXT[state.dir] || "No routes.");
		body.innerHTML = rows.length ? rows.join("") : '<tr><td colspan="8" class="empty">' + emptyText + "</td></tr>";
		return rows.length;
	}

	function setBusy(busy) {
		tabs.forEach(function (b) { b.disabled = busy; });
		prevBtn.disabled = busy || state.offset === 0;
		nextBtn.disabled = busy || !state.hasMore;
	}

	// The numbered page selector, mirroring the server-side pager partial. A BIRD
	// route table is never counted to draw one, so the last page is unknown: offer
	// the pages we can prove exist — everything up to the next one.
	function renderPages() {
		if (!pages) return;
		var cur = Math.floor(state.offset / LIMIT) + 1;
		var last = state.hasMore ? cur + 1 : cur;
		if (last <= 1) { pages.innerHTML = ""; return; }

		var want = {};
		want[1] = true;
		want[last] = true;
		for (var d = -2; d <= 2; d++) {
			var n = cur + d;
			if (n >= 1 && n <= last) want[n] = true;
		}
		var html = "", prev = 0;
		for (var i = 1; i <= last; i++) {
			if (!want[i]) continue;
			if (prev && i !== prev + 1) html += '<span class="pager-gap">&hellip;</span>';
			html += i === cur
				? '<span class="pager-page is-current" aria-current="page">' + i + "</span>"
				: '<button type="button" class="pager-page" data-page="' + i + '">' + i + "</button>";
			prev = i;
		}
		pages.innerHTML = html;
		pages.querySelectorAll("button[data-page]").forEach(function (b) {
			b.addEventListener("click", function () {
				state.offset = (parseInt(b.getAttribute("data-page"), 10) - 1) * LIMIT;
				load();
			});
		});
	}

	function load() {
		body.innerHTML = '<tr><td colspan="8" class="empty">Loading&hellip;</td></tr>';
		status.textContent = "";
		setBusy(true);
		var url = "/api/peers/" + encodeURIComponent(peer) + "/routes?dir=" + encodeURIComponent(state.dir) +
			"&offset=" + state.offset + "&limit=" + LIMIT;
		fetch(url, { credentials: "same-origin" })
			.then(function (r) { return r.json(); })
			.then(function (data) {
				if (data.err) {
					body.innerHTML = '<tr><td colspan="8" class="empty">' + esc(data.err) + "</td></tr>";
					state.hasMore = false;
					setBusy(false);
					renderPages();
					return;
				}
				var shown = renderRows(data.tables);
				state.hasMore = !!data.hasMore;
				setBusy(false);
				renderPages();
				status.textContent = shown > 0
					? "rows " + (state.offset + 1) + "–" + (state.offset + shown) + (state.hasMore ? " · more available" : "")
					: "no rows";
			})
			.catch(function () {
				body.innerHTML = '<tr><td colspan="8" class="empty">Failed to load routes. Retry with Prev/Next or reload the page.</td></tr>';
				setBusy(false);
			});
	}

	tabs.forEach(function (btn) {
		btn.addEventListener("click", function () {
			tabs.forEach(function (b) { b.classList.remove("active"); });
			btn.classList.add("active");
			state.dir = btn.getAttribute("data-dir");
			state.offset = 0;
			load();
		});
	});

	prevBtn.addEventListener("click", function () {
		state.offset = Math.max(0, state.offset - LIMIT);
		load();
	});
	nextBtn.addEventListener("click", function () {
		state.offset = state.offset + LIMIT;
		load();
	});

	load();
})();
