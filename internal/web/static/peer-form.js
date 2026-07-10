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
