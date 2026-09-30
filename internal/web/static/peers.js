// Peers list: the bulk "attach to template" bar. Row checkboxes belong to the
// bar's form through their form= attribute (the table's own per-row forms rule
// out nesting), so all this script adds is a select-all box and a live count.
(function () {
	var form = document.getElementById("bulk-attach");
	if (!form) return;
	var all = document.querySelector("[data-check-all]");
	var boxes = Array.prototype.slice.call(document.querySelectorAll('input[name="peer"][form="bulk-attach"]'));
	var count = form.querySelector("[data-selected-count]");
	var submit = form.querySelector('button[type="submit"]');

	function refresh() {
		var n = boxes.filter(function (b) { return b.checked; }).length;
		if (count) count.textContent = n ? n + " selected" : "none selected";
		if (submit) submit.disabled = n === 0;
		if (all) {
			all.checked = n > 0 && n === boxes.length;
			all.indeterminate = n > 0 && n < boxes.length;
		}
	}
	boxes.forEach(function (b) { b.addEventListener("change", refresh); });
	if (all) {
		all.addEventListener("change", function () {
			boxes.forEach(function (b) { b.checked = all.checked; });
			refresh();
		});
	}
	refresh();
})();
