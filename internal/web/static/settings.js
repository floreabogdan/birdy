(function () {
	var form = document.getElementById("restore-form");
	if (!form) return;
	form.addEventListener("submit", function (event) {
		event.preventDefault();
		var msg = document.getElementById("restore-msg");
		var data = new FormData(form);
		msg.textContent = "Uploading...";
		fetch("/api/snapshot/restore", { method: "POST", body: data, credentials: "same-origin" })
			.then(function (response) {
				return response.json().catch(function () { return {}; }).then(function (body) {
					return { ok: response.ok, body: body };
				});
			})
			.then(function (result) {
				msg.textContent = result.ok ? result.body.message : (result.body.error || "Restore failed.");
			})
			.catch(function () {
				msg.textContent = "Restore failed.";
			});
	});
})();
