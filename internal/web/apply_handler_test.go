package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/floreabogdan/birdy/internal/birdc"
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

func TestHistoryListsAppliedVersions(t *testing.T) {
	env := applyReady(t)
	env.do(t, "POST", "/apply", nil)
	env.do(t, "POST", "/apply/confirm", nil)

	body := env.do(t, "GET", "/changes/history", nil).Body.String()
	if !strings.Contains(body, "confirmed") {
		t.Error("a confirmed apply should appear in history")
	}

	// The single version's detail page shows the config, masked.
	versions, _ := env.store.ListConfigVersions(10)
	if len(versions) != 1 {
		t.Fatalf("want 1 version, got %d", len(versions))
	}
	body = env.do(t, "GET", "/changes/history/"+itoa(versions[0].ID), nil).Body.String()
	if !strings.Contains(body, "define BOGON_ASNS") {
		t.Error("the version page should show the applied config")
	}
	if !strings.Contains(body, "on disk now") {
		t.Error("the just-confirmed version should be marked as on disk")
	}
}

// Re-applying a stored version goes through the same timeout-armed pipeline.
func TestReapplyOldVersion(t *testing.T) {
	env := applyReady(t)
	env.do(t, "POST", "/apply", nil)
	env.do(t, "POST", "/apply/confirm", nil)
	v1, _ := env.store.ListConfigVersions(10)
	firstID := v1[0].ID

	// Change the model so a fresh apply differs from version 1, then apply it.
	f := peerForm()
	f.Set("name", "extra_peer")
	f.Set("neighborIp", "198.51.100.9")
	env.do(t, "POST", "/peers/new", f)
	env.do(t, "POST", "/apply", nil)
	env.do(t, "POST", "/apply/confirm", nil)

	// Now re-apply the ORIGINAL version 1 — emergency rollback.
	env.fc.calls = nil
	rec := env.do(t, "POST", "/changes/history/"+itoa(firstID)+"/reapply", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reapply: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(env.fc.calls, ","); got != "check,timeout" {
		t.Fatalf("reapply should run the pipeline, calls=%q", got)
	}
	pending, ok, _ := env.store.PendingConfigVersion()
	if !ok || strings.Contains(pending.ConfigText, "extra_peer") {
		t.Error("re-apply should stage the OLD config, without the later peer")
	}
}

func TestReapplyBlockedInReadOnly(t *testing.T) {
	env := applyReady(t)
	env.do(t, "POST", "/apply", nil)
	env.do(t, "POST", "/apply/confirm", nil)
	v, _ := env.store.ListConfigVersions(10)

	ro := newTestEnv(t, true) // fresh read-only env; just needs the route to 403
	_ = v
	rec := ro.do(t, "POST", "/changes/history/1/reapply", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("reapply in read-only: code=%d, want 403", rec.Code)
	}
}

func TestApplySoftByDefault(t *testing.T) {
	env := applyReady(t)
	// The button submits soft=on; a soft apply asks BIRD not to bounce sessions.
	env.do(t, "POST", "/apply", url.Values{"soft": {"on"}})
	if !env.fc.lastSoft {
		t.Error("apply with soft=on should request a soft reconfigure")
	}
}

func TestApplyHardWhenUnchecked(t *testing.T) {
	env := applyReady(t)
	env.do(t, "POST", "/apply", nil) // no soft field -> hard
	if env.fc.lastSoft {
		t.Error("apply without soft should be a hard reconfigure")
	}
}

func TestReapplyIsSoft(t *testing.T) {
	env := applyReady(t)
	env.do(t, "POST", "/apply", nil)
	env.do(t, "POST", "/apply/confirm", nil)
	v, _ := env.store.ListConfigVersions(10)

	// change the model and apply so the old version differs, then re-apply it
	f := peerForm()
	f.Set("name", "another")
	f.Set("neighborIp", "198.51.100.7")
	env.do(t, "POST", "/peers/new", f)
	env.do(t, "POST", "/apply", nil)
	env.do(t, "POST", "/apply/confirm", nil)

	env.do(t, "POST", "/changes/history/"+itoa(v[0].ID)+"/reapply", nil)
	if !env.fc.lastSoft {
		t.Error("emergency re-apply should default to soft")
	}
}

func TestPendingPanelShowsLiveSessions(t *testing.T) {
	env := applyReady(t)
	env.do(t, "POST", "/apply", nil)
	body := env.do(t, "GET", "/changes", nil).Body.String()
	// edge_v4 is Established in the fake client.
	if !strings.Contains(body, "edge_v4") || !strings.Contains(body, "Established") {
		t.Error("a pending apply should show live session states so the operator can judge it")
	}
}

// Apply, confirm and auto-revert reach alert destinations, not just the timeline.
func TestApplyEventsAreNotified(t *testing.T) {
	fn := &fakeNotifier{}
	env := newTestEnv(t, false, func(c *Config) { c.Notifier = fn })
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: sql.NullInt64{Int64: 65551, Valid: true},
		BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "127.0.0.1:8080",
	}); err != nil {
		t.Fatal(err)
	}

	env.do(t, "POST", "/apply", nil)
	env.do(t, "POST", "/apply/confirm", nil)

	var apply int
	for _, k := range fn.kinds {
		if k == store.EventConfigApply {
			apply++
		}
	}
	if apply < 2 { // one for the timeout-apply, one for the confirm
		t.Fatalf("apply and confirm should both notify, got kinds %v", fn.kinds)
	}
}

// Ten applies fired at once must produce exactly one pending version and one
// write, not ten interleaved ones.
func TestConcurrentAppliesSerialize(t *testing.T) {
	env := applyReady(t)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env.do(t, "POST", "/apply", nil)
		}()
	}
	wg.Wait()

	versions, err := env.store.ListConfigVersions(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Fatalf("concurrent applies created %d versions, want exactly 1", len(versions))
	}
	// BIRD's timeout-apply ran exactly once.
	var timeouts int
	for _, c := range env.fc.calls {
		if c == "timeout" {
			timeouts++
		}
	}
	if timeouts != 1 {
		t.Fatalf("configure timeout ran %d times, want 1: %v", timeouts, env.fc.calls)
	}
}

// The pending panel flags a session that was established at apply but dropped.
func TestPendingPanelFlagsRegression(t *testing.T) {
	env := applyReady(t) // edge_v4 is Established in the fake client
	env.do(t, "POST", "/apply", nil)

	// The baseline captured edge_v4 as established.
	v, _, _ := env.store.PendingConfigVersion()
	if !contains(v.BaselineSessions, "edge_v4") {
		t.Fatalf("baseline should include edge_v4, got %q", v.BaselineSessions)
	}

	// Now edge_v4 goes down in the fake client, and the poller re-snapshots.
	env.fc.protocols = []birdc.ProtocolSummary{{Name: "edge_v4", Proto: "BGP", State: "start", Info: "Active"}}
	env.srv.poller.Run(cancelledCtx())

	body := env.do(t, "GET", "/changes", nil).Body.String()
	if !contains(body, "regressed since you applied") {
		t.Error("a dropped baseline session should be flagged on the pending panel")
	}
}

func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
