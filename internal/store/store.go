// Package store wraps birdy's SQLite database: settings, local user accounts,
// login sessions and the event timeline. modernc.org/sqlite is pure Go, so
// the whole tool cross-compiles from any host without cgo.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
//
// The database holds password hashes, BGP session secrets and API token
// hashes, so the file and its -wal/-shm sidecars are kept at mode 0600 whatever
// the umask, and a path whose last component is a symlink is refused (see
// prepareDBFile).
func Open(path string) (*Store, error) {
	onDisk := isDiskPath(path)
	if onDisk {
		if err := prepareDBFile(path); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// SQLite serializes writers. Keep the pool deliberately small so a burst of
	// HTTP requests cannot create an unbounded set of connections and contend on
	// the same database file.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	// Before the schema is applied: a database from a newer birdy is refused
	// without first having this binary's idea of the tables created in it.
	if err := checkNotNewer(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	if onDisk {
		restrictSidecars(path)
	}
	return &Store{db: db}, nil
}

// dbFileMode is the mode of the database and everything derived from it.
const dbFileMode = 0o600

// isDiskPath reports whether path names an ordinary file birdy should manage
// the permissions of. SQLite's special names — an in-memory database, or a
// "file:" URI carrying its own options — are passed through untouched.
func isDiskPath(path string) bool {
	return path != "" && path != ":memory:" && !strings.HasPrefix(path, "file:")
}

// prepareDBFile makes sure the database file exists with mode 0600 before
// SQLite sees it, and that it is not a symlink.
//
// Left to itself SQLite creates the file 0644 under the usual umask, which made
// every secret in it readable by any local user. Creating it here first fixes
// the sidecars as well: SQLite gives -wal and -shm the main file's mode.
//
// The symlink refusal is for "birdy init" and other commands a human runs as
// root: the database lives in a directory the unprivileged service account
// owns, so that account could otherwise point birdy.db at any file on the
// system and have root open, write and chown it. Only the final component is
// checked — a symlinked parent directory is a normal way to relocate state and
// is not under the service account's control in the same way.
func prepareDBFile(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// O_EXCL also refuses a symlink that appeared since the Lstat.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, dbFileMode)
		if err != nil {
			return fmt.Errorf("store: create database file: %w", err)
		}
		return f.Close()
	case err != nil:
		return fmt.Errorf("store: inspect database file: %w", err)
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("store: refusing to open %s: it is a symbolic link; point --db at the real file", path)
	case !info.Mode().IsRegular():
		// A directory, device or socket: let SQLite report it in its own words.
		return nil
	}
	return restrictMode(path, info)
}

// restrictSidecars tightens -wal and -shm files that were already on disk with
// a wider mode (left by an older birdy that crashed, or still held open by
// one). Ones SQLite creates from now on inherit 0600 from the main file.
// Best-effort: the main file is what gates access to a fresh sidecar, and a
// failure here must not stop birdy starting.
func restrictSidecars(path string) {
	for _, p := range []string{path + "-wal", path + "-shm"} {
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			_ = restrictMode(p, info)
		}
	}
}

// restrictMode drops any group/other access from an existing file. Not being
// allowed to (the file belongs to another user and we reach it through group
// permissions) is not an error: that install worked before and must keep
// working — "birdy server" warns about the mode instead.
func restrictMode(path string, info fs.FileInfo) error {
	if runtime.GOOS == "windows" || info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(path, dbFileMode); err != nil && !errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("store: restrict permissions on %s: %w", path, err)
	}
	return nil
}

// PermissionWarnings describes ways the database at path is exposed to other
// local users: a directory that "other" can enter or list, or a database file
// that is still readable beyond its owner. They are advisory — birdy keeps
// running — and meant to be logged once at startup. Empty on Windows, where
// these mode bits mean nothing.
func PermissionWarnings(path string) []string {
	if runtime.GOOS == "windows" || !isDiskPath(path) {
		return nil
	}
	var out []string
	dir := filepath.Dir(path)
	if info, err := os.Stat(dir); err == nil && info.Mode().Perm()&0o007 != 0 {
		out = append(out, fmt.Sprintf("the database directory %s is accessible to every local user (mode %04o); restrict it with: chmod 0750 %s",
			dir, info.Mode().Perm(), dir))
	}
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0 {
		out = append(out, fmt.Sprintf("the database file %s has mode %04o and birdy could not tighten it (it belongs to another user); it should be 0600 and owned by the user birdy runs as",
			path, info.Mode().Perm()))
	}
	return out
}

func (s *Store) Close() error {
	return s.db.Close()
}

// CheckWritable fails when the database can be read but not written — the state a
// root-created file leaves behind when the service then runs as another user.
// SQLite only complains at the first write, so nothing above notices: birdy
// starts, serves a login page, and the login (which inserts a session row) is
// what finally breaks. Probing at startup turns that into one clear error.
//
// It writes, so it is a probe, not a query: a schema-only round trip that leaves
// the database exactly as it found it.
func (s *Store) CheckWritable() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS write_probe (ok INTEGER)`); err != nil {
		return fmt.Errorf("store: database is not writable: %w", err)
	}
	if _, err := s.db.Exec(`DROP TABLE write_probe`); err != nil {
		return fmt.Errorf("store: database is not writable: %w", err)
	}
	return nil
}

// VacuumInto writes a consistent, point-in-time copy of the database to path
// using SQLite's VACUUM INTO — safe to run against a live database with
// concurrent readers/writers, no external locking required. path must either
// not exist or be an empty file; the snapshot manager pre-creates it empty with
// mode 0600 so the copy is never readable by anyone else, even while it is
// being written.
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
