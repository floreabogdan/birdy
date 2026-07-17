(function () {
	document.addEventListener("submit", function (event) {
		var form = event.target.closest("form[data-confirm]");
		if (!form) return;
		if (!window.confirm(form.getAttribute("data-confirm"))) {
			event.preventDefault();
		}
	});

	document.addEventListener("click", function (event) {
		var confirmTarget = event.target.closest("[data-confirm]:not(form)");
		if (confirmTarget && !window.confirm(confirmTarget.getAttribute("data-confirm"))) {
			event.preventDefault();
			return;
		}
		var row = event.target.closest("[data-href]");
		if (!row || event.target.closest("a, button, input, select, textarea, label")) return;
		window.location.assign(row.getAttribute("data-href"));
	});

	document.addEventListener("keydown", function (event) {
		if (event.key !== "Enter" && event.key !== " ") return;
		var row = event.target.closest("[data-href]");
		if (!row) return;
		event.preventDefault();
		window.location.assign(row.getAttribute("data-href"));
	});
})();
