package web

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
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
	msg := ""
	if code := r.URL.Query().Get("error"); code != "" {
		msg = "Invalid username or password."
		for verdict, c := range loginRefusalCodes {
			if c == code {
				msg = verdict.refusal()
			}
		}
	}
	render(w, s.log, "login.html", map[string]any{"Error": msg})
}

// loginRefusalCodes are the ?error= codes the login page explains a limiter
// refusal with.
var loginRefusalCodes = map[loginVerdict]string{loginLocked: "locked", loginThrottled: "throttled", loginBusy: "busy"}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")

	// The lookup runs inside the limiter too, so a locked-out or throttled
	// client costs neither a database query nor a bcrypt run.
	var user store.User
	var lookupErr error
	matched, verdict := s.login.check(r.Context(), clientAddr(r), func() bool {
		u, found, err := s.store.GetUserByUsername(username)
		if err != nil {
			lookupErr = err
			return false
		}
		// Always run one bcrypt comparison, against a dummy hash when the user
		// is absent, so response time doesn't reveal whether the username
		// exists. The dummy's password is in this source file, so matching it
		// must still fail — and must not count as a success to the limiter.
		hash := dummyPasswordHash
		if found {
			hash = []byte(u.PasswordHash)
		}
		match := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
		user = u
		return match && found
	})
	if lookupErr != nil {
		s.log.Error("login lookup failed", "error", lookupErr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if code, refused := loginRefusalCodes[verdict]; refused {
		http.Redirect(w, r, "/login?error="+code, http.StatusSeeOther)
		return
	}
	if !matched {
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
	setSessionCookie(w, token, s.cookieSecure(r))
	// Seed the theme cookie from this user's saved preference so their theme is
	// applied on the very first page, and follows them onto a new browser.
	mode, accent := user.ThemeMode, user.ThemeAccent
	if !themeModes[mode] {
		mode = "system"
	}
	if !themeAccents[accent] {
		accent = "green"
	}
	s.setThemeCookie(w, r, mode, accent)
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
