package web

import (
	"context"
	"net/http"
	"strings"
)

const sessionCookieName = "birdy_session"

type ctxKey int

const ctxUserID ctxKey = 1

// requireAuth wraps a handler so it only runs for requests carrying a valid,
// unexpired session cookie; otherwise it redirects to /login.
func (s *Server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		sess, ok, err := s.store.GetSession(cookie.Value)
		if err != nil {
			s.log.Error("session lookup failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserID, sess.UserID)
		next(w, r.WithContext(ctx))
	})
}

// requireDashboardAuth also accepts a configured instance bearer token. It is
// intentionally separate from requireAuth so a remote token can never reach a
// write endpoint or a sensitive page.
func (s *Server) requireDashboardAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(auth, "Bearer ") {
			valid, err := s.store.VerifyInstanceAPIToken(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
			if err != nil {
				s.serverError(w, "verify instance API token", err)
				return
			}
			if valid {
				next(w, r)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.requireAuth(next).ServeHTTP(w, r)
	})
}

func setSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}
