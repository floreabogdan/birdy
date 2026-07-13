package web

import "net/http"

// exploreView merges birdy's two read-only investigative tools — the route
// looking glass and the ping/traceroute diagnostics — onto one tabbed page.
type exploreView struct {
	Active   string
	ReadOnly bool
	Tab      string // "routes" | "diag"
	LG       LGView
	Diag     diagView
}

// explore renders the merged page. Only the active tab's query runs; the other
// tab shows a blank form. Both /lg and /diagnostics land here (each with its own
// default tab), and both forms submit back to /lg with a tab marker.
func (s *Server) explore(w http.ResponseWriter, r *http.Request, defaultTab string) {
	tab := r.URL.Query().Get("tab")
	if tab != "routes" && tab != "diag" {
		tab = defaultTab
	}
	v := exploreView{Active: "lg", ReadOnly: s.readOnly, Tab: tab, LG: s.blankLG(), Diag: s.blankDiag()}
	if tab == "diag" {
		v.Diag = s.runDiagnostics(r)
	} else {
		v.LG = s.runLookingGlass(r)
	}
	render(w, s.log, "explore.html", v)
}

func (s *Server) handleLookingGlass(w http.ResponseWriter, r *http.Request) {
	s.explore(w, r, "routes")
}

// handleDiagnostics keeps the /diagnostics URL working (bookmarks, old links); it
// renders the same merged page with the Diagnostics tab active.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	s.explore(w, r, "diag")
}
