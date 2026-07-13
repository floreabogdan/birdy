package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func TestMetricsOnlyWhenEnabled(t *testing.T) {
	// Default test env has metrics off.
	env := newTestEnv(t, false)
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("metrics should be absent unless enabled, got %d", rec.Code)
	}
}

// /metrics carries the router's session inventory and cannot be authenticated, so
// it stays shut while the access list allows every IP — the state a fresh, openly
// bound install starts in.
func TestMetricsClosedWhileAccessIsWideOpen(t *testing.T) {
	env := newTestEnvMetrics(t) // whitelist defaults to 0.0.0.0/0
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an allow-all access list must keep /metrics closed, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Access") {
		t.Errorf("the refusal should point at the access list, got %q", rec.Body.String())
	}
}

func TestMetricsExposition(t *testing.T) {
	env := newTestEnvMetrics(t)
	// Narrowing the access list is what opens /metrics — no flag, no restart.
	if err := env.store.SaveSettings(store.Settings{}); err != nil { // the row the whitelist lives on
		t.Fatal(err)
	}
	if err := env.store.SaveAccessWhitelist("192.0.2.0/24\n127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	env.srv.reloadAccess()

	// No cookie: /metrics is unauthenticated by design.
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics code=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"birdy_up 1",
		"# TYPE birdy_bgp_session_up gauge",
		`birdy_bgp_session_up{name="edge_v4",proto="BGP"} 1`,
		"birdy_routes_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
}
