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
