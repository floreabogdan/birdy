package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// birdy ships bound to every interface with an allow-all access list, so it works
// the moment it is installed. It says so where the operator can act on it — the
// Access settings page — and NOT on the dashboard, which is the page you keep
// open all day; a warning you see a hundred times is one you stop reading.
func TestAccessPageFlagsWideOpen(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.ListenAddr = "0.0.0.0:8080" })

	rec := env.do(t, "GET", "/settings?tab=access", nil)
	if !strings.Contains(rec.Body.String(), "open to any IP") {
		t.Error("the access page should flag an openly bound birdy with an allow-all list")
	}
	if dash := env.do(t, "GET", "/", nil); strings.Contains(dash.Body.String(), "reachable from any IP") {
		t.Error("the dashboard must not nag about it")
	}

	// Narrowing the list is what clears it.
	if err := env.store.SaveSettings(store.Settings{}); err != nil { // the row SaveAccessWhitelist updates
		t.Fatal(err)
	}
	if err := env.store.SaveAccessWhitelist("192.0.2.0/24"); err != nil {
		t.Fatal(err)
	}
	env.srv.reloadAccess()
	rec = env.do(t, "GET", "/settings?tab=access", nil)
	if strings.Contains(rec.Body.String(), "open to any IP") {
		t.Error("a narrowed access list should clear the flag")
	}
}

// Bound to loopback, nothing off-box can reach birdy whatever the access list
// says — flagging it would be noise.
func TestAccessPageQuietOnLoopback(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.ListenAddr = "127.0.0.1:8080" })
	rec := env.do(t, "GET", "/settings?tab=access", nil)
	if strings.Contains(rec.Body.String(), "open to any IP") {
		t.Error("a loopback bind must not be flagged as open")
	}
}

func TestListenLoopbackDetection(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080":   true,
		"localhost:8080":   true,
		"[::1]:8080":       true,
		"0.0.0.0:8080":     false,
		":8080":            false, // every interface
		"203.0.113.7:8080": false,
		"":                 false,
	}
	for addr, want := range cases {
		s := &Server{listenAddr: addr}
		if got := s.listenLoopback(); got != want {
			t.Errorf("listenLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// The unauthenticated liveness probe must keep working regardless of posture —
// it is what a load balancer or systemd watchdog polls.
func TestHealthzIgnoresPosture(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.ListenAddr = "0.0.0.0:8080" })
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("healthz code=%d", rec.Code)
	}
}
