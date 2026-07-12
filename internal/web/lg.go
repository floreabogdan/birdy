package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/floreabogdan/birdy/internal/birdc"
)

// lgRoute is a route decorated with its decoded communities for display.
type lgRoute struct {
	birdc.RouteEntry
	Comms []commChip
}

// lgTable groups decorated routes for one BIRD table.
type lgTable struct {
	Name   string
	Routes []lgRoute
}

type LGView struct {
	Active   string    `json:"-"`
	ReadOnly bool      `json:"readOnly"`
	Type     string    `json:"type"`
	Target   string    `json:"target"`
	All      bool      `json:"all"`
	Tables   []lgTable `json:"tables,omitempty"`
	Err      string    `json:"err,omitempty"`
	Ran      bool      `json:"ran"`
	Offset   int       `json:"offset"`
	Limit    int       `json:"limit"`
	HasMore  bool      `json:"hasMore"`
	// FirstRow/LastRow are the 1-based row numbers of this page, precomputed
	// because templates can't do arithmetic. Zero when the page is empty.
	FirstRow int    `json:"firstRow"`
	LastRow  int    `json:"lastRow"`
	PrevLink string `json:"-"`
	NextLink string `json:"-"`
}

var lgTypes = map[string]string{
	"for":      "Route for prefix / IP",
	"protocol": "Imported from peer",
	"export":   "Exported to peer",
	"noexport": "Rejected on export to peer",
}

func (s *Server) runLookingGlass(r *http.Request) LGView {
	offset, limit := parsePageParams(r)
	v := LGView{
		Active:   "lg",
		ReadOnly: s.readOnly,
		Type:     r.URL.Query().Get("type"),
		Target:   strings.TrimSpace(r.URL.Query().Get("target")),
		All:      r.URL.Query().Get("all") == "1",
		Offset:   offset,
		Limit:    limit,
	}
	if v.Type == "" {
		v.Type = "for"
	}
	if v.Target == "" {
		return v
	}
	if _, ok := lgTypes[v.Type]; !ok {
		v.Err = "unknown query type"
		return v
	}

	var page birdc.RoutePage
	var err error
	switch v.Type {
	case "for":
		page, err = s.client.RoutesForPage(v.Target, v.All, offset, limit)
	case "protocol":
		page, err = s.client.RoutesByProtocolPage(v.Target, v.All, offset, limit)
	case "export":
		page, err = s.client.RoutesExportPage(v.Target, v.All, offset, limit)
	case "noexport":
		page, err = s.client.RoutesNoExportPage(v.Target, v.All, offset, limit)
	}
	v.Ran = true
	if err != nil {
		v.Err = err.Error()
		return v
	}
	v.Tables = s.decorateRoutes(page.Tables)
	v.HasMore = page.HasMore
	shown := 0
	for _, t := range page.Tables {
		shown += len(t.Routes)
	}
	if shown > 0 {
		v.FirstRow = v.Offset + 1
		v.LastRow = v.Offset + shown
	}
	if v.Offset > 0 {
		v.PrevLink = lgPageLink(v, max(0, v.Offset-v.Limit))
	}
	if v.HasMore {
		v.NextLink = lgPageLink(v, v.Offset+v.Limit)
	}
	return v
}

// decorateRoutes annotates every route's communities with decoded labels so the
// looking glass can show what a route is tagged with, not just the raw tuples.
func (s *Server) decorateRoutes(tables []birdc.RouteTable) []lgTable {
	var localASN int64
	if st, ok, err := s.store.GetSettings(); err == nil && ok && st.LocalASN.Valid {
		localASN = st.LocalASN.Int64
	}
	semantic := s.semanticLabels(localASN)
	named := s.namedCommunities()
	out := make([]lgTable, 0, len(tables))
	for _, t := range tables {
		dt := lgTable{Name: t.Name, Routes: make([]lgRoute, 0, len(t.Routes))}
		for _, r := range t.Routes {
			dt.Routes = append(dt.Routes, lgRoute{RouteEntry: r, Comms: decodeCommunities(r.Communities, semantic, named)})
		}
		out = append(out, dt)
	}
	return out
}

func (s *Server) handleLookingGlass(w http.ResponseWriter, r *http.Request) {
	render(w, s.log, "lg.html", s.runLookingGlass(r))
}

func (s *Server) apiLookingGlass(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.runLookingGlass(r))
}

// lgPageLink builds a /lg URL preserving the current query and target but with a
// different offset — used for the Prev/Next controls.
func lgPageLink(v LGView, newOffset int) string {
	q := url.Values{"type": {v.Type}, "target": {v.Target}, "offset": {strconv.Itoa(newOffset)}}
	if v.All {
		q.Set("all", "1")
	}
	return "/lg?" + q.Encode()
}
