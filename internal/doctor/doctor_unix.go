//go:build unix

package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// checkConfigReadable verifies that the user BIRD runs as can read the files
// birdy writes. birdy writes bird.conf and its birdy.d/ includes as 0640 files
// in a 0750 directory, so if birdy is neither BIRD's user nor in BIRD's group,
// the first apply produces a config BIRD physically cannot read — a confusing
// failure to hit for the first time on a live router. BIRD's identity is taken
// from its control socket, which BIRD owns; birdy's write identity is this
// process. All outcomes are advisory (nothing here matters in read-only mode).
func checkConfigReadable(cfg Config) Result {
	const name = "config readable by BIRD"

	sockUID, sockGID, ok := fileOwner(cfg.SocketPath)
	if !ok {
		return Result{name, Warn, "cannot stat the BIRD control socket to learn which user BIRD runs as; skipping the read check"}
	}

	dir := cfg.ConfigDir
	if dir == "" {
		dir = filepath.Dir(cfg.BirdConfPath)
	}
	if dir == "" || dir == "." {
		dir = "/etc/bird"
	}
	incDir := filepath.Join(dir, "birdy.d")

	euid := os.Geteuid()
	egid := os.Getegid()

	// A file birdy writes takes birdy's effective group, unless the directory is
	// setgid, in which case new files inherit the directory's group.
	writeGID := egid
	if _, dirGID, dirMode, ok := fileOwnerMode(dir); ok && dirMode&os.ModeSetgid != 0 {
		writeGID = dirGID
	}

	switch {
	case euid == sockUID:
		return Result{name, OK, fmt.Sprintf("birdy runs as BIRD's user (uid %d), so BIRD reads its 0640 files as owner", euid)}
	case writeGID == sockGID:
		return Result{name, OK, fmt.Sprintf("birdy writes group %d, which is BIRD's group, so its 0640 files are group-readable by BIRD", writeGID)}
	default:
		return Result{name, Warn, fmt.Sprintf(
			"birdy writes as uid %d / gid %d but BIRD runs as uid %d / gid %d — BIRD may not be able to read the 0640 files under %s. Run birdy as BIRD's user, add birdy to BIRD's group, or make %s setgid to that group.",
			euid, writeGID, sockUID, sockGID, incDir, dir)}
	}
}

func fileOwner(path string) (uid, gid int, ok bool) {
	uid, gid, _, ok = fileOwnerMode(path)
	return uid, gid, ok
}

func fileOwnerMode(path string) (uid, gid int, mode os.FileMode, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, false
	}
	return int(st.Uid), int(st.Gid), fi.Mode(), true
}
