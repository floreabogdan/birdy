package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// A stolen session must not be a free password oracle: the profile's
// current-password check goes through the same limiter as the login form.
func TestProfilePasswordCheckIsRateLimited(t *testing.T) {
	env := newTestEnv(t, false)
	change := func(current string) *httptest.ResponseRecorder {
		return env.do(t, http.MethodPost, "/profile/password", url.Values{
			"currentPassword": {current},
			"newPassword":     {"attacker chosen password"},
			"confirmPassword": {"attacker chosen password"},
		})
	}
	for i := range env.srv.login.max {
		if rec := change("guess"); rec.Code != http.StatusOK {
			t.Fatalf("wrong guess %d: code=%d", i, rec.Code)
		}
	}
	rec := change(goodPassword)
	if rec.Code == http.StatusSeeOther {
		t.Fatal("after five wrong guesses the right one must be refused, not change the password")
	}
	if !strings.Contains(rec.Body.String(), "Too many failed attempts") {
		t.Errorf("the refusal should say why:\n%s", rec.Body.String())
	}
	u, _, err := env.store.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(goodPassword)) != nil {
		t.Fatal("the password was changed")
	}
	// The guesses count against the client's login lockout too.
	if loc := loginFrom(env, "192.0.2.1:1234", goodPassword); loc != "/login?error=locked" {
		t.Errorf("profile guesses should lock the address out of the login form too, got %q", loc)
	}
}

// Renaming the account with nothing but a stolen session would lock the real
// admin out (their username stops working), so it needs the password too.
func TestProfileUsernameChangeNeedsCurrentPassword(t *testing.T) {
	env := newTestEnv(t, false)
	for _, current := range []string{"", "wrong"} {
		rec := env.do(t, http.MethodPost, "/profile/identity", url.Values{"username": {"mallory"}, "currentPassword": {current}})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "not your current password") {
			t.Fatalf("current password %q: code=%d, want the form back with an error", current, rec.Code)
		}
		if _, ok, _ := env.store.GetUserByUsername("admin"); !ok {
			t.Fatalf("current password %q: the username was changed", current)
		}
	}
	rec := env.do(t, http.MethodPost, "/profile/identity", url.Values{"username": {"operator"}, "currentPassword": {goodPassword}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("with the right password: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := env.store.GetUserByUsername("operator"); !ok {
		t.Fatal("the rename with the right password did not happen")
	}
}
