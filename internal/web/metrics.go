package web

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/floreabogdan/birdy/internal/poller"
)

// handleMetrics exposes birdy's poll state in Prometheus text format. It is
// unauthenticated by design — Prometheus scrapes cannot carry a session cookie.
//
// birdy binds every interface out of the box and its access list starts as
// allow-all, so an always-on /metrics would publish the router's session
// inventory to anyone who found the port. Hence the gate: the endpoint is on by
// default but serves nothing until the access list actually narrows who can
// reach birdy. Add the scraper's IP under Settings → Access and it works — no
// flag, no restart.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.accessRestricted() {
		http.Error(w, "birdy: /metrics is unauthenticated, so it stays closed while the access list allows every IP.\n"+
			"Add the scraper's address under Settings → Access control and it starts serving immediately.\n", http.StatusForbidden)
		return
	}
	snap := s.poller.Snapshot()
	var b strings.Builder

	metric := func(name, typ, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	reachable := 0
	if snap.Err == nil {
		reachable = 1
	}

	metric("birdy_up", "gauge", "Whether birdy is running (always 1 when scrapeable).")
	b.WriteString("birdy_up 1\n")

	metric("birdy_bird_reachable", "gauge", "Whether the last poll of the BIRD control socket succeeded.")
	fmt.Fprintf(&b, "birdy_bird_reachable %d\n", reachable)

	metric("birdy_last_poll_timestamp_seconds", "gauge", "Unix time of the last completed poll.")
	fmt.Fprintf(&b, "birdy_last_poll_timestamp_seconds %d\n", snap.UpdatedAt.Unix())

	metric("birdy_routes_total", "gauge", "Total routes across all BIRD tables, excluding RPKI ROA tables.")
	fmt.Fprintf(&b, "birdy_routes_total %d\n", snap.TotalRoutes)

	// buildProtoRows counts every protocol; the BGP metrics must count only the
	// sessions, not the device/kernel/static plumbing.
	rows, _, _ := buildProtoRows(snap)
	var bgpUp, bgpDown int
	for _, row := range rows {
		if !row.IsBGP() {
			continue
		}
		if row.Up {
			bgpUp++
		} else {
			bgpDown++
		}
	}
	metric("birdy_bgp_sessions", "gauge", "Number of BGP sessions by state.")
	fmt.Fprintf(&b, "birdy_bgp_sessions{state=\"up\"} %d\n", bgpUp)
	fmt.Fprintf(&b, "birdy_bgp_sessions{state=\"down\"} %d\n", bgpDown)

	metric("birdy_bgp_session_up", "gauge", "Whether a BGP session is established (1) or not (0).")
	for _, row := range rows {
		if row.IsBGP() {
			fmt.Fprintf(&b, "birdy_bgp_session_up{name=%q,proto=%q} %d\n", esc(row.Name), esc(row.Proto), b2i(row.Up))
		}
	}

	writeCounts := func(name, help string, pick func(protoRow) int) {
		metric(name, "gauge", help)
		for _, row := range rows {
			if row.IsBGP() && row.HasCounts {
				fmt.Fprintf(&b, "%s{name=%q} %d\n", name, esc(row.Name), pick(row))
			}
		}
	}
	writeCounts("birdy_bgp_routes_imported", "Routes imported (accepted) from a session.", func(r protoRow) int { return r.Imported })
	writeCounts("birdy_bgp_routes_exported", "Routes exported (announced) to a session.", func(r protoRow) int { return r.Exported })
	writeCounts("birdy_bgp_routes_filtered", "Routes received from a session but rejected on import.", func(r protoRow) int { return r.Filtered })

	writeRPKIMetrics(&b, metric, s.poller.RPKIFeeds(), time.Now())

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// writeRPKIMetrics exposes each RTR session's data so a monitoring system that
// sees several routers can do what one router cannot: notice a feed whose ROA
// count falls behind the others on the same cache while its serial stands still —
// connected, refreshing, and stale. The serial's age alone is not that signal; a
// healthy cache can go hours without a new serial.
func writeRPKIMetrics(b *strings.Builder, metric func(name, typ, help string), feeds map[string]poller.RPKIFeed, now time.Time) {
	names := slices.Sorted(maps.Keys(feeds))
	metric("birdy_rpki_feed_established", "gauge", "Whether an RPKI protocol's RTR session is established (1) or not (0).")
	for _, n := range names {
		fmt.Fprintf(b, "birdy_rpki_feed_established{name=%q} %d\n", esc(n), b2i(feeds[n].Cache.Status == "Established"))
	}
	metric("birdy_rpki_roas", "gauge", "ROAs an RPKI feed holds, by address family.")
	for _, n := range names {
		fmt.Fprintf(b, "birdy_rpki_roas{name=%q,afi=\"ipv4\"} %d\n", esc(n), feeds[n].ROAs4)
		fmt.Fprintf(b, "birdy_rpki_roas{name=%q,afi=\"ipv6\"} %d\n", esc(n), feeds[n].ROAs6)
	}
	metric("birdy_rpki_serial", "gauge", "The RTR serial an RPKI feed is on; absent until its first sync. Serials are per session.")
	for _, n := range names {
		if f := feeds[n]; f.Cache.Serial >= 0 {
			fmt.Fprintf(b, "birdy_rpki_serial{name=%q} %d\n", esc(n), f.Cache.Serial)
		}
	}
	metric("birdy_rpki_serial_unchanged_seconds", "gauge", "How long birdy has seen an RPKI feed's session and serial stand still (counted from birdy's start at most).")
	for _, n := range names {
		fmt.Fprintf(b, "birdy_rpki_serial_unchanged_seconds{name=%q} %d\n", esc(n), int64(feeds[n].SerialAge(now).Seconds()))
	}
}

// esc escapes a Prometheus label value. Peer/proto names are plain BIRD symbols,
// but a backslash or quote must never break the exposition format.
func esc(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n").Replace(s)
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}
