// Package doctor implements birdy's preflight checks (`birdy doctor`): is
// BIRD reachable, is the config directory writable, is the service healthy.
// Every check is independent and best-effort — one failing check should
// never prevent the others from running and reporting.
package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
)

type Status int

const (
	OK Status = iota
	Warn
	Fail
)

func (s Status) String() string {
	switch s {
	case OK:
		return "OK"
	case Warn:
		return "WARN"
	default:
		return "FAIL"
	}
}

type Result struct {
	Name   string
	Status Status
	Detail string
}

type Config struct {
	SocketPath   string // BIRD control socket, e.g. /run/bird/bird.ctl
	ConfigDir    string // e.g. /etc/bird — only needs to be writable from M2 onward
	BirdConfPath string // the bird.conf birdy reads and (unless read-only) writes
	BirdBinary   string // e.g. "bird" (resolved via PATH) or an absolute path
	DBPath       string // birdy's own SQLite file
	SystemdUnit  string // e.g. "bird" — the unit name BIRD runs under
}

// Run executes every check and returns all results, regardless of individual
// failures.
func Run(cfg Config) []Result {
	return []Result{
		checkBirdBinary(cfg),
		checkSocket(cfg),
		checkConfigDir(cfg),
		checkApplyReady(cfg),
		checkConfigReadable(cfg),
		checkSystemd(cfg),
		checkDBDir(cfg),
	}
}

// Failed reports whether any result is a hard failure.
func Failed(results []Result) bool {
	for _, r := range results {
		if r.Status == Fail {
			return true
		}
	}
	return false
}

func checkBirdBinary(cfg Config) Result {
	bin := cfg.BirdBinary
	if bin == "" {
		bin = "bird"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return Result{"bird binary", Fail, fmt.Sprintf("%q not found in PATH: %v", bin, err)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return Result{"bird binary", Warn, fmt.Sprintf("found at %s but --version failed: %v", path, err)}
	}
	return Result{"bird binary", OK, fmt.Sprintf("%s (%s)", path, strings.TrimSpace(string(out)))}
}

func checkSocket(cfg Config) Result {
	c, err := birdc.Dial(cfg.SocketPath, 3*time.Second)
	if err != nil {
		return Result{"control socket", Fail, fmt.Sprintf("%s: %v", cfg.SocketPath, err)}
	}
	defer c.Close()
	st, err := c.Status()
	if err != nil {
		return Result{"control socket", Fail, fmt.Sprintf("connected but \"show status\" failed: %v", err)}
	}
	return Result{"control socket", OK, fmt.Sprintf("BIRD %s, router id %s", st.Version, st.RouterID)}
}

func checkConfigDir(cfg Config) Result {
	dir := cfg.ConfigDir
	if dir == "" {
		dir = "/etc/bird"
	}
	info, err := os.Stat(dir)
	if err != nil {
		return Result{"config directory", Warn, fmt.Sprintf("%s: %v (not needed until birdy manages config)", dir, err)}
	}
	if !info.IsDir() {
		return Result{"config directory", Fail, fmt.Sprintf("%s is not a directory", dir)}
	}
	probe := filepath.Join(dir, ".birdy-write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0o640); err != nil {
		return Result{"config directory", Warn, fmt.Sprintf("%s exists but is not writable: %v (fine in read-only mode)", dir, err)}
	}
	os.Remove(probe)
	return Result{"config directory", OK, dir + " is writable"}
}

// checkApplyReady tells the operator whether birdy could apply a config, and
// catches the quiet footgun: if --bird-conf is not the file BIRD actually loads,
// an apply would reconfigure the wrong file. All findings are warnings — none of
// this matters in read-only mode.
func checkApplyReady(cfg Config) Result {
	path := cfg.BirdConfPath
	if path == "" {
		return Result{"apply readiness", Warn, "no bird.conf path configured"}
	}

	// The directory must be writable for the atomic temp-file-and-rename write.
	dir := filepath.Dir(path)
	probe := filepath.Join(dir, ".birdy-apply-test")
	writable := true
	if err := os.WriteFile(probe, []byte("ok"), 0o640); err != nil {
		writable = false
	} else {
		os.Remove(probe)
	}

	c, err := birdc.Dial(cfg.SocketPath, 3*time.Second)
	if err != nil {
		if !writable {
			return Result{"apply readiness", Warn, fmt.Sprintf("%s is not writable and BIRD is unreachable; apply needs both (fine in read-only mode)", path)}
		}
		return Result{"apply readiness", Warn, "cannot reach BIRD to confirm its config path"}
	}
	defer c.Close()

	daemonPath, err := c.DaemonConfigPath()
	switch {
	case err != nil:
		return Result{"apply readiness", Warn, "could not read BIRD's config path: " + err.Error()}
	case daemonPath != path:
		return Result{"apply readiness", Fail, fmt.Sprintf("--bird-conf is %s but BIRD loads %s — apply would reconfigure the wrong file", path, daemonPath)}
	case !writable:
		return Result{"apply readiness", Warn, fmt.Sprintf("%s matches BIRD, but is not writable by birdy; grant write access to enable apply (fine in read-only mode)", path)}
	default:
		return Result{"apply readiness", OK, fmt.Sprintf("%s is writable and is the file BIRD loads", path)}
	}
}

func checkSystemd(cfg Config) Result {
	unit := cfg.SystemdUnit
	if unit == "" {
		unit = "bird"
	}
	path, err := exec.LookPath("systemctl")
	if err != nil {
		return Result{"bird service", Warn, "systemctl not found (not a systemd host?)"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "is-active", unit).Output()
	state := strings.TrimSpace(string(out))
	if state == "active" {
		return Result{"bird service", OK, fmt.Sprintf("systemd unit %q is active", unit)}
	}
	if err != nil && state == "" {
		state = "unknown"
	}
	return Result{"bird service", Warn, fmt.Sprintf("systemd unit %q is %s", unit, state)}
}

func checkDBDir(cfg Config) Result {
	dir := filepath.Dir(cfg.DBPath)
	if dir == "" || dir == "." {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Result{"database path", Fail, fmt.Sprintf("cannot create %s: %v", dir, err)}
	}
	probe := filepath.Join(dir, ".birdy-write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0o640); err != nil {
		return Result{"database path", Fail, fmt.Sprintf("%s is not writable: %v", dir, err)}
	}
	os.Remove(probe)
	return Result{"database path", OK, dir + " is writable"}
}
