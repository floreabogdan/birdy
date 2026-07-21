package web

import "net/http"

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

func (s *Server) currentUserID(r *http.Request) (int64, bool) {
	uid, ok := r.Context().Value(ctxUserID).(int64)
	return uid, ok
}

// handleThemeMode persists ONLY the light/dark/system mode. The top-bar toggle
// calls it with fetch and flips the attribute (and writes the cookie) itself, so
// it just records the choice and answers 204 — it does not touch the cookie,
// which would risk overwriting a concurrent accent change's value.
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
	uid, ok := s.currentUserID(r)
	if !ok {
		http.Error(w, "no user", http.StatusUnauthorized)
		return
	}
	if err := s.store.SaveUserThemeMode(uid, mode); err != nil {
		s.serverError(w, "save theme mode", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleThemeSave persists ONLY the accent. The picker calls it with fetch and
// manages the attribute + cookie itself (204, no cookie touched here). A plain
// form post without JS still works: it sets the cookie from the DB and redirects.
func (s *Server) handleThemeSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	accent := r.FormValue("accent")
	if !themeAccents[accent] {
		accent = "green"
	}
	uid, ok := s.currentUserID(r)
	if !ok {
		http.Error(w, "no user", http.StatusUnauthorized)
		return
	}
	if err := s.store.SaveUserThemeAccent(uid, accent); err != nil {
		s.serverError(w, "save theme", err)
		return
	}
	if r.Header.Get("X-Requested-With") == "fetch" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	mode, savedAccent := s.userTheme(r)
	s.setThemeCookie(w, r, mode, savedAccent)
	http.Redirect(w, r, "/settings?tab=theme", http.StatusSeeOther)
}
