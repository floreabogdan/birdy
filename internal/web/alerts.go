package web

import "net/http"

// apiAlertsSummary backs the top bar's notification bell and the sidebar's
// BIRD connection indicator, polled on every authenticated page. Both values
// are real state (currently-down sessions, last poll outcome), not
// decorative placeholders.
func (s *Server) apiAlertsSummary(w http.ResponseWriter, _ *http.Request) {
	snap := s.poller.Snapshot()
	down := 0
	for _, st := range snap.States {
		if !st.Up {
			down++
		}
	}
	writeJSON(w, map[string]any{
		"downCount": down,
		"pollOK":    snap.Err == nil && !snap.UpdatedAt.IsZero(),
	})
}
