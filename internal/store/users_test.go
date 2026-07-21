package store

import "testing"

func TestSaveUserThemeRoundTrip(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateUser("admin", "hash")
	if err != nil {
		t.Fatal(err)
	}

	// A fresh user takes the schema defaults.
	u, ok, err := s.GetUserByID(id)
	if err != nil || !ok {
		t.Fatal("user missing after create")
	}
	if u.ThemeMode != "system" || u.ThemeAccent != "green" {
		t.Errorf("defaults = %q/%q, want system/green", u.ThemeMode, u.ThemeAccent)
	}

	// Each setter writes a single column, so setting one never clobbers the other.
	if err := s.SaveUserThemeAccent(id, "violet"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUserThemeMode(id, "dark"); err != nil {
		t.Fatal(err)
	}
	u, _, _ = s.GetUserByID(id)
	if u.ThemeMode != "dark" || u.ThemeAccent != "violet" {
		t.Errorf("after save = %q/%q, want dark/violet", u.ThemeMode, u.ThemeAccent)
	}
}
