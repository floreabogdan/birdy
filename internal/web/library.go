package web

import (
	"net/http"
	"strings"

	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

type prefixSetsView struct {
	Active   string
	ReadOnly bool
	Sets     []store.PrefixSet
	InUse    map[int64]int // set id -> number of peers announcing it
	Flash    string
}

type prefixSetFormView struct {
	Active     string
	ReadOnly   bool
	IsNew      bool
	Set        store.PrefixSet
	Errs       map[string]string
	Preview    string
	PreviewErr string
}

func (s *Server) handlePrefixSetsList(w http.ResponseWriter, r *http.Request) {
	// The bogon lists are router settings, not library objects.
	sets, err := s.store.ListSelectablePrefixSets()
	if err != nil {
		s.serverError(w, "list prefix sets", err)
		return
	}
	inUse := map[int64]int{}
	for _, ps := range sets {
		n, err := s.store.PrefixSetUsage(ps.ID)
		if err != nil {
			s.serverError(w, "prefix set usage", err)
			return
		}
		inUse[ps.ID] = n
	}
	render(w, s.log, "prefix_sets.html", prefixSetsView{
		Active: "library", ReadOnly: s.readOnly, Sets: sets, InUse: inUse,
		Flash: r.URL.Query().Get("flash"),
	})
}

func (s *Server) handlePrefixSetNew(w http.ResponseWriter, r *http.Request) {
	s.renderPrefixSetForm(w, prefixSetFormView{
		Active: "library", ReadOnly: s.readOnly, IsNew: true,
		Set: store.PrefixSet{Family: store.FamilyV4, OriginateAction: store.OriginateBlackhole},
	})
}

func (s *Server) handlePrefixSetEdit(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.GetPrefixSetByName(r.PathValue("name"))
	if err == store.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "get prefix set", err)
		return
	}
	s.renderPrefixSetForm(w, prefixSetFormView{Active: "library", ReadOnly: s.readOnly, Set: ps})
}

// prefixSetFromForm parses the textarea of prefixes. One entry per line, with
// an optional BIRD pattern suffix glued to the prefix ("10.0.0.0/8+"). Blank
// lines and # comments are ignored so a list can be pasted in with notes.
func prefixSetFromForm(r *http.Request) store.PrefixSet {
	ps := store.PrefixSet{
		Name:            r.FormValue("name"),
		Description:     strings.TrimSpace(r.FormValue("description")),
		Family:          r.FormValue("family"),
		Originate:       r.FormValue("originate") == "on",
		OriginateAction: r.FormValue("originateAction"),
	}
	for line := range strings.Lines(r.FormValue("entries")) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		prefix, modifier := splitPattern(line)
		ps.Entries = append(ps.Entries, store.PrefixEntry{Prefix: prefix, Modifier: modifier})
	}
	return ps
}

// splitPattern separates "10.0.0.0/8{16,24}" into prefix and modifier. The
// modifier is whatever follows the prefix; validation decides if it is legal.
func splitPattern(s string) (prefix, modifier string) {
	if i := strings.IndexAny(s, "+-{"); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
	}
	return s, ""
}

func (s *Server) handlePrefixSetSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	isNew := r.PathValue("name") == ""
	ps := prefixSetFromForm(r)

	if !isNew {
		existing, err := s.store.GetPrefixSetByName(r.PathValue("name"))
		if err == store.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.serverError(w, "get prefix set", err)
			return
		}
		ps.ID = existing.ID
		ps.Builtin = existing.Builtin
		ps.System = existing.System
		if ps.System {
			// Generated filters name this set; only its contents may change.
			ps.Name, ps.Family, ps.Originate = existing.Name, existing.Family, false
		}
	}

	errs := ps.Validate()
	if len(errs) == 0 {
		var err error
		if isNew {
			_, err = s.store.CreatePrefixSet(ps)
		} else {
			err = s.store.UpdatePrefixSet(ps)
		}
		if err != nil {
			if isUniqueViolation(err) {
				errs["name"] = "A prefix set with this name already exists."
			} else {
				s.serverError(w, "save prefix set", err)
				return
			}
		}
	}
	if len(errs) == 0 {
		http.Redirect(w, r, "/library/prefix-sets?flash="+flash("Saved "+ps.Name), http.StatusSeeOther)
		return
	}
	s.renderPrefixSetForm(w, prefixSetFormView{Active: "library", ReadOnly: s.readOnly, IsNew: isNew, Set: ps, Errs: errs})
}

func (s *Server) handlePrefixSetDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ps, err := s.store.GetPrefixSetByName(name)
	if err == store.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "get prefix set", err)
		return
	}
	if err := s.store.DeletePrefixSet(ps.ID); err != nil {
		// Expected failures: still referenced by a policy, or a system set.
		// Both are the user's to resolve, not a server fault.
		http.Redirect(w, r, "/library/prefix-sets?flash="+flash("Could not delete "+name+": "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/library/prefix-sets?flash="+flash("Deleted "+name), http.StatusSeeOther)
}

func (s *Server) renderPrefixSetForm(w http.ResponseWriter, v prefixSetFormView) {
	v.Preview, v.PreviewErr = previewPrefixSet(v.Set)
	render(w, s.log, "prefix_set_form.html", v)
}

func previewPrefixSet(ps store.PrefixSet) (string, string) {
	probe := ps
	if errs := probe.Validate(); len(errs) > 0 {
		return "", "Fix the errors above to see the generated BIRD code."
	}
	full, err := birdconf.Config(birdconf.Input{
		RouterID: "0.0.0.1", LocalASN: 65000, PrefixSets: []store.PrefixSet{probe},
	})
	if err != nil {
		return "", err.Error()
	}
	return prefixSetSection(full, probe.Name), ""
}

// prefixSetSection carves the define (and the static protocol, when the set
// originates) out of the full config.
func prefixSetSection(cfg, name string) string {
	start := strings.Index(cfg, "define "+name+" = [")
	if start < 0 {
		return ""
	}
	out := cfg[start:]
	// Stop at the next define, or keep going through an originating protocol.
	end := strings.Index(out, "];")
	if end < 0 {
		return strings.TrimRight(out, "\n")
	}
	head := out[:end+2]
	if origin := strings.Index(cfg, "protocol static originate_"+name+" {"); origin >= 0 {
		rest := cfg[origin:]
		if blockEnd := strings.Index(rest, "\n}\n"); blockEnd >= 0 {
			return head + "\n\n" + rest[:blockEnd+2]
		}
	}
	return head
}
