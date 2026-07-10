package web

import (
	"net/http"

	"github.com/floreabogdan/birdy/internal/birdc"
)

// PeerRoutesView backs the paginated Imports/Exports/Rejected tabs on the
// peer detail page — inline, so getting route detail for a session no
// longer means navigating away to the looking glass.
type PeerRoutesView struct {
	Name    string             `json:"name"`
	Dir     string             `json:"dir"`
	Tables  []birdc.RouteTable `json:"tables,omitempty"`
	Err     string             `json:"err,omitempty"`
	Offset  int                `json:"offset"`
	Limit   int                `json:"limit"`
	HasMore bool               `json:"hasMore"`
}

var peerRouteDirs = map[string]bool{"protocol": true, "export": true, "noexport": true}

func (s *Server) apiPeerRoutes(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = "protocol"
	}
	offset, limit := parsePageParams(r)

	v := PeerRoutesView{Name: name, Dir: dir, Offset: offset, Limit: limit}
	if !peerRouteDirs[dir] {
		v.Err = "invalid dir"
		writeJSON(w, v)
		return
	}

	var page birdc.RoutePage
	var err error
	switch dir {
	case "protocol":
		page, err = s.client.RoutesByProtocolPage(name, offset, limit)
	case "export":
		page, err = s.client.RoutesExportPage(name, offset, limit)
	case "noexport":
		page, err = s.client.RoutesNoExportPage(name, offset, limit)
	}
	if err != nil {
		v.Err = err.Error()
		writeJSON(w, v)
		return
	}
	v.Tables = page.Tables
	v.HasMore = page.HasMore
	writeJSON(w, v)
}
