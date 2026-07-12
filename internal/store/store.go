// Package store wraps birdy's SQLite database: settings, local user accounts,
// login sessions and the event timeline. modernc.org/sqlite is pure Go, so
// the whole tool cross-compiles from any host without cgo.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store is birdy's SQLite-backed model — settings, peers, policies, the library,
// the config-version history and the event timeline — and the only thing that
// touches the database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path and applies
// the schema. SQLite is single-writer; a small pool is fine since birdy's
// write volume is tiny (settings, sessions, events).
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// VacuumInto writes a consistent, point-in-time copy of the database to path
// using SQLite's VACUUM INTO — safe to run against a live database with
// concurrent readers/writers, no external locking required. path must not
// already exist.
func (s *Store) VacuumInto(path string) error {
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("store: vacuum into: %w", err)
	}
	return nil
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// pluralize returns one when n == 1, else many — for "used by 1 policy" vs
// "used by 3 policies" style messages.
func pluralize(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
