package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/floreabogdan/birdy/internal/store"
)

// serverError logs the real cause and shows the user a generic message. SQL
// text and file paths are for the journal, not the browser.
func (s *Server) serverError(w http.ResponseWriter, what string, err error) {
	s.log.Error("request failed", "op", what, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// namedEntity resolves a name-addressed model object from the {name} path value,
// writing the right response and returning ok=false when the handler must stop:
// a 404 for ErrNotFound, a logged 500 otherwise. It replaces the lookup block
// every Edit/Save/Delete handler for a named entity would otherwise repeat.
func namedEntity[T any](s *Server, w http.ResponseWriter, r *http.Request, get func(string) (T, error), what string) (T, bool) {
	v, err := get(r.PathValue("name"))
	if err == store.ErrNotFound {
		http.NotFound(w, r)
		return v, false
	}
	if err != nil {
		s.serverError(w, "get "+what, err)
		return v, false
	}
	return v, true
}

// formInt reads a trimmed integer form value, defaulting to 0 when the field is
// absent or unparseable — a bad number is rejected by Validate, not here.
func formInt(r *http.Request, key string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(key)))
	return n
}

// actor resolves the username of the operator behind a request, for the audit
// trail. Best-effort: returns "" if it cannot be determined, since auditing must
// never fail a request.
func (s *Server) actor(r *http.Request) string {
	id, ok := r.Context().Value(ctxUserID).(int64)
	if !ok {
		return ""
	}
	u, found, err := s.store.GetUserByID(id)
	if err != nil || !found {
		return ""
	}
	return u.Username
}

// audit records an operator's model change on the timeline, attributed to them.
// Best-effort: a failed audit write is logged, never surfaced to the operator.
func (s *Server) audit(r *http.Request, message string) {
	if err := s.store.InsertAudit(s.actor(r), store.EventModelChange, message); err != nil {
		s.log.Warn("failed to record audit event", "error", err)
	}
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
