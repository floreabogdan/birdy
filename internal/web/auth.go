package web

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionTTL = 7 * 24 * time.Hour

// dummyPasswordHash is compared against when the submitted username does not
// exist, so a failed login spends the same bcrypt time whether or not the user
// is real — closing a username-enumeration timing oracle. Cost matches
// HashPassword's DefaultCost so the timing lines up with real accounts.
var dummyPasswordHash, _ = bcrypt.GenerateFromPassword([]byte("birdy-login-timing-guard"), bcrypt.DefaultCost)

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	// Already logged in? go straight to the dashboard.
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if _, ok, _ := s.store.GetSession(cookie.Value); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	render(w, s.log, "login.html", map[string]any{"Error": r.URL.Query().Get("error")})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}
	ip := clientIP(r)
	if s.login.blocked(ip) {
		http.Redirect(w, r, "/login?error=locked", http.StatusSeeOther)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")

	user, ok, err := s.store.GetUserByUsername(username)
	if err != nil {
		s.log.Error("login lookup failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Always run one bcrypt comparison, against a dummy hash when the user is
	// absent, so response time doesn't reveal whether the username exists. The
	// trailing !ok rejects the (impossible) case of a password matching the dummy.
	hash := dummyPasswordHash
	if ok {
		hash = []byte(user.PasswordHash)
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || !ok {
		s.login.fail(ip)
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}
	s.login.reset(ip)

	token, err := newSessionToken()
	if err != nil {
		s.log.Error("failed to generate session token", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.store.CreateSession(token, user.ID, time.Now().Add(sessionTTL)); err != nil {
		s.log.Error("failed to create session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	setSessionCookie(w, token, s.cookieSecure(r))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := s.store.DeleteSession(cookie.Value); err != nil {
			s.log.Warn("failed to delete session", "error", err)
		}
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashPassword is used by `birdy init` / password-reset flows.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}
