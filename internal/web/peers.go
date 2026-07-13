package web

import (
	"net/http"
	"strings"

	// Aliased: this package already has a render() helper for templates.
	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

type peersView struct {
	Active   string
	ReadOnly bool
	Peers    []store.Peer
	Pager    Pager
	// Live indexes the running BIRD protocols by name, so each configured peer
	// can say whether it is actually up — and link to its live session.
	Live  map[string]protoRow
	Flash string
}

type peerFormView struct {
	Active   string
	ReadOnly bool
	IsNew    bool
	Peer     store.Peer
	Imports  []store.Policy // every import policy, for the picker
	Exports  []store.Policy
	Errs     map[string]string
	// ClonedFrom names the peer a new form was pre-filled from, so the operator
	// knows the shape came from somewhere and only the identity needs its values.
	ClonedFrom string
	// PeeringDB reports whether the PeeringDB lookup is enabled, so the form can
	// show the "look up ASN" button.
	PeeringDB bool
	// Preview is the BIRD code this peer alone would contribute, rendered with
	// secrets masked. Empty when the form does not yet validate.
	Preview    string
	PreviewErr string
	Warnings   []birdconf.Warning
	// Communities are the library's named communities, shown as a hint so the
	// operator knows which names the export field will resolve.
	Communities []store.CommunityDef
}

// loadPeerChains fills in a peer's ordered import and export policy lists.
func (s *Server) loadPeerChains(p *store.Peer) error {
	imports, exports, err := s.store.PeerPolicies(p.ID)
	if err != nil {
		return err
	}
	p.ImportPolicies, p.ExportPolicies = imports, exports
	return nil
}

func (s *Server) handlePeersList(w http.ResponseWriter, r *http.Request) {
	peers, err := s.store.ListPeers()
	if err != nil {
		s.serverError(w, "list peers", err)
		return
	}
	for i := range peers {
		if err := s.loadPeerChains(&peers[i]); err != nil {
			s.serverError(w, "peer policies", err)
			return
		}
	}
	offset, limit := parsePageParams(r)
	page := pageSlice(peers, offset, limit)
	render(w, s.log, "peers.html", peersView{
		Active: "peers", ReadOnly: s.readOnly, Peers: page, Live: s.liveStates(),
		Pager: pagerFor(r, offset, limit, len(page), len(peers)),
		Flash: r.URL.Query().Get("flash"),
	})
}

func (s *Server) handlePeerNew(w http.ResponseWriter, r *http.Request) {
	// Clone an existing peer as a template: keep its role, policy chains, limits
	// and export transforms; drop the identity (name, addresses, ASN) and never
	// carry the password. This is birdy's "peer template" — the common shape of a
	// customer or IX peer, captured from one you already made.
	if from := r.URL.Query().Get("from"); from != "" {
		src, err := s.store.GetPeerByName(from)
		if err == nil {
			if err := s.loadPeerChains(&src); err != nil {
				s.serverError(w, "peer policies", err)
				return
			}
			src.ID, src.Name, src.NeighborIP, src.LocalIP, src.RemoteASN, src.Password = 0, "", "", "", 0, ""
			src.Drained = false // a fresh session is not in maintenance
			s.renderPeerForm(w, peerFormView{Active: "peers", ReadOnly: s.readOnly, IsNew: true, Peer: src, ClonedFrom: from})
			return
		}
		// Source gone — fall through to a blank form rather than 404.
	}

	// A new session is an eBGP upstream, enabled, with the first-AS check on,
	// RFC 9234 leak prevention on, and BIRD restarting it if the peer floods us
	// past the import limit. NextHopSelf is pre-ticked for the moment the operator
	// switches to iBGP: it is ignored for every other role, and off is the setting
	// that blackholes.
	p := store.Peer{Role: store.RoleUpstream, Enabled: true, EnforceFirstAS: true,
		BGPRole: true, NextHopSelf: true, ImportLimitAction: "restart", GracefulRestart: store.GRAware}
	s.renderPeerForm(w, peerFormView{Active: "peers", ReadOnly: s.readOnly, IsNew: true, Peer: p})
}

func (s *Server) handlePeerEdit(w http.ResponseWriter, r *http.Request) {
	p, ok := namedEntity(s, w, r, s.store.GetPeerByName, "peer")
	if !ok {
		return
	}
	if err := s.loadPeerChains(&p); err != nil {
		s.serverError(w, "peer policies", err)
		return
	}
	s.renderPeerForm(w, peerFormView{Active: "peers", ReadOnly: s.readOnly, Peer: p})
}

// peerFromForm reads a peer out of the posted form. It never trusts anything:
// the result goes straight to Validate before it can reach the database.
func peerFromForm(r *http.Request) store.Peer {
	return store.Peer{
		Name:              r.FormValue("name"),
		Description:       strings.TrimSpace(r.FormValue("description")),
		Role:              r.FormValue("role"),
		Enabled:           r.FormValue("enabled") == "on",
		NeighborIP:        r.FormValue("neighborIp"),
		RemoteASN:         int64(formInt(r, "remoteAsn")),
		LocalIP:           r.FormValue("localIp"),
		Multihop:          formInt(r, "multihop"),
		Passive:           r.FormValue("passive") == "on",
		Password:          r.FormValue("password"),
		ImportLimit:       formInt(r, "importLimit"),
		ImportLimitAction: r.FormValue("importLimitAction"),
		EnforceFirstAS:    r.FormValue("enforceFirstAs") == "on",
		OriginPeerOnly:    r.FormValue("originPeerOnly") == "on",
		BGPRole:           r.FormValue("bgpRole") == "on",
		NextHopSelf:       r.FormValue("nextHopSelf") == "on",
		RRClient:          r.FormValue("rrClient") == "on",
		PrependCount:      formInt(r, "prependCount"),
		ExportCommunities: strings.TrimSpace(r.FormValue("exportCommunities")),
		Drained:           r.FormValue("drained") == "on",
		BFD:               r.FormValue("bfd") == "on",
		GTSM:              r.FormValue("gtsm") == "on",
		GracefulRestart:   r.FormValue("gracefulRestart"),
	}
}

func (s *Server) handlePeerSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	isNew := r.PathValue("name") == ""
	p := peerFromForm(r)
	// Document order of the repeated selects is the chain order.
	importIDs := idList(r.Form["importPolicyIds"])
	exportIDs := idList(r.Form["exportPolicyIds"])

	if !isNew {
		existing, ok := namedEntity(s, w, r, s.store.GetPeerByName, "peer")
		if !ok {
			return
		}
		p.ID = existing.ID
		// A blank password field means "leave it alone", not "clear it": the
		// edit form never renders the stored secret back to the browser.
		if p.Password == "" {
			p.Password = existing.Password
		}
	}

	errs := p.Validate()
	s.checkChains(p, importIDs, exportIDs, errs)
	if msg := s.checkCommunityRefs(p.ExportCommunities); msg != "" {
		errs["exportCommunities"] = msg
	}

	if len(errs) == 0 {
		var err error
		if isNew {
			p.ID, err = s.store.CreatePeer(p)
		} else {
			err = s.store.UpdatePeer(p)
		}
		if err != nil {
			if isUniqueViolation(err) {
				errs["name"] = "A peer with this name already exists."
			} else {
				s.serverError(w, "save peer", err)
				return
			}
		}
	}
	if len(errs) == 0 {
		if err := s.store.SetPeerPolicies(p.ID, importIDs, exportIDs); err != nil {
			s.serverError(w, "attach policies", err)
			return
		}
		verb := "updated"
		if isNew {
			verb = "created"
		}
		s.audit(r, verb+" peer "+p.Name)
		http.Redirect(w, r, "/peers?flash="+flash("Saved "+p.Name), http.StatusSeeOther)
		return
	}

	// Re-render with what the user typed, chains included — one policy scan,
	// resolved via the shared helper.
	if all, aerr := s.store.ListPolicies(); aerr == nil {
		p.ImportPolicies, p.ExportPolicies = s.resolvePolicies(all, importIDs), s.resolvePolicies(all, exportIDs)
	}
	s.renderPeerForm(w, peerFormView{Active: "peers", ReadOnly: s.readOnly, IsNew: isNew, Peer: p, Errs: errs})
}

// checkChains rejects a chain that names a policy of the wrong direction, or a
// policy that no longer exists. iBGP sessions take no policies yet.
func (s *Server) checkChains(p store.Peer, importIDs, exportIDs []int64, errs map[string]string) {
	if p.IsIBGP() && (len(importIDs) > 0 || len(exportIDs) > 0) {
		errs["policies"] = "iBGP sessions import and export everything and do not take policies yet."
		return
	}
	// Resolve every referenced policy from one table scan, not one per id.
	all, err := s.store.ListPolicies()
	if err != nil {
		errs["policies"] = "Could not load policies to validate the chain."
		return
	}
	byID := make(map[int64]store.Policy, len(all))
	for _, pol := range all {
		byID[pol.ID] = pol
	}
	for dir, ids := range map[string][]int64{store.DirImport: importIDs, store.DirExport: exportIDs} {
		for _, id := range ids {
			pol, ok := byID[id]
			if !ok {
				errs["policies"] = "One of the selected policies no longer exists."
				return
			}
			if pol.Direction != dir {
				errs["policies"] = pol.Name + " is an " + pol.Direction + " policy and cannot be used as an " + dir + " policy."
				return
			}
		}
	}
}

func (s *Server) handlePeerDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := namedEntity(s, w, r, s.store.GetPeerByName, "peer")
	if !ok {
		return
	}
	if err := s.store.DeletePeer(p.ID); err != nil {
		s.serverError(w, "delete peer", err)
		return
	}
	s.audit(r, "deleted peer "+p.Name)
	http.Redirect(w, r, "/peers?flash="+flash("Deleted "+p.Name), http.StatusSeeOther)
}

// handlePeerToggle switches a peer off (or back on) straight from the list,
// because "shut this session" is a thing you reach for in a hurry and should not
// require opening a form and finding a checkbox.
//
// It changes the model, not the router — like every other edit in birdy. A
// disabled peer renders with BIRD's "disabled", so once applied BIRD stops
// trying to connect entirely; until then the session keeps running and the
// pending change sits on the Changes page. The flash says so, because a toggle
// that looks instant but is not would be worse than no toggle at all.
func (s *Server) handlePeerToggle(w http.ResponseWriter, r *http.Request) {
	p, ok := namedEntity(s, w, r, s.store.GetPeerByName, "peer")
	if !ok {
		return
	}
	if err := s.store.SetPeerEnabled(p.ID, !p.Enabled); err != nil {
		s.serverError(w, "toggle peer", err)
		return
	}
	verb := "Disabled"
	if !p.Enabled {
		verb = "Enabled"
	}
	s.audit(r, strings.ToLower(verb)+" peer "+p.Name)
	http.Redirect(w, r, "/peers?flash="+flash(verb+" "+p.Name+" — review it under Changes and apply to take effect on the router."), http.StatusSeeOther)
}

// renderPeerForm fills in the live BIRD-code preview and the lint findings
// before rendering. The preview always masks secrets: it goes to a browser.
func (s *Server) renderPeerForm(w http.ResponseWriter, v peerFormView) {
	v.PeeringDB = s.peeringDB
	policies, err := s.store.ListPolicies()
	if err != nil {
		s.serverError(w, "list policies", err)
		return
	}
	for _, p := range policies {
		if p.IsImport() {
			v.Imports = append(v.Imports, p)
		} else {
			v.Exports = append(v.Exports, p)
		}
	}
	sets, err := s.store.ListPrefixSets()
	if err != nil {
		s.serverError(w, "list prefix sets", err)
		return
	}
	asSets, err := s.store.ListASSets()
	if err != nil {
		s.serverError(w, "list AS sets", err)
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
	var rrClusterID string
	if settings, ok, err := s.store.GetSettings(); err == nil && ok {
		if settings.LocalASN.Valid {
			localASN = settings.LocalASN.Int64
		}
		rrClusterID = settings.RRClusterID
	}
	if defs, err := s.store.ListCommunityDefs(); err == nil {
		v.Communities = defs
	}
	v.Preview, v.PreviewErr, v.Warnings = previewPeer(v.Peer, sets, asSets, policies, rpkiServers, bogonASNs, localASN, rrClusterID)
	render(w, s.log, "peer_form.html", v)
}

// previewPeer renders just this peer's contribution to bird.conf, plus any lint
// findings about the session.
//
// The real local ASN is required, not a placeholder: it appears verbatim in the
// AS-path loop guard and in the large communities, and showing the wrong number
// there would teach the operator to distrust the preview.
func previewPeer(p store.Peer, sets []store.PrefixSet, asSets []store.ASSet, policies []store.Policy, rpkiServers []store.RPKIServer, bogonASNs []store.BogonASN, localASN int64, rrClusterID string) (string, string, []birdconf.Warning) {
	if localASN == 0 {
		return "", "Set the local ASN under Settings to preview the generated BIRD code.", nil
	}
	// Validate on a copy: Validate normalises in place and we do not want the
	// form to silently rewrite what the user typed while they are still typing.
	probe := p
	if errs := probe.Validate(); len(errs) > 0 {
		return "", "Fix the errors above to see the generated BIRD code.", nil
	}
	in := birdconf.Input{
		RouterID: "0.0.0.1", LocalASN: localASN, // the router id never appears in a peer block
		PrefixSets: sets, ASSets: asSets, Policies: policies, Peers: []store.Peer{probe},
		RPKIServers: rpkiServers, BogonASNs: bogonASNs, RRClusterID: rrClusterID, MaskSecrets: true,
	}
	full, err := birdconf.Config(in)
	if err != nil {
		return "", err.Error(), nil
	}
	return peerSection(full, probe.Name), "", birdconf.Lint(in)
}

// peerSection slices the generated config down to the filters and protocol
// block belonging to one peer, so the form preview is not swamped by globals
// and by every policy function in the library.
func peerSection(cfg, name string) string {
	markers := []string{"filter ebgp_in_" + name, "filter ebgp_out_" + name, "protocol bgp " + name + " {"}
	start := -1
	for _, m := range markers {
		if i := strings.Index(cfg, m); i >= 0 && (start < 0 || i < start) {
			start = i
		}
	}
	if start < 0 {
		return ""
	}
	return strings.TrimRight(cfg[start:], "\n")
}
