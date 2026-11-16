package snapshot

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// openStore opens a database with one user in it — the minimum a restorable
// birdy database has.
func openStore(t *testing.T, dbPath string) *store.Store {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if ok, err := s.HasAnyUser(); err != nil {
		t.Fatal(err)
	} else if !ok {
		if _, err := s.CreateUser("admin", "hash"); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// modeProbe wraps a DB and records what the VACUUM INTO target looked like at
// the moment SQLite was asked to write it.
type modeProbe struct {
	DB
	t      *testing.T
	called int
}

func (p *modeProbe) VacuumInto(path string) error {
	p.called++
	info, err := os.Lstat(path)
	if err != nil {
		p.t.Errorf("the snapshot target must already exist (pre-created private) when SQLite writes it: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		p.t.Errorf("snapshot target mode before VACUUM INTO = %o, want 600", info.Mode().Perm())
	}
	return p.DB.VacuumInto(path)
}

// The snapshot is a full copy of the database. It used to be created 0644 and
// chmodded afterwards, leaving it world-readable for as long as the VACUUM took.
// It must be private from the first byte — and VACUUM INTO must accept the
// pre-created empty file, with the real driver.
func TestSnapshotIsNeverWorldReadable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "birdy.db")
	probe := &modeProbe{DB: openStore(t, dbPath), t: t}
	m := NewManager(dbPath, filepath.Join(dir, "snapshots"), 3)

	path, err := m.CreateSnapshot(probe)
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := m.CreateTempSnapshot(probe)
	if err != nil {
		t.Fatal(err)
	}
	if probe.called != 2 {
		t.Fatalf("VacuumInto called %d times, want 2", probe.called)
	}
	for _, p := range []string{path, tmp} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), info.Mode().Perm())
		}
		// A real database came out the other end.
		if err := store.ValidateRestoreFile(p); err != nil {
			t.Errorf("%s is not a valid snapshot: %v", filepath.Base(p), err)
		}
	}
}

// Pruning used to delete the oldest *file* in the directory, whatever it was.
// Only birdy's own snapshots may be listed or removed.
func TestPruneLeavesForeignFilesAlone(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "birdy.db")
	s := openStore(t, dbPath)
	snapDir := filepath.Join(dir, "snapshots")
	if err := os.MkdirAll(snapDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Names chosen to sort before, between and after real snapshots, plus
	// near-misses of the pattern.
	foreign := []string{
		"aaa-notes.txt", "birdy.db", "birdy-20200101-000000.db.bak", "birdy-2020.db",
		"xbirdy-20200101-000000.db", "birdy-20200101-000000_1.db", "zzz.db",
	}
	for _, name := range foreign {
		if err := os.WriteFile(filepath.Join(snapDir, name), []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(dbPath, snapDir, 1)
	var last string
	for range 3 {
		p, err := m.CreateSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		last = p
	}
	for _, name := range foreign {
		if _, err := os.Stat(filepath.Join(snapDir, name)); err != nil {
			t.Errorf("pruning removed a file that is not a birdy snapshot: %v", err)
		}
	}
	if names := m.list(); len(names) != 1 || filepath.Join(snapDir, names[0]) != last {
		t.Errorf("list() = %v, want only %s", names, filepath.Base(last))
	}
	if latest, ok := m.LatestSnapshot(); !ok || latest != last {
		t.Errorf("LatestSnapshot = %q, %v; want %q (not a foreign file)", latest, ok, last)
	}
}

// --snapshot-retain 0 or a negative number used to delete every snapshot, or
// panic slicing an empty list. Either way the operator loses their backups; keep
// at least one.
func TestRetainIsClampedToOne(t *testing.T) {
	for _, retain := range []int{-5, -1, 0} {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "birdy.db")
		s := openStore(t, dbPath)
		m := NewManager(dbPath, filepath.Join(dir, "snapshots"), retain)
		for range 2 {
			if _, err := m.CreateSnapshot(s); err != nil {
				t.Fatalf("retain %d: %v", retain, err)
			}
		}
		if n := len(m.list()); n != 1 {
			t.Errorf("retain %d kept %d snapshots, want 1", retain, n)
		}
	}
}

// Names have one-second resolution; a second snapshot in the same second used
// to fail because VACUUM INTO refuses an existing file. They must both succeed
// and still sort oldest-first.
func TestSnapshotsInTheSameSecond(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "birdy.db")
	s := openStore(t, dbPath)
	m := NewManager(dbPath, filepath.Join(dir, "snapshots"), 10)
	var paths []string
	for i := range 5 { // five back to back: several are bound to share a second
		p, err := m.CreateSnapshot(s)
		if err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
		paths = append(paths, p)
	}
	names := m.list()
	if len(names) != len(paths) {
		t.Fatalf("list() = %v, want %d distinct snapshots", names, len(paths))
	}
	for i, p := range paths {
		if names[i] != filepath.Base(p) {
			t.Fatalf("snapshots do not sort in creation order:\n created %v\n listed  %v", paths, names)
		}
	}
}

// An on-demand download must not count toward retention: it used to create a
// retained snapshot, so a few clicks evicted every nightly one.
func TestTempSnapshotIsNotRetained(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "birdy.db")
	s := openStore(t, dbPath)
	snapDir := filepath.Join(dir, "snapshots")
	m := NewManager(dbPath, snapDir, 2)
	var nightly []string
	for range 2 {
		p, err := m.CreateSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		nightly = append(nightly, p)
	}
	for range 3 {
		tmp, err := m.CreateTempSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.list()) != 2 {
			t.Fatalf("a temporary snapshot shows up as a retained one: %v", m.list())
		}
		if err := os.Remove(tmp); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range nightly {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("a download evicted a nightly snapshot: %v", err)
		}
	}
	if got := dirNames(t, snapDir); len(got) != 2 {
		t.Errorf("snapshot dir holds %v, want only the two nightly snapshots", got)
	}
}

// rawDB builds a SQLite file with the given statements, bypassing the store.
func rawDB(t *testing.T, stmts ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "raw.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Anything starting with the SQLite magic used to be staged and then swapped in
// over the live database at the next start — bricking birdy if it was not a
// usable birdy database. Staging now proves the upload is one.
func TestStageRestoreValidatesTheUpload(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "birdy.db")
	s := openStore(t, dbPath)
	m := NewManager(dbPath, filepath.Join(dir, "snapshots"), 3)
	snapPath, err := m.CreateTempSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(snapPath)

	// Corrupt a page in the middle of an otherwise valid snapshot.
	corrupt := bytes.Clone(good)
	for i := len(corrupt) / 2; i < len(corrupt)/2+8192 && i < len(corrupt); i++ {
		corrupt[i] = 0xff
	}
	const users = `CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT)`

	before := dirNames(t, dir)
	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"not sqlite":       {[]byte("hello, this is not a database at all"), "not a valid SQLite database"},
		"magic only":       {append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte{0x41}, 4096)...), "not a usable birdy database"},
		"corrupt":          {corrupt, "not a usable birdy database"},
		"no version":       {rawDB(t, users, `INSERT INTO users (username) VALUES ('a')`), "schema version"},
		"newer version":    {rawDB(t, users, `INSERT INTO users (username) VALUES ('a')`, `PRAGMA user_version = 9999`), "newer than this birdy"},
		"no users table":   {rawDB(t, `CREATE TABLE other (x)`, `PRAGMA user_version = 1`), "users"},
		"users is a view":  {rawDB(t, `CREATE VIEW users AS SELECT 1 AS id`, `PRAGMA user_version = 1`), "users"},
		"no user accounts": {rawDB(t, users, `PRAGMA user_version = 1`), "no user account"},
	} {
		err := m.StageRestore(tc.data)
		if err == nil {
			t.Errorf("%s: staged", name)
			os.Remove(dbPath + pendingRestoreSuffix)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q should mention %q", name, err, tc.want)
		}
		if m.HasPendingRestore() {
			t.Errorf("%s: a rejected upload was left staged", name)
		}
	}
	if after := dirNames(t, dir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("rejected uploads left files behind:\n before %v\n after  %v", before, after)
	}

	if err := m.StageRestore(good); err != nil {
		t.Fatalf("a genuine snapshot was rejected: %v", err)
	}
	info, err := os.Lstat(dbPath + pendingRestoreSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("staged restore mode = %o, want 600", info.Mode().Perm())
	}
	// Only the staged file is new: no temp file or SQLite sidecar left over.
	want := append(append([]string{}, before...), filepath.Base(dbPath)+pendingRestoreSuffix)
	if after := dirNames(t, dir); len(after) != len(want) {
		t.Errorf("after a successful stage the directory holds %v, want %v", after, want)
	}
}

// ApplyPendingRestore runs as root when an operator runs "birdy init", in a
// directory the service account controls. It must not read or write through a
// symlink planted under either name.
func TestApplyPendingRestoreRefusesSymlinks(t *testing.T) {
	stage := func(t *testing.T) (dir, dbPath string, snapshot []byte) {
		dir = t.TempDir()
		dbPath = filepath.Join(dir, "birdy.db")
		s := openStore(t, dbPath)
		m := NewManager(dbPath, filepath.Join(dir, "snapshots"), 3)
		p, err := m.CreateTempSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err = os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		os.Remove(p)
		s.Close()
		return dir, dbPath, snapshot
	}

	t.Run("pending is a symlink", func(t *testing.T) {
		dir, dbPath, snap := stage(t)
		secret := filepath.Join(dir, "secret")
		if err := os.WriteFile(secret, snap, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, dbPath+pendingRestoreSuffix); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		before, _ := os.ReadFile(dbPath)
		if err := ApplyPendingRestore(dbPath, nil); err == nil {
			t.Fatal("a symlinked pending-restore file was applied")
		}
		if after, _ := os.ReadFile(dbPath); !bytes.Equal(before, after) {
			t.Error("the database changed although the restore was refused")
		}
	})

	t.Run("database is a symlink", func(t *testing.T) {
		dir, dbPath, snap := stage(t)
		victim := filepath.Join(dir, "victim")
		if err := os.WriteFile(victim, []byte("root's file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(dbPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, dbPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := os.WriteFile(dbPath+pendingRestoreSuffix, snap, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ApplyPendingRestore(dbPath, nil); err == nil {
			t.Fatal("a restore was applied through a symlinked database path")
		}
		if got, _ := os.ReadFile(victim); string(got) != "root's file" {
			t.Errorf("the symlink target was overwritten: %q", got)
		}
		if matches, _ := filepath.Glob(dbPath + ".before-restore-*"); len(matches) != 0 {
			t.Errorf("the symlink target was copied into a backup: %v", matches)
		}
	})
}

// The pre-restore backup is the operator's way back. With the database in WAL
// mode the newest commits live in the -wal file, so copying only the main file
// gave a backup that silently missed them.
func TestPreRestoreBackupIsComplete(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "birdy.db")
	s := openStore(t, dbPath)
	m := NewManager(dbPath, filepath.Join(dir, "snapshots"), 3)
	p, err := m.CreateTempSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(p)

	// Written after the snapshot and still only in the WAL: the store stays open
	// (as after a crash, the -wal is never checkpointed into the main file).
	if err := s.InsertEvent(store.EventSessionUp, "edge_v4", "only in the wal"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dbPath + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("setup: expected a non-empty -wal: %v", err)
	}
	if err := m.StageRestore(snap); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPendingRestore(dbPath, nil); err != nil {
		t.Fatal(err)
	}

	var backup string
	matches, _ := filepath.Glob(dbPath + ".before-restore-*")
	for _, match := range matches {
		if !strings.HasSuffix(match, "-wal") {
			backup = match
		}
	}
	if backup == "" || len(matches) != 2 {
		t.Fatalf("want a backup and its -wal, got %v", matches)
	}
	for _, match := range matches {
		if info, err := os.Stat(match); err != nil {
			t.Fatal(err)
		} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(match), info.Mode().Perm())
		}
	}
	// Opened as a pair, the backup has the commit that was only in the WAL.
	old, err := store.Open(backup)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer old.Close()
	events, err := old.ListEvents(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Message != "only in the wal" {
		t.Errorf("the backup lost what was in the WAL: %+v", events)
	}

	// And the restored database is the snapshot, with no stale sidecars or
	// leftover temp files next to it.
	restored, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, snap) {
		t.Error("the restored database is not the staged snapshot")
	}
	for _, name := range dirNames(t, dir) {
		full := filepath.Join(dir, name)
		if full == dbPath || full == backup || full == backup+"-wal" || name == "snapshots" ||
			// the backup opened just above has its own live sidecars
			strings.HasPrefix(full, backup+"-") {
			continue
		}
		t.Errorf("unexpected file left after restore: %s", name)
	}
}

// With birdy.db a symlink, "birdy server" still runs (as 0.7.0 did) but a
// restore can never be applied: ApplyPendingRestore refuses to swap a file in
// over a symlink, and that refusal stops the server starting. So the upload is
// turned away here, while the server is up and can say why, instead of staging
// something that would keep birdy down at its next restart.
func TestStageRestoreRefusedWhenDatabaseIsSymlink(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.db")
	s := openStore(t, realPath)
	link := filepath.Join(dir, "birdy.db")
	if err := os.Symlink(realPath, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	m := NewManager(link, filepath.Join(dir, "snapshots"), 3)
	p, err := m.CreateTempSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(p)

	err = m.StageRestore(snap)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("StageRestore with a symlinked database: err=%v", err)
	}
	if m.HasPendingRestore() {
		t.Fatal("a restore that can never be applied was staged")
	}
}

// A restore replaces the database file, and must not quietly change who can
// read it: an install that shares the database with a group (a backup job,
// birdy:birdy 0640) keeps that after restoring. Access for "other" is never
// carried over.
func TestRestoreKeepsDatabaseGroupAccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	for _, tc := range []struct{ before, after os.FileMode }{{0o640, 0o640}, {0o600, 0o600}, {0o664, 0o660}} {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "birdy.db")
		s := openStore(t, dbPath)
		m := NewManager(dbPath, filepath.Join(dir, "snapshots"), 3)
		p, err := m.CreateTempSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		os.Remove(p)
		if err := m.StageRestore(snap); err != nil {
			t.Fatal(err)
		}
		s.Close()
		if err := os.Chmod(dbPath, tc.before); err != nil {
			t.Fatal(err)
		}
		if err := ApplyPendingRestore(dbPath, nil); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != tc.after {
			t.Errorf("database was %o before the restore and is %o after, want %o", tc.before, got, tc.after)
		}
	}
}
