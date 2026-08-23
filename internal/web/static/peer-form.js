// The peer form, and the template editor that reuses it.
//
// Two facts decide whether a control is live: the role (iBGP peers take
// next-hop-self and route reflection, eBGP peers take the AS-path checks and
// export transforms — neither set makes sense for the other) and, on a peer
// linked to a template, inheritance (every governed control shows the
// template's value and is disabled). A disabled input is not submitted, which
// is what keeps an iBGP peer from arriving at the server carrying eBGP knobs —
// and a linked peer from posting governed values at all. The server re-applies
// the template on save regardless; this script only shows what will happen.
(function () {
	var form = document.querySelector("form[data-peer-form]");
	if (!form) return;
	var role = form.elements.namedItem("role");
	var templateSelect = form.elements.namedItem("templateId"); // absent on the template editor
	var isTemplateForm = form.hasAttribute("data-template-form");
	var blocks = form.querySelectorAll("[data-role-only]");

	// The templates' governed values, keyed by id, embedded by the server.
	var templates = {};
	var dataEl = document.getElementById("peer-templates-data");
	if (dataEl) {
		try { templates = JSON.parse(dataEl.textContent) || {}; } catch (e) { templates = {}; }
	}

	function inherited() { return !!(templateSelect && templateSelect.value); }
	function field(name) { return form.elements.namedItem(name); }
	function controlsIn(el) {
		if (el.matches("input, select, textarea, button")) return [el];
		return Array.prototype.slice.call(el.querySelectorAll("input, select, textarea, button"));
	}
	function isGoverned(f) { return f.hasAttribute("data-governed") || !!f.closest("[data-governed]"); }
	function dispatch(el) {
		if (!el) return;
		el.dispatchEvent(new Event("change", { bubbles: true }));
		el.dispatchEvent(new Event("input", { bubbles: true }));
	}

	// refresh sets every control's disabled state from the two facts above, and
	// shows or hides the "inherited from" notes.
	function refresh() {
		var want = role.value === "ibgp" ? "ibgp" : "ebgp";
		var inh = inherited();
		blocks.forEach(function (el) {
			var show = el.getAttribute("data-role-only") === want;
			el.hidden = !show;
			controlsIn(el).forEach(function (f) { f.disabled = !show || (inh && isGoverned(f)); });
		});
		form.querySelectorAll("[data-governed]").forEach(function (el) {
			if (el.closest("[data-role-only]")) return; // handled with its role block above
			if (el.classList.contains("chain")) {
				// The chain script owns its rows; tell it whether they are locked.
				if (inh) el.setAttribute("data-locked", ""); else el.removeAttribute("data-locked");
				el.dispatchEvent(new CustomEvent("chain:set"));
				return;
			}
			el.disabled = inh;
		});
		form.querySelectorAll("[data-inherit-note]").forEach(function (n) { n.hidden = !inh; });
		var assistant = document.getElementById("peer-setup-assistant");
		if (assistant) assistant.hidden = inh;
	}

	// fill writes a template's values into the governed controls. Chains are
	// rebuilt by the chain script from the template's policy ids.
	function fill(tpl) {
		Object.keys(tpl).forEach(function (key) {
			if (key === "name" || key === "importPolicyIds" || key === "exportPolicyIds") return;
			var f = field(key);
			if (!f || typeof f.type !== "string") return;
			if (f.type === "checkbox") f.checked = !!tpl[key]; else f.value = String(tpl[key]);
		});
		["importPolicyIds", "exportPolicyIds"].forEach(function (name) {
			var chain = form.querySelector('.chain[data-name="' + name + '"]');
			if (chain) chain.dispatchEvent(new CustomEvent("chain:set", { detail: { ids: tpl[name] || [] } }));
		});
		form.querySelectorAll("[data-inherit-link]").forEach(function (a) {
			a.textContent = tpl.name;
			a.setAttribute("href", "/peers/templates/" + encodeURIComponent(tpl.name) + "/edit");
		});
	}

	if (templateSelect) {
		// Listening on the select itself, not the form: this runs before the
		// form-level listeners (the live preview, the readiness strip) see the
		// same change event, so by the time they read the form the template's
		// values are in place. Choosing "none" leaves the values where they are
		// and re-enables the controls — the peer now owns what it inherited.
		templateSelect.addEventListener("change", function () {
			var tpl = templates[templateSelect.value];
			if (tpl) fill(tpl);
			refresh();
		});
	}
	role.addEventListener("change", refresh);
	refresh();

	// Setup assistant: one-time presets for the transport safeguards and limits.
	var preset = document.getElementById("peer-preset");
	var applyButton = document.getElementById("apply-peer-preset");
	if (preset && applyButton) {
		function setCheck(name, value) {
			var el = field(name);
			if (el) el.checked = value;
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
			if (!profile || inherited()) return;
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
	}

	// Readiness strip: the four things a session needs before it is worth
	// saving. A template supplies policy, limit and transport for a linked peer.
	var panel = document.getElementById("peer-readiness");
	if (panel && !isTemplateForm) {
		function hasPolicy(name) {
			var fields = form.querySelectorAll('[name="' + name + '"]');
			return Array.prototype.some.call(fields, function (f) { return f.value && (!f.disabled || inherited()); });
		}
		function check() {
			var r = field("role").value;
			var inh = inherited();
			var identity = field("name").value.trim() && field("neighborIp").value.trim() && Number(field("remoteAsn").value) > 0;
			var policy = r === "ibgp" || hasPolicy("importPolicyIds");
			var limit = r === "ibgp" || Number(field("importLimit").value) > 0;
			var transport = r === "ibgp" || inh || ["enforceFirstAs", "bgpRole", "gtsm"].some(function (name) {
				var control = field(name);
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
	}
})();
