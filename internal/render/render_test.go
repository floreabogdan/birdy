package render

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

func baseInput() Input {
	return Input{
		RouterID:  "192.0.2.1",
		LocalASN:  65551,
		Version:   "test",
		Generated: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
	}
}

func bogonSets() []store.PrefixSet {
	return []store.PrefixSet{
		{ID: 1, Name: store.BogonSetV4, Family: store.FamilyV4, System: true, Entries: []store.PrefixEntry{{Prefix: "10.0.0.0/8", Modifier: "+"}}},
		{ID: 2, Name: store.BogonSetV6, Family: store.FamilyV6, System: true, Entries: []store.PrefixEntry{{Prefix: "fc00::/7", Modifier: "+"}}},
	}
}

func sanityPolicy() store.Policy {
	return store.Policy{
		ID: 1, Name: "IMPORT_SANITY", Direction: store.DirImport,
		DefaultRoute: store.DefaultReject, RejectBogonPrefixes: true,
		MinLenV4: 8, MaxLenV4: 24, MinLenV6: 12, MaxLenV6: 48,
		RejectOwnASN: true, MaxASPathLen: 64, BogonASNs: store.BogonASNsAll,
	}
}

func ebgpPeer() store.Peer {
	return store.Peer{
		ID: 1, Name: "edge_v4", Role: store.RoleUpstream, Enabled: true, EnforceFirstAS: true,
		NeighborIP: "198.51.100.1", RemoteASN: 64497,
		ImportLimit: 1000000, ImportLimitAction: "restart",
	}
}

func mustRender(t *testing.T, in Input) string {
	t.Helper()
	out, err := Config(in)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	return out
}

// block returns the body of a named "filter x {...}" or "function x() {...}".
func block(t *testing.T, cfg, decl string) string {
	t.Helper()
	start := strings.Index(cfg, decl)
	if start < 0 {
		t.Fatalf("no %q in:\n%s", decl, cfg)
	}
	end := strings.Index(cfg[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("unterminated %q", decl)
	}
	return cfg[start : start+end]
}

func TestConfigRejectsBadGlobals(t *testing.T) {
	in := baseInput()
	in.RouterID = "not-an-ip"
	if _, err := Config(in); err == nil {
		t.Error("expected error for invalid router id")
	}
	in = baseInput()
	in.RouterID = "2001:db8::1" // a router id is a 32-bit value
	if _, err := Config(in); err == nil {
		t.Error("expected error for IPv6 router id")
	}
	in = baseInput()
	in.LocalASN = 0
	if _, err := Config(in); err == nil {
		t.Error("expected error for zero ASN")
	}
}

// The whole design rests on this: import functions never accept, export
// functions never reject except on bogons. Otherwise chaining silently breaks,
// because the first policy to accept or reject ends the filter.
func TestImportPoliciesOnlyRejectAndExportPoliciesOnlyAccept(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()
	in.Policies = []store.Policy{
		sanityPolicy(),
		{ID: 2, Name: "EXPORT_ALL", Direction: store.DirExport, AnnounceEverything: true},
		{ID: 3, Name: "EXPORT_MIX", Direction: store.DirExport, AnnounceDefault: true, AnnounceFromIX: true, RejectBogonPrefixes: true},
	}
	out := mustRender(t, in)

	imp := block(t, out, "function imp_IMPORT_SANITY_v4()")
	if strings.Contains(imp, "accept;") {
		t.Errorf("an import policy must never accept — it would end the chain early:\n%s", imp)
	}

	exp := block(t, out, "function exp_EXPORT_MIX_v4()")
	for _, line := range strings.Split(exp, "\n") {
		if strings.Contains(line, "reject") && !strings.Contains(line, "bogon") {
			t.Errorf("an export policy may only reject bogons, got: %s", strings.TrimSpace(line))
		}
	}
}

func TestImportGuards(t *testing.T) {
	p := ebgpPeer()
	pol := sanityPolicy()
	p.ImportPolicies = []store.Policy{pol}

	in := baseInput()
	in.PrefixSets = bogonSets()
	in.Policies = []store.Policy{pol}
	in.Peers = []store.Peer{p}
	out := mustRender(t, in)

	fn := block(t, out, "function imp_IMPORT_SANITY_v4()")
	for _, want := range []string{
		`if net = 0.0.0.0/0 then reject "default route not accepted";`,
		`if net.len < 8 || net.len > 24 then reject "prefix length out of bounds";`,
		`if net ~ BOGONS_V4 then reject "bogon prefix";`,
		`if bgp_path.len > 64 then reject "AS path too long";`,
		`if bgp_path ~ [65551] then reject "our own ASN in AS path";`,
		`if bgp_path ~ BOGON_ASNS then reject "bogon ASN in AS path";`,
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("missing guard:\n  %s", want)
		}
	}

	// The v6 twin must use the v6 constants and never the v4 set.
	fn6 := block(t, out, "function imp_IMPORT_SANITY_v6()")
	if !strings.Contains(fn6, "net ~ BOGONS_V6") || strings.Contains(fn6, "BOGONS_V4") {
		t.Errorf("v6 function must reference only the v6 bogon set:\n%s", fn6)
	}
	if !strings.Contains(fn6, "net.len < 12 || net.len > 48") {
		t.Error("v6 length bounds missing")
	}
	if !strings.Contains(fn6, `if net = ::/0 then reject`) {
		t.Error("v6 default-route guard missing")
	}
}

// The peer that only ever sends a default route.
func TestDefaultRouteModes(t *testing.T) {
	sets := bogonSets()

	only := store.Policy{ID: 1, Name: "P", Direction: store.DirImport, DefaultRoute: store.DefaultOnly, BogonASNs: store.BogonASNsOff}
	in := baseInput()
	in.PrefixSets, in.Policies = sets, []store.Policy{only}
	fn := block(t, mustRender(t, in), "function imp_P_v4()")
	if !strings.Contains(fn, `if net != 0.0.0.0/0 then reject "only the default route is accepted";`) {
		t.Errorf("accept-only-default must reject everything else:\n%s", fn)
	}

	// Accepting the default alongside everything else must not then trip the
	// prefix-length floor, since 0.0.0.0/0 is shorter than any minimum.
	accept := store.Policy{ID: 1, Name: "P", Direction: store.DirImport, DefaultRoute: store.DefaultAccept,
		MinLenV4: 8, MaxLenV4: 24, BogonASNs: store.BogonASNsOff}
	in.Policies = []store.Policy{accept}
	fn = block(t, mustRender(t, in), "function imp_P_v4()")
	if strings.Contains(fn, `if net = 0.0.0.0/0 then reject`) {
		t.Error("accept mode must not reject the default route")
	}
	if !strings.Contains(fn, `if net != 0.0.0.0/0 && (net.len < 8 || net.len > 24) then reject`) {
		t.Errorf("the length guard must exempt the default route:\n%s", fn)
	}
}

func TestBogonASNModes(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()

	for mode, want := range map[string]string{
		store.BogonASNsAll:           "if bgp_path ~ BOGON_ASNS then reject",
		store.BogonASNsExceptPrivate: "if bgp_path ~ BOGON_ASNS_EXCEPT_PRIVATE then reject",
	} {
		in.Policies = []store.Policy{{ID: 1, Name: "P", Direction: store.DirImport, DefaultRoute: store.DefaultReject, BogonASNs: mode}}
		if fn := block(t, mustRender(t, in), "function imp_P_v4()"); !strings.Contains(fn, want) {
			t.Errorf("mode %q should emit %q", mode, want)
		}
	}
	in.Policies = []store.Policy{{ID: 1, Name: "P", Direction: store.DirImport, DefaultRoute: store.DefaultReject, BogonASNs: store.BogonASNsOff}}
	if fn := block(t, mustRender(t, in), "function imp_P_v4()"); strings.Contains(fn, "BOGON_ASNS") {
		t.Error("mode off should emit no bogon-ASN check")
	}

	// The except-private set must not contain the private ranges.
	out := mustRender(t, in)
	set := block(t, out, "define BOGON_ASNS_EXCEPT_PRIVATE = [")
	for _, priv := range []string{"64512..65534", "4200000000..4294967294"} {
		if strings.Contains(set, priv) {
			t.Errorf("private range %s must not be in BOGON_ASNS_EXCEPT_PRIVATE", priv)
		}
	}
	if !strings.Contains(set, "23456") {
		t.Error("AS_TRANS should still be a bogon in the except-private set")
	}
}

// A peer can otherwise pretend its routes came from a customer by pre-tagging
// them with one of our own large communities.
func TestOwnCommunitiesAreStrippedOnEBGPImport(t *testing.T) {
	p := ebgpPeer()
	p.ImportPolicies = []store.Policy{sanityPolicy()}
	in := baseInput()
	in.PrefixSets, in.Policies, in.Peers = bogonSets(), []store.Policy{sanityPolicy()}, []store.Peer{p}

	f := block(t, mustRender(t, in), "filter ebgp_in_edge_v4")
	strip := strings.Index(f, "bgp_large_community.delete([(65551, *, *)]);")
	add := strings.Index(f, "bgp_large_community.add(FROM_UPSTREAM);")
	if strip < 0 {
		t.Fatal("eBGP import must strip our own large communities")
	}
	if add < 0 {
		t.Fatal("an upstream's routes must be tagged FROM_UPSTREAM")
	}
	if strip > add {
		t.Error("the strip must happen before the tag, or a peer can forge its own tag")
	}
}

func TestRoleDecidesTheTag(t *testing.T) {
	for role, want := range map[string]string{
		store.RoleUpstream: "FROM_UPSTREAM",
		store.RoleIXPeer:   "FROM_IX",
		store.RoleCustomer: "FROM_CUSTOMER",
	} {
		p := ebgpPeer()
		p.Role = role
		in := baseInput()
		in.PrefixSets, in.Peers = bogonSets(), []store.Peer{p}
		f := block(t, mustRender(t, in), "filter ebgp_in_edge_v4")
		if !strings.Contains(f, "bgp_large_community.add("+want+");") {
			t.Errorf("role %q should tag %s:\n%s", role, want, f)
		}
	}
}

func TestEnforceFirstAS(t *testing.T) {
	p := ebgpPeer()
	in := baseInput()
	in.PrefixSets, in.Peers = bogonSets(), []store.Peer{p}
	if f := block(t, mustRender(t, in), "filter ebgp_in_edge_v4"); !strings.Contains(f, `if bgp_path.first != 64497 then reject`) {
		t.Error("enforce first AS should be rendered when enabled")
	}

	// A route server does not prepend itself, so the check must be omitted.
	p.EnforceFirstAS = false
	in.Peers = []store.Peer{p}
	if f := block(t, mustRender(t, in), "filter ebgp_in_edge_v4"); strings.Contains(f, "bgp_path.first") {
		t.Error("enforce first AS must be omitted for a route server")
	}
}

func TestExportDefaultDeny(t *testing.T) {
	p := ebgpPeer() // no export policies
	in := baseInput()
	in.PrefixSets, in.Peers = bogonSets(), []store.Peer{p}
	out := mustRender(t, in)

	proto := block(t, out, "protocol bgp edge_v4 {")
	if !strings.Contains(proto, "export none;") {
		t.Error("a peer with no export policy must announce nothing")
	}
	if strings.Contains(proto, "export all;") {
		t.Error("an eBGP session must never export all")
	}
	if strings.Contains(out, "filter ebgp_out_edge_v4") {
		t.Error("no export filter should be emitted when nothing is announced")
	}
}

// Export policies compose with OR, and the filter rejects whatever none of them
// permitted. That is RFC 8212 default deny.
func TestExportChainEndsInReject(t *testing.T) {
	p := ebgpPeer()
	a := store.Policy{ID: 2, Name: "EXPORT_OWN", Direction: store.DirExport, SetIDs: []int64{20}}
	b := store.Policy{ID: 3, Name: "EXPORT_CUSTOMERS", Direction: store.DirExport, AnnounceFromCustomer: true}
	p.ExportPolicies = []store.Policy{a, b}

	in := baseInput()
	in.PrefixSets = append(bogonSets(), store.PrefixSet{ID: 20, Name: "ANNOUNCE_V4", Family: store.FamilyV4,
		Entries: []store.PrefixEntry{{Prefix: "192.0.2.0/24"}}})
	in.Policies, in.Peers = []store.Policy{a, b}, []store.Peer{p}

	f := block(t, mustRender(t, in), "filter ebgp_out_edge_v4")
	first := strings.Index(f, "exp_EXPORT_OWN_v4();")
	second := strings.Index(f, "exp_EXPORT_CUSTOMERS_v4();")
	if first < 0 || second < 0 || first > second {
		t.Errorf("export policies must be called in attachment order:\n%s", f)
	}
	if !strings.Contains(f, `reject "not permitted by any export policy";`) {
		t.Error("the export filter must reject anything no policy accepted")
	}
}

func TestImportChainOrderIsPreserved(t *testing.T) {
	p := ebgpPeer()
	a := store.Policy{ID: 2, Name: "FIRST", Direction: store.DirImport, DefaultRoute: store.DefaultReject, BogonASNs: store.BogonASNsOff}
	b := store.Policy{ID: 3, Name: "SECOND", Direction: store.DirImport, DefaultRoute: store.DefaultReject, BogonASNs: store.BogonASNsOff}
	p.ImportPolicies = []store.Policy{b, a} // attached in this order

	in := baseInput()
	in.PrefixSets, in.Policies, in.Peers = bogonSets(), []store.Policy{a, b}, []store.Peer{p}
	f := block(t, mustRender(t, in), "filter ebgp_in_edge_v4")
	if strings.Index(f, "imp_SECOND_v4();") > strings.Index(f, "imp_FIRST_v4();") {
		t.Errorf("import policies must be called in attachment order, not name order:\n%s", f)
	}
}

func TestExportSetsAreFamilyFiltered(t *testing.T) {
	pol := store.Policy{ID: 2, Name: "EXP", Direction: store.DirExport, SetIDs: []int64{20, 21}}
	in := baseInput()
	in.PrefixSets = append(bogonSets(),
		store.PrefixSet{ID: 20, Name: "ANNOUNCE_V4", Family: store.FamilyV4, Entries: []store.PrefixEntry{{Prefix: "192.0.2.0/24"}}},
		store.PrefixSet{ID: 21, Name: "ANNOUNCE_V6", Family: store.FamilyV6, Entries: []store.PrefixEntry{{Prefix: "2001:db8::/32"}}})
	in.Policies = []store.Policy{pol}
	out := mustRender(t, in)

	if v4 := block(t, out, "function exp_EXP_v4()"); strings.Contains(v4, "ANNOUNCE_V6") {
		t.Error("a v4 function must not reference a v6 set")
	}
	if v6 := block(t, out, "function exp_EXP_v6()"); strings.Contains(v6, "ANNOUNCE_V4") {
		t.Error("a v6 function must not reference a v4 set")
	}
}

// An accept-only set names everything the policy permits. On the other address
// family it therefore permits nothing — emitting no rule would accept anything.
func TestAcceptOnlySetRejectsTheOtherFamily(t *testing.T) {
	pol := store.Policy{ID: 2, Name: "CUST", Direction: store.DirImport, DefaultRoute: store.DefaultReject,
		BogonASNs: store.BogonASNsOff, AcceptOnlySetID: sql.NullInt64{Int64: 30, Valid: true}}
	in := baseInput()
	in.PrefixSets = append(bogonSets(), store.PrefixSet{ID: 30, Name: "CUST_A_V4", Family: store.FamilyV4,
		Entries: []store.PrefixEntry{{Prefix: "198.51.100.0/24", Modifier: "+"}}})
	in.Policies = []store.Policy{pol}
	out := mustRender(t, in)

	if v4 := block(t, out, "function imp_CUST_v4()"); !strings.Contains(v4, `if ! (net ~ CUST_A_V4) then reject "not in CUST_A_V4";`) {
		t.Errorf("v4 function should permit only the set:\n%s", v4)
	}
	v6 := block(t, out, "function imp_CUST_v6()")
	if !strings.Contains(v6, `reject "no ipv6 prefixes are permitted by this policy";`) {
		t.Errorf("v6 function must reject everything, not silently accept:\n%s", v6)
	}
}

func TestIBGPTakesNoFilters(t *testing.T) {
	p := ebgpPeer()
	p.Name, p.Role, p.NeighborIP, p.RemoteASN = "core", store.RoleIBGP, "10.0.0.2", 65551
	in := baseInput()
	in.PrefixSets, in.Peers = bogonSets(), []store.Peer{p}
	out := mustRender(t, in)

	proto := block(t, out, "protocol bgp core {")
	if !strings.Contains(proto, "import all;") || !strings.Contains(proto, "export all;") {
		t.Error("an iBGP peer should import and export all")
	}
	if strings.Contains(out, "filter ebgp_in_core") {
		t.Error("no eBGP filter should be emitted for an iBGP peer")
	}
	// Attaching policies to iBGP is not supported yet; say so instead of
	// rendering something that silently ignores them.
	p.ImportPolicies = []store.Policy{sanityPolicy()}
	in.Peers = []store.Peer{p}
	if _, err := Config(in); err == nil {
		t.Error("expected an error when policies are attached to an iBGP peer")
	}
}

func TestPasswordMaskingAndAuth(t *testing.T) {
	p := ebgpPeer()
	p.Password = "s3cr3t-md5"
	in := baseInput()
	in.PrefixSets, in.Peers = bogonSets(), []store.Peer{p}

	in.MaskSecrets = true
	masked := mustRender(t, in)
	if strings.Contains(masked, "s3cr3t-md5") {
		t.Fatal("password leaked into a masked render")
	}
	if !strings.Contains(masked, `password "********";`) || !strings.Contains(masked, "authentication md5;") {
		t.Error("masked render should still show that an MD5 password is set")
	}

	in.MaskSecrets = false
	if !strings.Contains(mustRender(t, in), `password "s3cr3t-md5";`) {
		t.Error("unmasked render must carry the real password")
	}
}

func TestCommunitiesUseTheLocalASN(t *testing.T) {
	in := baseInput()
	out := mustRender(t, in)
	// AS65551 does not fit in half a standard community, so these must be large.
	for _, want := range []string{
		"define FROM_UPSTREAM = (65551, 1, 1000);",
		"define FROM_IX       = (65551, 1, 2000);",
		"define FROM_CUSTOMER = (65551, 1, 3000);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestDeterministicOrdering(t *testing.T) {
	a, b := ebgpPeer(), ebgpPeer()
	b.ID, b.Name, b.NeighborIP = 2, "aaa_first", "198.51.100.2"

	in := baseInput()
	in.PrefixSets = bogonSets()
	in.Peers = []store.Peer{a, b}
	first := mustRender(t, in)
	in.Peers = []store.Peer{b, a}
	if first != mustRender(t, in) {
		t.Error("render must not depend on input slice order")
	}
	if strings.Index(first, "protocol bgp aaa_first") > strings.Index(first, "protocol bgp edge_v4") {
		t.Error("peers should be emitted in name order")
	}
}

func TestIsPrivateASN(t *testing.T) {
	// With no list, the shipped defaults apply.
	for asn, want := range map[int64]bool{
		64511: false, 64512: true, 65534: true, 65535: false,
		4199999999: false, 4200000000: true, 4294967294: true, 4294967295: false,
		64497: false,
	} {
		if got := IsPrivateASN(nil, asn); got != want {
			t.Errorf("IsPrivateASN(nil, %d) = %v, want %v", asn, got, want)
		}
	}
	// An operator who marks a range private changes the answer.
	custom := []store.BogonASN{{Low: 100, High: 200, Private: true}}
	if !IsPrivateASN(custom, 150) {
		t.Error("a range marked private should be reported private")
	}
	if IsPrivateASN(custom, 64512) {
		t.Error("a range absent from the operator's list is not private")
	}
}

// The bogon ASN list is model data now, and a policy that checks the path must
// render whatever the operator put there.
func TestBogonASNsComeFromTheModel(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()
	in.BogonASNs = []store.BogonASN{
		{Low: 0, High: 0, Note: "reserved"},
		{Low: 64512, High: 65534, Private: true, Note: "private use"},
		{Low: 999999, High: 999999},
	}
	in.Policies = []store.Policy{{ID: 1, Name: "P", Direction: store.DirImport,
		DefaultRoute: store.DefaultReject, BogonASNs: store.BogonASNsAll}}
	out := mustRender(t, in)

	all := block(t, out, "define BOGON_ASNS = [")
	if !strings.Contains(all, "999999") || !strings.Contains(all, "64512..65534") {
		t.Errorf("the operator's list must be rendered verbatim: %s", all)
	}
	if !strings.Contains(all, "# private use") {
		t.Error("notes should survive into the config as comments")
	}
	except := block(t, out, "define BOGON_ASNS_EXCEPT_PRIVATE = [")
	if strings.Contains(except, "64512..65534") {
		t.Error("a range marked private must be excluded from the except-private set")
	}
	if !strings.Contains(except, "999999") {
		t.Error("non-private ranges belong in both sets")
	}
}

// Generated filters name BOGONS_V4/BOGONS_V6 as symbols. If the set is gone or
// empty, bird could not parse the result — say so rather than emit it.
func TestMissingOrEmptyBogonSetIsAnError(t *testing.T) {
	pol := store.Policy{ID: 1, Name: "P", Direction: store.DirImport,
		DefaultRoute: store.DefaultReject, RejectBogonPrefixes: true, BogonASNs: store.BogonASNsOff}

	in := baseInput()
	in.Policies = []store.Policy{pol}
	in.PrefixSets = nil // no bogon sets at all
	if _, err := Config(in); err == nil {
		t.Error("expected an error when a bogon set is missing")
	}

	in.PrefixSets = []store.PrefixSet{
		{ID: 1, Name: store.BogonSetV4, Family: store.FamilyV4, System: true},
		{ID: 2, Name: store.BogonSetV6, Family: store.FamilyV6, System: true, Entries: []store.PrefixEntry{{Prefix: "fc00::/7"}}},
	}
	if _, err := Config(in); err == nil {
		t.Error("expected an error when a bogon set is empty")
	}

	// A model with no policy that checks bogons does not need the sets.
	in.Policies = []store.Policy{{ID: 1, Name: "P", Direction: store.DirImport,
		DefaultRoute: store.DefaultReject, BogonASNs: store.BogonASNsOff}}
	in.PrefixSets = nil
	if _, err := Config(in); err != nil {
		t.Errorf("bogon sets should only be required when a policy uses them: %v", err)
	}
}

// The bug this fixes: iBGP used to render "import all; export all;" and nothing
// else, so a route learned from an eBGP peer was readvertised carrying that
// peer's address as its next hop. The far end has no route to it.
func TestIBGPRendersNextHopSelf(t *testing.T) {
	in := baseInput()
	p := store.Peer{Name: "ibgp_core", Role: store.RoleIBGP, Enabled: true,
		NeighborIP: "192.0.2.9", RemoteASN: in.LocalASN, NextHopSelf: true, ImportLimitAction: "restart"}
	in.Peers = []store.Peer{p}

	out, err := Config(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "next hop self;") {
		t.Errorf("iBGP channel must rewrite the next hop:\n%s", out)
	}
	// It belongs to the channel, not the protocol: BIRD 2 moved it there.
	if !strings.Contains(out, "\t\tnext hop self;\n") {
		t.Error("next hop self must sit inside the channel block")
	}

	p.NextHopSelf = false
	in.Peers = []store.Peer{p}
	out, err = Config(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "next hop self") {
		t.Error("an operator who turns it off must get BIRD's stock behaviour")
	}
}

func TestIBGPRouteReflector(t *testing.T) {
	in := baseInput()
	in.RRClusterID = "192.0.2.1"
	p := store.Peer{Name: "ibgp_client", Role: store.RoleIBGP, Enabled: true,
		NeighborIP: "192.0.2.9", RemoteASN: in.LocalASN, NextHopSelf: true,
		RRClient: true, ImportLimitAction: "restart"}
	in.Peers = []store.Peer{p}

	out, err := Config(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\trr client;\n", "\trr cluster id 192.0.2.1;\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}

	// An empty cluster ID lets BIRD fall back to the router ID.
	in.RRClusterID = ""
	out, _ = Config(in)
	if strings.Contains(out, "rr cluster id") {
		t.Error("no cluster id should be rendered when none is set")
	}
	if !strings.Contains(out, "rr client;") {
		t.Error("rr client stands on its own")
	}
}

// An eBGP peer must never pick up the iBGP-only options.
func TestEBGPHasNoIBGPOptions(t *testing.T) {
	in := baseInput()
	p := ebgpPeer()
	p.NextHopSelf, p.RRClient = true, true // as if a form had smuggled them in
	p.Validate()                           // Validate is where they are normalised away
	in.Peers = []store.Peer{p}

	out, err := Config(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "next hop self") || strings.Contains(out, "rr client") {
		t.Errorf("eBGP peer picked up iBGP options:\n%s", out)
	}
}

// Without a direct protocol BIRD has no connected routes: nothing to originate
// from, and no way to resolve a next hop on a peering subnet.
func TestRendersDirectProtocol(t *testing.T) {
	out, err := Config(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "protocol direct direct1 {") {
		t.Errorf("connected routes must be imported:\n%s", out)
	}
}

func TestRawConfigIsAppendedLast(t *testing.T) {
	in := baseInput()
	in.RawConfig = "protocol bfd {\n\tinterface \"eno1\";\n}"
	in.Peers = []store.Peer{ebgpPeer()}

	out, err := Config(in)
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Index(out, "protocol bfd")
	peer := strings.Index(out, "protocol bgp "+ebgpPeer().Name)
	if raw < 0 || peer < 0 || raw < peer {
		t.Errorf("raw config must come after everything birdy generated:\n%s", out)
	}
	if !strings.Contains(out, "birdy does not parse") {
		t.Error("the raw block should be labelled as unparsed")
	}
}

// A password inside the raw block is still a password.
func TestRawConfigIsMaskedToo(t *testing.T) {
	in := baseInput()
	in.MaskSecrets = true
	in.RawConfig = "protocol bgp x {\n\tpassword \"hunter2\";\n}"
	out, err := Config(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("raw block leaked a password into the browser:\n%s", out)
	}
}

// The anchor route is the prefix itself, never the prefix pattern. A "+" widens
// what the export filter matches; it must not widen what we originate.
func TestOriginateAnchorsThePrefixNotThePattern(t *testing.T) {
	in := baseInput()
	in.PrefixSets = []store.PrefixSet{{
		ID: 1, Name: "MY_AGGREGATES", Family: store.FamilyV4, Originate: true,
		OriginateAction: store.OriginateBlackhole,
		Entries:         []store.PrefixEntry{{Prefix: "192.0.2.0/24", Modifier: "+"}},
	}}
	out, err := Config(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\troute 192.0.2.0/24 blackhole;\n") {
		t.Errorf("anchor should be the bare prefix:\n%s", out)
	}
	if strings.Contains(out, "route 192.0.2.0/24+ blackhole") {
		t.Error("the modifier leaked into the static route")
	}
}

func TestOriginateAction(t *testing.T) {
	for _, action := range []string{store.OriginateBlackhole, store.OriginateUnreachable, store.OriginateProhibit} {
		in := baseInput()
		in.PrefixSets = []store.PrefixSet{{
			ID: 1, Name: "MY_AGGREGATES", Family: store.FamilyV4, Originate: true,
			OriginateAction: action,
			Entries:         []store.PrefixEntry{{Prefix: "192.0.2.0/24"}},
		}}
		out, err := Config(in)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "route 192.0.2.0/24 "+action+";") {
			t.Errorf("action %q not rendered:\n%s", action, out)
		}
	}
}

// A "+" on an announced aggregate accepts every more-specific inside it. That
// is how an internal /26 ends up on the internet.
func TestLintFlagsWideningModifierOnExport(t *testing.T) {
	in := baseInput()
	in.PrefixSets = []store.PrefixSet{{
		ID: 1, Name: "ANNOUNCE_V4", Family: store.FamilyV4,
		Entries: []store.PrefixEntry{{Prefix: "192.0.2.0/24", Modifier: "+"}},
	}}
	in.Policies = []store.Policy{{
		ID: 10, Name: "EXPORT_MINE", Direction: store.DirExport, SetIDs: []int64{1},
	}}

	var found bool
	for _, w := range Lint(in) {
		if strings.Contains(w.Message, "matches more-specifics inside 192.0.2.0/24") {
			found = true
		}
	}
	if !found {
		t.Errorf("a widening modifier on an export set should be flagged: %+v", Lint(in))
	}

	// Without the modifier the aggregate alone is announced, and nothing is wrong.
	in.PrefixSets[0].Entries[0].Modifier = ""
	for _, w := range Lint(in) {
		if strings.Contains(w.Message, "more-specifics") {
			t.Errorf("an exact-match export set should lint clean: %+v", w)
		}
	}
}

func TestStaticRoutesRender(t *testing.T) {
	in := baseInput()
	in.StaticRoutes = []store.StaticRoute{
		{Prefix: "192.0.2.128/26", Action: store.StaticVia, NextHop: "203.0.113.2", Enabled: true},
		{Prefix: "198.51.100.0/24", Action: store.OriginateBlackhole, Enabled: true},
		{Prefix: "2001:db8:dead::/48", Action: store.StaticVia, NextHop: "2001:db8:ffff::2", Enabled: true},
		{Prefix: "10.9.9.0/24", Action: store.OriginateProhibit, Enabled: false},
	}
	out, err := Config(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"protocol static static_v4 {",
		"route 192.0.2.128/26 via 203.0.113.2;",
		"route 198.51.100.0/24 blackhole;",
		"protocol static static_v6 {",
		"route 2001:db8:dead::/48 via 2001:db8:ffff::2;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	// A disabled route renders nothing.
	if strings.Contains(out, "10.9.9.0/24") {
		t.Error("a disabled static route should not render")
	}
	// v4 and v6 routes must land in the right protocol.
	v4 := out[strings.Index(out, "static_v4"):strings.Index(out, "static_v6")]
	if strings.Contains(v4, "2001:db8") {
		t.Error("a v6 route rendered into the v4 static protocol")
	}
}

// No static routes means no empty static protocol cluttering the config.
func TestNoStaticProtocolWhenNoRoutes(t *testing.T) {
	out, err := Config(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "protocol static static_v4") {
		t.Error("an empty static protocol should not be rendered")
	}
}

// A static route to the same prefix an aggregate originates is two protocols
// fighting over one net.
func TestLintFlagsStaticVsOriginateClash(t *testing.T) {
	in := baseInput()
	in.PrefixSets = []store.PrefixSet{{
		ID: 1, Name: "MY_AGGREGATES", Family: store.FamilyV4, Originate: true,
		OriginateAction: store.OriginateBlackhole,
		Entries:         []store.PrefixEntry{{Prefix: "192.0.2.0/24"}},
	}}
	in.StaticRoutes = []store.StaticRoute{
		{Prefix: "192.0.2.0/24", Action: store.StaticVia, NextHop: "10.0.0.2", Enabled: true},
	}
	var found bool
	for _, w := range Lint(in) {
		if strings.Contains(w.Message, "both a static route and an anchor") {
			found = true
		}
	}
	if !found {
		t.Errorf("a static/originate clash should be flagged: %+v", Lint(in))
	}
}
