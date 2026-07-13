package web

import (
	"net/http"
	"strings"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/store"
)

// rpkiInvalidLimit is the page size of the live-invalids list. It used to be a hard
// cap — the first 200 and no way to see the rest — which is no good when the whole
// point is counting what you would lose by switching to reject.
const rpkiInvalidLimit = 50

// isROATable reports whether a table name is an RPKI ROA table (birdy renders them
// as rpki4/rpki6). Their entries are ROAs, not routes: they appear in a "show route
// count" and would otherwise pollute any total taken from it.
func isROATable(name string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "rpki")
}

type rpkiView struct {
	Active   string
	ReadOnly bool
	Servers  []store.RPKIServer
	// ServerPager pages the RTR list; InvalidsPager (below) pages the dry run.
	ServerPager Pager
	// Live indexes the running BIRD protocols so an RTR session's state shows
	// up here once a config carrying it has been applied.
	Live map[string]protoRow

	// Which policies validate, and how.
	Rejecting []string
	Logging   []string
	Flash     string

	// Invalids are the routes BIRD is currently tagging RPKI-invalid in log-only
	// mode — exactly what a policy would drop if switched to reject. Populated
	// only when a policy is in log-only mode (that is what tags them).
	Invalids    []birdc.RouteEntry
	InvalidsErr string
	// InvalidTotal is how many routes carry the tag, across every routing table —
	// the number the dry run exists to produce. BIRD counts them itself, so asking
	// costs one command, not a walk of the whole RIB across the socket.
	// InvalidByTable breaks it down (master4 / master6), because "600 of them are
	// v6" is the next thing you want to know.
	InvalidTotal   int
	InvalidByTable []birdc.RouteCountEntry
	// Pager pages the listing, using the real total for its page numbers.
	Pager Pager
}

type rpkiFormView struct {
	Active   string
	ReadOnly bool
	IsNew    bool
	Server   store.RPKIServer
	Errs     map[string]string
}

func (s *Server) handleRPKIPage(w http.ResponseWriter, r *http.Request) {
	servers, err := s.store.ListRPKIServers()
	if err != nil {
		s.serverError(w, "list RPKI servers", err)
		return
	}
	policies, err := s.store.ListPolicies()
	if err != nil {
		s.serverError(w, "list policies", err)
		return
	}
	sOff, sLimit := parsePageParamsNamed(r, "soffset")
	serverPage := pageSlice(servers, sOff, sLimit)
	v := rpkiView{Active: "rpki", ReadOnly: s.readOnly, Servers: serverPage,
		ServerPager: pagerForNamed(r, "soffset", sOff, sLimit, len(serverPage), len(servers)),
		Live:        s.liveStates(), Flash: r.URL.Query().Get("flash")}
	for _, p := range policies {
		switch p.ROV {
		case store.ROVReject:
			v.Rejecting = append(v.Rejecting, p.Name)
		case store.ROVLog:
			v.Logging = append(v.Logging, p.Name)
		}
	}

	// The dry-run: while a policy is in log-only mode, BIRD tags invalids with
	// RPKI_INVALID instead of dropping them. List them so the operator can count
	// what they would lose before switching to reject.
	if len(v.Logging) > 0 {
		if settings, ok, err := s.store.GetSettings(); err == nil && ok && settings.LocalASN.Valid {
			asn := settings.LocalASN.Int64

			// Ask BIRD how many there are before asking for a page of them. This is
			// the answer the dry run is for — "742 routes would be dropped" — and it
			// also gives the pager a real page count.
			if counts, err := s.client.RoutesRPKIInvalidCount(asn); err != nil {
				v.InvalidsErr = err.Error()
			} else {
				for _, c := range counts {
					if isROATable(c.Table) {
						continue // the ROA tables hold ROAs, not routes; they never match
					}
					v.InvalidTotal += c.Routes
					if c.Routes > 0 {
						v.InvalidByTable = append(v.InvalidByTable, c)
					}
				}
			}

			offset, limit := parsePageParams(r)
			if limit == defaultPageSize {
				limit = rpkiInvalidLimit
			}
			page, err := s.client.RoutesRPKIInvalidPage(asn, offset, limit)
			if err != nil {
				v.InvalidsErr = err.Error()
			} else {
				for _, tbl := range page.Tables {
					v.Invalids = append(v.Invalids, tbl.Routes...)
				}
				v.Pager = newPager(r, offset, limit, len(v.Invalids), v.InvalidTotal, page.HasMore)
			}
		}
	}

	render(w, s.log, "rpki.html", v)
}

func (s *Server) handleRPKINew(w http.ResponseWriter, r *http.Request) {
	// Timers that match what most operators run: refresh every 15 minutes,
	// retry after 90 seconds, expire after two days.
	srv := store.RPKIServer{Port: 323, Enabled: true, Refresh: 900, Retry: 90, Expire: 172800}
	render(w, s.log, "rpki_form.html", rpkiFormView{Active: "rpki", ReadOnly: s.readOnly, IsNew: true, Server: srv})
}

func (s *Server) handleRPKIEdit(w http.ResponseWriter, r *http.Request) {
	srv, ok := namedEntity(s, w, r, s.store.GetRPKIServerByName, "RPKI server")
	if !ok {
		return
	}
	render(w, s.log, "rpki_form.html", rpkiFormView{Active: "rpki", ReadOnly: s.readOnly, Server: srv})
}

func rpkiFromForm(r *http.Request) store.RPKIServer {
	return store.RPKIServer{
		Name:        r.FormValue("name"),
		Description: strings.TrimSpace(r.FormValue("description")),
		Host:        r.FormValue("host"),
		Port:        formInt(r, "port"),
		Enabled:     r.FormValue("enabled") == "on",
		Refresh:     formInt(r, "refresh"),
		Retry:       formInt(r, "retry"),
		Expire:      formInt(r, "expire"),
	}
}

func (s *Server) handleRPKISave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	isNew := r.PathValue("name") == ""
	srv := rpkiFromForm(r)

	if !isNew {
		existing, ok := namedEntity(s, w, r, s.store.GetRPKIServerByName, "RPKI server")
		if !ok {
			return
		}
		srv.ID = existing.ID
	}

	errs := srv.Validate()
	// Disabling the last RTR server while a policy still validates would render
	// a config in which nothing is checked. Refuse, and say why.
	if len(errs) == 0 && !srv.Enabled {
		if msg := s.lastEnabledServerGuard(srv); msg != "" {
			errs["enabled"] = msg
		}
	}

	if len(errs) == 0 {
		var err error
		if isNew {
			_, err = s.store.CreateRPKIServer(srv)
		} else {
			err = s.store.UpdateRPKIServer(srv)
		}
		if err != nil {
			if isUniqueViolation(err) {
				errs["name"] = "An RPKI server with this name already exists."
			} else {
				s.serverError(w, "save RPKI server", err)
				return
			}
		}
	}
	if len(errs) == 0 {
		http.Redirect(w, r, "/rpki?flash="+flash("Saved "+srv.Name), http.StatusSeeOther)
		return
	}
	render(w, s.log, "rpki_form.html", rpkiFormView{Active: "rpki", ReadOnly: s.readOnly, IsNew: isNew, Server: srv, Errs: errs})
}

// lastEnabledServerGuard returns a message when turning this server off would
// leave a validating policy with no ROA data at all.
func (s *Server) lastEnabledServerGuard(srv store.RPKIServer) string {
	servers, err := s.store.ListRPKIServers()
	if err != nil {
		return ""
	}
	for _, other := range servers {
		if other.ID != srv.ID && other.Enabled {
			return "" // another server still feeds the ROA tables
		}
	}
	policies, err := s.store.ListPolicies()
	if err != nil {
		return ""
	}
	var validating []string
	for _, p := range policies {
		if p.ROV == store.ROVReject || p.ROV == store.ROVLog {
			validating = append(validating, p.Name)
		}
	}
	if len(validating) == 0 {
		return ""
	}
	return "This is the last enabled RTR server, and " + strings.Join(validating, ", ") +
		" still validates against it. Without ROA data every route would be \"unknown\" and nothing would be checked. Turn RPKI off on those policies first."
}

func (s *Server) handleRPKIDelete(w http.ResponseWriter, r *http.Request) {
	srv, ok := namedEntity(s, w, r, s.store.GetRPKIServerByName, "RPKI server")
	if !ok {
		return
	}
	if srv.Enabled {
		if msg := s.lastEnabledServerGuard(srv); msg != "" {
			http.Redirect(w, r, "/rpki?flash="+flash("Could not delete "+srv.Name+": "+msg), http.StatusSeeOther)
			return
		}
	}
	if err := s.store.DeleteRPKIServer(srv.ID); err != nil {
		s.serverError(w, "delete RPKI server", err)
		return
	}
	http.Redirect(w, r, "/rpki?flash="+flash("Deleted "+srv.Name), http.StatusSeeOther)
}
