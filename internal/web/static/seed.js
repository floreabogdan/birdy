// Import-from-BIRD page. Two conveniences for the thirty-sessions case: one
// select that sets the template on every checked row, and a row whose
// template is set greys its role select, because the template decides the role.
(function () {
	var all = document.getElementById("seed-template-all");
	var rows = document.querySelectorAll("[data-seed-template]");
	if (!rows.length) return;

	function sync(sel) {
		var row = sel.closest("tr");
		var role = row && row.querySelector("[data-seed-role]");
		if (role) role.disabled = !!sel.value;
	}
	rows.forEach(function (sel) {
		sel.addEventListener("change", function () { sync(sel); });
		sync(sel);
	});

	if (all) {
		all.addEventListener("change", function () {
			if (!all.value) return;
			var value = all.value === "-" ? "" : all.value;
			rows.forEach(function (sel) {
				var row = sel.closest("tr");
				var include = row && row.querySelector('input[name="include"]');
				if (include && !include.checked) return;
				sel.value = value;
				sync(sel);
			});
			all.value = "";
		});
	}
})();
