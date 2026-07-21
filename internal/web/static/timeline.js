// Timeline "unseen" highlighting. Each event row carries its monotonic id in
// data-event-id. Rows newer than the highest id this browser has already seen
// (SEEN_KEY, shared with the top-bar bell) get the is-new accent, and visiting
// this page advances the marker so the bell clears.
(function () {
	var SEEN_KEY = "birdyAlertsSeen";
	var items = document.querySelectorAll(".timeline-item[data-event-id]");
	if (!items.length) return;

	var seen = Number(localStorage.getItem(SEEN_KEY) || 0);
	var maxId = seen; // never regress the marker (e.g. when viewing an older page)
	items.forEach(function (el) {
		var id = Number(el.getAttribute("data-event-id"));
		if (id > seen) el.classList.add("is-new");
		if (id > maxId) maxId = id;
	});

	// Everything shown is now seen. Advance the marker and clear the bell so it
	// does not have to wait for its next 20s poll to catch up.
	try { localStorage.setItem(SEEN_KEY, String(maxId)); } catch (e) { /* private mode */ }
	var pill = document.getElementById("notif-pill");
	if (pill) pill.style.display = "none";
})();
