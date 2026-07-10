package web

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/floreabogdan/birdy/internal/peeringdb"
)

// handlePeeringDBLookup looks an ASN up in PeeringDB and returns the fields the
// peer form can pre-fill. Registered only when --peeringdb is set, since it
// dials out to a third party.
func (s *Server) handlePeeringDBLookup(w http.ResponseWriter, r *http.Request) {
	asn, err := strconv.ParseInt(r.PathValue("asn"), 10, 64)
	if err != nil {
		writeJSON(w, map[string]any{"err": "invalid ASN"})
		return
	}
	net, err := peeringdb.New().Lookup(r.Context(), asn)
	if err != nil {
		writeJSON(w, map[string]any{"err": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"name":        peerIdentFromName(net.Name, asn),
		"description": net.Name,
		"maxPrefixV4": net.MaxPrefixV4,
		"maxPrefixV6": net.MaxPrefixV6,
		"asSet":       net.IRRASSet,
	})
}

var identCleaner = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// peerIdentFromName turns a PeeringDB org name into a candidate BIRD symbol, so
// the pre-filled name does not fail validation. Falls back to as<N>.
func peerIdentFromName(name string, asn int64) string {
	s := identCleaner.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")
	s = strings.Trim(s, "_")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return "as" + strconv.FormatInt(asn, 10)
	}
	return s
}
