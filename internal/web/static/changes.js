// Copy the candidate config. The gutter numbers live in CSS ::before and never
// reach textContent, so what lands on the clipboard is the config exactly as
// birdy would write it.
(function () {
	var btn = document.getElementById("copy-config");
	var pre = document.getElementById("candidate-config");
	if (!btn || !pre) return;

	function text() {
		var lines = pre.querySelectorAll(".cl");
		return Array.prototype.map.call(lines, function (l) { return l.textContent; }).join("\n") + "\n";
	}

	function flash(msg) {
		var was = btn.textContent;
		btn.textContent = msg;
		setTimeout(function () { btn.textContent = was; }, 1600);
	}

	btn.addEventListener("click", function () {
		var cfg = text();
		if (navigator.clipboard && window.isSecureContext) {
			navigator.clipboard.writeText(cfg).then(function () { flash("Copied"); }, function () { flash("Press Ctrl+C"); });
			return;
		}
		// Plain http on a LAN address: the clipboard API is unavailable, so fall
		// back to selecting the config and letting the operator hit Ctrl+C.
		var sel = window.getSelection();
		sel.removeAllRanges();
		var range = document.createRange();
		range.selectNodeContents(pre);
		sel.addRange(range);
		flash(document.execCommand && document.execCommand("copy") ? "Copied" : "Press Ctrl+C");
	});
})();
