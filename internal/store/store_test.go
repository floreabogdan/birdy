package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "birdy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSettingsRoundTrip(t *testing.T) {
	s := openTest(t)

	if _, ok, err := s.GetSettings(); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("expected no settings row before init")
	}

	want := Settings{
		RouterLabel:    "rtr1.example.net",
		LocalASN:       sql.NullInt64{Int64: 65551, Valid: true},
		BirdSocketPath: "/run/bird/bird.ctl",
		ListenAddr:     "127.0.0.1:8080",
		WebhookURL:     "",
		// access_whitelist is managed by SaveAccessWhitelist, not SaveSettings, so
		// a fresh row carries its column default.
		AccessWhitelist: "0.0.0.0/0",
		UpdateChannel:   "stable",
	}
	if err := s.SaveSettings(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected settings row after save")
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	want.WebhookURL = "https://ntfy.sh/birdy"
	if err := s.SaveSettings(want); err != nil {
		t.Fatal(err)
	}
	got, _, err = s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.WebhookURL != want.WebhookURL {
		t.Fatalf("webhook not updated: %+v", got)
	}
}

func TestUsersAndSessions(t *testing.T) {
	s := openTest(t)

	if has, err := s.HasAnyUser(); err != nil || has {
		t.Fatalf("has=%v err=%v, want false/nil", has, err)
	}

	id, err := s.CreateUser("admin", "hash1")
	if err != nil {
		t.Fatal(err)
	}
	if has, err := s.HasAnyUser(); err != nil || !has {
		t.Fatalf("has=%v err=%v, want true/nil", has, err)
	}

	u, ok, err := s.GetUserByUsername("admin")
	if err != nil || !ok || u.ID != id || u.PasswordHash != "hash1" {
		t.Fatalf("u=%+v ok=%v err=%v", u, ok, err)
	}

	if _, ok, err := s.GetUserByUsername("nobody"); err != nil || ok {
		t.Fatalf("expected no such user, ok=%v err=%v", ok, err)
	}

	if err := s.SetPassword(id, "hash2"); err != nil {
		t.Fatal(err)
	}
	u, _, _ = s.GetUserByUsername("admin")
	if u.PasswordHash != "hash2" {
		t.Fatalf("password not updated: %+v", u)
	}

	// sessions
	if err := s.CreateSession("tok1", id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	sess, ok, err := s.GetSession("tok1")
	if err != nil || !ok || sess.UserID != id {
		t.Fatalf("sess=%+v ok=%v err=%v", sess, ok, err)
	}

	if err := s.CreateSession("expired", id, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetSession("expired"); err != nil || ok {
		t.Fatalf("expected expired session to be invalid, ok=%v err=%v", ok, err)
	}

	if err := s.DeleteSession("tok1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetSession("tok1"); err != nil || ok {
		t.Fatalf("expected deleted session gone, ok=%v err=%v", ok, err)
	}

	if err := s.PruneExpiredSessions(); err != nil {
		t.Fatal(err)
	}

	if err := s.CreateSession("old-a", id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("old-b", id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.RotatePasswordSession(id, "hash3", "replacement", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"old-a", "old-b"} {
		if _, ok, err := s.GetSession(token); err != nil || ok {
			t.Fatalf("old session %q survived rotation: ok=%v err=%v", token, ok, err)
		}
	}
	if sess, ok, err := s.GetSession("replacement"); err != nil || !ok || sess.UserID != id {
		t.Fatalf("replacement session: sess=%+v ok=%v err=%v", sess, ok, err)
	}
	u, _, _ = s.GetUserByUsername("admin")
	if u.PasswordHash != "hash3" {
		t.Fatalf("rotated password not stored: %+v", u)
	}
}

func TestEventsListingAndPagination(t *testing.T) {
	s := openTest(t)

	for i := range 5 {
		if err := s.InsertEvent(EventSessionUp, "edge_v4", "session established"); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	page1, err := s.ListEvents(3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 3 {
		t.Fatalf("got %d events, want 3", len(page1))
	}
	// newest first
	if page1[0].ID <= page1[1].ID || page1[1].ID <= page1[2].ID {
		t.Fatalf("events not in descending order: %+v", page1)
	}

	page2, err := s.ListEvents(3, page1[len(page1)-1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 {
		t.Fatalf("got %d events on page2, want 2", len(page2))
	}
	if page2[0].ID >= page1[len(page1)-1].ID {
		t.Fatalf("page2 should continue strictly before page1's last id: page1last=%d page2first=%d",
			page1[len(page1)-1].ID, page2[0].ID)
	}
}
