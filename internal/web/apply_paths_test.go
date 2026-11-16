package web

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

// The tests in this file stand in for a database restored from a crafted backup:
// the config_versions rows say things birdy itself would never have written.

// setVersionColumn overwrites one text column of a stored config version behind
// the store's back. The value travels as a hex blob so no quoting in it matters.
func setVersionColumn(t *testing.T, env *testEnv, id int64, column, value string) {
	t.Helper()
	sabotageDB(t, env, fmt.Sprintf("UPDATE config_versions SET %s = CAST(X'%s' AS TEXT) WHERE id = %d",
		column, hex.EncodeToString([]byte(value)), id))
}

// tamperFileSet rewrites a stored version's file set.
func tamperFileSet(t *testing.T, env *testEnv, v store.ConfigVersion, change func(*birdconf.FileSet)) {
	t.Helper()
	var set birdconf.FileSet
	if err := json.Unmarshal([]byte(v.ConfigFiles), &set); err != nil {
		t.Fatal(err)
	}
	change(&set)
	out, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	setVersionColumn(t, env, v.ID, "config_files", string(out))
}

// twoConfirmedVersions applies and confirms twice, so version one differs from
// what is on disk and can be re-applied. It returns version one.
func twoConfirmedVersions(t *testing.T, env *testEnv) store.ConfigVersion {
	t.Helper()
	env.do(t, "POST", "/apply", nil)
	env.do(t, "POST", "/apply/confirm", nil)
	f := peerForm()
	f.Set("name", "extra_peer")
	f.Set("neighborIp", "198.51.100.9")
	env.do(t, "POST", "/peers/new", f)
	env.do(t, "POST", "/apply", nil)
	env.do(t, "POST", "/apply/confirm", nil)
	versions, err := env.store.ListConfigVersions(10)
	if err != nil || len(versions) != 2 {
		t.Fatalf("want 2 versions, got %d (%v)", len(versions), err)
	}
	first := versions[len(versions)-1]
	if first.ID > versions[0].ID {
		first = versions[0]
	}
	env.fc.calls = nil
	return first
}

// A stored version's include names become paths. One that climbs out of the
// config directory must stop the re-apply before anything is written.
func TestReapplyRefusesIncludeOutsideBirdyD(t *testing.T) {
	env := applyReady(t)
	first := twoConfirmedVersions(t, env)
	dir := filepath.Dir(env.confPath)
	before := readFile(t, env.confPath)

	tamperFileSet(t, env, first, func(set *birdconf.FileSet) {
		set.Includes[0].Name = "../escaped/00-header.conf"
		set.Includes[1].Name = "birdy.d/sub/01-globals.conf"
	})
	rec := env.do(t, "POST", "/changes/history/"+itoa(first.ID)+"/reapply", nil)

	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped")); !os.IsNotExist(err) {
		t.Errorf("the re-apply wrote outside the config directory (stat err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "birdy.d", "sub")); !os.IsNotExist(err) {
		t.Errorf("the re-apply created a subdirectory under birdy.d (stat err %v)", err)
	}
	if got := readFile(t, env.confPath); got != before {
		t.Error("bird.conf changed although the file set was refused")
	}
	if len(env.fc.calls) != 0 {
		t.Errorf("BIRD must not hear of a refused file set, calls=%v", env.fc.calls)
	}
	if _, ok, _ := env.store.PendingConfigVersion(); ok {
		t.Error("a refused re-apply must not leave a pending version")
	}
	if loc := flashOf(rec); !strings.Contains(loc, "cannot be re-applied") {
		t.Errorf("the operator should be told why, flash=%q", loc)
	}
}

// The stored config text is what bird -p checks and what the operator reads; the
// file set is what gets written. A set that says something else is refused.
func TestReapplyRefusesFileSetThatDiffersFromItsText(t *testing.T) {
	cases := map[string]func(*birdconf.FileSet){
		"body":  func(set *birdconf.FileSet) { set.Includes[1].Body += "protocol static smuggled { ipv4; }\n" },
		"entry": func(set *birdconf.FileSet) { set.Entry += "include \"/etc/hostname\";\n" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			env := applyReady(t)
			first := twoConfirmedVersions(t, env)
			before := readFile(t, env.confPath)
			tamperFileSet(t, env, first, change)

			env.do(t, "POST", "/changes/history/"+itoa(first.ID)+"/reapply", nil)
			if len(env.fc.calls) != 0 {
				t.Errorf("BIRD must not hear of a refused file set, calls=%v", env.fc.calls)
			}
			if got := readFile(t, env.confPath); got != before {
				t.Error("bird.conf changed although the file set was refused")
			}
			if _, ok, _ := env.store.PendingConfigVersion(); ok {
				t.Error("a refused re-apply must not leave a pending version")
			}
		})
	}
}

// A pending apply is confirmed by writing its stored file set. Tampered with
// while pending, it must not be written, BIRD must not be told to keep it, and
// the apply must stay pending so the operator can roll it back.
func TestConfirmRefusesIncludeOutsideBirdyD(t *testing.T) {
	env := applyReady(t)
	prev := ownedPrev(t, env)
	env.do(t, "POST", "/apply", nil)
	pending, ok, _ := env.store.PendingConfigVersion()
	if !ok {
		t.Fatal("apply should leave a pending version")
	}
	dir := filepath.Dir(env.confPath)
	tamperFileSet(t, env, pending, func(set *birdconf.FileSet) {
		set.Includes[0].Name = "../escaped.conf"
	})
	env.fc.calls = nil

	rec := env.do(t, "POST", "/apply/confirm", nil)
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.conf")); !os.IsNotExist(err) {
		t.Errorf("confirm wrote outside the config directory (stat err %v)", err)
	}
	if data, _ := os.ReadFile(env.confPath); string(data) != string(prev) {
		t.Errorf("bird.conf must still be the previous config, got %q", data)
	}
	if len(env.fc.calls) != 0 {
		t.Errorf("BIRD must not be asked to confirm, calls=%v", env.fc.calls)
	}
	if _, ok, _ := env.store.PendingConfigVersion(); !ok {
		t.Error("the apply must stay pending so it can be rolled back")
	}
	if loc := flashOf(rec); !strings.Contains(loc, "Roll back") {
		t.Errorf("the operator should be pointed at rollback, flash=%q", loc)
	}
	// And rolling back still works.
	env.do(t, "POST", "/apply/rollback", nil)
	if _, ok, _ := env.store.PendingConfigVersion(); ok {
		t.Error("rollback should clear the refused apply")
	}
}

// writeFileSet is the last line of defence: whatever reaches it, no include
// lands outside birdy.d, and a refused set writes nothing at all.
func TestWriteFileSetRefusesBadNamesBeforeWriting(t *testing.T) {
	env := applyReady(t)
	dir := filepath.Dir(env.confPath)
	err := env.srv.writeFileSet(birdconf.FileSet{
		Entry: "# entry\n",
		Includes: []birdconf.File{
			{Name: "birdy.d/00-header.conf", Body: "# fine\n"},
			{Name: "../../var/lib/birdy/x", Body: "# not fine\n"},
		},
	})
	if err == nil {
		t.Fatal("writeFileSet accepted an include outside birdy.d")
	}
	if _, err := os.Stat(env.confPath); !os.IsNotExist(err) {
		t.Error("bird.conf was written although the set was refused")
	}
	if _, err := os.Stat(filepath.Join(dir, "birdy.d", "00-header.conf")); !os.IsNotExist(err) {
		t.Error("the valid include ahead of the bad one was written; the whole set must be refused first")
	}
}

// A version stored before the split layout has no file set: its text is written
// as bird.conf alone. That must keep working.
func TestReapplyLegacySingleFileVersion(t *testing.T) {
	env := applyReady(t)
	first := twoConfirmedVersions(t, env)
	setVersionColumn(t, env, first.ID, "config_files", "")

	env.do(t, "POST", "/changes/history/"+itoa(first.ID)+"/reapply", nil)
	if _, ok, _ := env.store.PendingConfigVersion(); !ok {
		t.Fatal("a single-file version should re-apply")
	}
	env.do(t, "POST", "/apply/confirm", nil)
	if got := readFile(t, env.confPath); got != first.ConfigText {
		t.Error("bird.conf should be the version's config text")
	}
	if entries, _ := os.ReadDir(filepath.Join(filepath.Dir(env.confPath), "birdy.d")); len(entries) != 0 {
		t.Errorf("a single-file version leaves birdy.d empty, got %d entries", len(entries))
	}
}

// The backup a pending apply names is read and copied over bird.conf when the
// apply is reverted. Pointed anywhere but birdy's own backup directory, it must
// be refused — and bird.conf left alone — however the revert is reached.
func TestRevertRefusesBackupOutsideBackupDir(t *testing.T) {
	type revert func(t *testing.T, env *testEnv)
	rollback := func(t *testing.T, env *testEnv) { env.do(t, "POST", "/apply/rollback", nil) }
	timeout := func(t *testing.T, env *testEnv) {
		setPendingDeadline(t, env, time.Now().Add(-revertGrace-time.Second))
		env.do(t, "GET", "/changes", nil)
	}
	confirmNothing := func(t *testing.T, env *testEnv) {
		env.fc.cfgConfirmNothing = true
		env.do(t, "POST", "/apply/confirm", nil)
	}
	for name, how := range map[string]revert{"rollback": rollback, "timeout": timeout, "confirm": confirmNothing} {
		for _, kind := range []string{"file", "dir", "dotdot"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				env := applyReady(t)
				prev := ownedPrev(t, env)
				env.do(t, "POST", "/apply", nil)
				pending, ok, _ := env.store.PendingConfigVersion()
				if !ok {
					t.Fatal("apply should leave a pending version")
				}

				// Something readable that is not a backup of birdy's.
				outside := t.TempDir()
				var path string
				switch kind {
				case "file":
					path = filepath.Join(outside, "secret")
					if err := os.WriteFile(path, []byte("root:secret\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "dir":
					path = outside
					if err := os.WriteFile(filepath.Join(path, "bird.conf"), []byte("root:secret\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "dotdot":
					// Lexically under the backup directory, really beside it.
					secret := filepath.Join(filepath.Dir(env.confPath), "bird.secret")
					if err := os.WriteFile(secret, []byte("root:secret\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					path = filepath.Join(filepath.Dir(env.confPath), "bird-backups") + "/../bird.secret"
				}
				setVersionColumn(t, env, pending.ID, "backup_path", path)

				env.fc.calls = nil
				how(t, env)
				if data, _ := os.ReadFile(env.confPath); strings.Contains(string(data), "secret") {
					t.Fatalf("bird.conf now holds the contents of %s", path)
				}
				// While pending, the previous config is what is on disk; a refused
				// restore leaves exactly that.
				if data, _ := os.ReadFile(env.confPath); string(data) != string(prev) {
					t.Errorf("bird.conf must be untouched, got %q", data)
				}
				v, err := env.store.GetConfigVersion(pending.ID)
				if err != nil {
					t.Fatal(err)
				}
				if name == "confirm" {
					// Confirm writes the new files first and falls back on the backup
					// if BIRD will not keep them — so with no usable backup it must
					// not start: nothing written, BIRD not asked, still pending.
					if v.Status != store.ConfigPending || len(env.fc.calls) != 0 {
						t.Errorf("confirm must refuse up front: status=%q calls=%v", v.Status, env.fc.calls)
					}
					return
				}
				// BIRD did revert; that is recorded even though the files could not
				// be restored from the path the row named.
				if v.Status != store.ConfigReverted {
					t.Errorf("the apply should still be recorded as reverted, status=%q", v.Status)
				}
			})
		}
	}
}

// What 0.7.0 and its predecessors stored as a backup path keeps working: a
// snapshot directory under the backup directory, an old single .bak file there,
// and the empty path that means "there was no config".
func TestRevertAcceptsBirdysOwnBackups(t *testing.T) {
	t.Run("snapshot directory", func(t *testing.T) {
		env := applyReady(t)
		prev := ownedPrev(t, env)
		env.do(t, "POST", "/apply", nil)
		// The post-arm restore already put prev back; disturb it so the rollback
		// has to restore from the backup for the test to pass.
		if err := os.WriteFile(env.confPath, []byte("# disturbed\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		env.do(t, "POST", "/apply/rollback", nil)
		if data, _ := os.ReadFile(env.confPath); string(data) != string(prev) {
			t.Errorf("rollback should restore from the snapshot directory, got %q", data)
		}
	})
	t.Run("legacy .bak file", func(t *testing.T) {
		env := applyReady(t)
		ownedPrev(t, env)
		env.do(t, "POST", "/apply", nil)
		pending, _, _ := env.store.PendingConfigVersion()
		bak := filepath.Join(filepath.Dir(env.confPath), "bird-backups", "bird.conf.20250101T000000Z.bak")
		if err := os.WriteFile(bak, []byte("# from the .bak\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		setVersionColumn(t, env, pending.ID, "backup_path", bak)
		env.do(t, "POST", "/apply/rollback", nil)
		if data, _ := os.ReadFile(env.confPath); string(data) != "# from the .bak\n" {
			t.Errorf("rollback should restore the legacy single-file backup, got %q", data)
		}
	})
	t.Run("no previous config", func(t *testing.T) {
		env := applyReady(t)
		env.do(t, "POST", "/apply", nil)
		if err := os.WriteFile(env.confPath, []byte("# left behind\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		env.do(t, "POST", "/apply/rollback", nil)
		if _, err := os.Stat(env.confPath); !os.IsNotExist(err) {
			t.Errorf("an empty backup path means no config: bird.conf should be gone (stat err %v)", err)
		}
	})
}

// A read-only birdy views the router; it does not write BIRD's files or send
// BIRD commands. Catching up with an auto-revert is bookkeeping it may still do,
// but the file restore and the reload that normally go with it are not.
func TestReconcileInReadOnlyTouchesNothing(t *testing.T) {
	env := newTestEnv(t, true)
	// An apply made by a read-write birdy on the same database, never confirmed.
	// Its backup path is empty ("there was no config"), so a restore would delete
	// bird.conf — easy to see.
	if err := os.WriteFile(env.confPath, []byte("# on disk\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	id, err := env.store.CreateConfigVersion(store.ConfigVersion{
		SHA256: hashBytes([]byte("# armed\n")), ConfigText: "# armed\n",
		Status: store.ConfigPending, Deadline: time.Now().Add(-revertGrace - time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	env.do(t, "GET", "/changes", nil)

	if data, err := os.ReadFile(env.confPath); err != nil || string(data) != "# on disk\n" {
		t.Errorf("read-only birdy rewrote bird.conf: %q, %v", data, err)
	}
	if len(env.fc.calls) != 0 {
		t.Errorf("read-only birdy sent BIRD %v", env.fc.calls)
	}
	v, err := env.store.GetConfigVersion(id)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != store.ConfigReverted {
		t.Fatalf("the expired apply should be recorded as reverted, status=%q", v.Status)
	}
	if !strings.Contains(v.Message, "read-only") {
		t.Errorf("the record should say the files were not restored and why, message=%q", v.Message)
	}
}

// A config that is renamed into place but never flushed can come back empty or
// truncated after a power cut, and BIRD then fails to start. Both the file and
// the directory entry that names it are synced.
func TestAtomicWriteFileSyncsFileAndDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bird.conf")
	var synced []string
	real := syncFile
	syncFile = func(f *os.File) error {
		synced = append(synced, f.Name())
		return real(f)
	}
	t.Cleanup(func() { syncFile = real })

	if err := atomicWriteFile(path, "router id 192.0.2.1;\n", 0o640); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "router id 192.0.2.1;\n" {
		t.Errorf("content = %q", got)
	}
	if len(synced) != 2 {
		t.Fatalf("synced %v, want the temp file and then its directory", synced)
	}
	if filepath.Dir(synced[0]) != dir || !strings.HasPrefix(filepath.Base(synced[0]), ".birdy-tmp-") {
		t.Errorf("first sync was %q, want the temp file", synced[0])
	}
	if synced[1] != dir {
		t.Errorf("second sync was %q, want the directory %q", synced[1], dir)
	}

	// A directory that cannot be synced (some filesystems refuse) is not a
	// failed write: the rename already happened.
	syncFile = func(f *os.File) error {
		if f.Name() == dir {
			return os.ErrInvalid
		}
		return real(f)
	}
	if err := atomicWriteFile(path, "v2\n", 0o640); err != nil {
		t.Errorf("a failed directory sync must not fail the write: %v", err)
	}
	// A file that cannot be synced is: the content may not be durable, and the
	// old file must still be the one in place.
	syncFile = func(f *os.File) error {
		if f.Name() != dir {
			return os.ErrInvalid
		}
		return nil
	}
	if err := atomicWriteFile(path, "v3\n", 0o640); err == nil {
		t.Error("a failed file sync must fail the write")
	}
	if got := readFile(t, path); got != "v2\n" {
		t.Errorf("after a failed write the old content must remain, got %q", got)
	}
}
