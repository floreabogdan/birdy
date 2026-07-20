package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// Migrations must add columns to existing tables and keep the data. Build a
// pre-v22 database by hand — an events table without the `actor` column, stamped
// at user_version 21 — then open it through the store and confirm the migration
// adds the column, preserves the old row, and advances user_version.
func TestMigrateAddsColumnFromOlderVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, kind TEXT NOT NULL,
			protocol TEXT NOT NULL DEFAULT '', message TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`INSERT INTO events (ts, kind, protocol, message, created_at)
			VALUES ('2026-07-12T00:00:00Z', 'session_up', 'edge_v4', 'established', '2026-07-12T00:00:00Z')`,
		`PRAGMA user_version = 21`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	raw.Close()

	// Opening runs the migrations forward from 21.
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open/migrate: %v", err)
	}
	defer st.Close()

	// v22 added events.actor; the pre-existing row survives with an empty actor.
	events, err := st.ListEvents(10, 0)
	if err != nil {
		t.Fatalf("list events after migrate (column missing?): %v", err)
	}
	if len(events) != 1 || events[0].Kind != "session_up" || events[0].Actor != "" {
		t.Errorf("migration should keep the old row and add an empty actor, got %+v", events)
	}

	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Errorf("user_version = %d after migrate, want %d", version, schemaVersion)
	}
}

// The instances/update-tracking columns and tables (v31–v34) are all created by
// schema.go's unconditional CREATE TABLE IF NOT EXISTS, so a test that opens the
// full schema never exercises the migration steps. Build a genuinely pre-v31
// database — settings and instances stripped back to their pre-instances shape,
// instance_tokens absent, stamped at v30 — and confirm opening it converges: the
// new settings columns, the instances metadata columns (v33 ALTERs), and the
// instance_tokens table (v34) all appear and work.
func TestMigrateInstancesFeatureFromV30(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v30.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(Settings{RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	// Peel the schema back to what a v30 database looked like, before the
	// instances/update-tracking work landed.
	if _, err := st.db.Exec(`
		ALTER TABLE settings DROP COLUMN update_channel;
		ALTER TABLE settings DROP COLUMN instance_api_token_hash;
		ALTER TABLE settings DROP COLUMN instance_api_token_expires_at;
		ALTER TABLE settings DROP COLUMN instance_api_token_revoked;
		ALTER TABLE instances DROP COLUMN group_name;
		ALTER TABLE instances DROP COLUMN tags;
		ALTER TABLE instances DROP COLUMN last_check_at;
		ALTER TABLE instances DROP COLUMN last_success_at;
		ALTER TABLE instances DROP COLUMN last_latency_ms;
		ALTER TABLE instances DROP COLUMN last_error;
		DROP TABLE instance_tokens;
		PRAGMA user_version = 30;
	`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatalf("open/migrate v30 database: %v", err)
	}
	defer st.Close()

	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d after migrate, want %d", version, schemaVersion)
	}
	// v31 settings.update_channel: the column is present and writable.
	if err := st.SaveUpdateChannel("development"); err != nil {
		t.Fatalf("update_channel column missing after migrate: %v", err)
	}
	// v33 instances metadata columns: an instance round-trips with its metadata.
	if _, err := st.CreateInstanceWithMetadata("edge", "https://198.51.100.7", "0123456789abcdef0123456789abcdef", "eu", "core, transit"); err != nil {
		t.Fatalf("instances metadata columns missing after migrate: %v", err)
	}
	got, err := st.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GroupName != "eu" || got[0].Tags != "core, transit" {
		t.Fatalf("instance metadata did not survive migrate: %+v", got)
	}
	// v34 instance_tokens: the table exists and a scoped token verifies.
	raw := "feedfacefeedfacefeedfacefeedface"
	if _, err := st.CreateInstanceToken("observer", HashInstanceAPIToken(raw), "dashboard timeline", ""); err != nil {
		t.Fatalf("instance_tokens table missing after migrate: %v", err)
	}
	if ok, err := st.VerifyScopedInstanceToken(raw); err != nil || !ok {
		t.Fatalf("scoped token should verify after migrate: ok=%v err=%v", ok, err)
	}
}

func TestMigrateKernelBGPExportDefaultsOffForFreshDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v29.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`
		ALTER TABLE settings DROP COLUMN kernel_export_bgp_v4;
		ALTER TABLE settings DROP COLUMN kernel_export_bgp_v6;
		PRAGMA user_version = 29;
	`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatalf("open/migrate v29 database: %v", err)
	}
	defer st.Close()

	got, ok, err := st.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("an uninitialized database should remain uninitialized")
	}
	if got.KernelExportBGPV4 || got.KernelExportBGPV6 {
		t.Fatal("kernel BGP export must default off on a fresh database")
	}
}

func TestMigrateKernelBGPExportEnabledForExistingRouter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v29.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(Settings{RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`
		ALTER TABLE settings DROP COLUMN kernel_export_bgp_v4;
		ALTER TABLE settings DROP COLUMN kernel_export_bgp_v6;
		PRAGMA user_version = 29;
	`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatalf("open/migrate v29 database: %v", err)
	}
	defer st.Close()

	got, ok, err := st.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("existing settings should survive migration")
	}
	if !got.KernelExportBGPV4 || !got.KernelExportBGPV6 {
		t.Fatal("kernel BGP export must be enabled for existing routers after migration")
	}
}
