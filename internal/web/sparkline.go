package web

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

// Route-count history is drawn as an inline SVG sparkline — no charting library,
// no Grafana, in keeping with "one router, done well". The poller records a
// point per session on a slow cadence; these helpers turn a series into a line.

const (
	// The dashboard grid shows a short, downsampled trend; the peer-detail page a
	// longer one. Points are capped so a session on a 60s sample cadence does not
	// ship thousands of numbers to the browser or a needle-thin line.
	dashboardHistoryWindow = 6 * time.Hour
	dashboardHistoryPoints = 32
	peerHistoryWindow      = 24 * time.Hour
	peerHistoryPoints      = 80
)

// sparklineHTML renders a series as an inline SVG polyline scaled to fill w×h.
// A series with fewer than two points has no shape to draw, so it renders a
// muted dash instead. preserveAspectRatio="none" lets the line stretch to the
// cell; a non-scaling stroke keeps it a hairline despite that stretch.
func sparklineHTML(vals []int, w, h int) template.HTML {
	if len(vals) < 2 {
		return template.HTML(`<span class="spark-empty text-muted">&mdash;</span>`)
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	span := float64(hi - lo)
	const pad = 2.0
	fw, fh := float64(w), float64(h)
	n := len(vals)

	var b strings.Builder
	for i, v := range vals {
		x := pad + float64(i)/float64(n-1)*(fw-2*pad)
		y := fh / 2 // a flat series sits centred
		if span != 0 {
			y = fh - pad - float64(v-lo)/span*(fh-2*pad)
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x, y)
	}
	svg := fmt.Sprintf(
		`<svg class="sparkline" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="route-count history">`+
			`<polyline fill="none" stroke="currentColor" stroke-width="1.5" vector-effect="non-scaling-stroke" points="%s"/>`+
			`</svg>`, w, h, b.String())
	return template.HTML(svg)
}

// downsample picks at most max evenly spaced points from vals, always keeping
// the first and last, so a long series still reads as one trend line.
func downsample(vals []int, max int) []int {
	if max < 2 || len(vals) <= max {
		return vals
	}
	out := make([]int, max)
	for i := range out {
		out[i] = vals[i*(len(vals)-1)/(max-1)]
	}
	return out
}

// seriesByProtocol groups samples into a per-protocol imported-route series,
// oldest first, downsampled to at most points. Samples must arrive in time
// order (the store returns them that way).
func seriesByProtocol(samples []store.Sample, points int) map[string][]int {
	raw := map[string][]int{}
	for _, sm := range samples {
		raw[sm.Protocol] = append(raw[sm.Protocol], sm.Imported)
	}
	out := make(map[string][]int, len(raw))
	for name, vals := range raw {
		out[name] = downsample(vals, points)
	}
	return out
}
