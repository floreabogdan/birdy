// Page tabs. The server already rendered every panel and marked the inactive
// ones hidden, so this only swaps which one is visible and keeps the URL in
// step. With JavaScript off the anchors still work — they reload with ?tab=.
(function () {
	var bar = document.querySelector("[data-tabs]");
	if (!bar) return;

	var tabs = bar.querySelectorAll("a[data-tab]");
	var panels = document.querySelectorAll("[data-tab-panel]");

	function show(name) {
		tabs.forEach(function (t) {
			t.setAttribute("aria-selected", t.getAttribute("data-tab") === name ? "true" : "false");
		});
		panels.forEach(function (p) {
			p.hidden = p.getAttribute("data-tab-panel") !== name;
		});
	}

	tabs.forEach(function (tab) {
		tab.addEventListener("click", function (e) {
			// Let the browser handle a modified click: the href is a real URL.
			if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
			e.preventDefault();
			var name = tab.getAttribute("data-tab");
			show(name);
			history.pushState({ tab: name }, "", tab.getAttribute("href"));
		});
	});

	window.addEventListener("popstate", function () {
		var name = new URLSearchParams(location.search).get("tab");
		var known = bar.querySelector('a[data-tab="' + name + '"]');
		show(known ? name : tabs[0].getAttribute("data-tab"));
	});
})();
