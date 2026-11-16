package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// ValidateRestoreFile checks that the file at path is a database this binary
// can actually run on, before it is staged to replace the live one. A staged
// restore is swapped in at the next start with nobody watching, so a file that
// merely looks like SQLite — truncated, from another application, from a newer
// birdy, or with no account to log in with — would otherwise take the router's
// UI down, or lock the operator out, at exactly the moment they are recovering
// from something else.
//
// The file is opened read-only and immutable: nothing is written to it, no
// -wal/-shm sidecars are created next to it, and it is left byte-for-byte as
// uploaded.
func ValidateRestoreFile(path string) error {
	db, err := sql.Open("sqlite", readOnlyURI(path))
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer db.Close()
	// One connection, so every statement sees the same handle and Close
	// releases the file promptly.
	db.SetMaxOpenConns(1)

	// integrity_check reports problems as rows, "ok" being the single row of a
	// healthy database; a file too damaged to walk fails the query itself.
	rows, err := db.Query(`PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			return fmt.Errorf("integrity check: %w", err)
		}
		if line != "ok" && len(problems) < 3 {
			problems = append(problems, line)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("integrity check failed: %s", strings.Join(problems, "; "))
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version <= 0 {
		return errors.New("it has no birdy schema version, so it is not a birdy database")
	}
	if version > schemaVersion {
		return newerSchemaError(version)
	}

	// A real table, not a view of the same name: counting rows of a view would
	// run whatever query the uploaded file defines.
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'users'`).Scan(&tables); err != nil {
		return fmt.Errorf("look for the users table: %w", err)
	}
	if tables == 0 {
		return errors.New("it has no users table, so it is not a birdy database")
	}
	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if users == 0 {
		return errors.New("it contains no user account, so nobody could log in after the restore")
	}
	return nil
}

// readOnlyURI is the SQLite "file:" URI that opens path read-only and
// immutable. A URI is the only way to ask for either; the path is escaped so
// "?", "#" and "%" in a directory name are not read as URI syntax, and a
// Windows drive path gets the leading slash the URI form requires.
func readOnlyURI(path string) string {
	p := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file:" + (&url.URL{Path: p}).EscapedPath() + "?mode=ro&immutable=1"
}
