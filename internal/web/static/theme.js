(function () {
	var btn = document.getElementById("theme-toggle");
	if (!btn) return;
	btn.addEventListener("click", function () {
		var cur = document.documentElement.getAttribute("data-theme");
		var next = cur === "dark" ? "light" : "dark";
		document.documentElement.setAttribute("data-theme", next);
		localStorage.setItem("birdy-theme", next);
	});
})();
