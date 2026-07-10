// Live BIRD-code preview. A form with data-preview-url is POSTed (debounced) on
// every edit, and the returned code, error and lint findings replace the
// server-rendered pane — so you see the config a change produces before saving.
//
// It degrades safely: the page already renders a correct preview server-side, so
// if the fetch fails or JS is off, the last good preview simply stays put.
(function () {
	var form = document.querySelector("form[data-preview-url]");
	if (!form) return;
	var url = form.getAttribute("data-preview-url");
	var body = document.querySelector("[data-preview-body]");
	var warnBox = document.querySelector("[data-preview-warnings]");
	if (!body) return;

	function esc(s) {
		var d = document.createElement("div");
		d.textContent = s == null ? "" : String(s);
		return d.innerHTML;
	}

	function renderCode(data) {
		if (data.err) {
			body.innerHTML = '<div class="card-body text-muted">' + esc(data.err) + "</div>";
		} else {
			body.innerHTML = '<pre class="code-block">' + esc(data.preview) + "</pre>";
		}
	}

	function renderWarnings(warnings) {
		if (!warnBox) return;
		if (!warnings || !warnings.length) {
			warnBox.hidden = true;
			warnBox.innerHTML = "";
			return;
		}
		var items = warnings.map(function (w) {
			var peer = w.Peer ? '<span class="chip">' + esc(w.Peer) + "</span> " : "";
			return '<li class="lint-' + esc(w.Severity) + '">' + peer + esc(w.Message) + "</li>";
		});
		warnBox.innerHTML =
			'<div class="card panel-warning"><div class="panel-head">' +
			"Review before you apply this</div><div class=\"card-body\"><ul class=\"lint-list\">" +
			items.join("") + "</ul></div></div>";
		warnBox.hidden = false;
	}

	var timer = null;
	var inflight = null;

	function run() {
		if (inflight) inflight.abort();
		var ctrl = new AbortController();
		inflight = ctrl;
		fetch(url, {
			method: "POST",
			credentials: "same-origin",
			headers: { "Content-Type": "application/x-www-form-urlencoded" },
			body: new URLSearchParams(new FormData(form)).toString(),
			signal: ctrl.signal,
		})
			.then(function (r) { return r.json(); })
			.then(function (data) {
				inflight = null;
				renderCode(data);
				renderWarnings(data.warnings);
			})
			.catch(function (e) {
				inflight = null;
				if (e && e.name === "AbortError") return;
				// Leave the last good preview in place on a transient error.
			});
	}

	function schedule() {
		clearTimeout(timer);
		timer = setTimeout(run, 350);
	}

	form.addEventListener("input", schedule);
	form.addEventListener("change", schedule);
})();
