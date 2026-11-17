package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// mode returns a file's permission bits, failing the test if it is missing.
func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// The claim the 0600 guarantee rests on: SQLite gives the -wal and -shm
// sidecars the main file's mode, whatever the umask. Checked against the raw
// modernc driver (no store code involved) so a driver upgrade that changed it
// would be noticed here rather than in production.
func TestSQLiteSidecarsInheritMainFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	path := filepath.Join(t.TempDir(), "raw.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE t (x INTEGER); INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if got := mode(t, p); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), got)
		}
	}
}

// A fresh database, and its sidecars, must be private even under the usual
// umask 022 — the file holds password hashes, BGP MD5 secrets and API tokens.
func TestOpenCreatesPrivateDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	path := filepath.Join(t.TempDir(), "birdy.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateUser("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if got := mode(t, p); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), got)
		}
	}
}

// An install made by an older birdy has a 0644 database; opening it must stop
// every local user reading it, with no operator action. Only the "other" bits
// go: the group bits are the operator's (a backup job reading the file through
// its group must keep working after the upgrade), and so are the owner's.
func TestOpenStripsOtherAccessFromExistingDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	for _, tc := range []struct{ before, after os.FileMode }{
		{0o644, 0o640}, {0o640, 0o640}, {0o666, 0o660}, {0o660, 0o660}, {0o604, 0o600}, {0o600, 0o600},
	} {
		path := filepath.Join(t.TempDir(), "birdy.db")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tc.before); err != nil { // exact, whatever the umask
			t.Fatal(err)
		}
		// A second connection held open keeps the sidecars (created with the
		// main file's mode) on disk, the way a crashed or still-running process
		// would.
		raw, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(`CREATE TABLE unrelated (x INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if got := mode(t, path+"-wal"); got != tc.before {
			t.Fatalf("setup: -wal mode = %o, want %o", got, tc.before)
		}

		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{path, path + "-wal", path + "-shm"} {
			if got := mode(t, p); got != tc.after {
				t.Errorf("%s: mode %o became %o, want %o", filepath.Base(p), tc.before, got, tc.after)
			}
		}
		st.Close()
		raw.Close()
	}
}

// "birdy server" must keep starting on an install whose birdy.db is a symlink
// (0.7.0 followed it), so it has its own explicit entry point; plain Open — what
// root-run "birdy init" uses — keeps refusing.
func TestOpenFollowingSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "data")
	if err := os.Mkdir(realDir, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(realDir, "real.db")
	link := filepath.Join(dir, "birdy.db")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got, ok := SymlinkTarget(link); !ok || got != target {
		t.Errorf("SymlinkTarget(link) = %q, %v; want %q, true", got, ok, target)
	}
	if got, ok := SymlinkTarget(target); ok {
		t.Errorf("SymlinkTarget(regular or missing file) = %q, true", got)
	}

	// Dangling link: the target is created, private, as a fresh database.
	st, err := OpenFollowingSymlink(link)
	if err != nil {
		t.Fatalf("server-style open through a symlink: %v", err)
	}
	if _, err := st.CreateUser("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		// SQLite keeps the sidecars next to the real file.
		for _, p := range []string{target, target + "-wal", target + "-shm"} {
			if got := mode(t, p); got != 0o600 {
				t.Errorf("%s mode = %o, want 600", filepath.Base(p), got)
			}
		}
	}
	st.Close()
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link itself must be left as it is: %v", err)
	}

	// Existing target, world-readable as 0.7.0 left it: followed and tightened.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err = OpenFollowingSymlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.HasAnyUser(); err != nil || !ok {
		t.Errorf("the linked database's contents were not found: %v %v", ok, err)
	}
	st.Close()
	if runtime.GOOS != "windows" {
		if got := mode(t, target); got != 0o640 {
			t.Errorf("linked database mode = %o, want 640", got)
		}
	}

	// The strict default is unchanged.
	if st, err := Open(link); err == nil {
		st.Close()
		t.Fatal("plain Open followed the symlink")
	}
	// And a regular path behaves exactly like Open.
	st, err = OpenFollowingSymlink(filepath.Join(dir, "plain.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
}

// birdy init runs as root in a directory the service account owns. A symlink
// planted as the database name must not be followed; a symlinked parent
// directory is an ordinary deployment choice and stays fine.
func TestOpenRefusesSymlinkedDatabaseFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.db")
	link := filepath.Join(dir, "birdy.db")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if st, err := Open(link); err == nil {
		st.Close()
		t.Fatal("Open followed a dangling symlink")
	} else if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error should name the symlink, got: %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("the symlink target was created: %v", err)
	}

	// Same refusal when the target exists.
	st, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if st, err := Open(link); err == nil {
		st.Close()
		t.Fatal("Open followed a symlink to an existing database")
	}

	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o750); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "linked")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	st, err = Open(filepath.Join(linkDir, "birdy.db"))
	if err != nil {
		t.Fatalf("a symlinked parent directory must keep working: %v", err)
	}
	st.Close()
}

// A database written by a newer birdy may have a shape this binary does not
// understand. Running on it anyway (the old behaviour) risks quietly writing
// wrong data, so Open refuses and says what to do.
func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "birdy.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`PRAGMA user_version = 9999`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(path)
	if err == nil {
		st.Close()
		t.Fatal("Open accepted a database from the future")
	}
	for _, want := range []string{"database schema 9999 is newer than this birdy", "upgrade birdy or restore an older snapshot"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
}

// The cookie value is a bearer credential. Only its SHA-256 may reach the
// database, so a leaked snapshot or backup cannot be replayed as a login.
func TestSessionTokenIsStoredHashed(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "birdy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateUser("admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	const login, rotated = "cookie-value-from-login", "cookie-value-after-rotation"
	if err := st.CreateSession(login, id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	stored := func() []string {
		t.Helper()
		rows, err := st.db.Query(`SELECT token_hash FROM sessions`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		return out
	}
	got := stored()
	if len(got) != 1 || got[0] == login || got[0] != hashSessionToken(login) {
		t.Fatalf("stored session keys = %v, want only sha256(token)", got)
	}
	// The stored value must not work as a cookie.
	if _, ok, err := st.GetSession(got[0]); err != nil || ok {
		t.Fatalf("the stored hash was accepted as a session token: ok=%v err=%v", ok, err)
	}
	if _, ok, err := st.GetSession(login); err != nil || !ok {
		t.Fatalf("the real token stopped working: ok=%v err=%v", ok, err)
	}

	// The password-change path inserts its own session row.
	if err := st.RotatePasswordSession(id, "hash2", rotated, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got = stored()
	if len(got) != 1 || got[0] != hashSessionToken(rotated) {
		t.Fatalf("after rotation stored session keys = %v, want only sha256(new token)", got)
	}
}

// Upgrading from 0.7.0 (schema 41): the raw tokens in the sessions table are
// dropped — everyone logs in once more — and the column holds hashes from then on.
func TestMigrateSessionsToHashedTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v41.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateUser("admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	// Peel the table back to its 0.7.0 shape, holding a raw token.
	if _, err := st.db.Exec(`
		ALTER TABLE sessions RENAME COLUMN token_hash TO token;
		INSERT INTO sessions (token, user_id, created_at, expires_at) VALUES ('raw-cookie', ?, '2026-11-01T00:00:00Z', '2099-01-01T00:00:00Z');
		PRAGMA user_version = 41;`, id); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatalf("open/migrate: %v", err)
	}
	defer st.Close()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d pre-upgrade sessions survived the migration", n)
	}
	if _, ok, err := st.GetSession("raw-cookie"); err != nil || ok {
		t.Errorf("a pre-upgrade cookie still logs in: ok=%v err=%v", ok, err)
	}
	if err := st.CreateSession("fresh", id, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("login after upgrade: %v", err)
	}
	if _, ok, err := st.GetSession("fresh"); err != nil || !ok {
		t.Errorf("session created after upgrade not found: ok=%v err=%v", ok, err)
	}
	if _, ok, err := st.GetUserByUsername("admin"); err != nil || !ok {
		t.Errorf("the user must survive the migration: ok=%v err=%v", ok, err)
	}
	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || schemaVersion < 42 {
		t.Errorf("user_version = %d, schemaVersion = %d, want both >= 42", version, schemaVersion)
	}
}

// The startup warning: a database directory any local user can enter exposes
// whatever is, or briefly appears, next to the database.
func TestPermissionWarnings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "birdy.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if got := PermissionWarnings(path); len(got) != 0 {
		t.Errorf("a 0750 directory and 0600 file should not warn: %v", got)
	}
	// Group access is the operator's choice (a backup job), not a finding.
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	if got := PermissionWarnings(path); len(got) != 0 {
		t.Errorf("a group-readable database should not warn: %v", got)
	}
	// "Other" access still on the file means Open could not remove it.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := PermissionWarnings(path); len(got) != 1 || !strings.Contains(got[0], path) {
		t.Errorf("a world-readable database should warn once, naming it: %v", got)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	for _, m := range []os.FileMode{0o755, 0o751, 0o754} {
		if err := os.Chmod(dir, m); err != nil {
			t.Fatal(err)
		}
		got := PermissionWarnings(path)
		if len(got) != 1 || !strings.Contains(got[0], dir) || !strings.Contains(got[0], "chmod 0750") {
			t.Errorf("mode %o: warnings = %v, want one naming the directory and the fix", m, got)
		}
	}
	if got := PermissionWarnings(":memory:"); got != nil {
		t.Errorf("an in-memory database has no directory to warn about: %v", got)
	}
}
