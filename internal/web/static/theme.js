// Theme controls: the top-bar light/dark toggle and the Settings accent + mode
// pickers. Every change applies instantly to <html>, updates the birdy_theme
// cookie so a reload before the round-trip is consistent, and POSTs to persist
// the preference on the user (the DB is the source of truth).
(function () {
	var el = document.documentElement;

	function currentMode() {
		var m = el.getAttribute("data-theme");
		return m === "light" || m === "dark" ? m : "system";
	}
	function effectiveDark() {
		var m = el.getAttribute("data-theme");
		if (m === "dark") return true;
		if (m === "light") return false;
		return !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
	}
	function currentAccent() { return el.getAttribute("data-theme-accent") || "green"; }

	function writeCookie(mode, accent) {
		try { document.cookie = "birdy_theme=" + encodeURIComponent(mode + "." + accent) + "; path=/; max-age=31536000; samesite=lax"; } catch (_) {}
	}
	function persist(url, params) {
		fetch(url, {
			method: "POST", credentials: "same-origin",
			headers: { "Content-Type": "application/x-www-form-urlencoded", "X-Requested-With": "fetch" },
			body: new URLSearchParams(params).toString(),
		}).catch(function () { /* the attribute + cookie already applied; persistence retries next change */ });
	}

	function applyMode(mode) {
		if (mode === "light" || mode === "dark") el.setAttribute("data-theme", mode);
		else el.removeAttribute("data-theme");
		writeCookie(mode, currentAccent());
		persist("/settings/theme/mode", { mode: mode });
		sync();
	}
	function applyAccent(accent) {
		if (accent && accent !== "green") el.setAttribute("data-theme-accent", accent);
		else el.removeAttribute("data-theme-accent");
		writeCookie(currentMode(), accent);
		persist("/settings/theme", { accent: accent });
		sync();
	}

	// Top-bar sun button (and the mirror button on the Theme tab): flip between
	// light and dark, resolving "system" to whatever it currently shows.
	var btn = document.getElementById("theme-toggle");
	function updateButton() {
		if (!btn) return;
		var dark = effectiveDark();
		btn.setAttribute("aria-pressed", dark ? "true" : "false");
		var next = dark ? "light" : "dark";
		btn.setAttribute("aria-label", "Switch to " + next + " mode");
		btn.setAttribute("title", "Switch to " + next + " mode");
	}
	function flip() { applyMode(effectiveDark() ? "light" : "dark"); }
	if (btn) btn.addEventListener("click", flip);
	var tabToggle = document.getElementById("theme-tab-toggle");
	if (tabToggle) tabToggle.addEventListener("click", flip);

	// Settings pickers.
	var modeChoices = document.querySelectorAll("[data-mode-choice]");
	var accentChoices = document.querySelectorAll("[data-accent-choice]");
	function sync() {
		modeChoices.forEach(function (c) { c.checked = c.value === currentMode(); });
		accentChoices.forEach(function (c) { c.checked = c.value === currentAccent(); });
		updateButton();
	}
	modeChoices.forEach(function (c) { c.addEventListener("change", function () { if (c.checked) applyMode(c.value); }); });
	accentChoices.forEach(function (c) { c.addEventListener("change", function () { if (c.checked) applyAccent(c.value); }); });

	// Keep the toggle glyph honest when the OS flips while in "system" mode.
	if (window.matchMedia) {
		var mq = window.matchMedia("(prefers-color-scheme: dark)");
		if (mq.addEventListener) mq.addEventListener("change", function () { if (currentMode() === "system") updateButton(); });
	}

	sync();
})();
