package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLoginLockoutAfterRepeatedFailures(t *testing.T) {
	env := newTestEnv(t, false)
	bad := url.Values{"username": {"admin"}, "password": {"wrong"}}

	// The limiter allows 5 failures, then locks out.
	for i := 0; i < 5; i++ {
		rec := env.do(t, "POST", "/login", bad)
		if loc := rec.Header().Get("Location"); loc != "/login?error=1" {
			t.Fatalf("attempt %d: location=%q", i, loc)
		}
	}
	// The 6th is locked out — even the CORRECT password is refused now.
	good := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}
	rec := env.do(t, "POST", "/login", good)
	if loc := rec.Header().Get("Location"); loc != "/login?error=locked" {
		t.Fatalf("a locked-out IP should be refused, location=%q", loc)
	}

	// The login page shows the lockout message (fetched without a session cookie,
	// since a logged-in request would just redirect to the dashboard).
	req := httptest.NewRequest("GET", "/login?error=locked", nil)
	lrec := httptest.NewRecorder()
	env.srv.ServeHTTP(lrec, req)
	if !strings.Contains(lrec.Body.String(), "Too many failed attempts") {
		t.Error("the lockout message should be shown")
	}
}

// A successful login clears the failure count.
func TestLoginResetsOnSuccess(t *testing.T) {
	env := newTestEnv(t, false)
	bad := url.Values{"username": {"admin"}, "password": {"wrong"}}
	for i := 0; i < 4; i++ { // one short of the limit
		env.do(t, "POST", "/login", bad)
	}
	good := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}
	if rec := env.do(t, "POST", "/login", good); rec.Header().Get("Location") != "/" {
		t.Fatalf("a good login should succeed before lockout")
	}
	// The counter is cleared, so four more failures do not lock out.
	for i := 0; i < 4; i++ {
		env.do(t, "POST", "/login", bad)
	}
	if env.srv.login.blocked("192.0.2.1") {
		t.Error("the failure count should have reset after the successful login")
	}
}

func TestHealthzIsPublicAndReportsBird(t *testing.T) {
	env := newTestEnv(t, false)
	req := httptest.NewRequest("GET", "/healthz", nil) // no cookie
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz should be public, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) || !strings.Contains(rec.Body.String(), "birdReachable") {
		t.Errorf("healthz body: %s", rec.Body.String())
	}
}
