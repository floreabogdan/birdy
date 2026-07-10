package web

import (
	"database/sql"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// applyReady sets a router identity so a config can render, and returns the env.
// bird.conf does not exist yet, so authorship is "absent" — a clean slate birdy
// may apply without adopting.
func applyReady(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: sql.NullInt64{Int64: 65551, Valid: true},
		BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "127.0.0.1:8080",
	}); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestApplyWritesAndArmsTimeout(t *testing.T) {
	env := applyReady(t)

	rec := env.do(t, "POST", "/apply", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("apply: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// BIRD was asked to check then apply, in that order.
	if got := strings.Join(env.fc.calls, ","); got != "check,timeout" {
		t.Fatalf("configure call sequence = %q, want check,timeout", got)
	}

	// A pending version now exists, holding the rendered config.
	pending, ok, err := env.store.PendingConfigVersion()
	if err != nil || !ok {
		t.Fatalf("expected a pending version: %v %v", ok, err)
	}
	if !strings.Contains(pending.ConfigText, "define BOGON_ASNS") {
		t.Error("the pending version should hold the rendered config")
	}

	// The trick: the file on disk is the PREVIOUS state (here, no file), not the
	// armed config — so a timeout revert leaves disk and daemon consistent.
	if _, err := os.Stat(env.confPath); !os.IsNotExist(err) {
		t.Error("bird.conf should be restored to its previous (absent) state while pending")
	}

	// The applied hash is not set until confirm.
	st, _, _ := env.store.GetSettings()
	if st.AppliedConfigHash != "" {
		t.Error("applied hash must not be set for a merely-pending apply")
	}
}

func TestApplyConfirmKeepsConfig(t *testing.T) {
	env := applyReady(t)
	env.do(t, "POST", "/apply", nil)

	rec := env.do(t, "POST", "/apply/confirm", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if env.fc.calls[len(env.fc.calls)-1] != "confirm" {
		t.Fatalf("last call = %q, want confirm", env.fc.calls[len(env.fc.calls)-1])
	}

	// After confirm, the new config is on disk and birdy owns it.
	data, err := os.ReadFile(env.confPath)
	if err != nil || !strings.Contains(string(data), "define BOGON_ASNS") {
		t.Fatalf("confirmed config not on disk: %v", err)
	}
	st, _, _ := env.store.GetSettings()
	if st.AppliedConfigHash != hashBytes(data) {
		t.Error("applied hash should match the on-disk file after confirm")
	}
	if _, ok, _ := env.store.PendingConfigVersion(); ok {
		t.Error("no version should be pending after confirm")
	}
}

func TestApplyRollbackRevertsAndUndoes(t *testing.T) {
	env := applyReady(t)
	env.do(t, "POST", "/apply", nil)

	rec := env.do(t, "POST", "/apply/rollback", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rollback: code=%d", rec.Code)
	}
	if env.fc.calls[len(env.fc.calls)-1] != "undo" {
		t.Fatalf("rollback should call configure undo, calls=%v", env.fc.calls)
	}
	// Disk stays at the previous (absent) state; birdy never took ownership.
	if _, err := os.Stat(env.confPath); !os.IsNotExist(err) {
		t.Error("bird.conf should remain absent after rolling back a first apply")
	}
	st, _, _ := env.store.GetSettings()
	if st.AppliedConfigHash != "" {
		t.Error("rolled-back apply must not leave an applied hash")
	}
}

// A config BIRD rejects must never stick, and the file must be restored.
func TestApplyRolledBackWhenBirdRejects(t *testing.T) {
	env := applyReady(t)
	// Seed a config birdy owns, so we can prove it is restored intact.
	if err := os.WriteFile(env.confPath, []byte("# previously applied\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetAppliedConfigHash(hashBytes([]byte("# previously applied\n"))); err != nil {
		t.Fatal(err)
	}
	env.fc.cfgApplyFail = true // BIRD refuses the timeout-apply

	rec := env.do(t, "POST", "/apply", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	data, _ := os.ReadFile(env.confPath)
	if string(data) != "# previously applied\n" {
		t.Errorf("the previous config must be restored after a rejected apply, got %q", string(data))
	}
	if _, ok, _ := env.store.PendingConfigVersion(); ok {
		t.Error("a rejected apply must not leave a pending version")
	}
}

// The authorship guard: birdy must refuse to overwrite a config it did not write.
func TestApplyRefusesForeignConfig(t *testing.T) {
	env := applyReady(t)
	if err := os.WriteFile(env.confPath, []byte("# hand-written by a human\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// applied_config_hash is empty, so this file is foreign.

	rec := env.do(t, "POST", "/apply", nil)
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "adopt") {
		t.Fatalf("apply should refuse a foreign config, redirect=%q", loc)
	}
	if len(env.fc.calls) != 0 {
		t.Error("BIRD must not be touched when the guard refuses")
	}
	data, _ := os.ReadFile(env.confPath)
	if string(data) != "# hand-written by a human\n" {
		t.Error("the foreign config must be left untouched")
	}
}

// The changes page surfaces the foreign state as an adopt prompt.
func TestChangesShowsAdoptForForeignConfig(t *testing.T) {
	env := applyReady(t)
	if err := os.WriteFile(env.confPath, []byte("# hand-written\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	body := env.do(t, "GET", "/changes", nil).Body.String()
	if !strings.Contains(body, "has not adopted this router") {
		t.Error("a foreign config should prompt for adoption")
	}
}

func TestAdoptTakesOwnership(t *testing.T) {
	env := applyReady(t)
	foreign := []byte("# hand-written\n")
	if err := os.WriteFile(env.confPath, foreign, 0o640); err != nil {
		t.Fatal(err)
	}

	if rec := env.do(t, "POST", "/apply/adopt", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("adopt: code=%d", rec.Code)
	}
	st, _, _ := env.store.GetSettings()
	if st.AppliedConfigHash != hashBytes(foreign) {
		t.Error("adopt should record the on-disk hash as birdy's baseline")
	}
	// Now apply is allowed.
	if rec := env.do(t, "POST", "/apply", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("apply after adopt: code=%d", rec.Code)
	}
	if _, ok, _ := env.store.PendingConfigVersion(); !ok {
		t.Error("apply should proceed once the router is adopted")
	}
}

// Read-only is the hard boundary: every write endpoint must refuse.
func TestApplyEndpointsBlockedInReadOnly(t *testing.T) {
	env := newTestEnv(t, true)
	for _, path := range []string{"/apply", "/apply/confirm", "/apply/rollback", "/apply/adopt"} {
		rec := env.do(t, "POST", path, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s in read-only: code=%d, want 403", path, rec.Code)
		}
	}
	if len(env.fc.calls) != 0 {
		t.Error("read-only mode must never reach BIRD")
	}
}

// Only one armed reconfigure at a time — BIRD holds only one previous config.
func TestApplyRefusesSecondPending(t *testing.T) {
	env := applyReady(t)
	env.do(t, "POST", "/apply", nil)

	rec := env.do(t, "POST", "/apply", nil)
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "pending") {
		t.Fatalf("a second apply should be refused while one is pending, redirect=%q", loc)
	}
}

func TestApplyNoopWhenInSync(t *testing.T) {
	env := applyReady(t)
	// Apply and confirm, so the on-disk config matches the model.
	env.do(t, "POST", "/apply", nil)
	env.do(t, "POST", "/apply/confirm", nil)

	env.fc.calls = nil
	rec := env.do(t, "POST", "/apply", nil)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "Already+applied") {
		t.Fatalf("re-applying an unchanged config should be a no-op, redirect=%q", loc)
	}
	if len(env.fc.calls) != 0 {
		t.Error("an in-sync apply must not touch BIRD")
	}
}

func TestApplyConfirmRolledBackWhenBirdWontConfirm(t *testing.T) {
	env := applyReady(t)
	if err := os.WriteFile(env.confPath, []byte("# prev\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetAppliedConfigHash(hashBytes([]byte("# prev\n"))); err != nil {
		t.Fatal(err)
	}
	env.do(t, "POST", "/apply", nil)
	env.fc.cfgConfirmFail = true

	env.do(t, "POST", "/apply/confirm", nil)
	// Confirm failed, so the previous config is back and the hash is unchanged.
	data, _ := os.ReadFile(env.confPath)
	if string(data) != "# prev\n" {
		t.Errorf("a failed confirm must restore the previous config, got %q", string(data))
	}
	st, _, _ := env.store.GetSettings()
	if st.AppliedConfigHash != hashBytes([]byte("# prev\n")) {
		t.Error("a failed confirm must not advance the applied hash")
	}
}
