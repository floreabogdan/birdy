// Ordered policy chains on the peer form.
//
// The chain order is simply the document order of the <select> elements: a form
// posts repeated fields in the order they appear, and Go's r.Form preserves it.
// So reordering the DOM is all there is to reordering the chain — no hidden
// index fields to keep in sync.
(function () {
	var chains = document.querySelectorAll(".chain");
	if (!chains.length) return;

	function refresh(chain) {
		var rows = chain.querySelectorAll(".chain-row");
		rows.forEach(function (row, i) {
			var pos = row.querySelector(".chain-pos");
			if (pos) pos.textContent = i + 1;
			row.querySelector('[data-move="up"]').disabled = i === 0;
			row.querySelector('[data-move="down"]').disabled = i === rows.length - 1;
		});
		var empty = chain.querySelector(".chain-empty");
		if (empty) empty.classList.toggle("is-hidden", rows.length > 0);
	}

	chains.forEach(function (chain) {
		var rowsBox = chain.querySelector(".chain-rows");
		var tmpl = chain.querySelector("template");

		chain.querySelector("[data-add]").addEventListener("click", function () {
			if (!tmpl || !tmpl.content.firstElementChild) return;
			rowsBox.appendChild(tmpl.content.firstElementChild.cloneNode(true));
			refresh(chain);
		});

		chain.addEventListener("click", function (e) {
			var btn = e.target.closest("button");
			if (!btn || !rowsBox.contains(btn)) return;
			var row = btn.closest(".chain-row");

			if (btn.hasAttribute("data-remove")) {
				row.remove();
			} else if (btn.dataset.move === "up" && row.previousElementSibling) {
				rowsBox.insertBefore(row, row.previousElementSibling);
			} else if (btn.dataset.move === "down" && row.nextElementSibling) {
				rowsBox.insertBefore(row.nextElementSibling, row);
			} else {
				return;
			}
			refresh(chain);
		});

		refresh(chain);
	});
})();
