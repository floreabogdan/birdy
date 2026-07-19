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
				// A remote token authorizes read-only observation of THIS
				// instance only. Drop any cookies (notably birdy_instance) so a
				// token holder cannot set the selection cookie and make us relay
				// another federated instance's dashboard with our stored token.
				r.Header.Del("Cookie")
				next(w, r)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.requireAuth(next).ServeHTTP(w, r)
	})
}

// cookieSecure decides the Secure flag for session and selection cookies.
// Native TLS always qualifies. When birdy is bound to loopback it sits behind a
// local reverse proxy, so that proxy's X-Forwarded-Proto: https is trustworthy
// and the cookie should be Secure even though this last hop is plaintext. On a
// directly exposed listener the header is not trusted — a client could forge it.
func (s *Server) cookieSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.listenLoopback() && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
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
