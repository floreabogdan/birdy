package web

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

type policiesView struct {
	Active   string
	ReadOnly bool
	Imports  []store.Policy
	Exports  []store.Policy
	InUse    map[int64]int // policy id -> peers attached
	SetNames map[int64]string
	Flash    string
}

type policyFormView struct {
	Active     string
	ReadOnly   bool
	IsNew      bool
	Policy     store.Policy
	Sets       []store.PrefixSet
	ASSets     []store.ASSet
	Errs       map[string]string
	Preview    string
	PreviewErr string
}

func (s *Server) handlePoliciesList(w http.ResponseWriter, r *http.Request) {
	policies, err := s.store.ListPolicies()
	if err != nil {
		s.serverError(w, "list policies", err)
		return
	}
	peers, err := s.store.ListPeers()
	if err != nil {
		s.serverError(w, "list peers", err)
		return
	}
	inUse := map[int64]int{}
	for _, p := range peers {
		imports, exports, err := s.store.PeerPolicies(p.ID)
		if err != nil {
			s.serverError(w, "peer policies", err)
			return
		}
		for _, pol := range append(imports, exports...) {
			inUse[pol.ID]++
		}
	}
	sets, err := s.store.ListPrefixSets()
	if err != nil {
		s.serverError(w, "list prefix sets", err)
		return
	}
	names := map[int64]string{}
	for _, ps := range sets {
		names[ps.ID] = ps.Name
	}

	v := policiesView{Active: "policies", ReadOnly: s.readOnly, InUse: inUse, SetNames: names,
		Flash: r.URL.Query().Get("flash")}
	for _, p := range policies {
		if p.IsImport() {
			v.Imports = append(v.Imports, p)
		} else {
			v.Exports = append(v.Exports, p)
		}
	}
	render(w, s.log, "policies.html", v)
}

func (s *Server) handlePolicyNew(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("direction")
	if dir != store.DirExport {
		dir = store.DirImport
	}
	// Defaults that make a new import policy safe rather than permissive.
	p := store.Policy{
		Direction: dir, DefaultRoute: store.DefaultReject, RejectBogonPrefixes: true,
		RejectOwnASN: true, BogonASNs: store.BogonASNsAll, ROV: store.ROVOff,
		MinLenV4: 8, MaxLenV4: 24, MinLenV6: 12, MaxLenV6: 48, MaxASPathLen: 64,
	}
	if dir == store.DirExport {
		p = store.Policy{Direction: dir, RejectBogonPrefixes: true}
	}
	s.renderPolicyForm(w, policyFormView{Active: "policies", ReadOnly: s.readOnly, IsNew: true, Policy: p})
}

func (s *Server) handlePolicyEdit(w http.ResponseWriter, r *http.Request) {
	p, ok := namedEntity(s, w, r, s.store.GetPolicyByName, "policy")
	if !ok {
		return
	}
	s.renderPolicyForm(w, policyFormView{Active: "policies", ReadOnly: s.readOnly, Policy: p})
}

func policyFromForm(r *http.Request) store.Policy {
	// Local preference is a 32-bit unsigned value, wider than a 32-bit int.
	atoi64 := func(k string) int64 {
		n, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue(k)), 10, 64)
		return n
	}
	p := store.Policy{
		Name:        r.FormValue("name"),
		Description: strings.TrimSpace(r.FormValue("description")),
		Direction:   r.FormValue("direction"),

		DefaultRoute: r.FormValue("defaultRoute"),
		MinLenV4:     formInt(r, "minLenV4"),
		MaxLenV4:     formInt(r, "maxLenV4"),
		MinLenV6:     formInt(r, "minLenV6"),
		MaxLenV6:     formInt(r, "maxLenV6"),
		RejectOwnASN: r.FormValue("rejectOwnAsn") == "on",
		MaxASPathLen: formInt(r, "maxAsPathLen"),
		BogonASNs:    r.FormValue("bogonAsns"),
		ROV:          r.FormValue("rov"),
		SetLocalPref: atoi64("setLocalPref"),

		AnnounceEverything:   r.FormValue("announceEverything") == "on",
		AnnounceDefault:      r.FormValue("announceDefault") == "on",
		AnnounceFromUpstream: r.FormValue("announceFromUpstream") == "on",
		AnnounceFromIX:       r.FormValue("announceFromIx") == "on",
		AnnounceFromCustomer: r.FormValue("announceFromCustomer") == "on",

		RejectBogonPrefixes: r.FormValue("rejectBogonPrefixes") == "on",
		MatchCommunity:      strings.TrimSpace(r.FormValue("matchCommunity")),
		AcceptBlackhole:     r.FormValue("acceptBlackhole") == "on",
	}
	if id, err := strconv.ParseInt(r.FormValue("acceptOnlySetId"), 10, 64); err == nil && id > 0 {
		p.AcceptOnlySetID = sql.NullInt64{Int64: id, Valid: true}
	}
	if id, err := strconv.ParseInt(r.FormValue("originAsSetId"), 10, 64); err == nil && id > 0 {
		p.OriginASSetID = sql.NullInt64{Int64: id, Valid: true}
	}
	p.SetIDs = idList(r.Form["announceSetIds"])
	return p
}

// idList turns posted select values into ids, dropping the "none" sentinel.
// Document order is preserved, which is what makes an ordered chain work.
func idList(values []string) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, v := range values {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (s *Server) handlePolicySave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	isNew := r.PathValue("name") == ""
	p := policyFromForm(r)

	if !isNew {
		existing, ok := namedEntity(s, w, r, s.store.GetPolicyByName, "policy")
		if !ok {
			return
		}
		p.ID, p.Builtin = existing.ID, existing.Builtin
		// A policy's direction decides which half of the row is meaningful and
		// which BIRD function it renders as. Changing it under an attached peer
		// would silently move the policy from one chain to the other.
		p.Direction = existing.Direction
	}

	errs := p.Validate()
	if len(errs) == 0 {
		var err error
		if isNew {
			_, err = s.store.CreatePolicy(p)
		} else {
			err = s.store.UpdatePolicy(p)
		}
		if err != nil {
			if isUniqueViolation(err) {
				errs["name"] = "A policy with this name already exists."
			} else {
				s.serverError(w, "save policy", err)
				return
			}
		}
	}
	if len(errs) == 0 {
		http.Redirect(w, r, "/policies?flash="+flash("Saved "+p.Name), http.StatusSeeOther)
		return
	}
	s.renderPolicyForm(w, policyFormView{Active: "policies", ReadOnly: s.readOnly, IsNew: isNew, Policy: p, Errs: errs})
}

func (s *Server) handlePolicyDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := namedEntity(s, w, r, s.store.GetPolicyByName, "policy")
	if !ok {
		return
	}
	if err := s.store.DeletePolicy(p.ID); err != nil {
		http.Redirect(w, r, "/policies?flash="+flash("Could not delete "+p.Name+": "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/policies?flash="+flash("Deleted "+p.Name), http.StatusSeeOther)
}

func (s *Server) renderPolicyForm(w http.ResponseWriter, v policyFormView) {
	// Bogon sets are wired into the "reject bogons" checkbox and must never be
	// offered here, or someone will announce BOGONS_V4 to a peer.
	sets, err := s.store.ListSelectablePrefixSets()
	if err != nil {
		s.serverError(w, "list prefix sets", err)
		return
	}
	v.Sets = sets

	asSets, err := s.store.ListASSets()
	if err != nil {
		s.serverError(w, "list AS sets", err)
		return
	}
	v.ASSets = asSets

	allSets, err := s.store.ListPrefixSets()
	if err != nil {
		s.serverError(w, "list prefix sets", err)
		return
	}
	rpkiServers, err := s.store.ListRPKIServers()
	if err != nil {
		s.serverError(w, "list RPKI servers", err)
		return
	}
	bogonASNs, err := s.store.ListBogonASNs()
	if err != nil {
		s.serverError(w, "list bogon ASNs", err)
		return
	}
	var localASN int64
	if settings, ok, err := s.store.GetSettings(); err == nil && ok && settings.LocalASN.Valid {
		localASN = settings.LocalASN.Int64
	}
	v.Preview, v.PreviewErr = previewPolicy(v.Policy, allSets, asSets, rpkiServers, bogonASNs, localASN)
	render(w, s.log, "policy_form.html", v)
}

// previewPolicy renders just this policy's two BIRD functions.
func previewPolicy(pol store.Policy, sets []store.PrefixSet, asSets []store.ASSet, rpkiServers []store.RPKIServer, bogonASNs []store.BogonASN, localASN int64) (string, string) {
	if localASN == 0 {
		return "", "Set the local ASN under Settings to preview the generated BIRD code."
	}
	probe := pol
	if errs := probe.Validate(); len(errs) > 0 {
		return "", "Fix the errors above to see the generated BIRD code."
	}
	full, err := birdconf.Config(birdconf.Input{
		RouterID: "0.0.0.1", LocalASN: localASN, PrefixSets: sets, ASSets: asSets,
		Policies: []store.Policy{probe}, RPKIServers: rpkiServers, BogonASNs: bogonASNs,
	})
	if err != nil {
		return "", err.Error()
	}
	return sectionsFrom(full, "function "+policyFuncPrefix(probe)+probe.Name+"_"), ""
}

func policyFuncPrefix(p store.Policy) string {
	if p.IsImport() {
		return "imp_"
	}
	return "exp_"
}

// sectionsFrom slices out every top-level block whose declaration starts with
// marker, keeping the comment line above it.
func sectionsFrom(cfg, marker string) string {
	var out []string
	rest := cfg
	for {
		i := strings.Index(rest, marker)
		if i < 0 {
			break
		}
		// Walk back to include a preceding comment line, if any.
		start := strings.LastIndex(rest[:i], "\n\n")
		if start < 0 {
			start = 0
		} else {
			start += 2
		}
		end := strings.Index(rest[i:], "\n}\n")
		if end < 0 {
			break
		}
		out = append(out, strings.TrimRight(rest[start:i+end+2], "\n"))
		rest = rest[i+end+2:]
	}
	return strings.Join(out, "\n\n")
}
