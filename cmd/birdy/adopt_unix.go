//go:build unix

package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// adoptDBOwnership hands a database created by root to the account birdy actually
// runs as, so "sudo birdy init" cannot leave a file the service can only read.
//
// This is the single sharpest edge in a packaged install: the deb/rpm creates
// /var/lib/birdy owned by `birdy` and a systemd unit with User=birdy, but init is
// a command a human runs, and a human on a router runs it as root. SQLite then
// opens the root-owned file read-only, birdy starts anyway, and the failure only
// surfaces on the first login — as "internal error", with the real reason buried
// in the journal. Rather than document the trap, close it.
//
// The target owner is whoever owns the directory the database sits in (the
// packages chown it to birdy), falling back to a `birdy` account if one exists.
// It is a no-op unless we are root: an unprivileged init already owns its files.
func adoptDBOwnership(dbPath string) {
	if os.Geteuid() != 0 {
		return
	}
	uid, gid, ok := serviceOwner(filepath.Dir(dbPath))
	if !ok {
		return
	}
	// The -wal and -shm siblings matter as much as the database: chown the file
	// alone and SQLite still cannot write, for the same reason and less obviously.
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := os.Chown(p, uid, gid); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not give %s to uid %d: %v\n", p, uid, err)
			continue
		}
	}
}

// serviceOwner is the uid/gid birdy's files should belong to: the owner of its
// state directory, or the `birdy` account. Root owning the directory tells us
// nothing, so that answer is refused.
func serviceOwner(dir string) (uid, gid int, ok bool) {
	if fi, err := os.Stat(dir); err == nil {
		if st, isUnix := fi.Sys().(*syscall.Stat_t); isUnix && st.Uid != 0 {
			return int(st.Uid), int(st.Gid), true
		}
	}
	u, err := user.Lookup("birdy")
	if err != nil {
		return 0, 0, false
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil || uid == 0 {
		return 0, 0, false
	}
	return uid, gid, true
}
