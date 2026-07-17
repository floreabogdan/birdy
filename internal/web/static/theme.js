(function () {
	var btn = document.getElementById("theme-toggle");
	if (!btn) return;

	function effectiveTheme() {
		var selected = document.documentElement.getAttribute("data-theme");
		if (selected === "light" || selected === "dark") return selected;
		return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
	}

	function updateButton() {
		var current = effectiveTheme();
		var next = current === "dark" ? "light" : "dark";
		btn.setAttribute("aria-pressed", current === "dark" ? "true" : "false");
		btn.setAttribute("aria-label", "Switch to " + next + " theme");
		btn.setAttribute("title", "Switch to " + next + " theme");
	}

	btn.addEventListener("click", function () {
		var cur = effectiveTheme();
		var next = cur === "dark" ? "light" : "dark";
		document.documentElement.setAttribute("data-theme", next);
		try {
			localStorage.setItem("birdy-theme", next);
		} catch (_) {
			// The theme still applies for this page when storage is unavailable.
		}
		updateButton();
	});
	updateButton();
})();
