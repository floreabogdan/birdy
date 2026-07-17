//go:build unix

package doctor

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
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

	// Doctor is a command a human runs, and a human on a router runs it with sudo —
	// but the server runs as the `birdy` service account. Answering for root would
	// warn that "birdy writes as uid 0", which is both alarming and false. Judge the
	// account the service will actually run as, and say whose behalf we answered on.
	who := "birdy"
	if euid == 0 {
		if svc, ok := serviceIdentityFn(); ok {
			return serviceReadable(name, svc, sockUID, sockGID, incDir, dir)
		}
		who = "root"
	}

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
			"%s writes as uid %d / gid %d but BIRD runs as uid %d / gid %d — BIRD may not be able to read the 0640 files under %s. Run birdy as BIRD's user, add birdy to BIRD's group, or make %s setgid to that group.",
			who, euid, writeGID, sockUID, sockGID, incDir, dir)}
	}
}

// svcIdentity is the account the birdy service runs as, and every group it is in.
type svcIdentity struct {
	name   string
	uid    int
	groups []int
}

// serviceIdentityFn is replaceable in tests so doctor checks are independent of
// whether a real birdy account happens to exist on the machine running them.
var serviceIdentityFn = serviceIdentity

// serviceIdentity looks up the `birdy` account the packages create. Absent on a
// source install run by hand, in which case the caller judges the current process.
func serviceIdentity() (svcIdentity, bool) {
	u, err := user.Lookup("birdy")
	if err != nil {
		return svcIdentity{}, false
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return svcIdentity{}, false
	}
	svc := svcIdentity{name: u.Username, uid: uid}
	ids, err := u.GroupIds()
	if err != nil {
		return svcIdentity{}, false
	}
	for _, g := range ids {
		if n, err := strconv.Atoi(g); err == nil {
			svc.groups = append(svc.groups, n)
		}
	}
	return svc, true
}

// serviceReadable answers the read check for the service account rather than for
// whoever ran doctor. The packaged unit runs birdy with Group=bird, so what
// matters is that the account is a member of BIRD's group — then its 0640 files
// are group-readable by BIRD.
func serviceReadable(name string, svc svcIdentity, sockUID, sockGID int, incDir, dir string) Result {
	if svc.uid == sockUID {
		return Result{name, OK, fmt.Sprintf("the service runs as %s, which is BIRD's own user, so BIRD reads its 0640 files as owner", svc.name)}
	}
	for _, g := range svc.groups {
		if g == sockGID {
			return Result{name, OK, fmt.Sprintf(
				"the service runs as %s, a member of BIRD's group (gid %d), so its 0640 files are group-readable by BIRD (checked for %s, not for the user running doctor)",
				svc.name, sockGID, svc.name)}
		}
	}
	return Result{name, Warn, fmt.Sprintf(
		"the service runs as %s (uid %d), which is not BIRD's user (uid %d) and not in BIRD's group (gid %d) — BIRD could not read the 0640 files birdy writes under %s. Fix with: sudo usermod -aG %d %s && sudo systemctl restart birdy",
		svc.name, svc.uid, sockUID, sockGID, incDir, sockGID, svc.name)}
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
