// Show the fields the chosen destination type needs. Webhook, Slack and Discord
// all take a URL; email takes the SMTP settings. Hidden inputs are disabled so
// they never submit — an email destination never carries a stale URL, and a
// Slack one never carries stale SMTP fields.
(function () {
	var type = document.getElementById("type");
	if (!type) return;
	var blocks = document.querySelectorAll("[data-alert-kind]");

	function apply() {
		var want = type.value === "email" ? "email" : "webhook";
		blocks.forEach(function (el) {
			var show = el.getAttribute("data-alert-kind") === want;
			el.hidden = !show;
			el.querySelectorAll("input, select").forEach(function (f) { f.disabled = !show; });
		});
	}

	type.addEventListener("change", apply);
	apply();
})();
