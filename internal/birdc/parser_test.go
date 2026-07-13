package birdc

import (
	"bufio"
	"strings"
	"testing"
)

func mustFrame(t *testing.T, raw string) Reply {
	t.Helper()
	r, err := readFrame(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	return r
}

func TestReadFrameBanner(t *testing.T) {
	r := mustFrame(t, fixtureBanner)
	if r.Terminal.Code != 1 {
		t.Fatalf("code = %d, want 1", r.Terminal.Code)
	}
	if r.Terminal.Lines[0] != "BIRD 2.17.1 ready." {
		t.Fatalf("text = %q", r.Terminal.Lines[0])
	}
}

func TestParseStatus(t *testing.T) {
	st, err := ParseStatus(mustFrame(t, fixtureShowStatus))
	if err != nil {
		t.Fatal(err)
	}
	want := Status{
		Version:      "2.17.1",
		RouterID:     "203.0.113.58",
		Hostname:     "rtr1.example.net",
		CurrentTime:  "2026-07-10 09:56:01.268",
		LastReboot:   "2026-07-08 14:39:57.632",
		LastReconfig: "2026-07-08 18:53:57.362",
		Message:      "Daemon is up and running",
	}
	if st != want {
		t.Fatalf("got %+v, want %+v", st, want)
	}
}

func TestParseProtocols(t *testing.T) {
	rows, err := ParseProtocols(mustFrame(t, fixtureShowProtocols))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 8 {
		t.Fatalf("got %d rows, want 8", len(rows))
	}
	last := rows[len(rows)-1]
	want := ProtocolSummary{Name: "edge_v6", Proto: "BGP", Table: "---", State: "up", Since: "2026-07-08", Info: "Established"}
	if last != want {
		t.Fatalf("last row = %+v, want %+v", last, want)
	}
	first := rows[0]
	if first.Name != "anchors6" || first.Proto != "Static" || first.Table != "master6" || first.State != "up" {
		t.Fatalf("first row = %+v", first)
	}
}

func TestParseProtocolsLongNames(t *testing.T) {
	rows, err := ParseProtocols(mustFrame(t, fixtureShowProtocolsLongNames))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ProtocolSummary{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	// The overflowing names must parse whole, keep "up" in State (not the table),
	// and read the bare-time Since correctly — the symptoms the operator saw.
	for _, name := range []string{"originate_ANNOUNCE_V4", "originate_ANNOUNCE_V6"} {
		r, ok := byName[name]
		if !ok {
			t.Fatalf("%s missing; got %+v", name, rows)
		}
		if r.Proto != "Static" || r.State != "up" {
			t.Errorf("%s = %+v, want Proto=Static State=up", name, r)
		}
		if r.Table == "" || r.State == r.Table {
			t.Errorf("%s: state/table columns look shifted: %+v", name, r)
		}
		if r.Since != "09:01:15.741" || r.Info != "" {
			t.Errorf("%s: Since=%q Info=%q, want Since=09:01:15.741 Info empty", name, r.Since, r.Info)
		}
	}
	if r := byName["cloudflare"]; r.Proto != "RPKI" || r.State != "up" || r.Info != "Established" {
		t.Errorf("cloudflare = %+v, want Proto=RPKI State=up Info=Established", r)
	}
	if r := byName["nav_v4"]; r.Proto != "BGP" || r.Info != "Established" {
		t.Errorf("nav_v4 = %+v, want Proto=BGP Info=Established", r)
	}
}

func TestParseProtocolDetailBGP(t *testing.T) {
	d, err := ParseProtocolDetail(mustFrame(t, fixtureShowProtocolsAllBGP))
	if err != nil {
		t.Fatal(err)
	}
	if d.Summary.Name != "edge_v4" || d.Summary.State != "up" {
		t.Fatalf("summary = %+v", d.Summary)
	}
	if d.BGPState != "Established" {
		t.Fatalf("BGPState = %q", d.BGPState)
	}
	if d.NeighborAddress != "203.0.113.57" {
		t.Fatalf("NeighborAddress = %q", d.NeighborAddress)
	}
	if d.NeighborAS != "64496" || d.LocalAS != "65551" {
		t.Fatalf("AS fields = %q / %q", d.NeighborAS, d.LocalAS)
	}
	if d.NeighborID != "198.51.100.1" {
		t.Fatalf("NeighborID = %q", d.NeighborID)
	}
	if d.SessionType != "external multihop AS4" {
		t.Fatalf("SessionType = %q", d.SessionType)
	}
	if d.SourceAddress != "203.0.113.58" {
		t.Fatalf("SourceAddress = %q", d.SourceAddress)
	}
	if d.HoldTimer != "50.850/90" || d.KeepaliveTimer != "11.488/30" {
		t.Fatalf("timers = %q / %q", d.HoldTimer, d.KeepaliveTimer)
	}
	if len(d.Channels) != 1 {
		t.Fatalf("got %d channels, want 1", len(d.Channels))
	}
	ch := d.Channels[0]
	if ch.AFI != "ipv4" || ch.State != "UP" || ch.Table != "master4" || ch.Preference != "100" {
		t.Fatalf("channel = %+v", ch)
	}
	if ch.ImportFilter != "import_v4" || ch.ExportFilter != "export_v4" {
		t.Fatalf("filters = %q / %q", ch.ImportFilter, ch.ExportFilter)
	}
	if ch.ImportLimit != "2000000" || ch.ImportLimitAction != "disable" {
		t.Fatalf("import limit = %q action=%q", ch.ImportLimit, ch.ImportLimitAction)
	}
	if ch.RoutesImported != 1 || ch.RoutesExported != 1 || ch.RoutesPreferred != 1 {
		t.Fatalf("route counts = %+v", ch)
	}
	if len(d.RawLines) == 0 {
		t.Fatal("RawLines should not be empty")
	}
}

func TestParseProtocolDetailNonBGP(t *testing.T) {
	d, err := ParseProtocolDetail(mustFrame(t, fixtureShowProtocolsAllDevice))
	if err != nil {
		t.Fatal(err)
	}
	if d.Summary.Name != "device1" || d.Summary.Proto != "Device" {
		t.Fatalf("summary = %+v", d.Summary)
	}
	if d.BGPState != "" || len(d.Channels) != 0 {
		t.Fatalf("expected no BGP fields for a Device protocol, got %+v", d)
	}
}

func TestParseRouteCount(t *testing.T) {
	entries, err := ParseRouteCount(mustFrame(t, fixtureShowRouteCount))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0] != (RouteCountEntry{Table: "master4", Routes: 5, Networks: 4}) {
		t.Fatalf("entries[0] = %+v", entries[0])
	}
	if entries[1] != (RouteCountEntry{Table: "master6", Routes: 5, Networks: 4}) {
		t.Fatalf("entries[1] = %+v", entries[1])
	}
}

func TestParseRoutesFullDump(t *testing.T) {
	tables, err := ParseRoutes(mustFrame(t, fixtureShowRoute))
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 {
		t.Fatalf("got %d tables, want 2", len(tables))
	}
	m4 := tables[0]
	if m4.Name != "master4" {
		t.Fatalf("table[0].Name = %q", m4.Name)
	}
	// 0.0.0.0/0, 203.0.113.56/30, 192.0.2.0/24 (unicast), 192.0.2.0/24 (unreachable), 192.168.10.0/24
	if len(m4.Routes) != 5 {
		t.Fatalf("master4 got %d routes, want 5: %+v", len(m4.Routes), m4.Routes)
	}

	def := m4.Routes[0]
	if def.Network != "0.0.0.0/0" || def.Type != "unicast" || def.Protocol != "edge_v4" || !def.Primary {
		t.Fatalf("default route = %+v", def)
	}
	if def.Preference != 100 || def.ASPath != "AS64496i" {
		t.Fatalf("default route pref/aspath = %d / %q", def.Preference, def.ASPath)
	}
	if def.NextHop != "via 203.0.113.57 on eno1" {
		t.Fatalf("default route nexthop = %q", def.NextHop)
	}

	// blank-network continuation row must inherit the previous network (192.0.2.0/24)
	unreach := m4.Routes[3]
	if unreach.Network != "192.0.2.0/24" || unreach.Type != "unreachable" || unreach.Protocol != "anchors4" {
		t.Fatalf("carried-forward network row = %+v", unreach)
	}
	if unreach.Primary {
		t.Fatalf("unreachable row should not be primary: %+v", unreach)
	}
	if unreach.Preference != 200 {
		t.Fatalf("unreachable pref = %d", unreach.Preference)
	}

	m6 := tables[1]
	// ::/0, fd00:1234::/64, 2001:db8:1::/126, 2001:db8:100::/40 (unicast), 2001:db8:100::/40 (unreachable)
	if m6.Name != "master6" || len(m6.Routes) != 5 {
		t.Fatalf("master6 = %+v", m6)
	}
	v6def := m6.Routes[0]
	if v6def.Network != "::/0" || v6def.From != "2001:db8:1::1" {
		t.Fatalf("v6 default route = %+v", v6def)
	}
}

func TestParseRoutesAllWithAttrs(t *testing.T) {
	tables, err := ParseRoutes(mustFrame(t, fixtureShowRouteAllFor))
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || len(tables[0].Routes) != 2 {
		t.Fatalf("tables = %+v", tables)
	}
	best := tables[0].Routes[0]
	if best.Network != "192.0.2.0/24" || len(best.Attrs) != 1 || best.Attrs[0] != "Type: device univ" {
		t.Fatalf("best route = %+v", best)
	}
	unreach := tables[0].Routes[1]
	if unreach.Network != "192.0.2.0/24" || len(unreach.Attrs) != 1 || unreach.Attrs[0] != "Type: static univ" {
		t.Fatalf("unreachable route = %+v", unreach)
	}
}

func TestParseRoutesAllBGPCommunities(t *testing.T) {
	tables, err := ParseRoutes(mustFrame(t, fixtureShowRouteAllBGP))
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || len(tables[0].Routes) != 1 {
		t.Fatalf("tables = %+v", tables)
	}
	r := tables[0].Routes[0]
	if r.Origin != "IGP" {
		t.Errorf("origin = %q, want IGP", r.Origin)
	}
	if r.LocalPref != "100" {
		t.Errorf("local_pref = %q, want 100", r.LocalPref)
	}
	if r.MED != "0" {
		t.Errorf("med = %q, want 0", r.MED)
	}
	want := []Community{
		{A: 65000, B: 100},
		{A: 65535, B: 666},
		{Large: true, A: 64496, B: 1, C: 1000},
		{Large: true, A: 64496, B: 2, C: 1},
	}
	if len(r.Communities) != len(want) {
		t.Fatalf("communities = %+v, want %+v", r.Communities, want)
	}
	for i, c := range r.Communities {
		if c != want[i] {
			t.Errorf("community[%d] = %v, want %v", i, c, want[i])
		}
	}
	// The recognised BGP.* detail lines must be pulled out of the raw Attrs;
	// only the unrecognised ones (Type, as_path, next_hop) remain there.
	for _, a := range r.Attrs {
		if strings.HasPrefix(a, "BGP.community:") || strings.HasPrefix(a, "BGP.local_pref:") {
			t.Errorf("recognised attr left in Attrs: %q", a)
		}
	}
}

func TestParseRoutesExportProtocolNoExport(t *testing.T) {
	exp, err := ParseRoutes(mustFrame(t, fixtureShowRouteExport))
	if err != nil {
		t.Fatal(err)
	}
	if len(exp) != 1 || len(exp[0].Routes) != 1 || exp[0].Routes[0].Network != "192.0.2.0/24" {
		t.Fatalf("export = %+v", exp)
	}

	imp, err := ParseRoutes(mustFrame(t, fixtureShowRouteProtocol))
	if err != nil {
		t.Fatal(err)
	}
	if len(imp) != 1 || len(imp[0].Routes) != 1 || imp[0].Routes[0].Protocol != "edge_v4" {
		t.Fatalf("protocol = %+v", imp)
	}

	noexp, err := ParseRoutes(mustFrame(t, fixtureShowRouteNoExport))
	if err != nil {
		t.Fatal(err)
	}
	if len(noexp) != 1 || len(noexp[0].Routes) != 2 {
		t.Fatalf("noexport = %+v", noexp)
	}
}

func TestReplyIsError(t *testing.T) {
	r := mustFrame(t, fixtureErrorSyntax)
	if !r.IsError() {
		t.Fatalf("expected error reply, code=%d", r.Terminal.Code)
	}
	if r.Terminal.Lines[0] != "syntax error, unexpected CF_SYM_UNDEFINED" {
		t.Fatalf("text = %q", r.Terminal.Lines[0])
	}
}

func TestParseRouteForSingleResult(t *testing.T) {
	tables, err := ParseRoutes(mustFrame(t, fixtureShowRouteFor))
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || len(tables[0].Routes) != 2 {
		t.Fatalf("tables = %+v", tables)
	}
}
