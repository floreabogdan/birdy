package render

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func custASSet() store.ASSet {
	return store.ASSet{
		ID: 5, Name: "AS_CUSTOMER_A", Source: "AS-CUSTOMER-A",
		Description: "Expanded members of the customer's AS-SET",
		Entries: []store.ASNRange{
			{Low: 64600, High: 64600, Note: "the customer"},
			{Low: 65010, High: 65020, Note: "their downstreams"},
		},
	}
}

// "Transit for you, not for your downstreams." The route's origin AS — the
// last ASN in the path — must be the peer itself.
func TestOriginPeerOnlyRendersOnTheSession(t *testing.T) {
	p := ebgpPeer()
	p.Role = store.RoleCustomer
	p.RemoteASN = 64600
	p.OriginPeerOnly = true
	p.ImportPolicies = []store.Policy{sanityPolicy()}

	in := baseInput()
	in.PrefixSets, in.Policies, in.Peers = bogonSets(), []store.Policy{sanityPolicy()}, []store.Peer{p}
	f := block(t, mustRender(t, in), "filter ebgp_in_edge_v4")

	if !strings.Contains(f, `if bgp_path.last != 64600 then reject "prefix not originated by this peer";`) {
		t.Errorf("origin-peer-only guard missing:\n%s", f)
	}
	// It must land on the peer's filter, not inside a shared policy function:
	// the check names the peer's own ASN.
	if strings.Contains(block(t, mustRender(t, in), "function imp_IMPORT_SANITY_v4()"), "bgp_path.last") {
		t.Error("a shared policy function must not reference one peer's ASN")
	}

	p.OriginPeerOnly = false
	in.Peers = []store.Peer{p}
	if strings.Contains(block(t, mustRender(t, in), "filter ebgp_in_edge_v4"), "bgp_path.last !=") {
		t.Error("the guard must be absent when the switch is off")
	}
}

func TestOriginASSetRendersInThePolicy(t *testing.T) {
	as := custASSet()
	pol := sanityPolicy()
	pol.OriginASSetID = sql.NullInt64{Int64: as.ID, Valid: true}

	in := baseInput()
	in.PrefixSets, in.ASSets, in.Policies = bogonSets(), []store.ASSet{as}, []store.Policy{pol}
	out := mustRender(t, in)

	set := block(t, out, "define AS_CUSTOMER_A = [")
	for _, want := range []string{"64600", "65010..65020", "# the customer", "# Expanded from the IRR object AS-CUSTOMER-A."} {
		if !strings.Contains(set, want) && !strings.Contains(out, want) {
			t.Errorf("AS set should render %q", want)
		}
	}
	// It applies to both families: an ASN has no address family.
	for _, fn := range []string{"function imp_IMPORT_SANITY_v4()", "function imp_IMPORT_SANITY_v6()"} {
		if !strings.Contains(block(t, out, fn), `if ! (bgp_path.last ~ AS_CUSTOMER_A) then reject "origin AS not in AS_CUSTOMER_A";`) {
			t.Errorf("%s should filter the origin AS", fn)
		}
	}
}

func TestEmptyOrMissingASSetIsAnError(t *testing.T) {
	pol := sanityPolicy()
	pol.OriginASSetID = sql.NullInt64{Int64: 5, Valid: true}

	in := baseInput()
	in.PrefixSets, in.Policies = bogonSets(), []store.Policy{pol}
	if _, err := Config(in); err == nil {
		t.Error("expected an error when the AS set is missing")
	}

	in.ASSets = []store.ASSet{{ID: 5, Name: "AS_EMPTY"}}
	if _, err := Config(in); err == nil {
		t.Error("BIRD has no empty-set syntax; an empty AS set must be an error")
	}
}

func TestLintOriginFiltersOnUpstream(t *testing.T) {
	p := ebgpPeer()
	p.Role = store.RoleUpstream
	p.OriginPeerOnly = true
	p.ImportPolicies = []store.Policy{sanityPolicy()}

	in := baseInput()
	in.Peers = []store.Peer{p}
	if ws := findings(t, in, p.Name); !hasMessage(ws, "rejects nearly every route") {
		t.Errorf("origin-peer-only on an upstream must be flagged, got %+v", ws)
	}

	// The same switch on a customer is exactly right.
	p.Role = store.RoleCustomer
	in.Peers = []store.Peer{p}
	if ws := findings(t, in, p.Name); hasMessage(ws, "rejects nearly every route") {
		t.Error("origin-peer-only on a customer is correct and must not warn")
	}
}

// Both origin filters must pass. An AS set that omits the peer's own ASN
// therefore rejects everything, which parses fine and looks deliberate.
func TestLintPeerASNMissingFromItsOwnASSet(t *testing.T) {
	as := custASSet()
	as.Entries = []store.ASNRange{{Low: 65010, High: 65020}} // downstreams only
	pol := sanityPolicy()
	pol.OriginASSetID = sql.NullInt64{Int64: as.ID, Valid: true}

	p := ebgpPeer()
	p.Role = store.RoleCustomer
	p.RemoteASN = 64600
	p.OriginPeerOnly = true
	p.ImportPolicies = []store.Policy{pol}

	in := baseInput()
	in.ASSets, in.Peers = []store.ASSet{as}, []store.Peer{p}
	if ws := findings(t, in, p.Name); !hasMessage(ws, "will accept nothing") {
		t.Errorf("expected a warning, got %+v", ws)
	}

	// Adding the customer's own ASN resolves it.
	as.Entries = append(as.Entries, store.ASNRange{Low: 64600, High: 64600})
	in.ASSets = []store.ASSet{as}
	if ws := findings(t, in, p.Name); hasMessage(ws, "will accept nothing") {
		t.Error("with its own ASN in the set, the combination is fine")
	}
}
