package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Cloudflare ships disabled: an enabled server means birdy renders a protocol
// that dials a third party, which is the operator's decision, not a default.
func TestCloudflareIsSeededDisabled(t *testing.T) {
	s := openTest(t)
	srv, err := s.GetRPKIServerByName("cloudflare")
	if err != nil {
		t.Fatalf("cloudflare not seeded: %v", err)
	}
	if srv.Enabled {
		t.Error("the seeded RTR server must be disabled")
	}
	if srv.Host != "rtr.rpki.cloudflare.com" || srv.Port != 8282 {
		t.Errorf("wrong endpoint: %s:%d", srv.Host, srv.Port)
	}
	if srv.Refresh == 0 || srv.Expire <= srv.Refresh {
		t.Errorf("timers should be sane: refresh=%d expire=%d", srv.Refresh, srv.Expire)
	}
	if errs := srv.Validate(); len(errs) != 0 {
		t.Errorf("the seeded server should validate: %v", errs)
	}
}

func TestSeededRPKIServerIsIdempotent(t *testing.T) {
	s := openTest(t)
	before, _ := s.ListRPKIServers()
	if err := migrate(s.db); err != nil {
		t.Fatal(err)
	}
	after, _ := s.ListRPKIServers()
	if len(before) != len(after) {
		t.Errorf("seeding ran twice: %d -> %d", len(before), len(after))
	}
}

// An operator who enables it and then edits it must not have their changes
// reverted by a later migration.
func TestSeededRPKIServerIsNotReset(t *testing.T) {
	s := openTest(t)
	srv, err := s.GetRPKIServerByName("cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	srv.Enabled = true
	if err := s.UpdateRPKIServer(srv); err != nil {
		t.Fatal(err)
	}
	if err := migrate(s.db); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetRPKIServerByName("cloudflare")
	if !again.Enabled {
		t.Error("a re-run migration must not disable a server the operator enabled")
	}
}

// The highest RTR version is BIRD's to choose unless the operator pins it: a
// cache that refuses version 2 without the error report RFC 8210 asks for
// leaves BIRD retrying v2 forever, and pinning 1 is the way out.
func TestRPKIServerMaxVersionValidate(t *testing.T) {
	for _, tc := range []struct {
		v  int
		ok bool
	}{{RTRVersionDefault, true}, {1, true}, {2, true}, {-1, false}, {3, false}} {
		srv := RPKIServer{Name: "rtr", Host: "192.0.2.1", Port: 323, MaxVersion: tc.v}
		errs := srv.Validate()
		if _, bad := errs["max_version"]; bad == tc.ok {
			t.Errorf("max version %d: errs = %v, want ok=%v", tc.v, errs, tc.ok)
		}
	}
}

func TestRPKIServerMaxVersionRoundTrip(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateRPKIServer(RPKIServer{Name: "pinned", Host: "192.0.2.1", Port: 8282, Enabled: true, MaxVersion: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRPKIServerByName("pinned")
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxVersion != 1 {
		t.Errorf("MaxVersion = %d after a round trip, want 1", got.MaxVersion)
	}
	got.MaxVersion = RTRVersionDefault
	if err := s.UpdateRPKIServer(got); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetRPKIServerByName("pinned"); again.MaxVersion != RTRVersionDefault {
		t.Errorf("MaxVersion = %d after resetting, want the default (%d)", again.MaxVersion, RTRVersionDefault)
	}
	// The seeded server predates the setting and must keep BIRD's default.
	if cf, _ := s.GetRPKIServerByName("cloudflare"); cf.MaxVersion != RTRVersionDefault {
		t.Errorf("seeded server MaxVersion = %d, want the default", cf.MaxVersion)
	}
}

// A database from before schema 40 has RTR servers without the column; the
// migration must add it as "BIRD's default" so nothing renders differently.
func TestMigrateAddsRTRMaxVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v39.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE rpki_servers (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '', host TEXT NOT NULL, port INTEGER NOT NULL DEFAULT 323,
			enabled INTEGER NOT NULL DEFAULT 1, refresh INTEGER NOT NULL DEFAULT 0, retry INTEGER NOT NULL DEFAULT 0,
			expire INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`INSERT INTO rpki_servers (name, host, port, enabled, created_at, updated_at)
			VALUES ('local', '192.0.2.10', 3323, 1, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`,
		`PRAGMA user_version = 39`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	raw.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open/migrate: %v", err)
	}
	defer st.Close()
	srv, err := st.GetRPKIServerByName("local")
	if err != nil {
		t.Fatalf("read migrated server: %v", err)
	}
	if srv.MaxVersion != RTRVersionDefault || srv.Port != 3323 {
		t.Errorf("migrated server = %+v, want its data kept and MaxVersion at the default", srv)
	}
}
