(function () {
	var selector = document.getElementById("instance-selector");
	var contextName = document.getElementById("router-context-name");
	var contextState = document.getElementById("router-context-state");
	var context = document.getElementById("router-context");
	var targetBanner = document.getElementById("target-banner");
	var targetBannerText = document.getElementById("target-banner-text");
	if (selector) {
		function loadInstances() {
		fetch("/api/instances", { credentials: "same-origin" })
			.then(function (r) { return r.ok ? r.json() : null; })
			.then(function (data) {
				if (!data) return;
				selector.innerHTML = "";
				var local = document.createElement("option");
				local.value = "0"; local.textContent = data.local.name || "This Birdy";
				selector.appendChild(local);
				var groups = {};
				(data.remote || []).forEach(function (item) {
					var group = item.group || "Other instances";
					if (!groups[group]) { groups[group] = document.createElement("optgroup"); groups[group].label = group; selector.appendChild(groups[group]); }
					var option = document.createElement("option");
					option.value = String(item.id);
					option.textContent = item.name + (item.status === "healthy" && item.latencyMS >= 0 ? " · " + item.latencyMS + " ms" : item.status === "offline" ? " · offline" : "");
					option.title = item.lastError || item.status || "not checked";
					groups[group].appendChild(option);
				});
				selector.value = String(data.selected || 0);
				var selected = null;
				if (String(data.selected || 0) === "0") {
					selected = { name: data.local.name || "This Birdy", status: "local" };
				} else {
					(data.remote || []).some(function (item) {
						if (String(item.id) !== String(data.selected)) return false;
						selected = item;
						return true;
					});
				}
				if (selected) {
					if (contextName) contextName.textContent = selected.name;
					if (contextState) contextState.textContent = selected.status === "local" ? "local router" : (selected.status || "not checked");
					if (context) context.className = "router-context status-" + (selected.status || "unknown");
					if (targetBanner) {
						targetBanner.hidden = selected.status === "local";
						if (targetBannerText && selected.status !== "local") targetBannerText.textContent = "Viewing remote instance " + selected.name + ". Management changes remain on the local Birdy.";
					}
				}
			})
			.catch(function () { selector.disabled = true; });
		}
		selector.addEventListener("change", function () {
			window.location.href = "/instances/select?id=" + encodeURIComponent(selector.value);
		});
		loadInstances();
		setInterval(loadInstances, 60000);
	}

	// ---- relative time ----
	// Elements carrying data-ts (RFC3339) get their text swapped to a
	// relative form ("2m ago") and kept fresh; the absolute time stays
	// available in the title tooltip set by the template.
	function relTime(iso) {
		var d = new Date(iso);
		if (isNaN(d.getTime())) return "";
		var s = Math.floor((Date.now() - d.getTime()) / 1000);
		if (s < 0) s = 0;
		if (s < 10) return "just now";
		if (s < 60) return s + "s ago";
		if (s < 3600) return Math.floor(s / 60) + "m ago";
		if (s < 86400) {
			var h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
			return m ? h + "h " + m + "m ago" : h + "h ago";
		}
		return Math.floor(s / 86400) + "d ago";
	}
	function refreshTimes() {
		document.querySelectorAll("[data-ts]").forEach(function (el) {
			var iso = el.getAttribute("data-ts");
			if (iso) el.textContent = relTime(iso);
		});
	}
	window.birdyRelTime = relTime;
	window.birdyRefreshTimes = refreshTimes;
	refreshTimes();
		setInterval(refreshTimes, 60000);

	// ---- page filter ----
	// Filters any element marked data-search-target by its direct children's
	// text content, scoped to whatever's on the current page (sessions table,
	// timeline entries, looking-glass results).
	var input = document.getElementById("topbar-search-input");
	if (input) {
		var applyFilter = function () {
			var q = input.value.trim().toLowerCase();
			document.querySelectorAll("[data-search-target]").forEach(function (target) {
				Array.prototype.forEach.call(target.children, function (row) {
					var text = row.textContent.toLowerCase();
					row.style.display = !q || text.indexOf(q) !== -1 ? "" : "none";
				});
			});
		};
		window.birdyApplyFilter = applyFilter;
		input.addEventListener("input", applyFilter);
		input.addEventListener("keydown", function (e) {
			if (e.key === "Escape") {
				input.value = "";
				applyFilter();
				input.blur();
			}
		});
		document.addEventListener("keydown", function (e) {
			if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return;
			var t = e.target;
			if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable)) return;
			e.preventDefault();
			input.focus();
		});
	}

	// ---- mobile navigation ----
	var navToggle = document.getElementById("nav-toggle");
	var scrim = document.getElementById("nav-scrim");
	if (navToggle) {
		navToggle.addEventListener("click", function () {
			document.body.classList.toggle("nav-open");
		});
	}
	if (scrim) {
		scrim.addEventListener("click", function () {
			document.body.classList.remove("nav-open");
		});
	}
	document.addEventListener("keydown", function (e) {
		if (e.key === "Escape") document.body.classList.remove("nav-open");
	});

	// ---- notification bell + BIRD connection dot ----
	// The bell is an unread-alerts counter: how many fault events are newer than
	// the highest event id this browser has already seen (SEEN_KEY in
	// localStorage). Opening the Timeline advances that marker (see timeline.js),
	// which clears the bell. The connection dot reflects the last poll outcome.
	var SEEN_KEY = "birdyAlertsSeen";
	var pill = document.getElementById("notif-pill");
	var connDot = document.getElementById("bird-conn");
	var connLabel = document.getElementById("bird-conn-label");
	function setConn(cls, text) {
		if (connDot) connDot.className = "conn-dot " + cls;
		if (connLabel) connLabel.textContent = text;
	}
	function getSeen() { return Number(localStorage.getItem(SEEN_KEY) || 0); }
	function setSeen(id) { try { localStorage.setItem(SEEN_KEY, String(id)); } catch (e) { /* private mode */ } }
	function poll() {
		if (selector && selector.value !== "0") {
			setConn("ok", "remote dashboard");
			return;
		}
		var hadMarker = localStorage.getItem(SEEN_KEY) !== null;
		fetch("/api/alerts/summary?since=" + getSeen(), { credentials: "same-origin" })
			.then(function (r) { return r.ok ? r.json() : null; })
			.then(function (data) {
				if (!data) { setConn("bad", "birdy unreachable"); return; }
				// First run in this browser: treat everything already on record as
				// seen, so the bell only lights for what happens from now on.
				if (!hadMarker) setSeen(data.latestEventId);
				if (pill) {
					// Re-read the marker: the Timeline page may have advanced it while
					// this request was in flight. If we have caught up to the latest
					// event, there is nothing unread regardless of the server's count.
					var unread = getSeen() >= data.latestEventId ? 0 : data.unread;
					if (unread > 0) {
						pill.textContent = unread > 99 ? "99+" : String(unread);
						pill.style.display = "";
					} else {
						pill.style.display = "none";
					}
				}
				setConn(data.pollOK ? "ok" : "bad", data.pollOK ? "BIRD connected" : "BIRD unreachable");
			})
			.catch(function () { setConn("bad", "birdy unreachable"); });
	}
	if (pill || connDot) {
		poll();
		setInterval(poll, 20000);
	}
})();

// Fill the avatar with the logged-in user's initial (it renders a generic user
// icon until this resolves, so it degrades gracefully without JS).
(function () {
	var avatar = document.querySelector("[data-avatar]");
	if (!avatar) return;
	fetch("/api/me", { credentials: "same-origin" })
		.then(function (r) { return r.ok ? r.json() : null; })
		.then(function (data) {
			if (!data || !data.username) return;
			avatar.textContent = data.username.trim().charAt(0).toUpperCase() || "?";
			var summary = avatar.closest("summary");
			if (summary) summary.title = data.username;
		})
		.catch(function () {});
})();

// The profile menu is a bare <details>; close it on an outside click or Escape
// so it doesn't sit open over other controls once dismissed elsewhere.
(function () {
	var menu = document.querySelector("details.profile-menu");
	if (!menu) return;
	document.addEventListener("click", function (event) {
		if (menu.open && !menu.contains(event.target)) menu.open = false;
	});
	document.addEventListener("keydown", function (event) {
		if (event.key === "Escape" && menu.open) {
			menu.open = false;
			var summary = menu.querySelector("summary");
			if (summary) summary.focus();
		}
	});
})();
