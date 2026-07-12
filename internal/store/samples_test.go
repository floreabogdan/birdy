package store

import (
	"testing"
	"time"
)

func TestSamplesInsertListPrune(t *testing.T) {
	st := openTest(t)
	base := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

	// Two sessions, three points each, oldest first.
	var batch []Sample
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		batch = append(batch,
			Sample{Ts: ts, Protocol: "edge_v4", Imported: 100 + i, Exported: 5 + i},
			Sample{Ts: ts, Protocol: "edge_v6", Imported: 200 + i, Exported: 9 + i},
		)
	}
	if err := st.InsertSamples(batch); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListSamples("edge_v4", base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 samples for edge_v4, got %d", len(got))
	}
	if got[0].Imported != 100 || got[2].Imported != 102 {
		t.Errorf("samples out of order or wrong: %+v", got)
	}

	// RecentSamples spans both protocols.
	all, err := st.RecentSamples(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Errorf("RecentSamples should return all 6, got %d", len(all))
	}

	// Prune everything before the last point.
	if err := st.PruneSamples(base.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListSamples("edge_v4", base)
	if len(got) != 1 {
		t.Errorf("after prune only the newest sample should remain, got %d", len(got))
	}
}

// A window that excludes every sample returns nothing, not an error.
func TestSamplesWindowExcludesOld(t *testing.T) {
	st := openTest(t)
	old := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := st.InsertSamples([]Sample{{Ts: old, Protocol: "edge_v4", Imported: 1}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListSamples("edge_v4", old.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a sample before the window must be excluded, got %d", len(got))
	}
}
