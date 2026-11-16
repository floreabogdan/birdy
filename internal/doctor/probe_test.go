package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// "birdy doctor" is run as root, against directories other accounts can write
// to. Its write probes used fixed file names, so a symlink planted under one of
// them made root overwrite the link's target with "ok". The probe now uses a
// random name created exclusively; whatever sits under the old names is not
// touched, and nothing is left behind.
func TestWriteProbesDoNotFollowPlantedSymlinks(t *testing.T) {
	for name, check := range map[string]func(dir string) Result{
		".birdy-write-test (config dir)": func(dir string) Result { return checkConfigDir(Config{ConfigDir: dir}) },
		".birdy-apply-test": func(dir string) Result {
			return checkApplyReady(Config{BirdConfPath: filepath.Join(dir, "bird.conf"), SocketPath: filepath.Join(dir, "no.ctl")})
		},
		".birdy-write-test (db dir)": func(dir string) Result { return checkDBDir(Config{DBPath: filepath.Join(dir, "birdy.db")}) },
	} {
		dir := t.TempDir()
		victim := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(victim, []byte("important"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, strings.Fields(name)[0])
		if err := os.Symlink(victim, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		r := check(dir)
		if r.Status == Fail {
			t.Errorf("%s: %s", name, r.Detail)
		}
		if got, err := os.ReadFile(victim); err != nil || string(got) != "important" {
			t.Errorf("%s: the probe wrote through a planted symlink: %q %v", name, got, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != filepath.Base(link) {
			t.Errorf("%s: the probe changed the directory: %v", name, entries)
		}
	}
}

// A directory that cannot be written is still reported as such.
func TestWriteProbeReportsUnwritableDir(t *testing.T) {
	if err := probeWritable(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing directory probed as writable")
	}
	if err := probeWritable(t.TempDir()); err != nil {
		t.Fatalf("a temp dir probed as unwritable: %v", err)
	}
}
