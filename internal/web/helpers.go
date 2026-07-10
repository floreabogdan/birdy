package web

import (
	"net/http"
	"net/url"
	"strings"
)

// serverError logs the real cause and shows the user a generic message. SQL
// text and file paths are for the journal, not the browser.
func (s *Server) serverError(w http.ResponseWriter, what string, err error) {
	s.log.Error("request failed", "op", what, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// flash builds the ?flash= value for a post-redirect-get confirmation.
func flash(msg string) string { return url.QueryEscape(msg) }

// tabParam resolves ?tab= against the tabs a page actually has, defaulting to
// the first. An unknown value is a stale bookmark, not an error worth a 404.
func tabParam(r *http.Request, allowed ...string) string {
	want := r.URL.Query().Get("tab")
	for _, a := range allowed {
		if a == want {
			return a
		}
	}
	return allowed[0]
}

// isUniqueViolation reports whether err is SQLite's UNIQUE constraint failure.
// modernc.org/sqlite does not export a typed error for this, so the message is
// all there is; a name clash must surface on the form, not as a 500.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
