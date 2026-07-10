package web

import (
	"net/http"
	"strings"

	"github.com/floreabogdan/birdy/internal/irr"
	"github.com/floreabogdan/birdy/internal/store"
)

// handleIRRPrefixes expands an IRR AS-SET with bgpq4 and returns the prefixes as
// text for the prefix-set entries box, so the operator reviews them (and the
// live preview) before saving. Registered only when --bgpq4 is set, since it
// runs an external binary that dials IRR mirrors.
func (s *Server) handleIRRPrefixes(w http.ResponseWriter, r *http.Request) {
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source == "" {
		writeJSON(w, map[string]any{"err": "Enter an IRR AS-SET to expand."})
		return
	}
	v6 := r.URL.Query().Get("family") == store.FamilyV6

	client := irr.New(s.bgpq4Bin)
	if !client.Available() {
		writeJSON(w, map[string]any{"err": "bgpq4 is not installed on the router. Install it, then refresh."})
		return
	}
	prefixes, err := client.Prefixes(r.Context(), source, v6)
	if err != nil {
		writeJSON(w, map[string]any{"err": err.Error()})
		return
	}

	var b strings.Builder
	for _, p := range prefixes {
		b.WriteString(p.Prefix)
		b.WriteString(p.Modifier)
		b.WriteString("\n")
	}
	writeJSON(w, map[string]any{"entries": b.String(), "count": len(prefixes)})
}
