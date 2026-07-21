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
	Pager    Pager
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
	Bgpq4      bool // whether IRR expansion is enabled
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
	offset, limit := parsePageParams(r)
	page := pageSlice(sets, offset, limit)
	render(w, s.log, "prefix_sets.html", prefixSetsView{
		Active: "library", ReadOnly: s.readOnly, Sets: page, InUse: inUse,
		Pager: pagerFor(r, offset, limit, len(page), len(sets)),
		Flash: s.flashMsg(w, r),
	})
}

func (s *Server) handlePrefixSetNew(w http.ResponseWriter, r *http.Request) {
	s.renderPrefixSetForm(w, prefixSetFormView{
		Active: "library", ReadOnly: s.readOnly, IsNew: true,
		Set: store.PrefixSet{Family: store.FamilyV4, OriginateAction: store.OriginateBlackhole},
	})
}

func (s *Server) handlePrefixSetEdit(w http.ResponseWriter, r *http.Request) {
	ps, ok := namedEntity(s, w, r, s.store.GetPrefixSetByName, "prefix set")
	if !ok {
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
		Source:          strings.TrimSpace(r.FormValue("source")),
		AutoRefresh:     r.FormValue("autoRefresh") == "on",
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
		existing, ok := namedEntity(s, w, r, s.store.GetPrefixSetByName, "prefix set")
		if !ok {
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
		s.flashRedirect(w, r, "/library/prefix-sets", "Saved "+ps.Name, false)
		return
	}
	s.renderPrefixSetForm(w, prefixSetFormView{Active: "library", ReadOnly: s.readOnly, IsNew: isNew, Set: ps, Errs: errs})
}

func (s *Server) handlePrefixSetDelete(w http.ResponseWriter, r *http.Request) {
	ps, ok := namedEntity(s, w, r, s.store.GetPrefixSetByName, "prefix set")
	if !ok {
		return
	}
	if err := s.store.DeletePrefixSet(ps.ID); err != nil {
		// Expected failures: still referenced by a policy, or a system set.
		// Both are the user's to resolve, not a server fault.
		s.flashRedirect(w, r, "/library/prefix-sets", "Could not delete "+ps.Name+": "+err.Error(), false)
		return
	}
	s.flashRedirect(w, r, "/library/prefix-sets", "Deleted "+ps.Name, false)
}

// handlePrefixSetToggle switches a prefix set off (or back on) from the list, the
// way a peer can be. It changes the model, not the router: a disabled set stops
// rendering its define and originator on the next apply. A filter that still
// names a disabled set will then fail `bird -p` at apply time, so the operator is
// told to clear the reference first — nothing reaches the router until it parses.
func (s *Server) handlePrefixSetToggle(w http.ResponseWriter, r *http.Request) {
	ps, ok := namedEntity(s, w, r, s.store.GetPrefixSetByName, "prefix set")
	if !ok {
		return
	}
	if ps.System {
		s.flashRedirect(w, r, "/library/prefix-sets", ps.Name+" is a system set and cannot be disabled.", false)
		return
	}
	if err := s.store.SetPrefixSetDisabled(ps.ID, !ps.Disabled); err != nil {
		s.serverError(w, "toggle prefix set", err)
		return
	}
	verb := "Disabled"
	if ps.Disabled {
		verb = "Enabled"
	}
	s.audit(r, strings.ToLower(verb)+" prefix set "+ps.Name)
	s.flashRedirect(w, r, "/library/prefix-sets", verb+" "+ps.Name+" — review it under Changes and apply to take effect on the router.", false)
}

func (s *Server) renderPrefixSetForm(w http.ResponseWriter, v prefixSetFormView) {
	v.Bgpq4 = s.bgpq4Bin != ""
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
