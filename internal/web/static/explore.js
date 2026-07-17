(function () {
	var type = document.getElementById("type");
	var label = document.getElementById("target-label");
	var target = document.getElementById("target");
	if (!type || !label || !target) return;
	var hints = {
		"for": ["Prefix or IP", "e.g. 192.0.2.0/24 or 8.8.8.8"],
		"protocol": ["Peer name", "e.g. edge_v4"],
		"export": ["Peer name", "e.g. edge_v4"],
		"noexport": ["Peer name", "e.g. edge_v4"]
	};
	function apply() {
		var hint = hints[type.value] || hints["for"];
		label.textContent = hint[0];
		target.placeholder = hint[1];
	}
	type.addEventListener("change", apply);
	apply();
})();
