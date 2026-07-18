(function () {
	"use strict";

	var form = document.getElementById("instance-add-form");
	var filter = document.getElementById("instance-filter");
	var statusFilter = document.getElementById("instance-status-filter");
	var rows = Array.prototype.slice.call(document.querySelectorAll(".instances-list .instance-item"));
	function applyFilters() {
		var query = (filter ? filter.value : "").trim().toLowerCase();
		var wanted = statusFilter ? statusFilter.value : "all";
		rows.forEach(function (row) {
			var textMatch = !query || row.textContent.toLowerCase().indexOf(query) >= 0;
			var state = Array.prototype.slice.call(row.querySelectorAll(".instance-state"))[0];
			var statusMatch = wanted === "all" || (state && state.classList.contains(wanted));
			row.hidden = !(textMatch && statusMatch);
		});
	}
	if (filter) filter.addEventListener("input", applyFilters);
	if (statusFilter) statusFilter.addEventListener("change", applyFilters);
	var refresh = document.getElementById("refresh-instances");
	if (refresh) refresh.addEventListener("click", function () {
		refresh.disabled = true;
		refresh.textContent = "Refreshing...";
		fetch("/api/instances/refresh", { method: "POST", headers: { "Accept": "application/json" } })
			.then(function (response) { if (!response.ok) throw new Error("refresh failed"); return response.json(); })
			.then(function () { window.location.reload(); })
			.catch(function () { refresh.disabled = false; refresh.textContent = "Refresh failed"; setTimeout(function () { refresh.textContent = "Refresh health"; }, 2500); });
	});
	var testButton = document.getElementById("instance-test-button");
	var result = document.getElementById("instance-test-result");
	if (form && testButton && result) {
		testButton.addEventListener("click", function () {
			testButton.disabled = true;
			result.textContent = "Testing connection...";
			var body = new URLSearchParams(new FormData(form));
			fetch("/api/instances/test", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded", "Accept": "application/json" }, body: body })
				.then(function (response) { return response.json().then(function (data) { return { ok: response.ok, data: data }; }); })
				.then(function (reply) {
					result.textContent = reply.ok ? "Connection succeeded in " + reply.data.latencyMS + " ms." : (reply.data.error || "Connection failed.");
					result.classList.toggle("test-success", reply.ok);
					result.classList.toggle("test-error", !reply.ok);
				})
				.catch(function () { result.textContent = "Connection test failed."; result.classList.add("test-error"); })
				.finally(function () { testButton.disabled = false; });
		});
	}

	var activity = document.getElementById("fleet-activity-list");
	if (!activity) return;
	fetch("/api/instances/activity", { headers: { "Accept": "application/json" } })
		.then(function (response) { if (!response.ok) throw new Error("activity unavailable"); return response.json(); })
		.then(function (items) {
			activity.textContent = "";
			if (!items || !items.length) { activity.innerHTML = '<div class="empty">No recent activity.</div>'; return; }
			items.forEach(function (item) {
				var event = item.event || {};
				var row = document.createElement("div"); row.className = "activity-row";
				var main = document.createElement("div"); main.className = "activity-main";
				var title = document.createElement("strong"); title.textContent = item.instance; main.appendChild(title);
				var message = document.createElement("span"); message.textContent = event.message || event.kind || "Event"; main.appendChild(message);
				var time = document.createElement("time"); time.dateTime = event.ts || ""; time.textContent = event.ts ? new Date(event.ts).toLocaleString() : "";
				row.appendChild(main); row.appendChild(time); activity.appendChild(row);
			});
		})
		.catch(function () { activity.innerHTML = '<div class="empty">Activity is unavailable.</div>'; });
}());
