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

	rec := env.do(t, http.MethodPost, "/settings/theme", url.Values{"accent": {"ocean"}, "mode": {"dark"}})
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusNoContent {
		t.Fatalf("theme save status = %d, body=%s", rec.Code, rec.Body.String())
	}
	u, ok, err := env.store.GetUserByID(1)
	if err != nil || !ok {
		t.Fatal("admin user missing")
	}
	if u.ThemeMode != "dark" || u.ThemeAccent != "ocean" {
		t.Errorf("persisted %q/%q, want dark/ocean", u.ThemeMode, u.ThemeAccent)
	}
	if c := findCookie(rec.Result().Cookies(), themeCookieName); c == nil || c.Value != "dark.ocean" {
		t.Errorf("theme cookie = %+v, want dark.ocean", c)
	}

	// An unknown accent falls back to green (mode is left as the stored dark).
	env.do(t, http.MethodPost, "/settings/theme", url.Values{"accent": {"chartreuse"}})
	if u, _, _ := env.store.GetUserByID(1); u.ThemeAccent != "green" || u.ThemeMode != "dark" {
		t.Errorf("invalid accent gave %q/%q, want green/dark", u.ThemeMode, u.ThemeAccent)
	}

	// The mode endpoint persists mode and leaves the accent alone.
	rec = env.do(t, http.MethodPost, "/settings/theme/mode", url.Values{"mode": {"light"}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("theme mode status = %d", rec.Code)
	}
	if u, _, _ := env.store.GetUserByID(1); u.ThemeMode != "light" || u.ThemeAccent != "green" {
		t.Errorf("mode endpoint gave %q/%q, want light/green", u.ThemeMode, u.ThemeAccent)
	}

	// A bogus mode is rejected, not stored.
	rec = env.do(t, http.MethodPost, "/settings/theme/mode", url.Values{"mode": {"sepia"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bogus mode status = %d, want 400", rec.Code)
	}
}
