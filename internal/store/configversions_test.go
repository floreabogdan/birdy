package store

import (
	"testing"
	"time"
)

// The config-version lifecycle backs the whole apply pipeline: a pending version
// with an armed deadline, exactly one pending at a time, resolution, and the
// Expired signal the auto-revert reconciler depends on.
func TestConfigVersionLifecycle(t *testing.T) {
	st := openTest(t)

	// Nothing pending on a fresh store.
	if _, ok, err := st.PendingConfigVersion(); err != nil || ok {
		t.Fatalf("fresh store should have no pending version: ok=%v err=%v", ok, err)
	}

	deadline := time.Now().Add(90 * time.Second)
	id, err := st.CreateConfigVersion(ConfigVersion{
		SHA256: "abc123", Size: 42, ConfigText: "# config", Status: ConfigPending,
		Deadline: deadline, Message: "applied", BaselineSessions: "edge_v4",
	})
	if err != nil {
		t.Fatal(err)
	}

	pending, ok, err := st.PendingConfigVersion()
	if err != nil || !ok {
		t.Fatalf("expected a pending version: ok=%v err=%v", ok, err)
	}
	if pending.ID != id || pending.SHA256 != "abc123" || pending.BaselineSessions != "edge_v4" {
		t.Errorf("pending version round-tripped wrong: %+v", pending)
	}

	// Not expired before the deadline; expired after.
	if pending.Expired(deadline.Add(-time.Second)) {
		t.Error("must not be expired before the deadline")
	}
	if !pending.Expired(deadline.Add(time.Second)) {
		t.Error("must be expired after the deadline")
	}

	// Resolving clears the pending state.
	if err := st.ResolveConfigVersion(id, ConfigConfirmed, "confirmed"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.PendingConfigVersion(); ok {
		t.Error("no version should be pending after it resolves")
	}

	// A resolved version is no longer "expired" even past its old deadline.
	versions, err := st.ListConfigVersions(10)
	if err != nil || len(versions) != 1 {
		t.Fatalf("want 1 version, got %d (err %v)", len(versions), err)
	}
	if versions[0].Status != ConfigConfirmed {
		t.Errorf("status = %q, want confirmed", versions[0].Status)
	}
	if versions[0].Expired(deadline.Add(time.Hour)) {
		t.Error("a confirmed version is never 'expired'")
	}
}
