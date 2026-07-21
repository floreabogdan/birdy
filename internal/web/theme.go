package web

import (
	"fmt"
	"net/http"
)

// The theme lives on the user (in the DB), not in the browser, so it follows an
// operator across machines. It is transported to the page through a small,
// non-secret cookie the static bootstrap reads before first paint — the strict
// CSP forbids the inline script that would otherwise stamp <html> server-side,
// and birdy has no shared layout to carry the attributes.
const themeCookieName = "birdy_theme"

var themeModes = map[string]bool{"system": true, "light": true, "dark": true}
var themeAccents = map[string]bool{"green": true, "ocean": true, "violet": true, "amber": true}

// userTheme returns the logged-in user's saved (mode, accent), falling back to
// the defaults for a missing user or an unrecognised stored value.
func (s *Server) userTheme(r *http.Request) (mode, accent string) {
	mode, accent = "system", "green"
	uid, ok := r.Context().Value(ctxUserID).(int64)
	if !ok {
		return
	}
	u, found, err := s.store.GetUserByID(uid)
	if err != nil || !found {
		return
	}
	if themeModes[u.ThemeMode] {
		mode = u.ThemeMode
	}
	if themeAccents[u.ThemeAccent] {
		accent = u.ThemeAccent
	}
	return
}

// setThemeCookie writes the JS-readable cookie the bootstrap reads. It is not
// HttpOnly (the theme script must see it) and carries no secret — just the
// operator's own display preference, "<mode>.<accent>".
func (s *Server) setThemeCookie(w http.ResponseWriter, r *http.Request, mode, accent string) {
	http.SetCookie(w, &http.Cookie{
		Name:     themeCookieName,
		Value:    mode + "." + accent,
		Path:     "/",
		Secure:   s.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   365 * 24 * 3600,
	})
}

func (s *Server) saveTheme(r *http.Request, mode, accent string) error {
	uid, ok := r.Context().Value(ctxUserID).(int64)
	if !ok {
		return fmt.Errorf("theme: no user in context")
	}
	return s.store.SaveUserTheme(uid, mode, accent)
}

// handleThemeMode persists the light/dark/system mode, leaving the accent alone.
// The top-bar toggle calls it with fetch and flips the attribute itself, so it
// answers 204 rather than redirecting.
func (s *Server) handleThemeMode(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	mode := r.FormValue("mode")
	if !themeModes[mode] {
		http.Error(w, "invalid mode", http.StatusBadRequest)
		return
	}
	_, accent := s.userTheme(r)
	if err := s.saveTheme(r, mode, accent); err != nil {
		s.serverError(w, "save theme mode", err)
		return
	}
	s.setThemeCookie(w, r, mode, accent)
	w.WriteHeader(http.StatusNoContent)
}

// handleThemeSave persists the accent (and mode, if the form carried one). The
// accent picker calls it with fetch too; a plain form post without JS still
// works and lands back on the Theme tab.
func (s *Server) handleThemeSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	accent := r.FormValue("accent")
	if !themeAccents[accent] {
		accent = "green"
	}
	mode, _ := s.userTheme(r)
	if m := r.FormValue("mode"); themeModes[m] {
		mode = m
	}
	if err := s.saveTheme(r, mode, accent); err != nil {
		s.serverError(w, "save theme", err)
		return
	}
	s.setThemeCookie(w, r, mode, accent)
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/settings?tab=theme", http.StatusSeeOther)
}
