package web

import (
	"net/http"
	"strconv"
)

// apiAlertsSummary backs the top bar's notification bell and the BIRD
// connection indicator, polled on every authenticated page.
//
// The bell is an unread-alerts counter, not a live down-gauge: it reports how
// many fault events (session down, flap, limit hit, prefix drop, drift, BIRD
// unreachable) are newer than the id the viewer has already seen, passed as
// ?since. A disabled peer never counts — it is intentionally down and records
// no event. latestEventId lets a fresh browser seed its marker so it does not
// light up for the whole backlog. pollOK drives the connection dot.
func (s *Server) apiAlertsSummary(w http.ResponseWriter, r *http.Request) {
	snap := s.poller.Snapshot()

	latest, err := s.store.LatestEventID()
	if err != nil {
		latest = 0 // the bell should never fail the page over a count
	}
	unread := 0
	if raw := r.URL.Query().Get("since"); raw != "" {
		if since, perr := strconv.ParseInt(raw, 10, 64); perr == nil && since >= 0 {
			if n, cerr := s.store.CountAlertsAfter(since); cerr == nil {
				unread = n
			}
		}
	}
	writeJSON(w, map[string]any{
		"unread":        unread,
		"latestEventId": latest,
		"pollOK":        snap.Err == nil && !snap.UpdatedAt.IsZero(),
	})
}
