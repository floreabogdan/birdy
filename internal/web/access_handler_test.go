package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fromIP(t *testing.T, env *testEnv, method, path, remoteAddr string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	if auth {
		req.AddCookie(env.cookie)
	}
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	return rec
}

// A restrictive whitelist blocks non-listed IPs from every route, allows listed
// ones, and never blocks loopback.
func TestAccessWhitelistGate(t *testing.T) {
	env := applyReady(t)
	if err := env.store.SaveAccessWhitelist("203.0.113.0/24"); err != nil {
		t.Fatal(err)
	}
	env.srv.reloadAccess()

	// A blocked client does not reach the app (403 under test, connection-close in
	// production) — and it applies to public routes too.
	if rec := fromIP(t, env, "GET", "/login", "198.51.100.5:1234", false); rec.Code != http.StatusForbidden {
		t.Errorf("blocked IP on /login: code=%d, want 403", rec.Code)
	}
	if rec := fromIP(t, env, "GET", "/metrics", "198.51.100.5:1234", false); rec.Code != http.StatusForbidden {
		t.Errorf("blocked IP on /metrics: code=%d, want 403", rec.Code)
	}
	// A listed client reaches the app.
	if rec := fromIP(t, env, "GET", "/", "203.0.113.9:1234", true); rec.Code != http.StatusOK {
		t.Errorf("allowed IP: code=%d, want 200", rec.Code)
	}
	// Loopback is always allowed even though it is not listed — the anti-lockout.
	if rec := fromIP(t, env, "GET", "/healthz", "127.0.0.1:1234", false); rec.Code != http.StatusOK {
		t.Errorf("loopback must always pass: code=%d", rec.Code)
	}
}

// The default (and an empty list) allows everyone, so an upgrade never blocks.
func TestAccessWhitelistDefaultAllowsAll(t *testing.T) {
	env := applyReady(t)
	env.srv.reloadAccess() // settings row has the 0.0.0.0/0 default
	if rec := fromIP(t, env, "GET", "/healthz", "198.51.100.5:1234", false); rec.Code != http.StatusOK {
		t.Errorf("default whitelist should allow all: code=%d", rec.Code)
	}
}

// The settings page shows the operator's connecting IP and the access-control
// form, so they can add themselves before restricting.
func TestSettingsShowsConnectingIP(t *testing.T) {
	env := applyReady(t)
	body := fromIP(t, env, "GET", "/settings", "198.51.100.77:5555", true).Body.String()
	if !strings.Contains(body, "198.51.100.77") {
		t.Error("settings should show the connecting IP")
	}
	if !strings.Contains(body, "Access control") || !strings.Contains(body, `name="accessWhitelist"`) {
		t.Error("settings should show the access-control form")
	}
}

// Saving a whitelist takes effect immediately (the cache is reloaded).
func TestAccessWhitelistSaveTakesEffect(t *testing.T) {
	env := applyReady(t)
	// Save via the handler from a loopback request (always allowed), restricting
	// to a range that excludes 198.51.100.x.
	req := httptest.NewRequest("POST", "/settings/access", strings.NewReader("accessWhitelist=203.0.113.0/24"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "127.0.0.1:1"
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// Now a non-listed IP is blocked without any further reload call.
	if rec := fromIP(t, env, "GET", "/", "198.51.100.5:2", true); rec.Code != http.StatusForbidden {
		t.Errorf("a saved whitelist should take effect immediately: code=%d", rec.Code)
	}
}
