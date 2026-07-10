package web

import (
	"fmt"
	"net/http"
	"strings"
)

// handleMetrics exposes birdy's poll state in Prometheus text format. It is
// unauthenticated by design — Prometheus scrapes cannot carry a session cookie —
// so it is only registered when --metrics is set, and the operator is then
// responsible for keeping the port off the public internet (birdy binds
// loopback by default).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
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

	metric("birdy_routes_total", "gauge", "Total routes across all BIRD tables.")
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

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
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
