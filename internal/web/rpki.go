package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/floreabogdan/birdy/internal/store"
)

type rpkiView struct {
	Active   string
	ReadOnly bool
	Servers  []store.RPKIServer
	// Live indexes the running BIRD protocols so an RTR session's state shows
	// up here once a config carrying it has been applied.
	Live map[string]protoRow

	// Which policies validate, and how.
	Rejecting []string
	Logging   []string
	Flash     string
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
	v := rpkiView{Active: "rpki", ReadOnly: s.readOnly, Servers: servers,
		Live: s.liveStates(), Flash: r.URL.Query().Get("flash")}
	for _, p := range policies {
		switch p.ROV {
		case store.ROVReject:
			v.Rejecting = append(v.Rejecting, p.Name)
		case store.ROVLog:
			v.Logging = append(v.Logging, p.Name)
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
	srv, err := s.store.GetRPKIServerByName(r.PathValue("name"))
	if err == store.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "get RPKI server", err)
		return
	}
	render(w, s.log, "rpki_form.html", rpkiFormView{Active: "rpki", ReadOnly: s.readOnly, Server: srv})
}

func rpkiFromForm(r *http.Request) store.RPKIServer {
	atoi := func(k string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(k)))
		return n
	}
	return store.RPKIServer{
		Name:        r.FormValue("name"),
		Description: strings.TrimSpace(r.FormValue("description")),
		Host:        r.FormValue("host"),
		Port:        atoi("port"),
		Enabled:     r.FormValue("enabled") == "on",
		Refresh:     atoi("refresh"),
		Retry:       atoi("retry"),
		Expire:      atoi("expire"),
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
		existing, err := s.store.GetRPKIServerByName(r.PathValue("name"))
		if err == store.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.serverError(w, "get RPKI server", err)
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
	name := r.PathValue("name")
	srv, err := s.store.GetRPKIServerByName(name)
	if err == store.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "get RPKI server", err)
		return
	}
	if srv.Enabled {
		if msg := s.lastEnabledServerGuard(srv); msg != "" {
			http.Redirect(w, r, "/rpki?flash="+flash("Could not delete "+name+": "+msg), http.StatusSeeOther)
			return
		}
	}
	if err := s.store.DeleteRPKIServer(srv.ID); err != nil {
		s.serverError(w, "delete RPKI server", err)
		return
	}
	http.Redirect(w, r, "/rpki?flash="+flash("Deleted "+name), http.StatusSeeOther)
}
