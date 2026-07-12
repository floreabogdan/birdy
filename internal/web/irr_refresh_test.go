package web

import (
	"context"
	"testing"

	"github.com/floreabogdan/birdy/internal/irr"
	"github.com/floreabogdan/birdy/internal/store"
)

// fakeIRR returns an irr.Client whose bgpq4 is replaced by a canned JSON reply.
func fakeIRR(output string, err error) *irr.Client {
	return &irr.Client{Bin: "fake", Run: func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return []byte(output), err
	}}
}

func autoRefreshSet(t *testing.T, env *testEnv) store.PrefixSet {
	t.Helper()
	id, err := env.store.CreatePrefixSet(store.PrefixSet{
		Name: "CUST_V4", Family: store.FamilyV4, Source: "AS-CUSTOMER", AutoRefresh: true,
		Entries: []store.PrefixEntry{{Prefix: "192.0.2.0/24"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := env.store.GetPrefixSet(id)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// A changed IRR expansion replaces the set's prefixes, stamps the sync, and
// records an event — but the change stays in the model (no apply).
func TestIRRRefreshUpdatesModel(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.Bgpq4Bin = "fake" })
	set := autoRefreshSet(t, env)

	client := fakeIRR(`{"data":[{"prefix":"198.51.100.0/24","exact":true},{"prefix":"203.0.113.0/24","exact":true}]}`, nil)
	env.srv.refreshOneSet(context.Background(), client, set)

	got, err := env.store.GetPrefixSet(set.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries should be replaced with the fresh expansion, got %d", len(got.Entries))
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
		t.Errorf("a changed set should record one irr_refresh event, got %d", refresh)
	}
}

// An unchanged expansion stamps the sync time but does not churn entries or
// record a change event.
func TestIRRRefreshNoChange(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.Bgpq4Bin = "fake" })
	set := autoRefreshSet(t, env)

	client := fakeIRR(`{"data":[{"prefix":"192.0.2.0/24","exact":true}]}`, nil)
	env.srv.refreshOneSet(context.Background(), client, set)

	got, _ := env.store.GetPrefixSet(set.ID)
	if len(got.Entries) != 1 || got.Entries[0].Prefix != "192.0.2.0/24" {
		t.Errorf("an unchanged refresh should leave entries alone, got %+v", got.Entries)
	}
	if got.LastRefreshed == "" {
		t.Error("even an unchanged refresh should stamp the sync time")
	}
	events, _ := env.store.ListEvents(10, 0)
	for _, e := range events {
		if e.Kind == store.EventIRRRefresh {
			t.Error("an unchanged set must not fire a refresh event")
		}
	}
}

// An empty expansion for a populated set is treated as a mirror failure: the
// prefixes are kept and the error recorded, never wiped.
func TestIRRRefreshKeepsPrefixesOnEmpty(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.Bgpq4Bin = "fake" })
	set := autoRefreshSet(t, env)

	client := fakeIRR(`{"data":[]}`, nil)
	env.srv.refreshOneSet(context.Background(), client, set)

	got, _ := env.store.GetPrefixSet(set.ID)
	if len(got.Entries) != 1 {
		t.Errorf("an empty expansion must not wipe a populated set, got %d entries", len(got.Entries))
	}
	if got.RefreshError == "" {
		t.Error("the empty-result anomaly should be recorded as an error")
	}
}
