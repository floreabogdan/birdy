// Countdown on a pending apply. The server rendered the seconds left; this just
// ticks it down so the operator can see the window closing. When it reaches
// zero, BIRD has reverted (or is about to) — reload so the page reflects the
// resolved state rather than a stale "pending".
(function () {
	var panel = document.getElementById("apply-pending");
	var out = document.getElementById("apply-countdown");
	if (!panel || !out) return;

	var left = parseInt(panel.getAttribute("data-secs"), 10);
	if (isNaN(left)) return;

	var timer = setInterval(function () {
		left -= 1;
		if (left <= 0) {
			clearInterval(timer);
			out.textContent = "0";
			// Give BIRD a moment to finish reverting, then show the real state.
			setTimeout(function () { location.reload(); }, 1500);
			return;
		}
		out.textContent = String(left);
	}, 1000);
})();
