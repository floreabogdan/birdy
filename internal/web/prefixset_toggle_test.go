package web

import (
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// The list toggle switches a prefix set off and back on, the way a peer can be —
// a model change that takes effect on the next apply.
func TestPrefixSetToggleFromList(t *testing.T) {
	env := newTestEnv(t, false)
	if _, err := env.store.CreatePrefixSet(store.PrefixSet{
		Name: "CUST", Family: store.FamilyV4,
		Entries: []store.PrefixEntry{{Prefix: "203.0.113.0/24"}},
	}); err != nil {
		t.Fatal(err)
	}

	env.do(t, "POST", "/library/prefix-sets/CUST/toggle", nil)
	if ps, _ := env.store.GetPrefixSetByName("CUST"); !ps.Disabled {
		t.Error("toggling should disable the set")
	}

	env.do(t, "POST", "/library/prefix-sets/CUST/toggle", nil)
	if ps, _ := env.store.GetPrefixSetByName("CUST"); ps.Disabled {
		t.Error("toggling again should re-enable the set")
	}
}
