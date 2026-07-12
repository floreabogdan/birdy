package web

import "github.com/floreabogdan/birdy/internal/birdc"

// commChip is one route community annotated for display in the looking glass:
// the tuple as BIRD writes it, plus a decoded label and a colour hint when the
// value is recognised.
type commChip struct {
	Text string // BIRD tuple form, e.g. "(65535, 666)"
	Name string // decoded label, "" if unknown
	Kind string // css hint: "wellknown" | "origin" | "rpki" | "named" | ""
}

type commLabel struct{ Name, Kind string }

// wellKnownComm maps the RFC-reserved standard communities to their names.
var wellKnownComm = map[[2]int64]string{
	{65535, 0}:     "GRACEFUL_SHUTDOWN",   // RFC 8326
	{65535, 666}:   "BLACKHOLE",           // RFC 7999
	{65535, 65281}: "NO_EXPORT",           // RFC 1997
	{65535, 65282}: "NO_ADVERTISE",        // RFC 1997
	{65535, 65283}: "NO_EXPORT_SUBCONFED", // RFC 1997
	{65535, 65284}: "NOPEER",              // RFC 3765
}

// These mirror the origin-tag constants in internal/render (FROM_* = (ASN,1,N),
// RPKI_INVALID = (ASN,2,1)). Kept in sync by hand: the values are a wire
// contract baked into every config birdy renders, so they do not move.
const (
	birdyTagUpstream = 1000
	birdyTagIX       = 2000
	birdyTagCustomer = 3000
)

// semanticLabels are the communities with a fixed operational meaning: the RFC
// well-known set and birdy's own origin and RPKI large-community tags for this
// router's ASN. Their colour reflects that meaning regardless of what the
// operator may also have named them.
func (s *Server) semanticLabels(localASN int64) map[string]commLabel {
	m := make(map[string]commLabel)
	for k, name := range wellKnownComm {
		m[birdc.Community{A: k[0], B: k[1]}.String()] = commLabel{name, "wellknown"}
	}
	if localASN != 0 {
		tag := func(b, c int64) string {
			return birdc.Community{Large: true, A: localASN, B: b, C: c}.String()
		}
		m[tag(1, birdyTagUpstream)] = commLabel{"FROM_UPSTREAM", "origin"}
		m[tag(1, birdyTagIX)] = commLabel{"FROM_IX", "origin"}
		m[tag(1, birdyTagCustomer)] = commLabel{"FROM_CUSTOMER", "origin"}
		m[tag(2, 1)] = commLabel{"RPKI_INVALID", "rpki"}
	}
	return m
}

// namedCommunities maps a community tuple to the name the operator gave it in the
// library.
func (s *Server) namedCommunities() map[string]string {
	out := make(map[string]string)
	if defs, err := s.store.ListCommunityDefs(); err == nil {
		for _, d := range defs {
			out[d.Pattern()] = d.Name
		}
	}
	return out
}

// decodeCommunities annotates each community for display. A community with a
// fixed operational meaning keeps that meaning's colour; the operator's own name
// for it, if any, is preferred as the label. A community the operator has only
// named (no well-known meaning) shows as a plain library chip.
func decodeCommunities(comms []birdc.Community, semantic map[string]commLabel, named map[string]string) []commChip {
	if len(comms) == 0 {
		return nil
	}
	out := make([]commChip, 0, len(comms))
	for _, c := range comms {
		chip := commChip{Text: c.String()}
		sem, hasSem := semantic[chip.Text]
		name, hasName := named[chip.Text]
		switch {
		case hasSem:
			chip.Kind = sem.Kind
			if hasName {
				chip.Name = name
			} else {
				chip.Name = sem.Name
			}
		case hasName:
			chip.Name, chip.Kind = name, "named"
		}
		out = append(out, chip)
	}
	return out
}
