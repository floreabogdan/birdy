(function () {
	var sidebarCollapse = document.getElementById("sidebar-collapse");
	if (sidebarCollapse) {
		var collapsed = false;
		try { collapsed = localStorage.getItem("birdy-sidebar-collapsed") === "1"; } catch (_) {}
		function setSidebarCollapsed(value) {
			collapsed = value;
			document.body.classList.toggle("sidebar-collapsed", value);
			sidebarCollapse.setAttribute("aria-pressed", value ? "true" : "false");
			sidebarCollapse.setAttribute("aria-label", value ? "Expand navigation" : "Collapse navigation");
			sidebarCollapse.title = value ? "Expand navigation" : "Collapse navigation";
			try { localStorage.setItem("birdy-sidebar-collapsed", value ? "1" : "0"); } catch (_) {}
		}
		setSidebarCollapsed(collapsed);
		sidebarCollapse.addEventListener("click", function () { setSidebarCollapsed(!collapsed); });
	}

	var compact = document.getElementById("compact-toggle");
	if (compact) {
		var enabled = false;
		try { enabled = localStorage.getItem("birdy-compact") === "1"; } catch (_) {}
		function setCompact(value) {
			enabled = value;
			document.body.classList.toggle("compact", value);
			compact.setAttribute("aria-pressed", value ? "true" : "false");
			compact.setAttribute("aria-label", value ? "Use comfortable layout" : "Use compact layout");
			compact.title = value ? "Use comfortable layout" : "Use compact layout";
			try { localStorage.setItem("birdy-compact", value ? "1" : "0"); } catch (_) {}
		}
		setCompact(enabled);
		compact.addEventListener("click", function () { setCompact(!enabled); });
	}

	document.addEventListener("submit", function (event) {
		var form = event.target.closest("form[data-confirm]");
		if (!form) return;
		if (!window.confirm(form.getAttribute("data-confirm"))) {
			event.preventDefault();
		}
	});

	document.addEventListener("click", function (event) {
		var confirmTarget = event.target.closest("[data-confirm]:not(form)");
		if (confirmTarget && !window.confirm(confirmTarget.getAttribute("data-confirm"))) {
			event.preventDefault();
			return;
		}
		var row = event.target.closest("[data-href]");
		if (!row || event.target.closest("a, button, input, select, textarea, label")) return;
		window.location.assign(row.getAttribute("data-href"));
	});

	document.addEventListener("keydown", function (event) {
		if (event.key !== "Enter" && event.key !== " ") return;
		var row = event.target.closest("[data-href]");
		if (!row) return;
		event.preventDefault();
		window.location.assign(row.getAttribute("data-href"));
	});

	document.querySelectorAll("form[data-editor]").forEach(function (form) {
		var dirty = false;
		var bar = document.createElement("div");
		bar.className = "unsaved-bar";
		bar.hidden = true;
		bar.innerHTML = '<span><strong>Unsaved changes</strong><small>Review your edits before leaving this page.</small></span><button class="btn btn-primary" type="submit">Save changes</button>';
		form.appendChild(bar);
		function markDirty() {
			dirty = true;
			bar.hidden = false;
		}
		form.addEventListener("input", markDirty);
		form.addEventListener("change", markDirty);
		form.addEventListener("submit", function () { dirty = false; bar.hidden = true; });
		window.addEventListener("beforeunload", function (event) {
			if (!dirty) return;
			event.preventDefault();
			event.returnValue = "";
		});
	});
})();
