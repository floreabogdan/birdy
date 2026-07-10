package doctor

import (
	"path/filepath"
	"testing"
)

func TestCheckConfigDirWritable(t *testing.T) {
	dir := t.TempDir()
	r := checkConfigDir(Config{ConfigDir: dir})
	if r.Status != OK {
		t.Fatalf("got %v: %s", r.Status, r.Detail)
	}
}

func TestCheckConfigDirMissing(t *testing.T) {
	r := checkConfigDir(Config{ConfigDir: filepath.Join(t.TempDir(), "does-not-exist")})
	if r.Status != Warn {
		t.Fatalf("got %v: %s, want Warn", r.Status, r.Detail)
	}
}

func TestCheckDBDirCreatesAndWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "path")
	r := checkDBDir(Config{DBPath: filepath.Join(dir, "birdy.db")})
	if r.Status != OK {
		t.Fatalf("got %v: %s", r.Status, r.Detail)
	}
}

func TestCheckSocketUnreachable(t *testing.T) {
	r := checkSocket(Config{SocketPath: filepath.Join(t.TempDir(), "no-such.sock")})
	if r.Status != Fail {
		t.Fatalf("got %v: %s, want Fail", r.Status, r.Detail)
	}
}

func TestFailedHelper(t *testing.T) {
	if Failed([]Result{{Status: OK}, {Status: Warn}}) {
		t.Fatal("expected Failed=false with only OK/Warn")
	}
	if !Failed([]Result{{Status: OK}, {Status: Fail}}) {
		t.Fatal("expected Failed=true when a Fail is present")
	}
}

func TestApplyReadyMismatchIsFail(t *testing.T) {
	// No socket, so DaemonConfigPath can't be checked; with an unwritable path
	// and no BIRD, the check must warn, never crash.
	r := checkApplyReady(Config{BirdConfPath: "/nonexistent-dir/bird.conf", SocketPath: "/nonexistent.ctl"})
	if r.Status == OK {
		t.Errorf("apply readiness should not be OK when nothing is reachable: %+v", r)
	}
	if r.Name != "apply readiness" {
		t.Errorf("name = %q", r.Name)
	}
}

func TestApplyReadyNoPath(t *testing.T) {
	r := checkApplyReady(Config{})
	if r.Status != Warn {
		t.Errorf("no bird-conf path should warn, got %v", r.Status)
	}
}
