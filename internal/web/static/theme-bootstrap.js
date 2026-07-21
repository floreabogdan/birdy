// Applies the operator's saved theme before first paint, from the birdy_theme
// cookie the server sets from the DB (value "<mode>.<accent>"). No localStorage:
// the preference lives on the user, and the cookie just carries it to the page.
(function () {
	try {
		var el = document.documentElement;
		el.setAttribute("data-theme-style", "modern"); // the only style now
		var m = document.cookie.match(/(?:^|;\s*)birdy_theme=([^;]+)/);
		var mode = "system", accent = "green";
		if (m) {
			var parts = decodeURIComponent(m[1]).split(".");
			if (parts[0]) mode = parts[0];
			if (parts[1]) accent = parts[1];
		}
		// system → no data-theme, so the prefers-color-scheme media query decides.
		if (mode === "light" || mode === "dark") el.setAttribute("data-theme", mode);
		else el.removeAttribute("data-theme");
		// green is the base :root, so it needs no attribute.
		if (accent && accent !== "green") el.setAttribute("data-theme-accent", accent);
		else el.removeAttribute("data-theme-accent");
	} catch (_) {
		// Storage/cookie access can be blocked; the CSS defaults are a full fallback.
	}
})();
