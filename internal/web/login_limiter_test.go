package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const goodPassword = "correct horse battery staple"

// loginFrom posts the login form as admin from remoteAddr and returns where the
// server redirected: "/" for a session, "/login?error=…" otherwise.
func loginFrom(env *testEnv, remoteAddr, password string) string {
	form := url.Values{"username": {"admin"}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	return rec.Header().Get("Location")
}

// Wrong guesses fired in parallel from one address must not all be evaluated.
// The lockout used to be checked before bcrypt and recorded after it, so every
// guess already in flight passed the check while the first ones were hashing.
func TestLoginLockoutHoldsUnderParallelGuesses(t *testing.T) {
	env := newTestEnv(t, false)
	const n = 60
	locs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			locs[i] = loginFrom(env, "198.51.100.7:4000", "wrong")
		}()
	}
	wg.Wait()

	evaluated := 0
	for _, loc := range locs {
		if loc == "/login?error=1" {
			evaluated++
		}
	}
	if evaluated > env.srv.login.max {
		t.Fatalf("%d of %d parallel guesses were evaluated; the limit is %d", evaluated, n, env.srv.login.max)
	}
	if loc := loginFrom(env, "198.51.100.7:4001", goodPassword); loc != "/login?error=locked" {
		t.Fatalf("after the flood the address should be locked out, got %q", loc)
	}
}

// One IPv6 subscriber usually holds a whole /64, so a lockout keyed on the full
// address gave an attacker a fresh set of tries per address.
func TestLoginLockoutKeysIPv6OnSlash64(t *testing.T) {
	env := newTestEnv(t, false)
	for i := 1; i <= env.srv.login.max; i++ {
		if loc := loginFrom(env, fmt.Sprintf("[2001:db8:1:2::%x]:4000", i), "wrong"); loc != "/login?error=1" {
			t.Fatalf("guess %d: %q", i, loc)
		}
	}
	if loc := loginFrom(env, "[2001:db8:1:2:ffff:ffff:ffff:fff0]:4000", goodPassword); loc != "/login?error=locked" {
		t.Fatalf("another address in the same /64 should be locked out too, got %q", loc)
	}
	if loc := loginFrom(env, "[2001:db8:1:3::1]:4000", goodPassword); loc != "/" {
		t.Fatalf("a different /64 is a different client and should sign in, got %q", loc)
	}
}

// Spreading guesses over many addresses must run into a router-wide budget, but
// the budget must not become a way to lock the real admin out: loopback (an SSH
// tunnel) and an address that has signed in before are never charged against it.
func TestLoginGlobalThrottleSlowsDistributedGuessing(t *testing.T) {
	env := newTestEnv(t, false)
	// A smaller budget than the default keeps the bcrypt bill down under -race;
	// TestLoginLimiterBudgetRefills covers the default.
	env.srv.login.tokens, env.srv.login.burst = 6, 6
	if loc := loginFrom(env, "203.0.113.5:4000", goodPassword); loc != "/" {
		t.Fatalf("admin's first login: %q", loc)
	}

	const attempts = 20
	evaluated, throttled := 0, 0
	for i := range attempts {
		// Every guess from its own /64, so no per-client lockout ever triggers.
		switch loc := loginFrom(env, fmt.Sprintf("[2001:db8:%x::1]:4000", i), "wrong"); loc {
		case "/login?error=1":
			evaluated++
		case "/login?error=throttled":
			throttled++
		default:
			t.Fatalf("guess %d: unexpected %q", i, loc)
		}
	}
	if throttled == 0 || evaluated > int(env.srv.login.burst) {
		t.Fatalf("%d distributed guesses were evaluated (%d throttled); the router-wide budget is %v",
			evaluated, throttled, env.srv.login.burst)
	}

	if loc := loginFrom(env, "[2001:db8:ffff::1]:4000", goodPassword); loc != "/login?error=throttled" {
		t.Errorf("a new remote address is slowed while the budget is spent, got %q", loc)
	}
	page := httptest.NewRecorder()
	env.srv.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/login?error=throttled", nil))
	if !strings.Contains(page.Body.String(), "SSH tunnel") {
		t.Errorf("the throttled message should point at the SSH tunnel:\n%s", page.Body.String())
	}
	if loc := loginFrom(env, "127.0.0.1:4000", goodPassword); loc != "/" {
		t.Errorf("loopback (an SSH tunnel) must always get in, got %q", loc)
	}
	if loc := loginFrom(env, "203.0.113.5:4001", goodPassword); loc != "/" {
		t.Errorf("an address that signed in before must not be throttled, got %q", loc)
	}
}

// The dummy hash a missing user is compared against is in the source. Matching
// it must not count as a successful login, or anyone could clear their own
// lockout (and skip the router-wide budget) by "logging in" as nobody.
func TestLoginDummyHashMatchDoesNotResetLockout(t *testing.T) {
	env := newTestEnv(t, false)
	for range env.srv.login.max - 1 {
		loginFrom(env, "198.51.100.9:4000", "wrong")
	}
	form := url.Values{"username": {"nobody"}, "password": {"birdy-login-timing-guard"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "198.51.100.9:4000"
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if loc := rec.Header().Get("Location"); loc != "/login?error=1" {
		t.Fatalf("matching the dummy hash must fail, got %q", loc)
	}
	if loc := loginFrom(env, "198.51.100.9:4000", goodPassword); loc != "/login?error=locked" {
		t.Fatalf("the dummy match should have used up the last try, got %q", loc)
	}
}

func TestLoginLimiterBudgetRefills(t *testing.T) {
	l := newLoginLimiter()
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	guess := func(i int) loginVerdict {
		_, v := l.check(context.Background(), netip.MustParseAddr(fmt.Sprintf("2001:db8:%x::1", i)), func() bool { return false })
		return v
	}
	i := 0
	for i < 1000 && guess(i) == loginAllowed {
		i++
	}
	if i != int(l.burst) {
		t.Fatalf("the budget allowed %d guesses, want %v", i, l.burst)
	}
	now = now.Add(l.refill)
	if v := guess(i + 1); v != loginAllowed {
		t.Fatalf("one refill period later a guess should be allowed, got %v", v)
	}
	if v := guess(i + 2); v != loginThrottled {
		t.Fatalf("and only one, got %v", v)
	}
}

// bcrypt is deliberately expensive and birdy shares the router with BIRD, so
// however many logins arrive at once only a couple of comparisons run together.
func TestLoginLimiterCapsConcurrentChecks(t *testing.T) {
	l := newLoginLimiter()
	var cur, peak atomic.Int32
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.check(context.Background(), netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i+1)), func() bool {
				n := cur.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				cur.Add(-1)
				return false
			})
		}()
	}
	wg.Wait()
	if got := int(peak.Load()); got > cap(l.checks) {
		t.Fatalf("%d password checks ran at once; the cap is %d", got, cap(l.checks))
	}
}

// A request that gives up waiting for a check slot never had its password
// looked at, so it must not cost the client one of its tries.
func TestLoginLimiterBusyRefundsTheAttempt(t *testing.T) {
	l := newLoginLimiter()
	l.checkWait = 10 * time.Millisecond
	for range cap(l.checks) {
		l.checks <- struct{}{}
	}
	addr := netip.MustParseAddr("192.0.2.50")
	for range l.max + 2 {
		if _, v := l.check(context.Background(), addr, func() bool { return false }); v != loginBusy {
			t.Fatalf("with every slot taken the check should be busy, got %v", v)
		}
	}
	for range cap(l.checks) {
		<-l.checks
	}
	if _, v := l.check(context.Background(), addr, func() bool { return false }); v != loginAllowed {
		t.Fatalf("busy refusals must not count towards the lockout, got %v", v)
	}
	if l.tokens != l.burst-1 {
		t.Fatalf("busy refusals must not spend the router-wide budget: %v left of %v", l.tokens, l.burst)
	}
}
