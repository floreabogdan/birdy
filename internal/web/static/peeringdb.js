// Pre-fill a peer from PeeringDB. The button asks the server (which dials
// PeeringDB, so no CORS or key handling here), then fills the name, description
// and the import limit for the session's address family — leaving the operator
// to confirm and adjust. The AS-SET, if any, is shown as a hint to build an AS
// set from, since birdy has no place to auto-import it yet.
(function () {
	var btn = document.getElementById("pdb-lookup");
	if (!btn) return;
	var asn = document.getElementById("remoteAsn");
	var status = document.getElementById("pdb-status");

	function show(msg, isErr) {
		status.hidden = false;
		status.textContent = msg;
		status.className = isErr ? "field-error" : "hint";
	}

	function fillIfEmpty(id, value) {
		var el = document.getElementById(id);
		if (el && value && !el.value) el.value = value;
	}

	btn.addEventListener("click", function () {
		var n = parseInt(asn.value, 10);
		if (!n) { show("Enter an AS number first.", true); return; }
		btn.disabled = true;
		show("Looking up AS" + n + " in PeeringDB…", false);
		fetch("/api/peeringdb/" + n, { credentials: "same-origin" })
			.then(function (r) { return r.json(); })
			.then(function (d) {
				btn.disabled = false;
				if (d.err) { show(d.err, true); return; }
				fillIfEmpty("name", d.name);
				fillIfEmpty("description", d.description);
				// Import limit: pick the family from the neighbor address if set,
				// else prefer the v4 count.
				var ip = (document.getElementById("neighborIp") || {}).value || "";
				var v6 = ip.indexOf(":") >= 0;
				var limit = v6 ? d.maxPrefixV6 : d.maxPrefixV4;
				var limitEl = document.getElementById("importLimit");
				if (limitEl && limit && (!limitEl.value || limitEl.value === "0")) limitEl.value = limit;
				var msg = "Filled from PeeringDB: " + (d.description || d.name);
				if (d.asSet) msg += " · AS-SET " + d.asSet + " — build an AS set from it under Library.";
				show(msg, false);
				document.querySelector("form[data-preview-url]") && document.getElementById("name").dispatchEvent(new Event("input", { bubbles: true }));
			})
			.catch(function () { btn.disabled = false; show("Lookup failed.", true); });
	});
})();
