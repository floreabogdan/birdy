package store

import "testing"

func TestAutoRefreshListingAndRefresh(t *testing.T) {
	st := openTest(t)

	// One IRR-backed set opted into auto-refresh, one hand-maintained set.
	autoID, err := st.CreatePrefixSet(PrefixSet{
		Name: "CUST_V4", Family: FamilyV4, Source: "AS-CUSTOMER", AutoRefresh: true,
		Entries: []PrefixEntry{{Prefix: "192.0.2.0/24"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePrefixSet(PrefixSet{
		Name: "MANUAL_V4", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "198.51.100.0/24"}},
	}); err != nil {
		t.Fatal(err)
	}

	auto, err := st.ListAutoRefreshPrefixSets()
	if err != nil {
		t.Fatal(err)
	}
	if len(auto) != 1 || auto[0].Name != "CUST_V4" {
		t.Fatalf("only the auto-refresh set should be listed, got %+v", auto)
	}

	// A refresh replaces the entries and stamps the sync time.
	fresh := []PrefixEntry{{Prefix: "203.0.113.0/24"}, {Prefix: "198.51.100.0/24"}}
	if err := st.RefreshPrefixSetEntries(autoID, fresh); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetPrefixSet(autoID)
	if len(got.Entries) != 2 {
		t.Fatalf("entries should be replaced, got %d", len(got.Entries))
	}
	if got.LastRefreshed == "" {
		t.Error("refresh should stamp last_refreshed")
	}

	// A recorded error survives a read; a later successful mark clears it.
	if err := st.MarkPrefixSetRefreshError(autoID, "mirror timeout"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetPrefixSet(autoID)
	if got.RefreshError != "mirror timeout" {
		t.Errorf("refresh error should persist, got %q", got.RefreshError)
	}
	if err := st.MarkPrefixSetRefreshed(autoID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetPrefixSet(autoID)
	if got.RefreshError != "" {
		t.Errorf("a clean refresh should clear the error, got %q", got.RefreshError)
	}
}

// Auto-refresh is meaningless without a source; Validate clears it.
func TestAutoRefreshClearedWithoutSource(t *testing.T) {
	ps := PrefixSet{Name: "X_V4", Family: FamilyV4, AutoRefresh: true, Entries: []PrefixEntry{{Prefix: "192.0.2.0/24"}}}
	ps.Validate()
	if ps.AutoRefresh {
		t.Error("auto-refresh without an IRR source should be cleared")
	}
}
