package web

import (
	"net/http"

	birdconf "github.com/floreabogdan/birdy/internal/render"
)

// handleConfigExport serves the candidate configuration with passwords masked.
// It is intentionally read-only and uses the same renderer as Changes, so an
// exported file cannot diverge from what the panel presents before apply.
func (s *Server) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	in, reason, err := s.renderInput(true)
	if err != nil {
		s.serverError(w, "build config export", err)
		return
	}
	if reason != "" {
		http.Error(w, reason, http.StatusConflict)
		return
	}
	config, err := birdconf.Config(in)
	if err != nil {
		s.serverError(w, "render config export", err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="birdy-candidate.conf"`)
	_, _ = w.Write([]byte(config))
}
