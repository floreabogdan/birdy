// Hover a route-history chart and it tells you what you are looking at: the route
// count and the moment it was sampled. Without this a sparkline can only say
// "something changed shape" — and the question an operator has is always "how many,
// and when".
//
// One delegated listener on the document serves every chart on the page, including
// the dashboard's, which are destroyed and rebuilt on every poll. Binding per-SVG
// would leave the rebuilt ones dead.
(function () {
	var tip = null;
	var active = null; // the svg currently being hovered

	function tooltip() {
		if (!tip) {
			tip = document.createElement("div");
			tip.className = "spark-tip";
			tip.hidden = true;
			document.body.appendChild(tip);
		}
		return tip;
	}

	function fmtCount(n) {
		return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
	}

	// Sampled today: just the time. Older: the date too. A 24-hour chart that
	// repeats "Jul 13" on every point is spending pixels to say nothing.
	function fmtWhen(ms) {
		var d = new Date(ms);
		var now = new Date();
		var sameDay = d.getFullYear() === now.getFullYear() &&
			d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
		var time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
		if (sameDay) return time;
		return d.toLocaleDateString([], { month: "short", day: "numeric" }) + " " + time;
	}

	function points(svg) {
		try {
			return JSON.parse(svg.getAttribute("data-spark")) || [];
		} catch (e) {
			return [];
		}
	}

	// The chart is stretched to its cell (preserveAspectRatio="none"), so the
	// cursor's fraction across the element maps onto the viewBox x the line was
	// drawn in — pad included, or the marker would sit beside the line it names.
	function nearest(svg, clientX) {
		var pts = points(svg);
		if (pts.length < 2) return -1;
		var rect = svg.getBoundingClientRect();
		if (!rect.width) return -1;
		var w = parseFloat(svg.getAttribute("data-spark-w")) || rect.width;
		var pad = parseFloat(svg.getAttribute("data-spark-pad")) || 0;
		var x = ((clientX - rect.left) / rect.width) * w;
		var i = Math.round(((x - pad) / (w - 2 * pad)) * (pts.length - 1));
		return Math.max(0, Math.min(pts.length - 1, i));
	}

	// Draw the guide line and the dot inside the SVG itself, in viewBox units, so
	// they land exactly on the polyline whatever the cell has been stretched to.
	function mark(svg, i) {
		var pts = points(svg);
		var w = parseFloat(svg.getAttribute("data-spark-w"));
		var h = parseFloat(svg.getAttribute("data-spark-h"));
		var pad = parseFloat(svg.getAttribute("data-spark-pad")) || 0;
		var lo = pts[0].v, hi = pts[0].v;
		pts.forEach(function (p) {
			if (p.v < lo) lo = p.v;
			if (p.v > hi) hi = p.v;
		});
		var span = hi - lo;
		var x = pad + (i / (pts.length - 1)) * (w - 2 * pad);
		var y = span === 0 ? h / 2 : h - pad - ((pts[i].v - lo) / span) * (h - 2 * pad);

		var g = svg.querySelector(".spark-marker");
		if (!g) {
			g = document.createElementNS("http://www.w3.org/2000/svg", "g");
			g.setAttribute("class", "spark-marker");
			g.innerHTML =
				'<line y1="0" stroke="currentColor" stroke-width="1" vector-effect="non-scaling-stroke" opacity="0.35"/>' +
				'<circle r="2.5" fill="currentColor" vector-effect="non-scaling-stroke"/>';
			svg.appendChild(g);
		}
		var line = g.querySelector("line");
		line.setAttribute("x1", x);
		line.setAttribute("x2", x);
		line.setAttribute("y2", h);
		var dot = g.querySelector("circle");
		dot.setAttribute("cx", x);
		dot.setAttribute("cy", y);
		// The dot is drawn in a stretched coordinate system; counter the stretch so
		// it stays a circle rather than an ellipse in a wide, short cell.
		var rect = svg.getBoundingClientRect();
		var sx = rect.width / w, sy = rect.height / h;
		if (sx > 0 && sy > 0) {
			dot.setAttribute("transform", "translate(" + x + "," + y + ") scale(" + (1 / sx) + "," + (1 / sy) + ") translate(" + (-x) + "," + (-y) + ")");
		}
		return pts[i];
	}

	function clear() {
		if (active) {
			var g = active.querySelector(".spark-marker");
			if (g) g.remove();
			active = null;
		}
		if (tip) tip.hidden = true;
	}

	document.addEventListener("mousemove", function (e) {
		var svg = e.target.closest ? e.target.closest("svg.sparkline[data-spark]") : null;
		if (!svg) {
			clear();
			return;
		}
		if (active && active !== svg) clear();
		active = svg;

		var i = nearest(svg, e.clientX);
		if (i < 0) return;
		var p = mark(svg, i);
		if (!p) return;

		var t = tooltip();
		t.innerHTML = '<span class="spark-tip-count">' + fmtCount(p.v) + " routes</span>" +
			'<span class="spark-tip-when">' + fmtWhen(p.t) + "</span>";
		t.hidden = false;
		// Keep it on screen: flip to the left of the cursor near the right edge.
		var tw = t.offsetWidth;
		var x = e.clientX + 12;
		if (x + tw > window.innerWidth - 8) x = e.clientX - tw - 12;
		t.style.left = Math.max(8, x) + "px";
		t.style.top = Math.max(8, e.clientY - t.offsetHeight - 10) + "px";
	});

	// A chart can leave under the cursor (the dashboard rebuilds its rows every
	// poll); scrolling away should not leave a tooltip stranded either.
	document.addEventListener("mouseleave", clear, true);
	window.addEventListener("scroll", clear, true);
	window.addEventListener("blur", clear);
})();
