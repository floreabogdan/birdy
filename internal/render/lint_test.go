package render

import (
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func findings(t *testing.T, in Input, peer string) []Warning {
	t.Helper()
	var out []Warning
	for _, w := range Lint(in) {
		if w.Peer == peer {
			out = append(out, w)
		}
	}
	return out
}

func hasMessage(ws []Warning, substr string) bool {
	for _, w := range ws {
		if strings.Contains(w.Message, substr) {
			return true
		}
	}
	return false
}

func TestLintPrivateASNAgainstStrictBogonPolicy(t *testing.T) {
	p := ebgpPeer()
	p.RemoteASN = 65001 // private
	p.ImportPolicies = []store.Policy{sanityPolicy()}

	in := baseInput()
	in.Peers = []store.Peer{p}
	ws := findings(t, in, p.Name)
	if !hasMessage(ws, "reject every route it receives") {
		t.Errorf("a private-ASN peer under a strict bogon policy must be flagged, got %+v", ws)
	}

	// The tolerant policy is fine.
	pol := sanityPolicy()
	pol.BogonASNs = store.BogonASNsExceptPrivate
	p.ImportPolicies = []store.Policy{pol}
	in.Peers = []store.Peer{p}
	if ws := findings(t, in, p.Name); hasMessage(ws, "reject every route") {
		t.Error("except-private should not be flagged")
	}
}

func TestLintUnreachableDefaultAccept(t *testing.T) {
	p := ebgpPeer()
	rejects := sanityPolicy() // DefaultReject
	accepts := store.Policy{ID: 9, Name: "WITH_DEFAULT", Direction: store.DirImport, DefaultRoute: store.DefaultAccept}
	p.ImportPolicies = []store.Policy{rejects, accepts}

	in := baseInput()
	in.Peers = []store.Peer{p}
	if ws := findings(t, in, p.Name); !hasMessage(ws, "can never accept it") {
		t.Errorf("chained reject-then-accept of the default must be flagged, got %+v", ws)
	}
}

func TestLintDefaultOnlyAfterReject(t *testing.T) {
	p := ebgpPeer()
	only := store.Policy{ID: 9, Name: "DEFAULT_ONLY", Direction: store.DirImport, DefaultRoute: store.DefaultOnly}
	p.ImportPolicies = []store.Policy{sanityPolicy(), only}

	in := baseInput()
	in.Peers = []store.Peer{p}
	if ws := findings(t, in, p.Name); !hasMessage(ws, "accept nothing at all") {
		t.Errorf("a chain that rejects everything must be flagged, got %+v", ws)
	}
}

// A default-only peer on its own is a perfectly ordinary thing and must not warn.
func TestLintDefaultOnlyAloneIsFine(t *testing.T) {
	p := ebgpPeer()
	p.ImportPolicies = []store.Policy{{ID: 9, Name: "DEFAULT_ONLY", Direction: store.DirImport, DefaultRoute: store.DefaultOnly}}
	in := baseInput()
	in.Peers = []store.Peer{p}
	for _, w := range findings(t, in, p.Name) {
		if w.Severity == SeverityDanger {
			t.Errorf("unexpected danger: %s", w.Message)
		}
	}
}

func TestLintFullTableToUpstreamIsARouteLeak(t *testing.T) {
	full := store.Policy{ID: 9, Name: "EXPORT_FULL_TABLE", Direction: store.DirExport, AnnounceEverything: true}

	for _, role := range []string{store.RoleUpstream, store.RoleIXPeer} {
		p := ebgpPeer()
		p.Role = role
		p.ImportPolicies = []store.Policy{sanityPolicy()}
		p.ExportPolicies = []store.Policy{full}
		in := baseInput()
		in.Peers = []store.Peer{p}
		if ws := findings(t, in, p.Name); !hasMessage(ws, "route leak") {
			t.Errorf("full table to a %s must be flagged as a leak, got %+v", role, ws)
		}
	}

	// To a customer it is exactly what you sell them.
	p := ebgpPeer()
	p.Role = store.RoleCustomer
	p.ImportPolicies = []store.Policy{sanityPolicy()}
	p.ExportPolicies = []store.Policy{full}
	in := baseInput()
	in.Peers = []store.Peer{p}
	if ws := findings(t, in, p.Name); hasMessage(ws, "route leak") {
		t.Error("full table to a customer is not a leak")
	}
}

func TestLintUpstreamRoutesBackToUpstream(t *testing.T) {
	p := ebgpPeer()
	p.Role = store.RoleUpstream
	p.ImportPolicies = []store.Policy{sanityPolicy()}
	p.ExportPolicies = []store.Policy{{ID: 9, Name: "E", Direction: store.DirExport, AnnounceFromUpstream: true}}
	in := baseInput()
	in.Peers = []store.Peer{p}
	if ws := findings(t, in, p.Name); !hasMessage(ws, "route leak") {
		t.Errorf("re-announcing upstream routes to an upstream is a leak, got %+v", ws)
	}
}

// EXPORT_OWN ships with no prefix set attached. Attach it and forget, and the
// session announces nothing while looking perfectly configured.
func TestLintExportPolicyThatPermitsNothing(t *testing.T) {
	p := ebgpPeer()
	p.Role = store.RoleCustomer
	p.ImportPolicies = []store.Policy{sanityPolicy()}
	p.ExportPolicies = []store.Policy{{ID: 9, Name: "EXPORT_OWN", Direction: store.DirExport, RejectBogonPrefixes: true}}

	in := baseInput()
	in.Peers = []store.Peer{p}
	if ws := findings(t, in, p.Name); !hasMessage(ws, "announces no routes at all") {
		t.Errorf("an export policy permitting nothing must be flagged, got %+v", ws)
	}

	// A set that exists but is empty — how birdy ships ANNOUNCE_V4 — still
	// permits nothing, and the message should say so.
	p.ExportPolicies[0].SetIDs = []int64{20}
	in.PrefixSets = []store.PrefixSet{{ID: 20, Name: "ANNOUNCE_V4", Family: store.FamilyV4}}
	in.Peers = []store.Peer{p}
	ws := findings(t, in, p.Name)
	if !hasMessage(ws, "prefix sets are empty") {
		t.Errorf("an empty attached set should say so, got %+v", ws)
	}

	// Filling it resolves the finding.
	in.PrefixSets[0].Entries = []store.PrefixEntry{{Prefix: "192.0.2.0/24"}}
	if ws := findings(t, in, p.Name); hasMessage(ws, "announces no routes at all") {
		t.Error("a policy with a non-empty prefix set announces something")
	}
}

func TestLintNoImportPolicy(t *testing.T) {
	p := ebgpPeer()
	in := baseInput()
	in.Peers = []store.Peer{p}
	ws := findings(t, in, p.Name)
	if !hasMessage(ws, "No import policy") {
		t.Errorf("an unfiltered session should be flagged, got %+v", ws)
	}
	for _, w := range ws {
		if strings.Contains(w.Message, "No import policy") && w.Severity != SeverityDanger {
			t.Errorf("an unfiltered eBGP import must be serious, got %+v", w)
		}
	}
}

// iBGP peers take no policies, so none of the policy-chain checks apply to
// them. A correctly configured one produces nothing at all.
func TestLintIgnoresPolicyChecksForIBGP(t *testing.T) {
	in := baseInput()
	p := ibgpPeer(in.LocalASN)
	in.Peers = []store.Peer{p}
	if ws := findings(t, in, p.Name); len(ws) != 0 {
		t.Errorf("a well-formed iBGP peer should lint clean, got %+v", ws)
	}
}

// The finding that matters most: without next-hop-self, readvertised eBGP
// routes carry a next hop the far end cannot resolve.
func TestLintFlagsIBGPWithoutNextHopSelf(t *testing.T) {
	in := baseInput()
	p := ibgpPeer(in.LocalASN)
	p.NextHopSelf = false
	in.Peers = []store.Peer{p}

	ws := findings(t, in, p.Name)
	if len(ws) != 1 || !strings.Contains(ws[0].Message, "Next-hop-self is off") {
		t.Fatalf("want a next-hop-self warning, got %+v", ws)
	}
}

// A role/ASN mismatch means BIRD opens the opposite kind of session to the one
// the operator configured, and every filter decision follows from that.
func TestLintFlagsRoleASNMismatch(t *testing.T) {
	in := baseInput()

	ibgp := ibgpPeer(in.LocalASN)
	ibgp.RemoteASN = in.LocalASN + 1
	in.Peers = []store.Peer{ibgp}
	ws := findings(t, in, ibgp.Name)
	if len(ws) == 0 || !strings.Contains(ws[0].Message, "marked iBGP but its remote AS") {
		t.Errorf("an iBGP peer with a foreign ASN should be flagged, got %+v", ws)
	}

	ebgp := ebgpPeer()
	ebgp.RemoteASN = in.LocalASN
	in.Peers = []store.Peer{ebgp}
	ws = findings(t, in, ebgp.Name)
	if len(ws) == 0 || !strings.Contains(ws[0].Message, "is not marked iBGP") {
		t.Errorf("an eBGP peer carrying our own ASN should be flagged, got %+v", ws)
	}
}

func TestLintFlagsRawConfig(t *testing.T) {
	in := baseInput()
	in.RawConfig = "protocol bfd {}\n"
	var found bool
	for _, w := range Lint(in) {
		if strings.Contains(w.Message, "raw block") {
			found = true
		}
	}
	if !found {
		t.Error("a raw config block should be called out as unchecked by birdy")
	}
}

// ibgpPeer is a correctly configured internal session: our own ASN, and the
// next-hop rewrite that keeps readvertised eBGP routes usable.
func ibgpPeer(localASN int64) store.Peer {
	return store.Peer{
		Name: "ibgp_core", Role: store.RoleIBGP, Enabled: true,
		NeighborIP: "192.0.2.9", RemoteASN: localASN,
		NextHopSelf: true, ImportLimitAction: "restart",
	}
}

// A community referenced by name but not defined in the library is flagged as a
// danger — it would render an undefined BIRD symbol.
func TestLintFlagsDanglingCommunityReference(t *testing.T) {
	in := baseInput()
	in.Policies = nil // start clean of the base fixtures' policies

	p := ebgpPeer()
	p.ExportCommunities = "GHOST_COMMUNITY"
	in.Peers = []store.Peer{p}
	in.Policies = []store.Policy{{
		ID: 9, Name: "P_MATCH", Direction: store.DirImport,
		DefaultRoute: store.DefaultReject, MatchCommunity: "ANOTHER_GHOST",
	}}

	var peerFlag, polFlag bool
	for _, w := range Lint(in) {
		if w.Severity == SeverityDanger && strings.Contains(w.Message, "GHOST_COMMUNITY") {
			peerFlag = true
		}
		if w.Severity == SeverityDanger && strings.Contains(w.Message, "ANOTHER_GHOST") {
			polFlag = true
		}
	}
	if !peerFlag {
		t.Error("a peer export referencing an undefined community should be flagged")
	}
	if !polFlag {
		t.Error("a policy matching an undefined community should be flagged")
	}

	// Defining both clears the findings.
	in.Communities = []store.CommunityDef{
		{Name: "GHOST_COMMUNITY", A: 1, B: 1},
		{Name: "ANOTHER_GHOST", A: 1, B: 2},
	}
	for _, w := range Lint(in) {
		if strings.Contains(w.Message, "GHOST_COMMUNITY") || strings.Contains(w.Message, "ANOTHER_GHOST") {
			t.Errorf("a defined community must not be flagged: %s", w.Message)
		}
	}
}
