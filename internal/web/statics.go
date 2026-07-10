package web

import (
	"net/http"
	"strconv"
	"strings"

	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

type staticRoutesView struct {
	Active   string
	ReadOnly bool
	Routes   []store.StaticRoute
	Flash    string
}

type staticRouteFormView struct {
	Active     string
	ReadOnly   bool
	IsNew      bool
	Route      store.StaticRoute
	Errs       map[string]string
	Preview    string
	PreviewErr string
}

func (s *Server) handleStaticRoutesList(w http.ResponseWriter, r *http.Request) {
	routes, err := s.store.ListStaticRoutes()
	if err != nil {
		s.serverError(w, "list static routes", err)
		return
	}
	render(w, s.log, "static_routes.html", staticRoutesView{
		Active: "library", ReadOnly: s.readOnly, Routes: routes,
		Flash: r.URL.Query().Get("flash"),
	})
}

func (s *Server) handleStaticRouteNew(w http.ResponseWriter, r *http.Request) {
	s.renderStaticRouteForm(w, staticRouteFormView{
		Active: "library", ReadOnly: s.readOnly, IsNew: true,
		Route: store.StaticRoute{Action: store.StaticVia, Enabled: true},
	})
}

func (s *Server) handleStaticRouteEdit(w http.ResponseWriter, r *http.Request) {
	route, err := s.staticRouteFromPath(w, r)
	if err != nil {
		return
	}
	s.renderStaticRouteForm(w, staticRouteFormView{
		Active: "library", ReadOnly: s.readOnly, Route: route,
	})
}

func (s *Server) handleStaticRouteSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	isNew := r.PathValue("id") == ""

	route := store.StaticRoute{
		Prefix:      r.FormValue("prefix"),
		Action:      r.FormValue("action"),
		NextHop:     r.FormValue("nextHop"),
		Description: r.FormValue("description"),
		Enabled:     r.FormValue("enabled") == "on",
	}
	if !isNew {
		existing, err := s.staticRouteFromPath(w, r)
		if err != nil {
			return
		}
		route.ID = existing.ID
	}

	errs := route.Validate()
	if len(errs) > 0 {
		s.renderStaticRouteForm(w, staticRouteFormView{
			Active: "library", ReadOnly: s.readOnly, IsNew: isNew, Route: route, Errs: errs,
		})
		return
	}

	var err error
	if isNew {
		_, err = s.store.CreateStaticRoute(route)
	} else {
		err = s.store.UpdateStaticRoute(route)
	}
	if isUniqueViolation(err) {
		errs["prefix"] = "A static route for this prefix already exists. Edit that one instead."
		s.renderStaticRouteForm(w, staticRouteFormView{
			Active: "library", ReadOnly: s.readOnly, IsNew: isNew, Route: route, Errs: errs,
		})
		return
	}
	if err != nil {
		s.serverError(w, "save static route", err)
		return
	}
	http.Redirect(w, r, "/library/static-routes?flash="+flash("Saved "+route.Prefix), http.StatusSeeOther)
}

func (s *Server) handleStaticRouteDelete(w http.ResponseWriter, r *http.Request) {
	route, err := s.staticRouteFromPath(w, r)
	if err != nil {
		return
	}
	if err := s.store.DeleteStaticRoute(route.ID); err != nil {
		s.serverError(w, "delete static route", err)
		return
	}
	http.Redirect(w, r, "/library/static-routes?flash="+flash("Deleted "+route.Prefix), http.StatusSeeOther)
}

// staticRouteFromPath resolves {id}. Static routes are addressed by id, not by
// name: a prefix carries a slash, and a slash is a path separator.
func (s *Server) staticRouteFromPath(w http.ResponseWriter, r *http.Request) (store.StaticRoute, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return store.StaticRoute{}, err
	}
	route, err := s.store.GetStaticRoute(id)
	if err == store.ErrNotFound {
		http.NotFound(w, r)
		return store.StaticRoute{}, err
	}
	if err != nil {
		s.serverError(w, "get static route", err)
		return store.StaticRoute{}, err
	}
	return route, nil
}

func (s *Server) renderStaticRouteForm(w http.ResponseWriter, v staticRouteFormView) {
	v.Preview, v.PreviewErr = previewStaticRoute(v.Route)
	render(w, s.log, "static_route_form.html", v)
}

// previewStaticRoute renders this one route through the real renderer, so the
// form shows the BIRD line it will actually produce rather than a mock-up.
func previewStaticRoute(r store.StaticRoute) (string, string) {
	probe := r
	probe.Enabled = true // a disabled route renders nothing; preview it anyway
	if errs := probe.Validate(); len(errs) > 0 {
		return "", "Fix the errors above to see the generated BIRD code."
	}
	full, err := birdconf.Config(birdconf.Input{
		RouterID: "0.0.0.1", LocalASN: 65551, // neither appears in a static protocol
		StaticRoutes: []store.StaticRoute{probe},
	})
	if err != nil {
		return "", err.Error()
	}
	return staticSection(full, probe.Family()), ""
}

// staticSection slices the generated config down to one static protocol.
func staticSection(cfg, family string) string {
	suffix := "v4"
	if family == store.FamilyV6 {
		suffix = "v6"
	}
	start := strings.Index(cfg, "protocol static static_"+suffix+" {")
	if start < 0 {
		return ""
	}
	end := strings.Index(cfg[start:], "\n}")
	if end < 0 {
		return cfg[start:]
	}
	return cfg[start : start+end+2]
}
