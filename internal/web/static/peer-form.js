// Role-dependent fields. iBGP peers take next-hop-self and route reflection;
// eBGP peers take policy chains and the AS-path checks. Neither set makes sense
// for the other, so the form shows one and disables the other — a disabled input
// is not submitted, which is what keeps an iBGP peer from arriving at the server
// carrying policy chains it cannot have.
(function () {
	var role = document.getElementById("role");
	if (!role) return;

	var blocks = document.querySelectorAll("[data-role-only]");

	function apply() {
		var want = role.value === "ibgp" ? "ibgp" : "ebgp";
		blocks.forEach(function (el) {
			var show = el.getAttribute("data-role-only") === want;
			el.hidden = !show;
			el.querySelectorAll("input, select, textarea, button").forEach(function (f) {
				f.disabled = !show;
			});
		});
	}

	role.addEventListener("change", apply);
	apply();
})();

(function () {
	var preset = document.getElementById("peer-preset");
	var applyButton = document.getElementById("apply-peer-preset");
	var form = document.querySelector("form[data-preview-url='/peers/preview']");
	if (!preset || !applyButton || !form) return;

	function field(name) { return form.elements.namedItem(name); }
	function setCheck(name, value) {
		var el = field(name);
		if (el) el.checked = value;
	}
	function dispatch(el) {
		if (!el) return;
		el.dispatchEvent(new Event("change", { bubbles: true }));
		el.dispatchEvent(new Event("input", { bubbles: true }));
	}
	var profiles = {
		"transit": { role: "upstream", limit: 1000000, first: true, origin: false, bgpRole: true, gtsm: true },
		"route-server": { role: "ix_peer", limit: 250000, first: false, origin: false, bgpRole: false, gtsm: true },
		"pni": { role: "ix_peer", limit: 100000, first: true, origin: false, bgpRole: true, gtsm: true },
		"customer": { role: "customer", limit: 100000, first: true, origin: true, bgpRole: true, gtsm: true },
		"ibgp": { role: "ibgp", limit: 0, first: false, origin: false, bgpRole: false, gtsm: false }
	};
	applyButton.addEventListener("click", function () {
		var profile = profiles[preset.value];
		if (!profile) return;
		field("role").value = profile.role;
		field("importLimit").value = String(profile.limit);
		field("importLimitAction").value = profile.limit ? "restart" : "warn";
		setCheck("enforceFirstAs", profile.first);
		setCheck("originPeerOnly", profile.origin);
		setCheck("bgpRole", profile.bgpRole);
		setCheck("gtsm", profile.gtsm);
		setCheck("nextHopSelf", profile.role === "ibgp");
		setCheck("rrClient", false);
		dispatch(field("role"));
		dispatch(field("importLimit"));
		applyButton.textContent = "Profile applied";
		window.setTimeout(function () { applyButton.textContent = "Apply profile"; }, 1600);
	});
})();

(function () {
	var form = document.querySelector("form[data-preview-url='/peers/preview']");
	var panel = document.getElementById("peer-readiness");
	if (!form || !panel) return;
	function named(name) { return form.elements.namedItem(name); }
	function hasPolicy(name) {
		var fields = form.querySelectorAll('[name="' + name + '"]');
		return Array.prototype.some.call(fields, function (field) { return !field.disabled && field.value; });
	}
	function check() {
		var role = named("role").value;
		var identity = named("name").value.trim() && named("neighborIp").value.trim() && Number(named("remoteAsn").value) > 0;
		var policy = role === "ibgp" || hasPolicy("importPolicyIds");
		var limit = role === "ibgp" || Number(named("importLimit").value) > 0;
		var transport = role === "ibgp" || ["enforceFirstAs", "bgpRole", "gtsm"].some(function (name) {
			var control = named(name);
			return control && control.checked;
		});
		var state = { identity: !!identity, policy: !!policy, limit: !!limit, transport: !!transport };
		var complete = Object.keys(state).filter(function (key) { return state[key]; }).length;
		panel.querySelectorAll("[data-ready-check]").forEach(function (item) {
			item.classList.toggle("is-ready", state[item.getAttribute("data-ready-check")]);
		});
		panel.classList.toggle("is-ready", complete === 4);
		document.getElementById("peer-readiness-title").textContent = complete === 4 ? "Ready to save" : complete + " of 4 checks ready";
		document.getElementById("peer-readiness-copy").textContent = complete === 4
			? "Saving updates Birdy's model. Validate the generated config before applying."
			: "Complete the unchecked safeguards before relying on this session.";
	}
	form.addEventListener("input", check);
	form.addEventListener("change", check);
	var observer = new MutationObserver(check);
	form.querySelectorAll(".chain-rows").forEach(function (rows) { observer.observe(rows, { childList: true }); });
	check();
})();
