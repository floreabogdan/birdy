package web

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
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
	// Templates feeds the bulk "attach selected peers to" control.
	Templates []store.PeerTemplate
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

	// Templates is every peer template, for the link picker; TemplateData is
	// the same list keyed by id in the shape the form script fills governed
	// controls from when the picker changes.
	Templates    []store.PeerTemplate
	TemplateData map[int64]templateFormData
	// IsTemplate renders the form as a template editor: the identity card,
	// password and operational switches drop out, and the preview renders the
	// shape on a sample neighbor. Template is the one being edited.
	IsTemplate bool
	Template   store.PeerTemplate
	// Usage is how many linked peers a template save rewrites.
	Usage int
	// LinkSource names the peer a new template is being captured from, so the
	// form can offer to link that peer to the template it creates.
	LinkSource string
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
	// ?template=NAME narrows the list to one template's peers — the link the
	// templates page offers from its "used by N peers" count.
	if want := r.URL.Query().Get("template"); want != "" {
		kept := peers[:0]
		for _, p := range peers {
			if p.TemplateName == want {
				kept = append(kept, p)
			}
		}
		peers = kept
	}
	for i := range peers {
		if err := s.loadPeerChains(&peers[i]); err != nil {
			s.serverError(w, "peer policies", err)
			return
		}
	}
	templates, err := s.store.ListPeerTemplates()
	if err != nil {
		s.serverError(w, "list peer templates", err)
		return
	}
	offset, limit := parsePageParams(r)
	page := pageSlice(peers, offset, limit)
	render(w, s.log, "peers.html", peersView{
		Active: "peers", ReadOnly: s.readOnly, Peers: page, Live: s.liveStates(),
		Pager: pagerFor(r, offset, limit, len(page), len(peers)),
		Flash: s.flashMsg(w, r), Templates: templates,
	})
}

func (s *Server) handlePeerNew(w http.ResponseWriter, r *http.Request) {
	// Clone an existing peer: keep its role, policy chains, limits and export
	// transforms; drop the identity (name, addresses, ASN) and never carry the
	// password. A clone of a linked peer is linked to the same template — the
	// shape came from there, and should keep following it. A clone of an
	// unlinked peer is the one-off version of that: the shape is copied, and
	// the copy is its own.
	if from := r.URL.Query().Get("from"); from != "" {
		src, err := s.store.GetPeerByName(from)
		if err == nil {
			if err := s.loadPeerChains(&src); err != nil {
				s.serverError(w, "peer policies", err)
				return
			}
			src.ID, src.Name, src.NeighborIP, src.LocalIP, src.Interface, src.TransportEndpoint, src.RemoteASN, src.Password = 0, "", "", "", "", "", 0, ""
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
		BGPRole: true, NextHopSelf: true, ImportLimit: 1500000,
		ImportLimitAction: "restart", GracefulRestart: store.GRAware}
	// "Add peer" from a template's row: the new session starts linked, so only
	// the identity is left to type.
	if name := r.URL.Query().Get("template"); name != "" {
		if t, err := s.store.GetPeerTemplateByName(name); err == nil {
			t.ApplyTo(&p)
			s.renderPeerForm(w, peerFormView{Active: "peers", ReadOnly: s.readOnly, IsNew: true, Peer: p})
			return
		}
	}
	// A first-time operator should get a useful, fail-closed upstream by filling
	// in identity fields and saving. These are ordinary selections in the form,
	// so changing the relationship remains explicit and reversible.
	if pol, err := s.store.GetPolicyByName("IMPORT_SANITY"); err == nil {
		p.ImportPolicies = []store.Policy{pol}
	}
	if pol, err := s.store.GetPolicyByName("EXPORT_OWN"); err == nil {
		p.ExportPolicies = []store.Policy{pol}
	}
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
		Interface:         r.FormValue("interface"),
		TransportEndpoint: r.FormValue("transportEndpoint"),
		Multihop:          formInt(r, "multihop"),
		Passive:           r.FormValue("passive") == "on",
		Password:          r.FormValue("password"),
		ImportLimit:       formInt(r, "importLimit"),
		ImportLimitAction: r.FormValue("importLimitAction"),
		ImportCommunities: strings.TrimSpace(r.FormValue("importCommunities")),
		EnforceFirstAS:    r.FormValue("enforceFirstAs") == "on",
		OriginPeerOnly:    r.FormValue("originPeerOnly") == "on",
		BGPRole:           r.FormValue("bgpRole") == "on",
		NextHopSelf:       r.FormValue("nextHopSelf") == "on",
		RRClient:          r.FormValue("rrClient") == "on",
		IBGPExportDefault: r.FormValue("ibgpExportDefault"),
		PrependCount:      formInt(r, "prependCount"),
		ExportCommunities: strings.TrimSpace(r.FormValue("exportCommunities")),
		Drained:           r.FormValue("drained") == "on",
		BFD:               r.FormValue("bfd") == "on",
		GTSM:              r.FormValue("gtsm") == "on",
		GracefulRestart:   r.FormValue("gracefulRestart"),
		TemplateID:        formNullInt(r, "templateId"),
		TemplateOverrides: overridesFromForm(r),
	}
}

// overridesFromForm reads the per-peer override switches a linked peer's form
// offers. One for now: the import limit, so one oversized IX peer can carry a
// higher limit than the rest of its template without a template of its own.
func overridesFromForm(r *http.Request) string {
	if r.FormValue("overrideImportLimit") == "on" {
		return store.OverrideImportLimit
	}
	return ""
}

// formNullInt reads an optional positive integer — a foreign key picker whose
// blank option means "none".
func formNullInt(r *http.Request, key string) sql.NullInt64 {
	n, err := strconv.ParseInt(strings.TrimSpace(r.FormValue(key)), 10, 64)
	if err != nil || n <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: n, Valid: true}
}

// applyTemplate makes a linked peer take its shape from its template, before
// validation and regardless of what the form posted for the governed fields
// (the form disables them, so it posts nothing; a hand-built request could post
// anything, and is ignored the same way). It returns the template's chain ids,
// which replace whatever chain was posted, and a message for the form when the
// template no longer exists.
func (s *Server) applyTemplate(p *store.Peer) (importIDs, exportIDs []int64, errMsg string, err error) {
	if !p.TemplateID.Valid {
		return nil, nil, "", nil
	}
	t, err := s.store.GetPeerTemplate(p.TemplateID.Int64)
	if err == store.ErrNotFound {
		return nil, nil, "That template no longer exists. Choose another, or none.", nil
	}
	if err != nil {
		return nil, nil, "", err
	}
	t.ApplyTo(p)
	return store.PolicyIDs(t.ImportPolicies), store.PolicyIDs(t.ExportPolicies), "", nil
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

	// The template wins over the posted governed fields and chain, and is
	// applied before Validate so the effective peer is what gets checked.
	tplImports, tplExports, tplMsg, err := s.applyTemplate(&p)
	if err != nil {
		s.serverError(w, "load peer template", err)
		return
	}
	if p.TemplateID.Valid && tplMsg == "" {
		importIDs, exportIDs = tplImports, tplExports
	}

	errs := p.Validate()
	if tplMsg != "" {
		errs["template"] = tplMsg
	}
	s.checkChains(importIDs, exportIDs, errs)
	if msg := s.checkCommunityRefs(p.ExportCommunities); msg != "" {
		errs["exportCommunities"] = msg
	}
	if msg := s.checkCommunityRefs(p.ImportCommunities); msg != "" {
		errs["importCommunities"] = msg
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
		s.flashRedirect(w, r, "/peers", "Saved "+p.Name, false)
		return
	}

	// Re-render with what the user typed, chains included — one policy scan,
	// resolved via the shared helper.
	if all, aerr := s.store.ListPolicies(); aerr == nil {
		p.ImportPolicies, p.ExportPolicies = s.resolvePolicies(all, importIDs), s.resolvePolicies(all, exportIDs)
	}
	s.renderPeerForm(w, peerFormView{Active: "peers", ReadOnly: s.readOnly, IsNew: isNew, Peer: p, Errs: errs})
}

// handlePeersAttach links every selected peer to one template — or, with no
// template chosen, detaches them — straight from the peers list. It is the
// migration path for a router whose thirty IX peers were configured one by one
// before templates existed: capture one as a template, then attach the rest.
// Attaching overwrites each peer's shape with the template's; like every model
// edit, nothing reaches the router until the result is applied.
func (s *Server) handlePeersAttach(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	names := r.Form["peer"]
	if len(names) == 0 {
		s.flashRedirect(w, r, "/peers", "Select at least one peer first.", true)
		return
	}
	var tmpl store.PeerTemplate
	if id := formNullInt(r, "templateId"); id.Valid {
		t, err := s.store.GetPeerTemplate(id.Int64)
		if err == store.ErrNotFound {
			s.flashRedirect(w, r, "/peers", "That template no longer exists.", true)
			return
		}
		if err != nil {
			s.serverError(w, "get peer template", err)
			return
		}
		tmpl = t
	}

	var done, missing []string
	for _, name := range names {
		p, err := s.store.GetPeerByName(name)
		if err == store.ErrNotFound {
			missing = append(missing, name)
			continue
		}
		if err != nil {
			s.serverError(w, "get peer", err)
			return
		}
		if tmpl.ID != 0 {
			err = s.store.LinkPeerToTemplate(p.ID, tmpl.ID)
		} else {
			err = s.store.DetachPeer(p.ID)
		}
		if err != nil {
			s.serverError(w, "attach peer to template", err)
			return
		}
		done = append(done, name)
	}

	var msg string
	if tmpl.ID != 0 {
		s.audit(r, fmt.Sprintf("attached %d peer(s) to template %s: %s", len(done), tmpl.Name, strings.Join(done, ", ")))
		msg = fmt.Sprintf("Attached %d %s to %s — their chains, limits and safeguards now come from the template. Review them under Changes and apply to take effect on the router.", len(done), plural(len(done), "peer"), tmpl.Name)
	} else {
		s.audit(r, fmt.Sprintf("detached %d peer(s) from their templates: %s", len(done), strings.Join(done, ", ")))
		msg = fmt.Sprintf("Detached %d %s — each keeps the settings it had and owns them from now on.", len(done), plural(len(done), "peer"))
	}
	if len(missing) > 0 {
		msg += " Not found: " + strings.Join(missing, ", ") + "."
	}
	s.flashRedirect(w, r, "/peers", msg, false)
}

// checkChains rejects a chain that names a policy of the wrong direction, or a
// policy that no longer exists.
func (s *Server) checkChains(importIDs, exportIDs []int64, errs map[string]string) {
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
	s.flashRedirect(w, r, "/peers", "Deleted "+p.Name, false)
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
	s.flashRedirect(w, r, "/peers", verb+" "+p.Name+" — review it under Changes and apply to take effect on the router.", false)
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
	if !v.IsTemplate {
		templates, err := s.store.ListPeerTemplates()
		if err != nil {
			s.serverError(w, "list peer templates", err)
			return
		}
		v.Templates = templates
		v.TemplateData = make(map[int64]templateFormData, len(templates))
		for _, t := range templates {
			v.TemplateData[t.ID] = formDataFor(t)
		}
	}
	if defs, err := s.store.ListCommunityDefs(); err == nil {
		v.Communities = defs
	}
	subject, templates := v.Peer, v.Templates
	if v.IsTemplate {
		// A template has no neighbor; the preview shows its shape on a sample one,
		// declared "from" the template as it is on the form right now.
		t := store.TemplateFromPeer(v.Peer)
		t.Name, t.Description = v.Peer.Name, v.Peer.Description
		subject, templates = samplePeer(t), []store.PeerTemplate{t}
	}
	var perr error
	if v.Preview, v.PreviewErr, v.Warnings, perr = s.previewWithLibrary(subject, policies, templates); perr != nil {
		s.serverError(w, "load library for preview", perr)
		return
	}
	if v.IsTemplate {
		v.Warnings = attributeToTemplate(v.Warnings, subject.Name, v.Peer.Name)
	}
	render(w, s.log, "peer_form.html", v)
}

// previewWithLibrary renders one peer's BIRD code against the current library
// — sets, AS sets, RPKI servers, bogons, identity — which every peer-shaped
// preview (the form, the live endpoint, the template editor) needs in the same
// way. The error is a failed library read; the form page surfaces it, the live
// endpoint leaves the last good preview in place.
//
// templates is what the preview may declare a peer "from": the stored ones for
// a peer form, or the one being edited for the template editor.
func (s *Server) previewWithLibrary(p store.Peer, policies []store.Policy, templates []store.PeerTemplate) (preview, previewErr string, warnings []birdconf.Warning, err error) {
	lib := previewLibrary{policies: policies, templates: templates}
	if lib.sets, err = s.store.ListPrefixSets(); err != nil {
		return "", "", nil, err
	}
	if lib.asSets, err = s.store.ListASSets(); err != nil {
		return "", "", nil, err
	}
	if lib.rpkiServers, err = s.store.ListRPKIServers(); err != nil {
		return "", "", nil, err
	}
	if lib.bogonASNs, err = s.store.ListBogonASNs(); err != nil {
		return "", "", nil, err
	}
	if settings, ok, err := s.store.GetSettings(); err == nil && ok {
		if settings.LocalASN.Valid {
			lib.localASN = settings.LocalASN.Int64
		}
		lib.rrClusterID = settings.RRClusterID
	}
	preview, previewErr, warnings = previewPeer(p, lib)
	return preview, previewErr, warnings, nil
}

// previewPeer renders just this peer's contribution to bird.conf, plus any lint
// findings about the session.
//
// The real local ASN is required, not a placeholder: it appears verbatim in the
// AS-path loop guard and in the large communities, and showing the wrong number
// there would teach the operator to distrust the preview.
func previewPeer(p store.Peer, lib previewLibrary) (string, string, []birdconf.Warning) {
	if lib.localASN == 0 {
		return "", "Set the local ASN under Settings to preview the generated BIRD code.", nil
	}
	// Validate on a copy: Validate normalises in place and we do not want the
	// form to silently rewrite what the user typed while they are still typing.
	probe := p
	if errs := probe.Validate(); len(errs) > 0 {
		return "", "Fix the errors above to see the generated BIRD code.", nil
	}
	in := birdconf.Input{
		RouterID: "0.0.0.1", LocalASN: lib.localASN, // the router id never appears in a peer block
		PrefixSets: lib.sets, ASSets: lib.asSets, Policies: lib.policies, Peers: []store.Peer{probe},
		Templates: lib.templates, RPKIServers: lib.rpkiServers, BogonASNs: lib.bogonASNs,
		RRClusterID: lib.rrClusterID, MaskSecrets: true,
	}
	secs, err := birdconf.Sections(in)
	if err != nil {
		return "", err.Error(), nil
	}
	return peerSection(secs, probe), "", birdconf.Lint(in)
}

// previewLibrary is everything a peer-shaped preview renders against besides
// the peer itself: the library, the identity, and — for a linked peer — its
// template.
type previewLibrary struct {
	sets        []store.PrefixSet
	asSets      []store.ASSet
	policies    []store.Policy
	templates   []store.PeerTemplate
	rpkiServers []store.RPKIServer
	bogonASNs   []store.BogonASN
	localASN    int64
	rrClusterID string
}

// peerSection is the form preview: just this peer's filters and protocol
// block, so it is not swamped by globals and by every policy function in the
// library — preceded, for a linked peer, by the template block it is declared
// "from", since that is where half of its session options now live.
func peerSection(secs []birdconf.Section, p store.Peer) string {
	var out strings.Builder
	for _, s := range secs {
		if p.TemplateName != "" && s.Path == "templates/"+p.TemplateName {
			out.WriteString(s.Body)
		}
	}
	for _, s := range secs {
		if s.Path == "peers/"+p.Name {
			out.WriteString(s.Body)
		}
	}
	return strings.TrimRight(out.String(), "\n")
}
