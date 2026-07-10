package web

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionTTL = 7 * 24 * time.Hour

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
	username := r.FormValue("username")
	password := r.FormValue("password")

	user, ok, err := s.store.GetUserByUsername(username)
	if err != nil {
		s.log.Error("login lookup failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !ok || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}

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
	setSessionCookie(w, token, r.TLS != nil)
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
