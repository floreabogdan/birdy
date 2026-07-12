package web

import (
	"net/http"
	"net/url"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
)

// SessionDetailView is the live view of one BIRD protocol, served at
// /peers/{name}. A peer is what birdy's model says should run; this page shows
// what BIRD is actually doing with it, which is not always the same thing.
type SessionDetailView struct {
	Active   string               `json:"-"`
	ReadOnly bool                 `json:"readOnly"`
	Tab      string               `json:"-"`
	Name     string               `json:"name"`
	Detail   birdc.ProtocolDetail `json:"detail"`
	Err      string               `json:"err,omitempty"`
	// Configured reports whether birdy's model has a peer of this name, so the
	// page can offer a link to its configuration instead of a dead end — and so
	// a peer BIRD has never heard of reads as "not applied" rather than an error.
	Configured bool `json:"configured"`

	// Imported/Exported are the recent route-count history for this session, drawn
	// as sparklines. Empty until the poller has recorded at least two samples.
	Imported []int `json:"imported,omitempty"`
	Exported []int `json:"exported,omitempty"`
}

// HasHistory reports whether there are enough samples to draw a trend.
func (v SessionDetailView) HasHistory() bool { return len(v.Imported) >= 2 }

func (s *Server) buildSessionDetailView(name string) SessionDetailView {
	v := SessionDetailView{Active: "peers", ReadOnly: s.readOnly, Name: name}
	if _, err := s.store.GetPeerByName(name); err == nil {
		v.Configured = true
	}
	if samples, err := s.store.ListSamples(name, time.Now().Add(-peerHistoryWindow)); err == nil {
		for _, sm := range samples {
			v.Imported = append(v.Imported, sm.Imported)
			v.Exported = append(v.Exported, sm.Exported)
		}
		v.Imported = downsample(v.Imported, peerHistoryPoints)
		v.Exported = downsample(v.Exported, peerHistoryPoints)
	}
	detail, err := s.client.ProtocolDetail(name)
	if err != nil {
		v.Err = err.Error()
		return v
	}
	v.Detail = detail
	return v
}

func (s *Server) handlePeerDetail(w http.ResponseWriter, r *http.Request) {
	v := s.buildSessionDetailView(r.PathValue("name"))
	v.Tab = tabParam(r, "general", "bird")
	render(w, s.log, "peer_detail.html", v)
}

func (s *Server) apiPeerDetail(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildSessionDetailView(r.PathValue("name")))
}

// handleLegacySessionDetail keeps /sessions/{name} bookmarks alive now that the
// live view lives beside the peer it belongs to.
func (s *Server) handleLegacySessionDetail(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/peers/"+url.PathEscape(r.PathValue("name")), http.StatusMovedPermanently)
}

// liveStates indexes the poll snapshot by protocol name, so the peers list can
// show whether each configured peer is actually running.
func (s *Server) liveStates() map[string]protoRow {
	rows, _, _ := buildProtoRows(s.poller.Snapshot())
	out := make(map[string]protoRow, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}
