package web

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

// driftEnv is a writable server with a settings row (so an applied hash can be
// recorded) and a notifier whose alerts the test can inspect.
func driftEnv(t *testing.T) (*testEnv, *fakeNotifier) {
	t.Helper()
	fn := &fakeNotifier{}
	env := newTestEnv(t, false, func(c *Config) { c.Notifier = fn })
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: sql.NullInt64{Int64: 65551, Valid: true},
		BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "127.0.0.1:8080",
	}); err != nil {
		t.Fatal(err)
	}
	return env, fn
}

func countKind(kinds []string, want string) int {
	n := 0
	for _, k := range kinds {
		if k == want {
			n++
		}
	}
	return n
}

// A config birdy owns, changed out of band, alerts exactly once; returning to
// sync re-arms it, and a permanent drift never spams.
func TestDriftDetectsOutOfBandChange(t *testing.T) {
	env, fn := driftEnv(t)

	applied := []byte("# applied by birdy\n")
	if err := os.WriteFile(env.confPath, applied, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetAppliedConfigHash(hashBytes(applied)); err != nil {
		t.Fatal(err)
	}

	var last string
	env.srv.checkDrift(&last)
	if len(fn.kinds) != 0 {
		t.Fatalf("an in-sync config must not alert, got %v", fn.kinds)
	}

	// Someone edits the file behind birdy's back.
	if err := os.WriteFile(env.confPath, []byte("# hand-edited\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	env.srv.checkDrift(&last)
	if countKind(fn.kinds, store.EventConfigDrift) != 1 {
		t.Fatalf("drift should alert once, kinds=%v", fn.kinds)
	}

	// The same standing drift must not re-alert.
	env.srv.checkDrift(&last)
	if countKind(fn.kinds, store.EventConfigDrift) != 1 {
		t.Errorf("a standing drift must not re-alert, kinds=%v", fn.kinds)
	}

	// It is also recorded on the timeline.
	events, _ := env.store.ListEvents(10, 0)
	var drift int
	for _, e := range events {
		if e.Kind == store.EventConfigDrift {
			drift++
		}
	}
	if drift != 1 {
		t.Errorf("drift should be recorded once on the timeline, got %d", drift)
	}

	// Put the config back the way birdy left it: drift clears and re-arms.
	if err := os.WriteFile(env.confPath, applied, 0o640); err != nil {
		t.Fatal(err)
	}
	env.srv.checkDrift(&last)
	if last != "" {
		t.Error("returning to sync should re-arm the detector")
	}
}

// birdy that owns nothing (never applied / read-only viewer) never cries drift,
// even when a config exists on disk.
func TestDriftSilentWhenBirdyOwnsNothing(t *testing.T) {
	env, fn := driftEnv(t)
	if err := os.WriteFile(env.confPath, []byte("# hand-written, never adopted\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// No applied hash set.
	var last string
	env.srv.checkDrift(&last)
	if len(fn.kinds) != 0 {
		t.Errorf("no applied hash means nothing to drift from, got %v", fn.kinds)
	}
}

// While an apply is armed, the previous config is on disk by design — not drift.
func TestDriftSilentDuringPendingApply(t *testing.T) {
	env, fn := driftEnv(t)
	if err := os.WriteFile(env.confPath, []byte("# the previous config\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetAppliedConfigHash(hashBytes([]byte("# a different applied config\n"))); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreateConfigVersion(store.ConfigVersion{
		SHA256: "pendinghash", Status: store.ConfigPending, Deadline: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	var last string
	env.srv.checkDrift(&last)
	if len(fn.kinds) != 0 {
		t.Errorf("an armed apply must not read as drift, got %v", fn.kinds)
	}
}
