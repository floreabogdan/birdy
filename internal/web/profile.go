package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/floreabogdan/birdy/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// minPasswordLen matches the floor `birdy init` enforces, so the rule an
// operator meets when they first set a password is the same one they meet when
// they change it.
const minPasswordLen = 8

type profileView struct {
	Active       string
	ReadOnly     bool
	Username     string // the stored name, or what was typed when a save failed
	AccountErrs  map[string]string
	PasswordErrs map[string]string
	Msg          string
}

// currentUser resolves the logged-in user from the id the auth middleware put on
// the request context. It returns ok=false having already written a response —
// a redirect to /login if the account is gone, a 500 if the lookup failed — so
// callers just return when ok is false.
func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	id, _ := r.Context().Value(ctxUserID).(int64)
	u, found, err := s.store.GetUserByID(id)
	if err != nil {
		s.serverError(w, "get user", err)
		return store.User{}, false
	}
	if !found {
		// The session outlived the account it belongs to. Clear it and start over.
		clearSessionCookie(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return store.User{}, false
	}
	return u, true
}

func (s *Server) handleProfilePage(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(w, r)
	if !ok {
		return
	}
	s.renderProfile(w, profileView{
		Active: "profile", ReadOnly: s.readOnly,
		Username: u.Username, Msg: r.URL.Query().Get("flash"),
	})
}

// handleProfileIdentity changes the logged-in user's username. It does not touch
// the session: sessions key on the user id, which does not change, so a rename
// leaves the operator logged in.
func (s *Server) handleProfileIdentity(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))

	if errs := validateUsername(username); len(errs) > 0 {
		s.renderProfile(w, profileView{Active: "profile", ReadOnly: s.readOnly, Username: username, AccountErrs: errs})
		return
	}
	if username == u.Username {
		http.Redirect(w, r, "/profile?flash="+flash("Username unchanged"), http.StatusSeeOther)
		return
	}
	if err := s.store.SetUsername(u.ID, username); err != nil {
		if isUniqueViolation(err) {
			s.renderProfile(w, profileView{Active: "profile", ReadOnly: s.readOnly, Username: username,
				AccountErrs: map[string]string{"username": "That username is already taken."}})
			return
		}
		s.serverError(w, "set username", err)
		return
	}
	http.Redirect(w, r, "/profile?flash="+flash("Username updated"), http.StatusSeeOther)
}

// handleProfilePassword changes the password. It re-checks the current password
// first: a stolen session should not be enough to lock the real operator out.
func (s *Server) handleProfilePassword(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	current := r.FormValue("currentPassword")
	next := r.FormValue("newPassword")
	confirm := r.FormValue("confirmPassword")

	errs := map[string]string{}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)) != nil {
		errs["currentPassword"] = "That is not your current password."
	}
	switch {
	case len(next) < minPasswordLen:
		errs["newPassword"] = fmt.Sprintf("Use at least %d characters.", minPasswordLen)
	case next == current:
		errs["newPassword"] = "Choose a password different from the current one."
	}
	if confirm != next {
		errs["confirmPassword"] = "This does not match the new password."
	}
	if len(errs) > 0 {
		s.renderProfile(w, profileView{Active: "profile", ReadOnly: s.readOnly, Username: u.Username, PasswordErrs: errs})
		return
	}

	hash, err := HashPassword(next)
	if err != nil {
		s.serverError(w, "hash password", err)
		return
	}
	token, err := newSessionToken()
	if err != nil {
		s.serverError(w, "generate replacement session", err)
		return
	}
	expires := time.Now().Add(sessionTTL)
	if err := s.store.RotatePasswordSession(u.ID, hash, token, expires); err != nil {
		s.serverError(w, "rotate password session", err)
		return
	}
	setSessionCookie(w, token, s.cookieSecure(r))
	http.Redirect(w, r, "/profile?flash="+flash("Password changed; other sessions signed out"), http.StatusSeeOther)
}

func (s *Server) renderProfile(w http.ResponseWriter, v profileView) {
	render(w, s.log, "profile.html", v)
}

// validateUsername keeps a username to something that is safe to show and to log
// in with: a single printable token, no whitespace or control characters.
func validateUsername(u string) map[string]string {
	errs := map[string]string{}
	switch {
	case u == "":
		errs["username"] = "Enter a username."
	case len(u) > 64:
		errs["username"] = "Keep it to 64 characters or fewer."
	case strings.ContainsFunc(u, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
		errs["username"] = "No spaces or control characters."
	}
	return errs
}
