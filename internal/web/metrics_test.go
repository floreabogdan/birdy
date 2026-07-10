package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestMetricsExposition(t *testing.T) {
	env := newTestEnvMetrics(t)
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
