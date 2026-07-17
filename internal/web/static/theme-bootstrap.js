(function () {
	try {
		var saved = localStorage.getItem("birdy-theme");
		if (saved === "light" || saved === "dark") {
			document.documentElement.setAttribute("data-theme", saved);
		}
	} catch (_) {
		// Storage can be unavailable in hardened/private browser contexts. The
		// CSS system preference remains a complete fallback.
	}
})();
