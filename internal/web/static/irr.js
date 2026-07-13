// Refresh a set from an IRR AS-SET. The button asks the server (which runs
// bgpq4), then drops the expansion into the entries box for review — nothing is
// saved until the operator clicks save, and the live preview updates so they see
// exactly what the filter becomes.
//
// It serves both library forms: a prefix set expands to prefixes (per address
// family), an AS set to member AS numbers. The button says which via data-*.
(function () {
	var btn = document.getElementById("irr-refresh");
	if (!btn) return;
	var endpoint = btn.dataset.irrEndpoint || "/api/irr/prefixes";
	var noun = btn.dataset.irrNoun || "prefixes";
	var source = document.getElementById("source");
	var family = document.getElementById("family");
	var entries = document.getElementById("entries");
	var status = document.getElementById("irr-status");

	function show(msg, isErr) {
		status.hidden = false;
		status.textContent = msg;
		status.className = isErr ? "field-error" : "hint";
	}

	btn.addEventListener("click", function () {
		var src = (source.value || "").trim();
		if (!src) { show("Enter an IRR AS-SET first.", true); return; }
		var url = endpoint + "?source=" + encodeURIComponent(src);
		if (family) url += "&family=" + encodeURIComponent(family.value);
		btn.disabled = true;
		show("Expanding " + src + " with bgpq4 — this can take a few seconds…", false);
		fetch(url, { credentials: "same-origin" })
			.then(function (r) { return r.json(); })
			.then(function (d) {
				btn.disabled = false;
				if (d.err) { show(d.err, true); return; }
				entries.value = d.entries;
				show("Filled " + d.count + " " + noun + " from " + src + ". Review, then save.", false);
				entries.dispatchEvent(new Event("input", { bubbles: true })); // refresh the live preview
			})
			.catch(function () { btn.disabled = false; show("Refresh failed.", true); });
	});
})();
