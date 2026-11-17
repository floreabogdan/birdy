package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// A rejected "birdy init" must leave nothing behind. The admin user used to be
// created before --router-id was checked, so a typo there left a database with
// a user but no settings: init then refused to run again ("already
// initialized") and the server refused to start ("not initialized").
func TestInitRejectsBadInputBeforeWritingAnything(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "birdy.db")
	base := []string{"--db", dbPath, "--username", "admin"}

	for name, extra := range map[string][]string{
		"router id not an address": {"--password", "long enough password", "--router-id", "not-an-ip"},
		"router id is IPv6":        {"--password", "long enough password", "--router-id", "2001:db8::1"},
		"password too short":       {"--password", "short", "--router-id", "192.0.2.1"},
		"empty username":           {"--password", "long enough password", "--username", " "},
	} {
		if err := cmdInit(append(append([]string{}, base...), extra...)); err == nil {
			t.Fatalf("%s: init succeeded", name)
		}
		if _, err := os.Stat(dbPath); err == nil {
			st, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			hasUser, _ := st.HasAnyUser()
			_, hasSettings, _ := st.GetSettings()
			st.Close()
			if hasUser || hasSettings {
				t.Fatalf("%s: a failed init left user=%v settings=%v behind", name, hasUser, hasSettings)
			}
		}
	}

	// The corrected command then simply works.
	if err := cmdInit(append(append([]string{}, base...), "--password", "long enough password", "--router-id", "192.0.2.1")); err != nil {
		t.Fatalf("init after a rejected attempt: %v", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	settings, ok, err := st.GetSettings()
	if err != nil || !ok || settings.RouterID != "192.0.2.1" {
		t.Fatalf("settings after init: %+v ok=%v err=%v", settings, ok, err)
	}
	if hasUser, err := st.HasAnyUser(); err != nil || !hasUser {
		t.Fatalf("user after init: %v err=%v", hasUser, err)
	}
}

// init is the command an operator runs as root; the database directory belongs
// to the service account. A symlink planted as birdy.db must not be followed.
func TestInitRefusesSymlinkedDatabase(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	dbPath := filepath.Join(dir, "birdy.db")
	if err := os.Symlink(victim, dbPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	err := cmdInit([]string{"--db", dbPath, "--password", "long enough password"})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("init through a symlink: err=%v", err)
	}
	if _, err := os.Lstat(victim); !os.IsNotExist(err) {
		t.Fatalf("init created the symlink's target: %v", err)
	}
}
