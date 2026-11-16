// Package snapshot manages point-in-time copies of birdy's SQLite database:
// a nightly background snapshot, on-demand download, and upload/restore.
//
// Restore is staged rather than applied live: swapping the file underneath
// an open *sql.DB (especially in WAL mode, with -wal/-shm sidecar files)
// risks corruption, so an uploaded database is written next to the real one
// and only swapped in at the next process startup, before it's opened.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

const pendingRestoreSuffix = ".pending-restore"

// Retained snapshots are named birdy-YYYYMMDD-HHMMSS.db (UTC). A second one in
// the same second gets a two-digit _NN suffix: "_" sorts after ".", and the
// suffix is fixed-width, so a plain string sort of the names is still oldest
// first. The pattern is anchored at both ends — list and prune act on nothing
// else, so a file an operator (or anyone else) drops into the snapshot
// directory is never mistaken for a snapshot and never deleted.
var snapshotNameRE = regexp.MustCompile(`^birdy-\d{8}-\d{6}(_\d{2})?\.db$`)

// Work-in-progress and on-demand snapshots live under a name the pattern above
// cannot match, so they are invisible to list, prune and LatestSnapshot.
const (
	tempSnapshotPattern = ".birdy-snapshot-*.tmp"
	tempSnapshotPrefix  = ".birdy-snapshot-"
	tempSnapshotSuffix  = ".tmp"
	// A temporary snapshot only lives for the length of a download; one this old
	// was orphaned by a crash and is cleared by the next nightly run.
	tempSnapshotMaxAge = time.Hour
)

// FileName is the name of a snapshot taken at t, as used for retained
// snapshots and offered to the browser for downloaded ones.
func FileName(t time.Time) string {
	return fmt.Sprintf("birdy-%s.db", t.UTC().Format("20060102-150405"))
}

// DownloadName is the file name to offer a browser for a snapshot streamed
// right now — the retained-snapshot naming, since a temporary snapshot's own
// on-disk name is random.
func (m *Manager) DownloadName() string {
	return FileName(time.Now())
}

// DB is the subset of *store.Store the snapshot manager needs.
type DB interface {
	VacuumInto(path string) error
}

// Manager takes and prunes nightly SQLite snapshots of birdy's database, so the
// whole state (one file) can be downloaded off-box and restored.
type Manager struct {
	dbPath string
	dir    string
	retain int
}

// NewManager keeps the retain newest snapshots in dir. A retain below 1 is
// treated as 1: "keep nothing" would delete each snapshot the moment it was
// taken, and is never what an operator setting --snapshot-retain meant.
func NewManager(dbPath, dir string, retain int) *Manager {
	if retain < 1 {
		retain = 1
	}
	return &Manager{dbPath: dbPath, dir: dir, retain: retain}
}

// CreateSnapshot produces a fresh, consistent copy of the live database and
// returns its path, pruning older snapshots beyond the retention count.
func (m *Manager) CreateSnapshot(db DB) (string, error) {
	tmp, err := m.CreateTempSnapshot(db)
	if err != nil {
		return "", err
	}
	path, err := m.publish(tmp)
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	m.prune()
	m.removeStaleTemps()
	return path, nil
}

// CreateTempSnapshot produces a fresh, consistent copy of the live database
// that is NOT part of the retained set: it does not appear in LatestSnapshot,
// does not count toward retention and so never evicts a nightly snapshot. It is
// what an on-demand download streams; the caller removes the file when done.
//
// The file is created empty, exclusively and with mode 0600 before SQLite
// writes into it (VACUUM INTO accepts an existing empty file), so the copy —
// which holds everything the database does — is private from its first byte
// rather than world-readable until a chmod afterwards.
func (m *Manager) CreateTempSnapshot(db DB) (string, error) {
	if err := os.MkdirAll(m.dir, 0o750); err != nil {
		return "", fmt.Errorf("snapshot: mkdir: %w", err)
	}
	f, err := os.CreateTemp(m.dir, tempSnapshotPattern) // O_EXCL, mode 0600
	if err != nil {
		return "", fmt.Errorf("snapshot: create: %w", err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("snapshot: create: %w", err)
	}
	if err := db.VacuumInto(path); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// publish moves a finished temporary snapshot to its retained name. Only a
// complete snapshot ever carries a name the pattern matches. The name is
// claimed with O_EXCL first, so two snapshots in the same second (a nightly run
// racing a restart, or a short --snapshot-interval) take successive suffixes
// instead of one failing or overwriting the other.
//
// The suffix continues after the newest snapshot already taken this second
// rather than filling the first free slot: with a small retain the earlier
// names of the same second may have been pruned already, and reusing one would
// make the newest snapshot sort as the oldest — and be the next one pruned.
func (m *Manager) publish(tmp string) (string, error) {
	base := strings.TrimSuffix(FileName(time.Now()), ".db")
	first := 0
	for _, name := range m.list() {
		switch {
		case name == base+".db":
			first = max(first, 1)
		case strings.HasPrefix(name, base+"_"):
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, base+"_"), ".db")); err == nil {
				first = max(first, n+1)
			}
		}
	}
	for n := first; n < 100; n++ {
		name := base + ".db"
		if n > 0 {
			name = fmt.Sprintf("%s_%02d.db", base, n)
		}
		path := filepath.Join(m.dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("snapshot: reserve name: %w", err)
		}
		f.Close()
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(path)
			return "", fmt.Errorf("snapshot: publish: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("snapshot: too many snapshots in the same second (%s)", base)
}

// LatestSnapshot returns the most recent snapshot's path, if any.
func (m *Manager) LatestSnapshot() (string, bool) {
	names := m.list()
	if len(names) == 0 {
		return "", false
	}
	return filepath.Join(m.dir, names[len(names)-1]), true
}

// list returns birdy's own retained snapshots, oldest first — only regular
// files whose name matches snapshotNameRE.
func (m *Manager) list() []string {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && snapshotNameRE.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // timestamped names sort chronologically
	return names
}

func (m *Manager) prune() {
	names := m.list()
	for len(names) > m.retain {
		os.Remove(filepath.Join(m.dir, names[0]))
		names = names[1:]
	}
}

// removeStaleTemps clears temporary snapshots orphaned by a crash or a killed
// download — each is a full copy of the database that nothing would otherwise
// ever delete. Only birdy's own temp names, and only old ones: a download in
// progress right now is left alone.
func (m *Manager) removeStaleTemps() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, tempSnapshotPrefix) || !strings.HasSuffix(name, tempSnapshotSuffix) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > tempSnapshotMaxAge {
			os.Remove(filepath.Join(m.dir, name))
		}
	}
}

// RunNightly triggers a snapshot once per interval until ctx is cancelled.
func (m *Manager) RunNightly(ctx context.Context, db DB, interval time.Duration, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// select picks a ready case at random, so a hot ticker can win over
			// a fresh cancel; re-check before the slow snapshot so shutdown is
			// honoured promptly instead of after another full backup.
			select {
			case <-ctx.Done():
				return
			default:
			}
			if path, err := m.CreateSnapshot(db); err != nil {
				log.Warn("nightly snapshot failed", "error", err)
			} else {
				log.Info("nightly snapshot created", "path", path)
			}
		}
	}
}

// StageRestore checks that data is a database this birdy can run on and writes
// it next to dbPath for ApplyPendingRestore to pick up on next start.
//
// The SQLite magic alone proves nothing, so the upload is written to a private
// temp file in the database's directory, validated there by SQLite itself
// (store.ValidateRestoreFile: integrity, schema version, at least one user),
// and only then renamed to the pending-restore name. The rename is atomic: the
// name ApplyPendingRestore looks for is either absent or a complete, validated
// file — never a half-written or rejected one.
func (m *Manager) StageRestore(data []byte) error {
	if len(data) < 16 || string(data[:15]) != "SQLite format 3" {
		return fmt.Errorf("snapshot: not a valid SQLite database file")
	}
	f, err := os.CreateTemp(filepath.Dir(m.dbPath), filepath.Base(m.dbPath)+".restore-upload-*") // O_EXCL, mode 0600
	if err != nil {
		return fmt.Errorf("snapshot: stage restore: %w", err)
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		// On disk before it is given the name that makes it the next database.
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("snapshot: stage restore: %w", err)
	}
	if err := store.ValidateRestoreFile(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("snapshot: not a usable birdy database: %w", err)
	}
	if err := os.Rename(tmp, m.dbPath+pendingRestoreSuffix); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("snapshot: stage restore: %w", err)
	}
	return nil
}

// HasPendingRestore reports whether a staged restore is waiting to be applied.
func (m *Manager) HasPendingRestore() bool {
	_, err := os.Lstat(m.dbPath + pendingRestoreSuffix)
	return err == nil
}

// ApplyPendingRestore must be called before the database at dbPath is
// opened. If a staged restore is present, it backs up the current database
// and swaps the staged file in, discarding any stale WAL/SHM sidecar files.
//
// It runs wherever the database is about to be opened, which includes "birdy
// init" run by root in a directory the unprivileged service account owns. That
// account can put anything under these names, so nothing here follows a
// symlink: the staged file, the database and its -wal must each be a regular
// file or the restore is refused, backups are created exclusively, and the swap
// is a rename (which replaces a name, never writes through it).
func ApplyPendingRestore(dbPath string, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	pending := dbPath + pendingRestoreSuffix
	pendingInfo, err := os.Lstat(pending)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("snapshot: pending restore: %w", err)
	}
	if !pendingInfo.Mode().IsRegular() {
		return fmt.Errorf("snapshot: pending restore: refusing to use %s: it is not a regular file (%s)", pending, pendingInfo.Mode().Type())
	}
	walPath := dbPath + "-wal"

	hasDB, err := regularFile(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot: database to be replaced: %w", err)
	}
	if hasDB {
		// In WAL mode the most recent commits may exist only in the -wal file
		// (always, after an unclean stop). The backup is the way back from a
		// restore that turns out to be the wrong one, so it takes both: the two
		// copies, kept side by side under matching names, open as one database.
		hasWAL, err := regularFile(walPath)
		if err != nil {
			return fmt.Errorf("snapshot: database WAL: %w", err)
		}
		backup, err := copyToNewFile(dbPath, dbPath+".before-restore-"+time.Now().UTC().Format("20060102-150405"))
		if err != nil {
			return fmt.Errorf("snapshot: backup before restore: %w", err)
		}
		if hasWAL {
			if err := copyExclusive(walPath, backup+"-wal"); err != nil {
				return fmt.Errorf("snapshot: backup WAL before restore: %w", err)
			}
		}
		log.Info("backed up current database before restore", "path", backup)
	}

	// The old sidecars go BEFORE the new database arrives: a -wal left next to a
	// database it does not belong to can be replayed into it. If we stop between
	// the two steps the staged file is still there and the next start finishes
	// the job; the complete old database is already in the backup.
	for _, p := range []string{walPath, dbPath + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("snapshot: remove stale sidecar: %w", err)
		}
	}
	// Staged files are created 0600; tighten one that was not (staged by an older
	// birdy, or copied into place by hand) before it becomes the database.
	if pendingInfo.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(pending, 0o600); err != nil {
			return fmt.Errorf("snapshot: restrict restored database: %w", err)
		}
	}
	// The staged file is in the same directory, so this is the atomic
	// temp-file-and-rename: the database is the old one or the new one, never a
	// partly written mix, and the pending marker disappears in the same step.
	if err := os.Rename(pending, dbPath); err != nil {
		return fmt.Errorf("snapshot: swap in restored database: %w", err)
	}
	log.Info("applied pending database restore")
	return nil
}

// regularFile reports whether path exists, refusing (with an error) anything
// that is not a plain file — above all a symlink, which Lstat does not follow.
func regularFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("refusing to use %s: it is not a regular file (%s)", path, info.Mode().Type())
	}
	return true, nil
}

// copyToNewFile copies src to dst, or to dst-2, dst-3, … if that name is taken
// (two restores in one second), and returns the name used. An earlier backup is
// never overwritten.
func copyToNewFile(src, dst string) (string, error) {
	name := dst
	for n := 2; ; n++ {
		err := copyExclusive(src, name)
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, fs.ErrExist) || n > 100 {
			return "", err
		}
		name = fmt.Sprintf("%s-%d", dst, n)
	}
}

// copyExclusive copies src to a file it creates itself: O_EXCL fails on an
// existing name, including a symlink, so the copy can never land in a file
// someone else chose. The copy is private (0600) and synced before it counts.
func copyExclusive(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
	}
	return err
}
