package web

import (
	"net/http"
	"strings"

	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

// previewResp is the JSON a live preview endpoint returns: the generated BIRD
// code, an error message if the form does not yet render, and any lint findings.
type previewResp struct {
	Preview  string             `json:"preview"`
	Err      string             `json:"err"`
	Warnings []birdconf.Warning `json:"warnings"`
}

// These endpoints render exactly what the server-side preview would, so the live
// pane and the reloaded page never disagree. They only read, so they are allowed
// in read-only mode.

func (s *Server) handlePeerPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, previewResp{Err: "bad form"})
		return
	}
	sets, _ := s.store.ListPrefixSets()
	asSets, _ := s.store.ListASSets()
	policiesAll, _ := s.store.ListPolicies()
	rpki, _ := s.store.ListRPKIServers()
	bogons, _ := s.store.ListBogonASNs()

	var localASN int64
	var rrClusterID string
	if settings, ok, err := s.store.GetSettings(); err == nil && ok {
		if settings.LocalASN.Valid {
			localASN = settings.LocalASN.Int64
		}
		rrClusterID = settings.RRClusterID
	}

	p := peerFromForm(r)
	// The chains come from the repeated selects, resolved to full policies so the
	// preview reflects the exact filter code they generate.
	p.ImportPolicies = s.resolvePolicies(policiesAll, idList(r.Form["importPolicyIds"]))
	p.ExportPolicies = s.resolvePolicies(policiesAll, idList(r.Form["exportPolicyIds"]))

	preview, previewErr, warnings := previewPeer(p, sets, asSets, policiesAll, rpki, bogons, localASN, rrClusterID)
	writeJSON(w, previewResp{Preview: preview, Err: previewErr, Warnings: warnings})
}

func (s *Server) handlePolicyPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, previewResp{Err: "bad form"})
		return
	}
	sets, _ := s.store.ListPrefixSets()
	asSets, _ := s.store.ListASSets()
	rpki, _ := s.store.ListRPKIServers()
	bogons, _ := s.store.ListBogonASNs()
	var localASN int64
	if settings, ok, err := s.store.GetSettings(); err == nil && ok && settings.LocalASN.Valid {
		localASN = settings.LocalASN.Int64
	}
	preview, previewErr := previewPolicy(policyFromForm(r), sets, asSets, rpki, bogons, localASN)
	writeJSON(w, previewResp{Preview: preview, Err: previewErr})
}

func (s *Server) handlePrefixSetPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, previewResp{Err: "bad form"})
		return
	}
	preview, previewErr := previewPrefixSet(prefixSetFromForm(r))
	writeJSON(w, previewResp{Preview: preview, Err: previewErr})
}

func (s *Server) handleASSetPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, previewResp{Err: "bad form"})
		return
	}
	entries, _ := store.ParseASNRanges(r.FormValue("entries"))
	as := store.ASSet{
		Name:        r.FormValue("name"),
		Description: strings.TrimSpace(r.FormValue("description")),
		Source:      strings.TrimSpace(r.FormValue("source")),
		Entries:     entries,
	}
	preview, previewErr := previewASSet(as)
	writeJSON(w, previewResp{Preview: preview, Err: previewErr})
}

func (s *Server) handleStaticRoutePreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, previewResp{Err: "bad form"})
		return
	}
	route := store.StaticRoute{
		Prefix:      r.FormValue("prefix"),
		Action:      r.FormValue("action"),
		NextHop:     r.FormValue("nextHop"),
		Description: r.FormValue("description"),
		Enabled:     r.FormValue("enabled") == "on",
	}
	preview, previewErr := previewStaticRoute(route)
	writeJSON(w, previewResp{Preview: preview, Err: previewErr})
}

// resolvePolicies turns an ordered list of ids into the matching policies, in
// the order given, so a preview reflects the chain the operator arranged.
func (s *Server) resolvePolicies(all []store.Policy, ids []int64) []store.Policy {
	by := make(map[int64]store.Policy, len(all))
	for _, p := range all {
		by[p.ID] = p
	}
	out := make([]store.Policy, 0, len(ids))
	for _, id := range ids {
		if p, ok := by[id]; ok {
			// An export policy needs its prefix-set ids for the preview; the list
			// from ListPolicies already has them filled.
			out = append(out, p)
		}
	}
	return out
}
