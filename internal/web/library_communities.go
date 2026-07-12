package web

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/floreabogdan/birdy/internal/store"
)

type communitiesView struct {
	Active   string
	ReadOnly bool
	Defs     []store.CommunityDef
	Flash    string
}

type communityFormView struct {
	Active   string
	ReadOnly bool
	IsNew    bool
	Def      store.CommunityDef
	// Value is the community as the form edits it, e.g. "65000:666".
	Value   string
	Errs    map[string]string
	Preview string
}

func (s *Server) handleCommunitiesList(w http.ResponseWriter, r *http.Request) {
	defs, err := s.store.ListCommunityDefs()
	if err != nil {
		s.serverError(w, "list communities", err)
		return
	}
	render(w, s.log, "communities.html", communitiesView{
		Active: "library", ReadOnly: s.readOnly, Defs: defs, Flash: r.URL.Query().Get("flash"),
	})
}

func (s *Server) handleCommunityNew(w http.ResponseWriter, r *http.Request) {
	s.renderCommunityForm(w, communityFormView{Active: "library", ReadOnly: s.readOnly, IsNew: true})
}

func (s *Server) handleCommunityEdit(w http.ResponseWriter, r *http.Request) {
	cd, ok := namedEntity(s, w, r, s.store.GetCommunityDefByName, "community")
	if !ok {
		return
	}
	s.renderCommunityForm(w, communityFormView{
		Active: "library", ReadOnly: s.readOnly, Def: cd, Value: communityValueText(cd),
	})
}

// communityFromForm reads a community out of the posted form. The value is one
// community ("65000:666" or "65551:1:2"); anything else is a field error.
func communityFromForm(r *http.Request) (store.CommunityDef, string) {
	cd := store.CommunityDef{
		Name:        r.FormValue("name"),
		Description: strings.TrimSpace(r.FormValue("description")),
	}
	c, set, msg := store.ParseMatchCommunity(r.FormValue("value"))
	if msg != "" {
		return cd, msg
	}
	if !set {
		return cd, "Enter a community, e.g. 65000:666 or 65551:1:2."
	}
	cd.Large, cd.A, cd.B, cd.C = c.Large, c.A, c.B, c.C
	return cd, ""
}

func (s *Server) handleCommunitySave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	isNew := r.PathValue("name") == ""
	cd, valueErr := communityFromForm(r)

	if !isNew {
		existing, ok := namedEntity(s, w, r, s.store.GetCommunityDefByName, "community")
		if !ok {
			return
		}
		cd.ID, cd.Builtin = existing.ID, existing.Builtin
	}

	errs := cd.Validate()
	if valueErr != "" {
		errs["value"] = valueErr
	}
	if len(errs) == 0 {
		var err error
		if isNew {
			_, err = s.store.CreateCommunityDef(cd)
		} else {
			err = s.store.UpdateCommunityDef(cd)
		}
		if err != nil {
			if isUniqueViolation(err) {
				errs["name"] = "A community with this name already exists."
			} else {
				s.serverError(w, "save community", err)
				return
			}
		}
	}
	if len(errs) == 0 {
		verb := "updated"
		if isNew {
			verb = "created"
		}
		s.audit(r, verb+" community "+cd.Name)
		http.Redirect(w, r, "/library/communities?flash="+flash("Saved "+cd.Name), http.StatusSeeOther)
		return
	}
	s.renderCommunityForm(w, communityFormView{
		Active: "library", ReadOnly: s.readOnly, IsNew: isNew, Def: cd,
		Value: r.FormValue("value"), Errs: errs,
	})
}

func (s *Server) handleCommunityDelete(w http.ResponseWriter, r *http.Request) {
	cd, ok := namedEntity(s, w, r, s.store.GetCommunityDefByName, "community")
	if !ok {
		return
	}
	// Refuse to delete a community a peer or policy still references by name, or
	// the next render would emit an undefined symbol.
	if users, err := s.communityInUse(cd.Name); err == nil && len(users) > 0 {
		http.Redirect(w, r, "/library/communities?flash="+
			flash("Could not delete "+cd.Name+": still used by "+strings.Join(users, ", ")), http.StatusSeeOther)
		return
	}
	if err := s.store.DeleteCommunityDef(cd.ID); err != nil {
		s.serverError(w, "delete community", err)
		return
	}
	s.audit(r, "deleted community "+cd.Name)
	http.Redirect(w, r, "/library/communities?flash="+flash("Deleted "+cd.Name), http.StatusSeeOther)
}

// checkCommunityRefs returns a field error if a named community referenced in
// text is not defined in the library, so a typo is caught at save rather than
// surfacing as an undefined symbol when bird -p checks the apply.
func (s *Server) checkCommunityRefs(text string) string {
	names := store.NamedCommunityRefs(text)
	if len(names) == 0 {
		return ""
	}
	defs, err := s.store.ListCommunityDefs()
	if err != nil {
		return ""
	}
	have := make(map[string]bool, len(defs))
	for _, d := range defs {
		have[d.Name] = true
	}
	var missing []string
	for _, n := range names {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "Unknown community: " + strings.Join(missing, ", ") + ". Define it under Library → Communities."
}

// communityInUse lists the peers and policies that reference a community by name.
func (s *Server) communityInUse(name string) ([]string, error) {
	var users []string
	peers, err := s.store.ListPeers()
	if err != nil {
		return nil, err
	}
	for _, p := range peers {
		if slices.Contains(store.NamedCommunityRefs(p.ExportCommunities), name) {
			users = append(users, "peer "+p.Name)
		}
	}
	policies, err := s.store.ListPolicies()
	if err != nil {
		return nil, err
	}
	for _, pol := range policies {
		if slices.Contains(store.NamedCommunityRefs(pol.MatchCommunity), name) {
			users = append(users, "policy "+pol.Name)
		}
	}
	return users, nil
}

func (s *Server) renderCommunityForm(w http.ResponseWriter, v communityFormView) {
	if v.Def.Name != "" && v.Errs == nil {
		v.Preview = fmt.Sprintf("define %s = %s;", v.Def.Name, v.Def.Pattern())
	}
	render(w, s.log, "community_form.html", v)
}

// communityValueText renders a stored community back to the "a:b" form the edit
// field uses.
func communityValueText(cd store.CommunityDef) string {
	if cd.Large {
		return fmt.Sprintf("%d:%d:%d", cd.A, cd.B, cd.C)
	}
	return fmt.Sprintf("%d:%d", cd.A, cd.B)
}
