package web

import (
	"encoding/json"
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

// Point is one sample: a route count at a moment. The time used to be dropped on
// the way to the chart, which made the line unreadable — you could see that
// something halved, but not when, and "when" is the whole question when a session
// starts leaking.
type Point struct {
	// Ts is milliseconds since the epoch: what a browser's Date wants, and what
	// keeps the JSON small.
	Ts int64 `json:"t"`
	V  int   `json:"v"`
}

// Series is a route-count history, oldest point first.
type Series []Point

// values is the plain numbers, for scaling the line.
func (s Series) values() []int {
	out := make([]int, len(s))
	for i, p := range s {
		out[i] = p.V
	}
	return out
}

// sparklineHTML renders a series as an inline SVG polyline scaled to fill w×h.
// A series with fewer than two points has no shape to draw, so it renders a
// muted dash instead. preserveAspectRatio="none" lets the line stretch to the
// cell; a non-scaling stroke keeps it a hairline despite that stretch.
//
// The points ride along in a data attribute so the hover script can name the one
// under the cursor. It is the same series the line is drawn from, so the tooltip
// can never disagree with the shape.
func sparklineHTML(s Series, w, h int) template.HTML {
	if len(s) < 2 {
		return template.HTML(`<span class="spark-empty text-muted">&mdash;</span>`)
	}
	vals := s.values()
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

	data, err := json.Marshal(s)
	if err != nil {
		data = []byte("[]")
	}
	svg := fmt.Sprintf(
		`<svg class="sparkline" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="route-count history"`+
			` data-spark="%s" data-spark-w="%d" data-spark-h="%d" data-spark-pad="%d">`+
			`<polyline fill="none" stroke="currentColor" stroke-width="1.5" vector-effect="non-scaling-stroke" points="%s"/>`+
			`</svg>`, w, h, template.HTMLEscapeString(string(data)), w, h, int(pad), b.String())
	return template.HTML(svg)
}

// downsample picks at most max evenly spaced points from a series, always keeping
// the first and last, so a long history still reads as one trend line.
func downsample(s Series, max int) Series {
	if max < 2 || len(s) <= max {
		return s
	}
	out := make(Series, max)
	for i := range out {
		out[i] = s[i*(len(s)-1)/(max-1)]
	}
	return out
}

// seriesByProtocol groups samples into a per-protocol imported-route series,
// oldest first, downsampled to at most points. Samples must arrive in time
// order (the store returns them that way).
func seriesByProtocol(samples []store.Sample, points int) map[string]Series {
	raw := map[string]Series{}
	for _, sm := range samples {
		raw[sm.Protocol] = append(raw[sm.Protocol], Point{Ts: sm.Ts.UnixMilli(), V: sm.Imported})
	}
	out := make(map[string]Series, len(raw))
	for name, s := range raw {
		out[name] = downsample(s, points)
	}
	return out
}
