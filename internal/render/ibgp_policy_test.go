package render

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func internalPeer() store.Peer {
	return store.Peer{
		ID: 7, Name: "core", Role: store.RoleIBGP, Enabled: true,
		NeighborIP: "192.0.2.9", RemoteASN: 65551, NextHopSelf: true,
		ImportLimitAction: "restart",
	}
}

// The old behaviour, unchanged: an internal session with no policies carries
// everything. Existing configs must render byte-identically, or an upgrade shows
// up as a spurious pending diff on every router that runs iBGP.
func TestIBGPWithoutPoliciesStillCarriesEverything(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()
	in.Peers = []store.Peer{internalPeer()}

	cfg := mustRender(t, in)
	if !strings.Contains(cfg, "import all;") || !strings.Contains(cfg, "export all;") {
		t.Error("an unpoliced iBGP session should still import all / export all")
	}
	if strings.Contains(cfg, "ibgp_in_core") || strings.Contains(cfg, "ibgp_out_core") {
		t.Error("no chain attached, so no filters should be generated")
	}
}

// The point of the feature: an internal session that carries only what you meant
// it to. The far router must not inherit an upstream's default route just because
// it is inside the same AS.
func TestIBGPWithPoliciesRendersFilters(t *testing.T) {
	in := baseInput()
	in.PrefixSets = append(bogonSets(), store.PrefixSet{
		ID: 20, Name: "INTERNAL_V4", Family: store.FamilyV4,
		Entries: []store.PrefixEntry{{Prefix: "192.0.2.0/26"}},
	})
	imp := store.Policy{
		ID: 10, Name: "IBGP_IN", Direction: store.DirImport,
		DefaultRoute: store.DefaultReject, AcceptOnlySetID: sql.NullInt64{Int64: 20, Valid: true},
		BogonASNs: store.BogonASNsOff,
	}
	exp := store.Policy{ID: 11, Name: "IBGP_OUT", Direction: store.DirExport, SetIDs: []int64{20}}
	in.Policies = []store.Policy{imp, exp}

	p := internalPeer()
	p.ImportPolicies = []store.Policy{imp}
	p.ExportPolicies = []store.Policy{exp}
	in.Peers = []store.Peer{p}

	cfg := mustRender(t, in)
	if !strings.Contains(cfg, "filter ibgp_in_core") || !strings.Contains(cfg, "filter ibgp_out_core") {
		t.Fatal("an iBGP session with a chain should get its own filters")
	}
	if !strings.Contains(cfg, "import filter ibgp_in_core;") || !strings.Contains(cfg, "export filter ibgp_out_core;") {
		t.Error("the channel should use the generated filters, not import all / export all")
	}
	// Named ibgp_, not ebgp_: the prefix tells a reader which rules were applied.
	if strings.Contains(cfg, "ebgp_in_core") {
		t.Error("an internal session must not render eBGP filters")
	}
}

// The correctness trap this feature could have introduced. An eBGP import filter
// strips our own large communities so a peer cannot forge an origin tag. On an
// internal session those same communities ARE the origin tags, stamped at the edge
// — deleting them would silently unmake every "announce what my customers sent me"
// decision on the far router.
func TestIBGPImportFilterKeepsOurOwnCommunities(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()
	imp := store.Policy{
		ID: 10, Name: "IBGP_IN", Direction: store.DirImport,
		DefaultRoute: store.DefaultReject, BogonASNs: store.BogonASNsOff,
	}
	in.Policies = []store.Policy{imp}
	p := internalPeer()
	p.ImportPolicies = []store.Policy{imp}
	in.Peers = []store.Peer{p}

	cfg := mustRender(t, in)
	body := filterBody(cfg, "ibgp_in_core")
	if body == "" {
		t.Fatal("expected an ibgp_in_core filter")
	}
	if strings.Contains(body, "bgp_large_community.delete") {
		t.Error("an internal session must NOT strip our own large communities — they are the origin tags")
	}
	if strings.Contains(body, "bgp_large_community.add") {
		t.Error("an internal session must not re-stamp an origin tag either; the edge already did")
	}

	// And the eBGP side must still strip them, or a customer could forge one.
	e := ebgpPeer()
	e.ImportPolicies = []store.Policy{imp}
	in.Peers = []store.Peer{e}
	ebgp := filterBody(mustRender(t, in), "ebgp_in_edge_v4")
	if !strings.Contains(ebgp, "bgp_large_community.delete([(65551, *, *)])") {
		t.Error("an eBGP import filter must still strip our own communities")
	}
}

// filterBody returns the text of one generated filter, so a test can assert on
// what is inside it rather than on the whole config.
func filterBody(cfg, name string) string {
	start := strings.Index(cfg, "filter "+name+"\n{")
	if start < 0 {
		return ""
	}
	end := strings.Index(cfg[start:], "\n}\n")
	if end < 0 {
		return cfg[start:]
	}
	return cfg[start : start+end]
}
