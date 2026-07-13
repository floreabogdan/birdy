package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func autoRefreshASSet(t *testing.T, env *testEnv) store.ASSet {
	t.Helper()
	if _, err := env.store.CreateASSet(store.ASSet{
		Name: "AS_CUSTOMER", Source: "AS-CUSTOMER", AutoRefresh: true,
		Entries: []store.ASNRange{{Low: 64600, High: 64600, Note: "the customer"}},
	}); err != nil {
		t.Fatal(err)
	}
	set, err := env.store.GetASSetByName("AS_CUSTOMER")
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// A changed expansion replaces the members, keeps the operator's notes on the
// ASNs that survived, stamps the sync and records an event — in the model only.
func TestASSetIRRRefreshUpdatesModel(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.Bgpq4Bin = "fake" })
	set := autoRefreshASSet(t, env)

	client := fakeIRR(`{"data": [
  64600,64601
]}`, nil)
	env.srv.refreshOneASSet(context.Background(), client, set)

	got, err := env.store.GetASSetByName("AS_CUSTOMER")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("members should be replaced with the fresh expansion, got %+v", got.Entries)
	}
	if got.Entries[0].Low != 64600 || got.Entries[0].Note != "the customer" {
		t.Errorf("a hand-written note should survive the refresh, got %+v", got.Entries[0])
	}
	if got.LastRefreshed == "" || got.RefreshError != "" {
		t.Errorf("a successful refresh should stamp the time and clear errors: %+v", got)
	}

	events, _ := env.store.ListEvents(10, 0)
	var refresh int
	for _, e := range events {
		if e.Kind == store.EventIRRRefresh {
			refresh++
		}
	}
	if refresh != 1 {
		t.Errorf("a changed AS set should record one irr_refresh event, got %d", refresh)
	}
}

// bgpq4 reports an unknown AS-SET as an empty list with exit 0. Applying that
// would leave a policy that rejects every route, so the members are kept.
func TestASSetIRRRefreshKeepsMembersOnEmpty(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.Bgpq4Bin = "fake" })
	set := autoRefreshASSet(t, env)

	env.srv.refreshOneASSet(context.Background(), fakeIRR(`{"data": [
]}`, nil), set)

	got, _ := env.store.GetASSetByName("AS_CUSTOMER")
	if len(got.Entries) != 1 || got.Entries[0].Low != 64600 {
		t.Errorf("an empty expansion must not wipe a populated set, got %+v", got.Entries)
	}
	if got.RefreshError == "" {
		t.Error("the empty-result anomaly should be recorded as an error")
	}
}

// An unchanged expansion advances the sync time without churning the config diff.
func TestASSetIRRRefreshNoChange(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.Bgpq4Bin = "fake" })
	set := autoRefreshASSet(t, env)

	env.srv.refreshOneASSet(context.Background(), fakeIRR(`{"data": [64600]}`, nil), set)

	got, _ := env.store.GetASSetByName("AS_CUSTOMER")
	if len(got.Entries) != 1 || got.LastRefreshed == "" {
		t.Errorf("an unchanged refresh should stamp the time and leave members alone: %+v", got)
	}
	events, _ := env.store.ListEvents(10, 0)
	for _, e := range events {
		if e.Kind == store.EventIRRRefresh {
			t.Error("an unchanged set must not fire a refresh event")
		}
	}
}

// The endpoint behind the form's Expand button. bgpq4 is absent in the test
// environment, so the answer should name it — obscure failures here just look
// like "the button does nothing".
func TestIRRASNsEndpointGuidesWhenBgpq4Missing(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.Bgpq4Bin = "definitely-not-bgpq4" })

	rec := env.do(t, "GET", "/api/irr/asns?source=AS-CUSTOMER", nil)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "bgpq4") {
		t.Errorf("expected an answer mentioning bgpq4, got %q", body)
	}
}

// The endpoint is registered only when bgpq4 is configured, so a birdy without
// it does not advertise an expansion it cannot run.
func TestIRRASNsEndpointAbsentWithoutBgpq4(t *testing.T) {
	env := newTestEnv(t, false)
	if rec := env.do(t, "GET", "/api/irr/asns?source=AS-CUSTOMER", nil); rec.Code != 404 {
		t.Errorf("want 404 without bgpq4, got %d", rec.Code)
	}
}

// Saving the form carries the auto-refresh opt-in and the AS-SET name.
func TestASSetFormStoresAutoRefresh(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.Bgpq4Bin = "fake" })

	env.do(t, "POST", "/library/as-sets/new", url.Values{
		"name": {"AS_CUST"}, "source": {"AS-CUSTOMER"}, "autoRefresh": {"on"}, "entries": {"64600"},
	})
	got, err := env.store.GetASSetByName("AS_CUST")
	if err != nil {
		t.Fatalf("the set should have been created: %v", err)
	}
	if !got.AutoRefresh || got.Source != "AS-CUSTOMER" {
		t.Errorf("auto-refresh and source should persist, got %+v", got)
	}

	// Auto-refresh with nothing to expand from is meaningless: it is cleared, not
	// rejected, so the form still saves.
	env.do(t, "POST", "/library/as-sets/new", url.Values{
		"name": {"AS_HAND"}, "autoRefresh": {"on"}, "entries": {"64700"},
	})
	got, err = env.store.GetASSetByName("AS_HAND")
	if err != nil {
		t.Fatalf("the hand-kept set should have been created: %v", err)
	}
	if got.AutoRefresh {
		t.Error("auto-refresh without an AS-SET source should be cleared")
	}
}
