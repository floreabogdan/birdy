package web

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"net/http/httptest"
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
