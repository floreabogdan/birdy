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
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("CSP still permits inline scripts: %q", csp)
	}
	if got := h.Get("Permissions-Policy"); !strings.Contains(got, "camera=()") {
		t.Errorf("Permissions-Policy = %q", got)
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

func TestCrossOriginPostRejected(t *testing.T) {
	env := newTestEnv(t, false)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST code=%d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestSameOriginWriteRequiresMatchingScheme(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://birdy.example/logout", nil)
	req.Header.Set("Origin", "http://birdy.example")
	if sameOriginWrite(req, true) {
		t.Fatal("HTTP origin should not be accepted for an HTTPS request")
	}

	req.Header.Set("Origin", "https://birdy.example")
	if !sameOriginWrite(req, true) {
		t.Fatal("matching HTTPS origin should be accepted")
	}
}
