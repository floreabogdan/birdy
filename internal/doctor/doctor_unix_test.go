//go:build unix

package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

// When birdy runs as BIRD's own user (here, the test process owns the stand-in
// socket), BIRD reads birdy's 0640 files as their owner, so the check passes.
func TestConfigReadableSameUser(t *testing.T) {
	old := serviceIdentityFn
	serviceIdentityFn = func() (svcIdentity, bool) { return svcIdentity{}, false }
	t.Cleanup(func() { serviceIdentityFn = old })

	dir := t.TempDir()
	sock := filepath.Join(dir, "bird.ctl")
	if err := os.WriteFile(sock, []byte("x"), 0o660); err != nil {
		t.Fatal(err)
	}
	r := checkConfigReadable(Config{SocketPath: sock, ConfigDir: dir})
	if r.Name != "config readable by BIRD" {
		t.Fatalf("name = %q", r.Name)
	}
	if r.Status != OK {
		t.Fatalf("same-user check should be OK, got %v: %s", r.Status, r.Detail)
	}
}

// With no socket to identify BIRD, the check warns rather than crashing.
func TestConfigReadableNoSocket(t *testing.T) {
	r := checkConfigReadable(Config{SocketPath: filepath.Join(t.TempDir(), "nope.ctl"), ConfigDir: t.TempDir()})
	if r.Status != Warn {
		t.Fatalf("missing socket should warn, got %v: %s", r.Status, r.Detail)
	}
}
