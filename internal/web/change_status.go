package web

import (
	"net/http"

	birdconf "github.com/floreabogdan/birdy/internal/render"
)

// apiChangeStatus gives the global navigation a cheap, explicit signal that
// the model differs from the last confirmed configuration. It is requested
// once per page load rather than folding config rendering into the fast BIRD
// status poll.
func (s *Server) apiChangeStatus(w http.ResponseWriter, _ *http.Request) {
	settings, _, err := s.store.GetSettings()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read settings")
		return
	}
	pending, _, err := s.store.PendingConfigVersion()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read apply state")
		return
	}
	in, reason, err := s.renderInput(false)
	if err != nil || reason != "" {
		writeJSON(w, map[string]any{"changed": true, "pending": pending.ID != 0, "blocked": true})
		return
	}
	candidate, err := birdconf.Config(in)
	if err != nil {
		writeJSON(w, map[string]any{"changed": true, "pending": pending.ID != 0, "blocked": true})
		return
	}
	changed := settings.AppliedConfigHash == "" || hashBytes([]byte(candidate)) != settings.AppliedConfigHash
	writeJSON(w, map[string]any{"changed": changed, "pending": pending.ID != 0, "blocked": false})
}
