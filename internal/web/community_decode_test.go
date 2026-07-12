package web

import (
	"database/sql"
	"testing"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/store"
)

func TestDecodeCommunities(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: sql.NullInt64{Int64: 64496, Valid: true},
		BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "127.0.0.1:8080",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreateCommunityDef(store.CommunityDef{
		Name: "CUSTOMER_EU", Large: false, A: 65000, B: 100,
	}); err != nil {
		t.Fatal(err)
	}

	semantic := env.srv.semanticLabels(64496)
	named := env.srv.namedCommunities()
	got := decodeCommunities([]birdc.Community{
		{A: 65000, B: 100},                     // operator-named library entry
		{A: 65535, B: 666},                     // RFC 7999 well-known (also seeded, named BLACKHOLE)
		{A: 65535, B: 65281},                   // RFC 1997 well-known, not in the library
		{Large: true, A: 64496, B: 1, C: 1000}, // birdy FROM_UPSTREAM tag
		{Large: true, A: 64496, B: 2, C: 1},    // birdy RPKI_INVALID tag
		{A: 65001, B: 7},                       // unknown
	}, semantic, named)

	want := []commChip{
		{Text: "(65000, 100)", Name: "CUSTOMER_EU", Kind: "named"},
		// well-known meaning keeps its colour; the seeded name is shown
		{Text: "(65535, 666)", Name: "BLACKHOLE", Kind: "wellknown"},
		{Text: "(65535, 65281)", Name: "NO_EXPORT", Kind: "wellknown"},
		{Text: "(64496, 1, 1000)", Name: "FROM_UPSTREAM", Kind: "origin"},
		{Text: "(64496, 2, 1)", Name: "RPKI_INVALID", Kind: "rpki"},
		{Text: "(65001, 7)", Name: "", Kind: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d chips, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chip[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The operator's own name for a value is shown as the label, but a well-known
// community keeps its operational colour so a blackhole still reads as one.
func TestDecodeCommunitiesNamePrefersOperatorKeepsColour(t *testing.T) {
	env := newTestEnv(t, false)
	// Give NO_EXPORT (not in the seed pack) the operator's own name.
	if _, err := env.store.CreateCommunityDef(store.CommunityDef{
		Name: "STAY_LOCAL", A: 65535, B: 65281,
	}); err != nil {
		t.Fatal(err)
	}
	semantic := env.srv.semanticLabels(64496)
	named := env.srv.namedCommunities()
	got := decodeCommunities([]birdc.Community{{A: 65535, B: 65281}}, semantic, named)
	if len(got) != 1 || got[0].Name != "STAY_LOCAL" || got[0].Kind != "wellknown" {
		t.Fatalf("operator name not shown with well-known colour: %+v", got)
	}
}
