(function () {
	var panel = document.getElementById("command-palette");
	var toggle = document.getElementById("command-palette-toggle");
	if (!panel || !toggle) return;
	var input = panel.querySelector("input");
	var items = Array.prototype.slice.call(panel.querySelectorAll("a[data-command]"));
	var active = -1;

	function close() { panel.hidden = true; document.body.classList.remove("palette-open"); active = -1; toggle.focus(); }
	function open() { panel.hidden = false; document.body.classList.add("palette-open"); input.value = ""; filter(); input.focus(); }
	function filter() {
		var query = input.value.trim().toLowerCase();
		items.forEach(function (item) { item.hidden = query && item.textContent.toLowerCase().indexOf(query) === -1; });
		active = -1;
		items.forEach(function (item) { item.classList.remove("is-active"); });
	}
	function move(delta) {
		var visible = items.filter(function (item) { return !item.hidden; });
		if (!visible.length) return;
		active = (active + delta + visible.length) % visible.length;
		items.forEach(function (item) { item.classList.remove("is-active"); });
		visible[active].classList.add("is-active");
		visible[active].scrollIntoView({ block: "nearest" });
	}
	toggle.addEventListener("click", open);
	input.addEventListener("input", filter);
	input.addEventListener("keydown", function (event) {
		if (event.key === "ArrowDown") { event.preventDefault(); move(1); }
		if (event.key === "ArrowUp") { event.preventDefault(); move(-1); }
		if (event.key === "Enter") {
			var visible = items.filter(function (item) { return !item.hidden; });
			if (visible.length) { event.preventDefault(); visible[Math.max(active, 0)].click(); }
		}
	});
	// Trap Tab within the dialog so focus can't wander onto the page behind the
	// modal; the input and the visible commands form the focus cycle.
	panel.addEventListener("keydown", function (event) {
		if (event.key !== "Tab") return;
		var focusables = [input].concat(items.filter(function (item) { return !item.hidden; }));
		var firstEl = focusables[0], lastEl = focusables[focusables.length - 1];
		if (event.shiftKey && document.activeElement === firstEl) { event.preventDefault(); lastEl.focus(); }
		else if (!event.shiftKey && document.activeElement === lastEl) { event.preventDefault(); firstEl.focus(); }
	});
	panel.addEventListener("click", function (event) { if (event.target === panel) close(); });
	document.addEventListener("keydown", function (event) {
		if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") { event.preventDefault(); open(); }
		if (event.key === "Escape" && !panel.hidden) { event.preventDefault(); close(); }
	});
	items.forEach(function (item) { item.addEventListener("click", close); });
})();
