package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// birdy ships bound to every interface with an allow-all access list, so it works
// the moment it is installed. That is only defensible if the UI says so: the
// dashboard must warn until the operator narrows the list.
func TestDashboardWarnsWhenWideOpen(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.ListenAddr = "0.0.0.0:8080" })

	rec := env.do(t, "GET", "/", nil)
	if !strings.Contains(rec.Body.String(), "reachable from any IP") {
		t.Error("an openly bound birdy with an allow-all access list must warn on the dashboard")
	}

	// Narrowing the list is what clears it.
	if err := env.store.SaveSettings(store.Settings{}); err != nil { // the row SaveAccessWhitelist updates
		t.Fatal(err)
	}
	if err := env.store.SaveAccessWhitelist("192.0.2.0/24"); err != nil {
		t.Fatal(err)
	}
	env.srv.reloadAccess()
	rec = env.do(t, "GET", "/", nil)
	if strings.Contains(rec.Body.String(), "reachable from any IP") {
		t.Error("a narrowed access list should clear the warning")
	}
}

// Bound to loopback, nothing off-box can reach birdy whatever the access list
// says — warning there would be noise.
func TestDashboardQuietOnLoopback(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.ListenAddr = "127.0.0.1:8080" })
	rec := env.do(t, "GET", "/", nil)
	if strings.Contains(rec.Body.String(), "reachable from any IP") {
		t.Error("a loopback bind must not warn")
	}
}

func TestListenLoopbackDetection(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080":    true,
		"localhost:8080":    true,
		"[::1]:8080":        true,
		"0.0.0.0:8080":      false,
		":8080":             false, // every interface
		"81.181.164.1:8080": false,
		"":                  false,
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
