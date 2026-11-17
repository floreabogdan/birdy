package web

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func TestBackupBundleContents(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: sql.NullInt64{Int64: 65551, Valid: true},
		BirdSocketPath: "/x", ListenAddr: "y",
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/backup/download", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content-type=%q", ct)
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"MANIFEST.txt", "birdy.db", "bird.conf"} {
		if !names[want] {
			t.Errorf("backup bundle missing %q (has %v)", want, names)
		}
	}
}

// Confirming an apply mails an off-box, password-masked copy of the config.
func TestConfirmMailsMaskedConfig(t *testing.T) {
	fn := &fakeNotifier{}
	env := newTestEnv(t, false, func(c *Config) { c.Notifier = fn })
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: sql.NullInt64{Int64: 65551, Valid: true},
		BirdSocketPath: "/x", ListenAddr: "y",
	}); err != nil {
		t.Fatal(err)
	}
	// A peer with a password, so we can prove masking.
	form := peerForm()
	form.Set("password", "s3cr3t-md5")
	env.do(t, "POST", "/peers/new", form)

	env.do(t, "POST", "/apply", nil)
	env.do(t, "POST", "/apply/confirm", nil)

	if fn.mailed == "" {
		t.Fatal("confirm should mail the applied config")
	}
	if !contains(fn.mailed, "password") {
		t.Error("the mailed config should include the peer's password line")
	}
	if contains(fn.mailed, "s3cr3t-md5") {
		t.Error("the mailed config must be password-masked")
	}
}

func contains(hay, needle string) bool { return bytes.Contains([]byte(hay), []byte(needle)) }

// On-demand downloads take a fresh snapshot, but must not leave it in the
// snapshot directory: each one used to become a retained snapshot, so a few
// clicks pushed every nightly snapshot out of the retention window.
func TestDownloadsDoNotTouchRetainedSnapshots(t *testing.T) {
	env := newTestEnv(t, false)
	snapDir := filepath.Join(filepath.Dir(env.confPath), "snapshots")
	// The test manager retains 3; fill it the way the nightly job would.
	var nightly []string
	for range 3 {
		p, err := env.srv.snap.CreateSnapshot(env.store)
		if err != nil {
			t.Fatal(err)
		}
		nightly = append(nightly, filepath.Base(p))
	}

	for _, path := range []string{"/api/snapshot/download", "/api/backup/download", "/api/snapshot/download", "/api/backup/download"} {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(env.cookie)
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		if rec.Code != 200 || rec.Body.Len() == 0 {
			t.Fatalf("%s: code=%d len=%d", path, rec.Code, rec.Body.Len())
		}
		if path == "/api/snapshot/download" {
			if !bytes.HasPrefix(rec.Body.Bytes(), []byte("SQLite format 3\x00")) {
				t.Errorf("%s did not return a SQLite database", path)
			}
			// A snapshot-style name, not the temp file's.
			if cd := rec.Header().Get("Content-Disposition"); !regexp.MustCompile(`^attachment; filename="birdy-\d{8}-\d{6}\.db"$`).MatchString(cd) {
				t.Errorf("Content-Disposition = %q", cd)
			}
		}
	}

	entries, err := os.ReadDir(snapDir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(nightly, ",") {
		t.Errorf("after four downloads the snapshot dir holds %v, want exactly the nightly snapshots %v", got, nightly)
	}
}
