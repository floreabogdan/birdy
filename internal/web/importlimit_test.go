package web

import (
	"testing"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/poller"
)

// Once an import limit triggers, BIRD prints "Import limit:   1000 [HIT]". The
// limit bar read that as "no limit" and vanished at exactly the moment it was
// the most important thing on the page.
func TestDashboardLimitBarSurvivesAHitLimit(t *testing.T) {
	snap := poller.Snapshot{
		Protocols: []birdc.ProtocolSummary{{Name: "edge_v4", Proto: "BGP", Table: "---", State: "up", Since: "12:22:55.700", Info: "Established"}},
		States: map[string]poller.ProtoState{"edge_v4": {Up: true, Channels: []birdc.ChannelDetail{
			{AFI: "ipv4", State: "UP", ImportLimit: "1000 [HIT]", RoutesImported: 1000},
		}}},
	}
	rows, _, _ := buildProtoRows(snap)
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].LimitPct != 100 || rows[0].LimitText != "1,000 / 1,000 (ipv4)" {
		t.Errorf("limit bar = %v %q, want 100 \"1,000 / 1,000 (ipv4)\"", rows[0].LimitPct, rows[0].LimitText)
	}
}

// The peer page draws the same bar from the same string.
func TestImportLimitPctReadsAHitLimit(t *testing.T) {
	pct := funcs["importLimitPct"].(func(int, string) float64)
	if got := pct(500, "1000 [HIT]"); got != 50 {
		t.Errorf("importLimitPct(500, \"1000 [HIT]\") = %v, want 50", got)
	}
	if got := pct(500, ""); got != -1 {
		t.Errorf("no limit should hide the bar (-1), got %v", got)
	}
}
