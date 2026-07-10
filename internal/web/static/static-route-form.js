// The next-hop field only applies to a "via" route. The discard actions
// (blackhole, unreachable, prohibit) take no next hop, so hide it and disable it
// — a disabled field is not submitted, so switching to blackhole and back never
// leaves a stale next hop behind.
(function () {
	var action = document.getElementById("action");
	var block = document.querySelector("[data-when-via]");
	if (!action || !block) return;

	function apply() {
		var via = action.value === "via";
		block.hidden = !via;
		block.querySelectorAll("input").forEach(function (f) { f.disabled = !via; });
	}

	action.addEventListener("change", apply);
	apply();
})();
