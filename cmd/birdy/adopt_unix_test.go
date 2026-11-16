//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func ownerOf(t *testing.T, path string) uint32 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Uid
}

// adoptDBOwnership runs as root and gives files to the owner of the database
// directory — an account that can also put a symlink under any of the three
// names. chown through it would hand that account any file on the system.
func TestAdoptDBOwnershipDoesNotFollowSymlinks(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("adoptDBOwnership only acts when run as root")
	}
	const serviceUID = 4242
	dir := t.TempDir()
	if err := os.Chown(dir, serviceUID, serviceUID); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(victim, []byte("root only"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "birdy.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Symlink(victim, link); err != nil {
			t.Fatal(err)
		}
	}

	adoptDBOwnership(dbPath)

	if got := ownerOf(t, victim); got != 0 {
		t.Errorf("the symlink target was given to uid %d", got)
	}
	if got := ownerOf(t, dbPath); got != serviceUID {
		t.Errorf("the database itself is owned by uid %d, want %d", got, serviceUID)
	}

	// And the database name itself being the symlink.
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, dbPath); err != nil {
		t.Fatal(err)
	}
	adoptDBOwnership(dbPath)
	if got := ownerOf(t, victim); got != 0 {
		t.Errorf("the symlink target was given to uid %d via the database name", got)
	}
}
