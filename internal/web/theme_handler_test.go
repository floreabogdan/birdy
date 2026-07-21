package web

import (
	"net/http"
	"net/url"
	"testing"
)

// The theme is saved on the user (not the browser) and echoed into the cookie
// the bootstrap reads; invalid values fall back rather than persisting garbage.
func TestThemePersistsPerUser(t *testing.T) {
	env := newTestEnv(t, false)

	// The accent endpoint persists ONLY the accent. env.do posts without the
	// X-Requested-With: fetch header, so it takes the no-JS redirect path (303).
	rec := env.do(t, http.MethodPost, "/settings/theme", url.Values{"accent": {"ocean"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("theme accent status = %d, body=%s", rec.Code, rec.Body.String())
	}
	// The mode endpoint persists ONLY the mode.
	rec = env.do(t, http.MethodPost, "/settings/theme/mode", url.Values{"mode": {"dark"}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("theme mode status = %d", rec.Code)
	}
	// Neither clobbered the other: both landed.
	u, ok, err := env.store.GetUserByID(1)
	if err != nil || !ok {
		t.Fatal("admin user missing")
	}
	if u.ThemeMode != "dark" || u.ThemeAccent != "ocean" {
		t.Errorf("persisted %q/%q, want dark/ocean", u.ThemeMode, u.ThemeAccent)
	}

	// An unknown accent falls back to green; the mode (dark) is untouched.
	env.do(t, http.MethodPost, "/settings/theme", url.Values{"accent": {"chartreuse"}})
	if u, _, _ := env.store.GetUserByID(1); u.ThemeAccent != "green" || u.ThemeMode != "dark" {
		t.Errorf("invalid accent gave %q/%q, want green/dark", u.ThemeMode, u.ThemeAccent)
	}

	// Changing the mode leaves the accent alone.
	env.do(t, http.MethodPost, "/settings/theme/mode", url.Values{"mode": {"light"}})
	if u, _, _ := env.store.GetUserByID(1); u.ThemeMode != "light" || u.ThemeAccent != "green" {
		t.Errorf("mode endpoint gave %q/%q, want light/green", u.ThemeMode, u.ThemeAccent)
	}

	// A bogus mode is rejected, not stored.
	rec = env.do(t, http.MethodPost, "/settings/theme/mode", url.Values{"mode": {"sepia"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bogus mode status = %d, want 400", rec.Code)
	}
}
