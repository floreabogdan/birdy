// Ordered policy chains on the peer form.
//
// The chain order is simply the document order of the <select> elements: a form
// posts repeated fields in the order they appear, and Go's r.Form preserves it.
// So reordering the DOM is all there is to reordering the chain — no hidden
// index fields to keep in sync.
//
// A chain with data-locked belongs to a peer linked to a template: its rows
// show the template's chain and nothing on it can be edited. The peer form
// script sets and clears the attribute and asks for a rebuild with "chain:set".
(function () {
	var chains = document.querySelectorAll(".chain");
	if (!chains.length) return;

	function refresh(chain) {
		var locked = chain.hasAttribute("data-locked");
		var rows = chain.querySelectorAll(".chain-row");
		rows.forEach(function (row, i) {
			var pos = row.querySelector(".chain-pos");
			if (pos) pos.textContent = i + 1;
			row.querySelector('[data-move="up"]').disabled = locked || i === 0;
			row.querySelector('[data-move="down"]').disabled = locked || i === rows.length - 1;
			row.querySelector("[data-remove]").disabled = locked;
			row.querySelector("select").disabled = locked;
		});
		var add = chain.querySelector("[data-add]");
		if (add) add.disabled = locked;
		var empty = chain.querySelector(".chain-empty");
		if (empty) empty.classList.toggle("is-hidden", rows.length > 0);
	}

	// Adding, removing or reordering a row mutates the DOM directly, which fires
	// no input or change event of its own. Emit a bubbling change so everything
	// listening on the form reacts — most importantly the live preview, which
	// would otherwise keep showing the pre-edit chain (a removed policy lingers).
	function notify(chain) {
		chain.dispatchEvent(new Event("change", { bubbles: true }));
	}

	chains.forEach(function (chain) {
		var rowsBox = chain.querySelector(".chain-rows");
		var tmpl = chain.querySelector("template");

		chain.querySelector("[data-add]").addEventListener("click", function () {
			if (!tmpl || !tmpl.content.firstElementChild) return;
			rowsBox.appendChild(tmpl.content.firstElementChild.cloneNode(true));
			refresh(chain);
			notify(chain);
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
			notify(chain);
		});

		// "chain:set" with ids rebuilds the rows to that chain (a template being
		// linked); without ids it only re-applies the locked state. No notify in
		// either case: the caller's own change event is still on its way to the
		// form, carrying the new state with it.
		chain.addEventListener("chain:set", function (e) {
			var ids = e.detail && e.detail.ids;
			if (ids && tmpl && tmpl.content.firstElementChild) {
				rowsBox.innerHTML = "";
				ids.forEach(function (id) {
					var row = tmpl.content.firstElementChild.cloneNode(true);
					var sel = row.querySelector("select");
					sel.value = String(id);
					// A policy the picker does not offer (deleted, or the wrong
					// direction) cannot be shown; the server renders the truth on reload.
					if (sel.value !== String(id)) return;
					rowsBox.appendChild(row);
				});
			}
			refresh(chain);
		});

		// Initial refresh only — no notify: the server-rendered preview already
		// matches the chain on load, so firing here would just cost a fetch.
		refresh(chain);
	});
})();
