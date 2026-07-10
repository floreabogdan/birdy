package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// loginLimiter throttles failed logins per client IP. Keying on the IP, not the
// username, matters: a username lockout would let anyone lock the real admin out
// by failing logins on purpose. An attacker only ever locks out themselves.
type loginLimiter struct {
	mu      sync.Mutex
	byIP    map[string]*attemptRecord
	max     int           // failures allowed before a lockout
	window  time.Duration // failures older than this are forgotten
	lockout time.Duration // how long a locked-out IP stays out
}

type attemptRecord struct {
	count int
	first time.Time
	until time.Time // lockout expiry, zero when not locked
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{byIP: map[string]*attemptRecord{}, max: 5, window: 15 * time.Minute, lockout: 5 * time.Minute}
}

// blocked reports whether this IP is currently locked out.
func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.byIP[ip]
	return rec != nil && time.Now().Before(rec.until)
}

// fail records a failed attempt and locks the IP out once it passes the limit.
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	rec := l.byIP[ip]
	if rec == nil || now.Sub(rec.first) > l.window {
		rec = &attemptRecord{first: now}
		l.byIP[ip] = rec
	}
	rec.count++
	if rec.count >= l.max {
		rec.until = now.Add(l.lockout)
	}
}

// reset clears an IP's record after a successful login.
func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}

// clientIP is the peer address without its port. birdy binds directly (no proxy
// is expected), so RemoteAddr is the real client.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// handleHealthz is an unauthenticated liveness probe. It always returns 200 when
// birdy is serving; the body reports whether the last BIRD poll succeeded, so a
// readiness check can look deeper if it wants.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	snap := s.poller.Snapshot()
	writeJSON(w, map[string]any{
		"status":        "ok",
		"birdReachable": snap.Err == nil,
		"lastPollUnix":  snap.UpdatedAt.Unix(),
	})
}
