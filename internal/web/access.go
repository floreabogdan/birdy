package web

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

// loginLimiter guards every password check — the login form and the profile's
// current-password confirmation share it. Three layers, each closing a way
// around the others:
//
//   - Per client: a few tries, then a lockout. It keys on the client, never the
//     username: a username lockout would let anyone lock the real admin out by
//     failing on purpose, while a client lockout only ever stops that client. A
//     client is an IPv4 address or an IPv6 /64 — one subscriber normally holds a
//     whole /64, so keying on the full address handed out endless fresh starts.
//   - Router-wide: a token bucket charged by every remote attempt, which is what
//     slows guesses spread over many addresses. It can only ever slow remote
//     logins, and only while an attack keeps it drained: loopback (an SSH tunnel)
//     and an address that has signed in successfully before are never charged,
//     sessions already open are unaffected, and it refills on its own.
//   - A cap on concurrent bcrypt comparisons. Each costs tens of milliseconds of
//     CPU on the router itself, and a flood of them must not starve BIRD.
//
// An attempt is reserved — counted against its client and the bucket, under one
// lock — before bcrypt runs. Recording a failure only after the comparison let
// every guess already in flight pass the check while the first ones hashed.
type loginLimiter struct {
	mu      sync.Mutex
	byKey   map[string]*attemptRecord
	max     int           // tries allowed per client before a lockout
	window  time.Duration // tries older than this are forgotten
	lockout time.Duration // how long a locked-out client stays out

	tokens     float64       // router-wide tries available now
	burst      float64       // bucket size
	refill     time.Duration // one token comes back per refill
	refilledAt time.Time

	// trusted remembers clients that recently proved they know the password, so
	// the admin's usual address keeps working while the bucket is drained.
	trusted  map[string]time.Time
	trustFor time.Duration

	checks    chan struct{} // one slot per concurrent bcrypt comparison
	checkWait time.Duration // how long a check queues for a slot

	now func() time.Time
}

type attemptRecord struct {
	count int
	first time.Time
	until time.Time // lockout expiry, zero when not locked
}

// loginVerdict is why the limiter did or did not let a password be checked.
type loginVerdict int

const (
	loginAllowed   loginVerdict = iota
	loginLocked                 // this client has used up its tries
	loginThrottled              // the router-wide budget is spent
	loginBusy                   // no password-check slot came free in time
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		byKey: map[string]*attemptRecord{}, max: 5, window: 15 * time.Minute, lockout: 5 * time.Minute,
		// 20 remote tries at once, then one a minute: plenty for an admin who
		// mistypes, ~1,400 guesses a day for a botnet instead of thousands a minute.
		tokens: 20, burst: 20, refill: time.Minute,
		trusted: map[string]time.Time{}, trustFor: 30 * 24 * time.Hour,
		// Two slots: a login never needs more than one, and the rest of the
		// router's cores stay with BIRD whatever arrives.
		checks: make(chan struct{}, 2), checkWait: 10 * time.Second,
		now: time.Now,
	}
}

const (
	maxLoginLimiterEntries = 4096
	maxTrustedClients      = 64
)

// limiterKey names the client an attempt is charged to. All of loopback is one
// client: 127.0.0.0/8 is sixteen million source addresses a local process can
// pick from, and loopback is exempt from the router-wide budget.
func limiterKey(addr netip.Addr) (key string, loopback bool) {
	addr = addr.Unmap()
	switch {
	case !addr.IsValid():
		return "invalid", false
	case addr.IsLoopback():
		return "loopback", true
	case addr.Is6():
		return netip.PrefixFrom(addr.WithZone(""), 64).Masked().String(), false
	}
	return addr.String(), false
}

// check runs compare — one password comparison — if the limiter allows it now,
// and reports whether the password matched. Any verdict but loginAllowed means
// compare never ran.
func (l *loginLimiter) check(ctx context.Context, addr netip.Addr, compare func() bool) (bool, loginVerdict) {
	key, loopback := limiterKey(addr)
	res, verdict := l.reserve(key, loopback)
	if verdict != loginAllowed {
		return false, verdict
	}
	wait := time.NewTimer(l.checkWait)
	defer wait.Stop()
	select {
	case l.checks <- struct{}{}:
	case <-ctx.Done():
		l.cancel(res)
		return false, loginBusy
	case <-wait.C:
		l.cancel(res)
		return false, loginBusy
	}
	// Released in a defer: net/http survives a panicking handler, and a slot
	// lost to one would leave every later login waiting for it.
	ok := func() bool {
		defer func() { <-l.checks }()
		return compare()
	}()
	if ok {
		l.succeed(res)
	}
	return ok, loginAllowed
}

// reservation is an attempt counted before its password is checked, so it can
// be handed back if the check never happens.
type reservation struct {
	key     string
	rec     *attemptRecord
	charged bool // took a router-wide token
}

func (l *loginLimiter) reserve(key string, loopback bool) (reservation, loginVerdict) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	rec := l.byKey[key]
	if rec != nil && now.Before(rec.until) {
		return reservation{}, loginLocked
	}
	charged := !loopback && !l.isTrusted(key, now)
	if charged {
		l.refillTokens(now)
		if l.tokens < 1 {
			return reservation{}, loginThrottled
		}
		l.tokens--
	}
	if rec == nil || now.Sub(rec.first) > l.window {
		rec = &attemptRecord{first: now}
		l.byKey[key] = rec
	}
	rec.count++
	if rec.count >= l.max {
		rec.until = now.Add(l.lockout)
	}
	return reservation{key: key, rec: rec, charged: charged}, loginAllowed
}

// cancel hands back an attempt whose password was never checked.
func (l *loginLimiter) cancel(res reservation) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if res.rec.count > 0 {
		res.rec.count--
	}
	if res.rec.count < l.max {
		res.rec.until = time.Time{}
	}
	if res.charged {
		l.tokens = min(l.burst, l.tokens+1)
	}
}

// succeed clears a client's record after a correct password, and remembers the
// client as one the router-wide budget does not charge. A correct password is
// not an attack, so its token goes back too.
func (l *loginLimiter) succeed(res reservation) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byKey, res.key)
	if res.charged {
		l.tokens = min(l.burst, l.tokens+1)
	}
	if res.key == "loopback" || res.key == "invalid" {
		return
	}
	if _, ok := l.trusted[res.key]; !ok && len(l.trusted) >= maxTrustedClients {
		var oldestKey string
		var oldest time.Time
		for k, at := range l.trusted {
			if oldestKey == "" || at.Before(oldest) {
				oldestKey, oldest = k, at
			}
		}
		delete(l.trusted, oldestKey)
	}
	l.trusted[res.key] = l.now()
}

func (l *loginLimiter) isTrusted(key string, now time.Time) bool {
	at, ok := l.trusted[key]
	if ok && now.Sub(at) > l.trustFor {
		delete(l.trusted, key)
		return false
	}
	return ok
}

func (l *loginLimiter) refillTokens(now time.Time) {
	if !l.refilledAt.IsZero() && now.After(l.refilledAt) {
		l.tokens = min(l.burst, l.tokens+float64(now.Sub(l.refilledAt))/float64(l.refill))
	}
	l.refilledAt = now
}

func (l *loginLimiter) prune(now time.Time) {
	for key, rec := range l.byKey {
		if now.Sub(rec.first) > l.window && !now.Before(rec.until) {
			delete(l.byKey, key)
		}
	}
	if len(l.byKey) < maxLoginLimiterEntries {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, rec := range l.byKey {
		if oldestKey == "" || rec.first.Before(oldest) {
			oldestKey, oldest = key, rec.first
		}
	}
	delete(l.byKey, oldestKey)
}

// refusal is what the operator is told when the limiter turns a check away.
func (v loginVerdict) refusal() string {
	switch v {
	case loginLocked:
		return "Too many failed attempts from your address. Try again in a few minutes."
	case loginThrottled:
		return "Too many failed sign-ins on this router, from many addresses. Try again later, or reach birdy over an SSH tunnel, which is never throttled."
	case loginBusy:
		return "birdy is busy checking other sign-ins. Try again in a moment."
	}
	return ""
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
	// Intentionally public and detailed: readiness monitoring cannot present a
	// session, and the port is already access-list gated. The disclosure (is the
	// last BIRD poll healthy, and when) is low sensitivity and by design.
	snap := s.poller.Snapshot()
	writeJSON(w, map[string]any{
		"status":        "ok",
		"birdReachable": snap.Err == nil,
		"lastPollUnix":  snap.UpdatedAt.Unix(),
	})
}
