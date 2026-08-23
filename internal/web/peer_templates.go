package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/floreabogdan/birdy/internal/store"
)

// Peer templates: the shape of a session — role, chains, limits, safeguards,
// transforms — kept once and linked from many peers. The editor is the peer
// form with the identity card removed; a template round-trips through the
// same peerFromForm and the same live preview, with a sample neighbor standing
// in for the identity it does not have.

type peerTemplatesView struct {
	Active    string
	ReadOnly  bool
	Templates []store.PeerTemplate
	// Usage counts the peers linked to each template, by template id.
	Usage map[int64]int
	Flash string
}

// templateFormData is what the peer form's script fills governed controls
// from when a template is picked. The JSON keys are the form field names, so
// the script can address each control by its key and never carry a mapping of
// its own. Chains travel as ordered policy ids.
type templateFormData struct {
	Name              string  `json:"name"`
	Role              string  `json:"role"`
	Multihop          int     `json:"multihop"`
	Passive           bool    `json:"passive"`
	ImportLimit       int     `json:"importLimit"`
	ImportLimitAction string  `json:"importLimitAction"`
	ImportCommunities string  `json:"importCommunities"`
	ExportCommunities string  `json:"exportCommunities"`
	PrependCount      int     `json:"prependCount"`
	EnforceFirstAS    bool    `json:"enforceFirstAs"`
	OriginPeerOnly    bool    `json:"originPeerOnly"`
	BGPRole           bool    `json:"bgpRole"`
	GTSM              bool    `json:"gtsm"`
	BFD               bool    `json:"bfd"`
	GracefulRestart   string  `json:"gracefulRestart"`
	NextHopSelf       bool    `json:"nextHopSelf"`
	RRClient          bool    `json:"rrClient"`
	IBGPExportDefault string  `json:"ibgpExportDefault"`
	ImportPolicyIDs   []int64 `json:"importPolicyIds"`
	ExportPolicyIDs   []int64 `json:"exportPolicyIds"`
}

func formDataFor(t store.PeerTemplate) templateFormData {
	return templateFormData{
		Name: t.Name, Role: t.Role, Multihop: t.Multihop, Passive: t.Passive,
		ImportLimit: t.ImportLimit, ImportLimitAction: t.ImportLimitAction,
		ImportCommunities: t.ImportCommunities, ExportCommunities: t.ExportCommunities,
		PrependCount: t.PrependCount, EnforceFirstAS: t.EnforceFirstAS, OriginPeerOnly: t.OriginPeerOnly,
		BGPRole: t.BGPRole, GTSM: t.GTSM, BFD: t.BFD, GracefulRestart: t.GracefulRestart,
		NextHopSelf: t.NextHopSelf, RRClient: t.RRClient, IBGPExportDefault: t.IBGPExportDefault,
		ImportPolicyIDs: store.PolicyIDs(t.ImportPolicies), ExportPolicyIDs: store.PolicyIDs(t.ExportPolicies),
	}
}

// sampleIdentity is the neighbor a template's preview renders for. A template
// has no neighbor of its own, but the generated filter embeds one — the
// first-AS and origin checks compare against the remote ASN — so the preview
// shows the shape on a documentation-range session named after the template.
const (
	sampleNeighborIP = "192.0.2.1"
	sampleRemoteASN  = 64496
)

// displayPeer is the peer the form renders for a template: the template's
// shape over a blank identity, with the template's own name and description in
// the shared name/description inputs.
func displayPeer(t store.PeerTemplate) store.Peer {
	var p store.Peer
	t.ApplyTo(&p)
	p.Name, p.Description, p.Enabled = t.Name, t.Description, true
	return p
}

// templateFromForm reads a template out of the posted peer form: the governed
// fields through peerFromForm, the identity from the two fields a template has.
func templateFromForm(r *http.Request) store.PeerTemplate {
	p := peerFromForm(r)
	t := store.TemplateFromPeer(p)
	t.Name, t.Description = p.Name, p.Description
	return t
}

func (s *Server) handlePeerTemplatesList(w http.ResponseWriter, r *http.Request) {
	templates, err := s.store.ListPeerTemplates()
	if err != nil {
		s.serverError(w, "list peer templates", err)
		return
	}
	usage, err := s.store.TemplateUsage()
	if err != nil {
		s.serverError(w, "template usage", err)
		return
	}
	render(w, s.log, "peer_templates.html", peerTemplatesView{
		Active: "peers", ReadOnly: s.readOnly, Templates: templates, Usage: usage, Flash: s.flashMsg(w, r),
	})
}

// handlePeerTemplateNew starts a blank template, or — with ?from=<peer> —
// captures an existing peer's shape so "make the other 29 look like this one"
// is a name and a save away. The source peer is offered a link to the new
// template, ticked, because that is nearly always what the operator means.
func (s *Server) handlePeerTemplateNew(w http.ResponseWriter, r *http.Request) {
	v := peerFormView{Active: "peers", ReadOnly: s.readOnly, IsNew: true, IsTemplate: true}
	if from := r.URL.Query().Get("from"); from != "" {
		src, err := s.store.GetPeerByName(from)
		if err == nil {
			if err := s.loadPeerChains(&src); err != nil {
				s.serverError(w, "peer policies", err)
				return
			}
			t := store.TemplateFromPeer(src)
			v.Peer, v.LinkSource = displayPeer(t), from
			s.renderPeerForm(w, v)
			return
		}
		// Source gone — a blank template rather than a 404.
	}
	t := store.PeerTemplate{Role: store.RoleIXPeer, ImportLimit: 100000, ImportLimitAction: "restart",
		EnforceFirstAS: true, BGPRole: true, GTSM: true, NextHopSelf: true, GracefulRestart: store.GRAware}
	if pol, err := s.store.GetPolicyByName("IMPORT_SANITY"); err == nil {
		t.ImportPolicies = []store.Policy{pol}
	}
	if pol, err := s.store.GetPolicyByName("EXPORT_OWN"); err == nil {
		t.ExportPolicies = []store.Policy{pol}
	}
	v.Peer = displayPeer(t)
	s.renderPeerForm(w, v)
}

func (s *Server) handlePeerTemplateEdit(w http.ResponseWriter, r *http.Request) {
	t, ok := namedEntity(s, w, r, s.store.GetPeerTemplateByName, "peer template")
	if !ok {
		return
	}
	s.renderPeerForm(w, s.templateFormView(t, false))
}

// templateFormView builds the editor view for a stored template, including
// how many peers a save would rewrite — the one number the editor must show.
func (s *Server) templateFormView(t store.PeerTemplate, isNew bool) peerFormView {
	v := peerFormView{Active: "peers", ReadOnly: s.readOnly, IsNew: isNew, IsTemplate: true, Template: t, Peer: displayPeer(t)}
	if usage, err := s.store.TemplateUsage(); err == nil {
		v.Usage = usage[t.ID]
	}
	return v
}

func (s *Server) handlePeerTemplateSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	isNew := r.PathValue("name") == ""
	t := templateFromForm(r)
	importIDs := idList(r.Form["importPolicyIds"])
	exportIDs := idList(r.Form["exportPolicyIds"])
	if !isNew {
		existing, ok := namedEntity(s, w, r, s.store.GetPeerTemplateByName, "peer template")
		if !ok {
			return
		}
		t.ID = existing.ID
	}

	errs := t.Validate()
	s.checkChains(importIDs, exportIDs, errs)
	if msg := s.checkCommunityRefs(t.ExportCommunities); msg != "" {
		errs["exportCommunities"] = msg
	}
	if msg := s.checkCommunityRefs(t.ImportCommunities); msg != "" {
		errs["importCommunities"] = msg
	}

	var rewrote int
	if len(errs) == 0 {
		var err error
		if isNew {
			t.ID, err = s.store.CreatePeerTemplate(t, importIDs, exportIDs)
		} else {
			rewrote, err = s.store.UpdatePeerTemplate(t, importIDs, exportIDs)
		}
		if err != nil {
			if isUniqueViolation(err) {
				errs["name"] = "A peer template with this name already exists."
			} else {
				s.serverError(w, "save peer template", err)
				return
			}
		}
	}
	if len(errs) == 0 {
		if isNew {
			msg := "Created template " + t.Name + "."
			// "Save as template" from a peer: link the source too, if asked.
			if from := r.FormValue("from"); from != "" && r.FormValue("linkSource") == "on" {
				if src, err := s.store.GetPeerByName(from); err == nil {
					if err := s.store.LinkPeerToTemplate(src.ID, t.ID); err != nil {
						s.serverError(w, "link peer to template", err)
						return
					}
					msg += " " + from + " is now linked to it."
				}
			}
			s.audit(r, "created peer template "+t.Name)
			s.flashRedirect(w, r, "/peers/templates", msg, false)
			return
		}
		s.audit(r, fmt.Sprintf("updated peer template %s (%d linked peer(s) rewritten)", t.Name, rewrote))
		msg := "Saved template " + t.Name + "."
		switch rewrote {
		case 0:
			msg += " No peers are linked to it yet."
		case 1:
			msg += " Its 1 linked peer was updated — review it under Changes and apply to take effect on the router."
		default:
			msg += fmt.Sprintf(" Its %d linked peers were updated — review them under Changes and apply to take effect on the router.", rewrote)
		}
		s.flashRedirect(w, r, "/peers/templates", msg, false)
		return
	}

	// Re-render with what the operator typed, chains included.
	if all, aerr := s.store.ListPolicies(); aerr == nil {
		t.ImportPolicies, t.ExportPolicies = s.resolvePolicies(all, importIDs), s.resolvePolicies(all, exportIDs)
	}
	v := s.templateFormView(t, isNew)
	v.Errs, v.LinkSource = errs, r.FormValue("from")
	s.renderPeerForm(w, v)
}

func (s *Server) handlePeerTemplateDelete(w http.ResponseWriter, r *http.Request) {
	t, ok := namedEntity(s, w, r, s.store.GetPeerTemplateByName, "peer template")
	if !ok {
		return
	}
	if err := s.store.DeletePeerTemplate(t.ID); err != nil {
		msg := strings.TrimPrefix(err.Error(), "store: ")
		s.flashRedirect(w, r, "/peers/templates", "Could not delete "+t.Name+": "+msg+". Detach those peers first (choose \"none\" as their template).", true)
		return
	}
	s.audit(r, "deleted peer template "+t.Name)
	s.flashRedirect(w, r, "/peers/templates", "Deleted template "+t.Name, false)
}

// handlePeerTemplatePreview is the template editor's live preview: the posted
// shape rendered for the sample neighbor.
func (s *Server) handlePeerTemplatePreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, previewResp{Err: "bad form"})
		return
	}
	policiesAll, _ := s.store.ListPolicies()
	t := templateFromForm(r)
	t.ImportPolicies = s.resolvePolicies(policiesAll, idList(r.Form["importPolicyIds"]))
	t.ExportPolicies = s.resolvePolicies(policiesAll, idList(r.Form["exportPolicyIds"]))
	preview, previewErr, warnings, err := s.previewWithLibrary(samplePeer(t), policiesAll)
	if err != nil {
		writeJSON(w, previewResp{Err: "could not load the library"})
		return
	}
	writeJSON(w, previewResp{Preview: preview, Err: previewErr, Warnings: warnings})
}

// samplePeer is the template's shape on the documentation-range neighbor the
// preview renders. The template name doubles as the protocol name, so the
// preview reads "protocol bgp IX_PEERS" — recognisably the template, not a peer.
func samplePeer(t store.PeerTemplate) store.Peer {
	p := displayPeer(t)
	p.NeighborIP, p.RemoteASN = sampleNeighborIP, sampleRemoteASN
	if p.Name == "" {
		p.Name = "template"
	}
	return p
}
