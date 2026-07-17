package web

import (
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
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

const maxLoginLimiterEntries = 4096

func (l *loginLimiter) prune(now time.Time) {
	for ip, rec := range l.byIP {
		if now.Sub(rec.first) > l.window && !now.Before(rec.until) {
			delete(l.byIP, ip)
		}
	}
	if len(l.byIP) < maxLoginLimiterEntries {
		return
	}
	var oldestIP string
	var oldest time.Time
	for ip, rec := range l.byIP {
		if oldestIP == "" || rec.first.Before(oldest) {
			oldestIP, oldest = ip, rec.first
		}
	}
	delete(l.byIP, oldestIP)
}

// blocked reports whether this IP is currently locked out.
func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(time.Now())
	rec := l.byIP[ip]
	return rec != nil && time.Now().Before(rec.until)
}

// fail records a failed attempt and locks the IP out once it passes the limit.
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.prune(now)
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

// accessRestricted reports whether the operator has narrowed the access list
// from its allow-all default. /metrics is gated on it: the endpoint cannot carry
// a session cookie, so the access list is the only thing standing between a
// scraper and anyone else who can reach the port.
func (s *Server) accessRestricted() bool {
	s.accessMu.RLock()
	defer s.accessMu.RUnlock()
	return store.AccessRestricted(s.accessList)
}

// WideOpen reports the posture a fresh install starts in: reachable from beyond
// this host, with an access list that still allows every IP. birdy has no TLS, so
// in that state the login and session cookie cross the network in the clear.
//
// It is said once, at startup, in the log, and shown on the Access settings page
// where the operator would act on it — deliberately NOT nagged on the dashboard,
// which is the page you stare at all day.
func (s *Server) WideOpen() bool {
	return !s.listenLoopback() && !s.accessRestricted()
}

// listenLoopback reports whether birdy is bound to loopback only, in which case
// nothing off-box can reach it whatever the access list says.
func (s *Server) listenLoopback() bool {
	host, _, err := net.SplitHostPort(s.listenAddr)
	if err != nil {
		host = s.listenAddr
	}
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false // unparseable or empty (":8080" = every interface)
	}
	return addr.IsLoopback()
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
