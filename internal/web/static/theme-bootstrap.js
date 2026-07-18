(function () {
	try {
		var style = localStorage.getItem("birdy-theme-style");
		var pageStyle = document.documentElement.getAttribute("data-theme-style");
		var initialStyle = style === "original" || style === "modern" ? style : (pageStyle === "original" || pageStyle === "modern" ? pageStyle : "modern");
		document.documentElement.setAttribute("data-theme-style", initialStyle);
		var saved = localStorage.getItem("birdy-theme");
		if (saved === "light" || saved === "dark") {
			document.documentElement.setAttribute("data-theme", saved);
		}
	} catch (_) {
		// Storage can be unavailable in hardened/private browser contexts. The
		// CSS system preference remains a complete fallback.
	}
})();
