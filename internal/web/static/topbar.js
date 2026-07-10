(function () {
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
	setInterval(refreshTimes, 30000);

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
	// Real state polled lightly on every authenticated page: count of
	// currently-down sessions, and whether the last BIRD poll succeeded.
	var pill = document.getElementById("notif-pill");
	var connDot = document.getElementById("bird-conn");
	var connLabel = document.getElementById("bird-conn-label");
	function setConn(cls, text) {
		if (connDot) connDot.className = "conn-dot " + cls;
		if (connLabel) connLabel.textContent = text;
	}
	function poll() {
		fetch("/api/alerts/summary", { credentials: "same-origin" })
			.then(function (r) { return r.ok ? r.json() : null; })
			.then(function (data) {
				if (!data) { setConn("bad", "birdy unreachable"); return; }
				if (pill) {
					if (data.downCount > 0) {
						pill.textContent = data.downCount > 99 ? "99+" : String(data.downCount);
						pill.style.display = "";
					} else {
						pill.style.display = "none";
					}
				}
				if (data.pollOK) {
					setConn("ok", "BIRD connected");
				} else {
					setConn("bad", "BIRD unreachable");
				}
			})
			.catch(function () { setConn("bad", "birdy unreachable"); });
	}
	if (pill || connDot) {
		poll();
		setInterval(poll, 10000);
	}
})();
