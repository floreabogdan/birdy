package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

func TestCreateSnapshotAndPrune(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "birdy.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.InsertEvent(store.EventSessionUp, "edge_v4", "test"); err != nil {
		t.Fatal(err)
	}

	m := NewManager(dbPath, filepath.Join(dir, "snapshots"), 2)

	var paths []string
	for i := range 4 {
		p, err := m.CreateSnapshot(s)
		if err != nil {
			t.Fatalf("CreateSnapshot %d: %v", i, err)
		}
		paths = append(paths, p)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("snapshot file missing: %v", err)
		}
		if runtime.GOOS != "windows" {
			if info, err := os.Stat(p); err != nil {
				t.Fatal(err)
			} else if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("snapshot mode = %o, want 600", got)
			}
		}
		time.Sleep(1100 * time.Millisecond) // ensure distinct second-granularity filenames
	}

	names := m.list()
	if len(names) != 2 {
		t.Fatalf("expected pruning to retain 2 snapshots, got %d: %v", len(names), names)
	}
	// the two oldest should have been pruned
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("expected oldest snapshot to be pruned: %v", err)
	}
	if _, err := os.Stat(paths[1]); !os.IsNotExist(err) {
		t.Fatalf("expected second-oldest snapshot to be pruned: %v", err)
	}
	if _, err := os.Stat(paths[3]); err != nil {
		t.Fatalf("expected newest snapshot to survive: %v", err)
	}

	latest, ok := m.LatestSnapshot()
	if !ok || latest != paths[3] {
		t.Fatalf("LatestSnapshot = %q, %v; want %q, true", latest, ok, paths[3])
	}
}

func TestStageAndApplyRestore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "birdy.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertEvent(store.EventSessionUp, "original", "before restore"); err != nil {
		t.Fatal(err)
	}

	m := NewManager(dbPath, filepath.Join(dir, "snapshots"), 5)
	snapPath, err := m.CreateSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertEvent(store.EventSessionDown, "original", "after snapshot, should not survive restore"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	data, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}

	if m.HasPendingRestore() {
		t.Fatal("should have no pending restore before staging")
	}
	if err := m.StageRestore(data); err != nil {
		t.Fatal(err)
	}
	if !m.HasPendingRestore() {
		t.Fatal("expected pending restore after staging")
	}

	if err := ApplyPendingRestore(dbPath, nil); err != nil {
		t.Fatal(err)
	}
	if m.HasPendingRestore() {
		t.Fatal("pending restore marker should be gone after apply")
	}

	// a backup of the pre-restore db should exist
	matches, err := filepath.Glob(dbPath + ".before-restore-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one pre-restore backup file, got %v", matches)
	}

	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	events, err := s2.ListEvents(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Message != "before restore" {
		t.Fatalf("expected restored db to contain only the pre-snapshot event, got %+v", events)
	}
}

func TestRunNightlyStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "birdy.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	m := NewManager(dbPath, filepath.Join(dir, "snapshots"), 3)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.RunNightly(ctx, s, 10*time.Millisecond, nil)
		close(done)
	}()
	time.Sleep(35 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunNightly did not return after cancel")
	}
	if len(m.list()) == 0 {
		t.Fatal("expected at least one nightly snapshot to have been created")
	}
}
