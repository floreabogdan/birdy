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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const pendingRestoreSuffix = ".pending-restore"

// DB is the subset of *store.Store the snapshot manager needs.
type DB interface {
	VacuumInto(path string) error
}

type Manager struct {
	dbPath string
	dir    string
	retain int
}

func NewManager(dbPath, dir string, retain int) *Manager {
	return &Manager{dbPath: dbPath, dir: dir, retain: retain}
}

// CreateSnapshot produces a fresh, consistent copy of the live database and
// returns its path, pruning older snapshots beyond the retention count.
func (m *Manager) CreateSnapshot(db DB) (string, error) {
	if err := os.MkdirAll(m.dir, 0o750); err != nil {
		return "", fmt.Errorf("snapshot: mkdir: %w", err)
	}
	name := fmt.Sprintf("birdy-%s.db", time.Now().UTC().Format("20060102-150405"))
	path := filepath.Join(m.dir, name)
	if err := db.VacuumInto(path); err != nil {
		return "", err
	}
	m.prune()
	return path, nil
}

// LatestSnapshot returns the most recent snapshot's path, if any.
func (m *Manager) LatestSnapshot() (string, bool) {
	names := m.list()
	if len(names) == 0 {
		return "", false
	}
	return filepath.Join(m.dir, names[len(names)-1]), true
}

func (m *Manager) list() []string {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
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
			if path, err := m.CreateSnapshot(db); err != nil {
				log.Warn("nightly snapshot failed", "error", err)
			} else {
				log.Info("nightly snapshot created", "path", path)
			}
		}
	}
}

// StageRestore validates that data looks like a SQLite database file and
// writes it next to dbPath for ApplyPendingRestore to pick up on next start.
func (m *Manager) StageRestore(data []byte) error {
	if len(data) < 16 || string(data[:15]) != "SQLite format 3" {
		return fmt.Errorf("snapshot: not a valid SQLite database file")
	}
	if err := os.WriteFile(m.dbPath+pendingRestoreSuffix, data, 0o640); err != nil {
		return fmt.Errorf("snapshot: stage restore: %w", err)
	}
	return nil
}

// HasPendingRestore reports whether a staged restore is waiting to be applied.
func (m *Manager) HasPendingRestore() bool {
	_, err := os.Stat(m.dbPath + pendingRestoreSuffix)
	return err == nil
}

// ApplyPendingRestore must be called before the database at dbPath is
// opened. If a staged restore is present, it backs up the current database
// and swaps the staged file in, discarding any stale WAL/SHM sidecar files.
func ApplyPendingRestore(dbPath string, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	pending := dbPath + pendingRestoreSuffix
	data, err := os.ReadFile(pending)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("snapshot: read pending restore: %w", err)
	}

	if _, err := os.Stat(dbPath); err == nil {
		backup := dbPath + ".before-restore-" + time.Now().UTC().Format("20060102-150405")
		if err := copyFile(dbPath, backup); err != nil {
			return fmt.Errorf("snapshot: backup before restore: %w", err)
		}
		log.Info("backed up current database before restore", "path", backup)
	}

	if err := os.WriteFile(dbPath, data, 0o640); err != nil {
		return fmt.Errorf("snapshot: write restored database: %w", err)
	}
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")
	if err := os.Remove(pending); err != nil {
		log.Warn("failed to remove pending-restore marker", "error", err)
	}
	log.Info("applied pending database restore")
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o640)
}
