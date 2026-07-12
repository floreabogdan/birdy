package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every response — even the unauthenticated login page — carries the hardening
// headers, since they are set before the mux runs.
func TestSecurityHeaders(t *testing.T) {
	env := newTestEnv(t, false)

	req := httptest.NewRequest("GET", "/login", nil)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)

	h := rec.Header()
	if got := h.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "form-action 'self'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q; got %q", want, csp)
		}
	}
}

// Authenticated pages carry them too.
func TestSecurityHeadersOnAuthedPage(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, "GET", "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard: %d", rec.Code)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("authenticated pages should also carry a CSP")
	}
}
