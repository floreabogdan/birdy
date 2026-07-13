package web

import (
	"strings"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/store"
)

// series builds a Series with one point per value, a minute apart.
func series(vals ...int) Series {
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	out := make(Series, len(vals))
	for i, v := range vals {
		out[i] = Point{Ts: base.Add(time.Duration(i) * time.Minute).UnixMilli(), V: v}
	}
	return out
}

func TestSparklineHTML(t *testing.T) {
	svg := string(sparklineHTML(series(1, 5, 2, 8), 100, 20))
	if !strings.Contains(svg, "<svg") || !strings.Contains(svg, "<polyline") {
		t.Errorf("a series should render an SVG polyline, got %q", svg)
	}
	// The hover script names the point under the cursor from this attribute; without
	// it the chart can only say "something changed", not how many and when.
	if !strings.Contains(svg, "data-spark=") || !strings.Contains(svg, "&#34;v&#34;:8") {
		t.Errorf("the series should ride along for the hover tooltip, got %q", svg)
	}
	// Too few points to draw a line.
	if got := string(sparklineHTML(series(7), 100, 20)); strings.Contains(got, "<polyline") {
		t.Errorf("a single point has no line to draw, got %q", got)
	}
}

func TestDownsample(t *testing.T) {
	// Short series is returned unchanged.
	if got := downsample(series(1, 2, 3), 8); len(got) != 3 {
		t.Errorf("a series within the cap should be unchanged, got %d", len(got))
	}
	// Long series is reduced, keeping the first and last point — value AND time, so
	// the chart's x axis still spans the window it claims to.
	vals := make([]int, 100)
	for i := range vals {
		vals[i] = i
	}
	in := series(vals...)
	got := downsample(in, 10)
	if len(got) != 10 {
		t.Fatalf("want 10 points, got %d", len(got))
	}
	if got[0] != in[0] || got[len(got)-1] != in[len(in)-1] {
		t.Errorf("downsample must keep the endpoints, got first=%+v last=%+v", got[0], got[len(got)-1])
	}
}

// Every point keeps the timestamp of the sample it came from — that is what the
// tooltip reads back.
func TestSeriesByProtocolCarriesTimestamps(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	out := seriesByProtocol([]store.Sample{
		{Ts: now.Add(-2 * time.Minute), Protocol: "edge_v4", Imported: 10},
		{Ts: now.Add(-1 * time.Minute), Protocol: "edge_v4", Imported: 20},
	}, 32)
	got := out["edge_v4"]
	if len(got) != 2 {
		t.Fatalf("want 2 points, got %d", len(got))
	}
	if got[0].Ts != now.Add(-2*time.Minute).UnixMilli() || got[0].V != 10 {
		t.Errorf("point 0 = %+v, want the first sample's time and count", got[0])
	}
	if got[1].Ts <= got[0].Ts {
		t.Error("points must stay in time order, oldest first")
	}
}

// The peer-detail page draws a route-history chart once samples exist.
func TestPeerDetailShowsHistory(t *testing.T) {
	env := newTestEnv(t, false)
	env.fc.details["edge_v4"] = birdc.ProtocolDetail{
		Summary:  birdc.ProtocolSummary{Name: "edge_v4", Proto: "BGP", State: "up"},
		BGPState: "Established",
	}

	now := time.Now()
	var batch []store.Sample
	for i := 0; i < 5; i++ {
		batch = append(batch, store.Sample{
			Ts: now.Add(time.Duration(-i) * time.Minute), Protocol: "edge_v4",
			Imported: 1000 + i*10, Exported: 3,
		})
	}
	if err := env.store.InsertSamples(batch); err != nil {
		t.Fatal(err)
	}

	body := env.do(t, "GET", "/peers/edge_v4", nil).Body.String()
	if !strings.Contains(body, "Route history") || !strings.Contains(body, "sparkline") {
		t.Errorf("peer detail should render a history chart when samples exist")
	}
}

// The dashboard JSON carries per-session history for the trend sparklines.
func TestDashboardHistoryInJSON(t *testing.T) {
	env := newTestEnv(t, false)
	now := time.Now()
	if err := env.store.InsertSamples([]store.Sample{
		{Ts: now.Add(-2 * time.Minute), Protocol: "edge_v4", Imported: 10, Exported: 1},
		{Ts: now.Add(-1 * time.Minute), Protocol: "edge_v4", Imported: 20, Exported: 1},
	}); err != nil {
		t.Fatal(err)
	}
	body := env.do(t, "GET", "/api/dashboard", nil).Body.String()
	if !strings.Contains(body, `"history"`) || !strings.Contains(body, "edge_v4") {
		t.Errorf("dashboard JSON should include per-session history, got %s", body)
	}
}
