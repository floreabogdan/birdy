package web

import (
	"net/http"
	"strings"

	"github.com/floreabogdan/birdy/internal/irr"
	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

type asSetsView struct {
	Active   string
	ReadOnly bool
	Bgpq4    bool // whether IRR expansion is enabled
	Sets     []store.ASSet
	InUse    map[int64]int // set id -> policies filtering through it
	Flash    string
}

type asSetFormView struct {
	Active     string
	ReadOnly   bool
	Bgpq4      bool
	IsNew      bool
	Set        store.ASSet
	EntryText  string
	Errs       map[string]string
	Preview    string
	PreviewErr string
}

func (s *Server) handleASSetsList(w http.ResponseWriter, r *http.Request) {
	sets, err := s.store.ListASSets()
	if err != nil {
		s.serverError(w, "list AS sets", err)
		return
	}
	inUse := map[int64]int{}
	for _, as := range sets {
		n, err := s.store.ASSetUsage(as.ID)
		if err != nil {
			s.serverError(w, "AS set usage", err)
			return
		}
		inUse[as.ID] = n
	}
	render(w, s.log, "as_sets.html", asSetsView{
		Active: "library", ReadOnly: s.readOnly, Bgpq4: s.bgpq4Bin != "", Sets: sets, InUse: inUse,
		Flash: r.URL.Query().Get("flash"),
	})
}

func (s *Server) handleASSetNew(w http.ResponseWriter, r *http.Request) {
	s.renderASSetForm(w, asSetFormView{Active: "library", ReadOnly: s.readOnly, IsNew: true})
}

func (s *Server) handleASSetEdit(w http.ResponseWriter, r *http.Request) {
	as, ok := namedEntity(s, w, r, s.store.GetASSetByName, "AS set")
	if !ok {
		return
	}
	s.renderASSetForm(w, asSetFormView{
		Active: "library", ReadOnly: s.readOnly, Set: as,
		EntryText: store.FormatASNRanges(as.Entries),
	})
}

// handleASSetRefresh re-expands one set from its AS-SET right now, so an
// operator does not have to wait for the timer (or open the form) after a
// customer adds a downstream. Like the timer, it updates the model only.
func (s *Server) handleASSetRefresh(w http.ResponseWriter, r *http.Request) {
	as, ok := namedEntity(s, w, r, s.store.GetASSetByName, "AS set")
	if !ok {
		return
	}
	if as.Source == "" {
		http.Redirect(w, r, "/library/as-sets?flash="+flash(as.Name+" has no IRR AS-SET to expand."), http.StatusSeeOther)
		return
	}
	client := irr.New(s.bgpq4Bin)
	if !client.Available() {
		http.Redirect(w, r, "/library/as-sets?flash="+flash("bgpq4 is not installed on the router, so "+as.Source+" cannot be expanded."), http.StatusSeeOther)
		return
	}
	msg := s.refreshOneASSet(r.Context(), client, as)
	http.Redirect(w, r, "/library/as-sets?flash="+flash(msg), http.StatusSeeOther)
}

func (s *Server) handleASSetSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	isNew := r.PathValue("name") == ""
	entryText := r.FormValue("entries")

	as := store.ASSet{
		Name:        r.FormValue("name"),
		Description: strings.TrimSpace(r.FormValue("description")),
		Source:      strings.TrimSpace(r.FormValue("source")),
		AutoRefresh: r.FormValue("autoRefresh") == "on",
	}
	entries, entryErrs := store.ParseASNRanges(entryText)
	as.Entries = entries

	if !isNew {
		existing, ok := namedEntity(s, w, r, s.store.GetASSetByName, "AS set")
		if !ok {
			return
		}
		as.ID = existing.ID
	}

	errs := as.Validate()
	for k, v := range entryErrs {
		errs[k] = v
	}

	if len(errs) == 0 {
		var err error
		if isNew {
			_, err = s.store.CreateASSet(as)
		} else {
			err = s.store.UpdateASSet(as)
		}
		if err != nil {
			if isUniqueViolation(err) {
				errs["name"] = "An AS set with this name already exists."
			} else {
				s.serverError(w, "save AS set", err)
				return
			}
		}
	}
	if len(errs) == 0 {
		http.Redirect(w, r, "/library/as-sets?flash="+flash("Saved "+as.Name), http.StatusSeeOther)
		return
	}
	s.renderASSetForm(w, asSetFormView{
		Active: "library", ReadOnly: s.readOnly, IsNew: isNew, Set: as, EntryText: entryText, Errs: errs,
	})
}

func (s *Server) handleASSetDelete(w http.ResponseWriter, r *http.Request) {
	as, ok := namedEntity(s, w, r, s.store.GetASSetByName, "AS set")
	if !ok {
		return
	}
	if err := s.store.DeleteASSet(as.ID); err != nil {
		http.Redirect(w, r, "/library/as-sets?flash="+flash("Could not delete "+as.Name+": "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/library/as-sets?flash="+flash("Deleted "+as.Name), http.StatusSeeOther)
}

func (s *Server) renderASSetForm(w http.ResponseWriter, v asSetFormView) {
	v.Bgpq4 = s.bgpq4Bin != ""
	v.Preview, v.PreviewErr = previewASSet(v.Set)
	render(w, s.log, "as_set_form.html", v)
}

func previewASSet(as store.ASSet) (string, string) {
	probe := as
	if errs := probe.Validate(); len(errs) > 0 {
		return "", "Fix the errors above to see the generated BIRD code."
	}
	full, err := birdconf.Config(birdconf.Input{
		RouterID: "0.0.0.1", LocalASN: 65000, ASSets: []store.ASSet{probe},
	})
	if err != nil {
		return "", err.Error()
	}
	return asSetSection(full, probe.Name), ""
}

func asSetSection(cfg, name string) string {
	start := strings.Index(cfg, "define "+name+" = [")
	if start < 0 {
		return ""
	}
	// Walk back over the comment lines birdy writes above the define.
	if i := strings.LastIndex(cfg[:start], "\n\n"); i >= 0 {
		start = i + 2
	}
	end := strings.Index(cfg[start:], "];")
	if end < 0 {
		return strings.TrimRight(cfg[start:], "\n")
	}
	return cfg[start : start+end+2]
}
