// Package render turns birdy's database model into a complete bird.conf.
//
// The generated config follows common BGP hygiene practice (RFC 8212 default
// deny, the NLNOG BGP filter guide, MANRS). Policies are the unit of reuse:
//
//   - An import policy only ever rejects. A peer's import policies are called
//     in order, so they compose with AND: any one of them can veto a route.
//   - An export policy only ever accepts. A peer's export policies compose with
//     OR: a route is announced if any one of them permits it, and rejected at
//     the end of the filter if none did. That is RFC 8212 default deny.
//
// Each policy renders as a pair of BIRD functions, one per address family, so
// no filter ever compares an IPv4 prefix against an IPv6 set.
//
// Nothing here escapes or quotes untrusted input. Every value that reaches the
// output has already been checked by store.Peer.Validate, store.Policy.Validate
// or store.PrefixSet.Validate; names are restricted to BIRD symbols and
// addresses are re-serialised from netip.
package render

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

// MaskedPassword replaces session secrets when rendering for the screen.
const MaskedPassword = "********"

// Large-community tags recording where a route was learned. Large communities
// (not the classic 16:16 pair) because a 32-bit ASN does not fit in half of a
// standard community.
const (
	tagUpstream = 1000
	tagIX       = 2000
	tagCustomer = 3000
)

var roleTag = map[string]struct {
	value int
	name  string
}{
	store.RoleUpstream: {tagUpstream, "FROM_UPSTREAM"},
	store.RoleIXPeer:   {tagIX, "FROM_IX"},
	store.RoleCustomer: {tagCustomer, "FROM_CUSTOMER"},
}

type Input struct {
	RouterID    string
	LocalASN    int64
	PrefixSets  []store.PrefixSet
	ASSets      []store.ASSet
	Policies    []store.Policy
	Peers       []store.Peer
	RPKIServers []store.RPKIServer
	// BogonASNs is editable model data, not a constant: IANA keeps handing out
	// ranges. Empty means "use the shipped defaults".
	BogonASNs []store.BogonASN

	// RRClusterID is emitted beside "rr client". Empty lets BIRD use the router ID.
	RRClusterID string
	// RawConfig is appended verbatim at the end of the file. birdy neither
	// parses it nor knows what it does.
	RawConfig string

	// MaskSecrets renders passwords as MaskedPassword. Always set for anything
	// shown in the browser; never set for a config destined for disk.
	MaskSecrets bool

	Version   string
	Generated time.Time
}

type family struct {
	suffix   string // "v4" / "v6"
	channel  string // "ipv4" / "ipv6"
	bogonSet string
	roaTable string // the ROA table roa_check() consults
	anyRoute string // the default route literal
	bits     int
}

var (
	familyV4 = family{"v4", "ipv4", "BOGONS_V4", "rpki4", "0.0.0.0/0", 32}
	familyV6 = family{"v6", "ipv6", "BOGONS_V6", "rpki6", "::/0", 128}
)

// Config renders the complete bird.conf. The output is deterministic: given the
// same model it is byte-identical, which is what makes diffing meaningful.
func Config(in Input) (string, error) {
	if err := validate(in); err != nil {
		return "", err
	}

	sets := slices.Clone(in.PrefixSets)
	slices.SortFunc(sets, func(a, b store.PrefixSet) int { return strings.Compare(a.Name, b.Name) })
	policies := slices.Clone(in.Policies)
	slices.SortFunc(policies, func(a, b store.Policy) int { return strings.Compare(a.Name, b.Name) })
	peers := slices.Clone(in.Peers)
	slices.SortFunc(peers, func(a, b store.Peer) int { return strings.Compare(a.Name, b.Name) })

	setsByID := map[int64]store.PrefixSet{}
	for _, ps := range sets {
		setsByID[ps.ID] = ps
	}
	asSets := slices.Clone(in.ASSets)
	slices.SortFunc(asSets, func(a, b store.ASSet) int { return strings.Compare(a.Name, b.Name) })
	asSetsByID := map[int64]store.ASSet{}
	for _, as := range asSets {
		asSetsByID[as.ID] = as
	}

	bogons := in.BogonASNs
	if len(bogons) == 0 {
		bogons = store.DefaultBogonASNs()
	}
	if err := checkBogonSets(sets, policies); err != nil {
		return "", err
	}
	var rtr []store.RPKIServer
	for _, srv := range in.RPKIServers {
		if srv.Enabled {
			rtr = append(rtr, srv)
		}
	}
	slices.SortFunc(rtr, func(a, b store.RPKIServer) int { return strings.Compare(a.Name, b.Name) })
	if err := checkRPKI(policies, rtr); err != nil {
		return "", err
	}

	var b strings.Builder
	writeHeader(&b, in)
	writeGlobals(&b, in)
	writeCommunities(&b, in.LocalASN)
	writeSets(&b, sets)
	writeASSets(&b, asSets)
	writeBogonASNs(&b, bogons)
	writeRPKITables(&b, rtr)
	writeBaseProtocols(&b)
	writeRPKIProtocols(&b, rtr)
	if err := writeOriginators(&b, sets); err != nil {
		return "", err
	}
	for _, pol := range policies {
		if err := writePolicy(&b, in, pol, setsByID, asSetsByID); err != nil {
			return "", fmt.Errorf("policy %q: %w", pol.Name, err)
		}
	}
	for _, p := range peers {
		if err := writePeer(&b, in, p); err != nil {
			return "", fmt.Errorf("peer %q: %w", p.Name, err)
		}
	}
	writeRawConfig(&b, in)
	return b.String(), nil
}

func validate(in Input) error {
	id, err := netip.ParseAddr(strings.TrimSpace(in.RouterID))
	if err != nil || !id.Is4() {
		return fmt.Errorf("render: router id must be an IPv4 address, got %q", in.RouterID)
	}
	if in.LocalASN < 1 || in.LocalASN > 4294967295 {
		return fmt.Errorf("render: local ASN %d out of range", in.LocalASN)
	}
	return nil
}

func writeHeader(b *strings.Builder, in Input) {
	ts := in.Generated.UTC().Format(time.RFC3339)
	fmt.Fprintf(b, `#
# bird.conf generated by birdy %s at %s
#
# birdy owns this file and rewrites it in full from its database on every
# apply. Hand edits will be lost — change the model in the birdy UI instead.
#

`, in.Version, ts)
}

func writeGlobals(b *strings.Builder, in Input) {
	fmt.Fprintf(b, "log syslog all;\nrouter id %s;\n\ndefine LOCAL_ASN = %d;\n\n", in.RouterID, in.LocalASN)
}

func writeCommunities(b *strings.Builder, localASN int64) {
	fmt.Fprintf(b, `# Where a route came from, stamped on import and read on export. Large
# communities because a 32-bit ASN does not fit in half of a standard one.
define FROM_UPSTREAM = (%d, 1, %d);
define FROM_IX       = (%d, 1, %d);
define FROM_CUSTOMER = (%d, 1, %d);

# Tagged onto routes an RPKI ROA contradicts, when a policy validates in
# log-only mode. Count them in the looking glass before you start dropping them.
define RPKI_INVALID  = (%d, 2, 1);

`, localASN, tagUpstream, localASN, tagIX, localASN, tagCustomer, localASN)
}

func writeSets(b *strings.Builder, sets []store.PrefixSet) {
	for _, ps := range sets {
		if len(ps.Entries) == 0 {
			// BIRD has no syntax for an empty prefix set, and a set that
			// matches nothing is never what the operator meant.
			fmt.Fprintf(b, "# prefix set %s is empty and was not rendered\n\n", ps.Name)
			continue
		}
		if ps.Description != "" {
			fmt.Fprintf(b, "# %s\n", ps.Description)
		}
		fmt.Fprintf(b, "define %s = [\n", ps.Name)
		for i, e := range ps.Entries {
			sep := ","
			if i == len(ps.Entries)-1 {
				sep = ""
			}
			fmt.Fprintf(b, "\t%s%s\n", e.Pattern(), sep)
		}
		b.WriteString("];\n\n")
	}
}

// writeASSets renders each AS set as a BIRD integer set. This is where an IRR
// AS-SET ends up: BIRD cannot expand AS-CUSTOMER, so the members are expanded
// beforehand and written out as plain AS numbers.
func writeASSets(b *strings.Builder, sets []store.ASSet) {
	for _, as := range sets {
		if len(as.Entries) == 0 {
			fmt.Fprintf(b, "# AS set %s is empty and was not rendered\n\n", as.Name)
			continue
		}
		if as.Description != "" {
			fmt.Fprintf(b, "# %s\n", as.Description)
		}
		if as.Source != "" {
			fmt.Fprintf(b, "# Expanded from the IRR object %s.\n", as.Source)
		}
		fmt.Fprintf(b, "define %s = [\n", as.Name)
		for i, e := range as.Entries {
			sep := ","
			if i == len(as.Entries)-1 {
				sep = ""
			}
			note := ""
			if e.Note != "" {
				note = "  # " + e.Note
			}
			if e.Low == e.High {
				fmt.Fprintf(b, "\t%d%s%s\n", e.Low, sep, note)
			} else {
				fmt.Fprintf(b, "\t%d..%d%s%s\n", e.Low, e.High, sep, note)
			}
		}
		b.WriteString("];\n\n")
	}
}

func writeBogonASNs(b *strings.Builder, list []store.BogonASN) {
	writeASNSet(b, "BOGON_ASNS", "AS numbers that must never appear in a received AS path.", list)
	var exceptPrivate []store.BogonASN
	for _, r := range list {
		if !r.Private {
			exceptPrivate = append(exceptPrivate, r)
		}
	}
	writeASNSet(b, "BOGON_ASNS_EXCEPT_PRIVATE",
		"The same, minus the private ranges — for peers that legitimately use a private ASN.", exceptPrivate)
}

func writeASNSet(b *strings.Builder, name, comment string, ranges []store.BogonASN) {
	fmt.Fprintf(b, "# %s\ndefine %s = [\n", comment, name)
	for i, r := range ranges {
		sep := ","
		if i == len(ranges)-1 {
			sep = ""
		}
		note := ""
		if r.Note != "" {
			note = "  # " + r.Note
		}
		if r.Low == r.High {
			fmt.Fprintf(b, "\t%d%s%s\n", r.Low, sep, note)
		} else {
			fmt.Fprintf(b, "\t%d..%d%s%s\n", r.Low, r.High, sep, note)
		}
	}
	b.WriteString("];\n\n")
}

// checkRPKI refuses to render a config that validates against a ROA table
// nothing fills. BIRD would parse it happily and quietly treat every route as
// "unknown", which looks exactly like RPKI working and protects nothing.
func checkRPKI(policies []store.Policy, enabled []store.RPKIServer) error {
	if len(enabled) > 0 {
		return nil
	}
	for _, p := range policies {
		if p.ROV != "" && p.ROV != store.ROVOff {
			return fmt.Errorf("render: policy %q performs RPKI validation, but no RTR server is enabled — every route would be \"unknown\" and nothing would be checked", p.Name)
		}
	}
	return nil
}

func writeRPKITables(b *strings.Builder, enabled []store.RPKIServer) {
	if len(enabled) == 0 {
		return
	}
	b.WriteString("# ROA tables filled by the RTR servers below; roa_check() reads them.\nroa4 table rpki4;\nroa6 table rpki6;\n\n")
}

func writeRPKIProtocols(b *strings.Builder, enabled []store.RPKIServer) {
	for _, srv := range enabled {
		if srv.Description != "" {
			fmt.Fprintf(b, "# %s\n", srv.Description)
		}
		fmt.Fprintf(b, "protocol rpki %s {\n\troa4 { table rpki4; };\n\troa6 { table rpki6; };\n", srv.Name)
		// BIRD takes an address literal bare and a hostname quoted.
		if srv.IsIP() {
			fmt.Fprintf(b, "\tremote %s port %d;\n", srv.Host, srv.Port)
		} else {
			fmt.Fprintf(b, "\tremote \"%s\" port %d;\n", srv.Host, srv.Port)
		}
		// "keep" holds the previous value when the session drops, rather than
		// falling back to BIRD's default mid-outage.
		if srv.Refresh > 0 {
			fmt.Fprintf(b, "\trefresh keep %d;\n", srv.Refresh)
		}
		if srv.Retry > 0 {
			fmt.Fprintf(b, "\tretry keep %d;\n", srv.Retry)
		}
		if srv.Expire > 0 {
			fmt.Fprintf(b, "\texpire keep %d;\n", srv.Expire)
		}
		b.WriteString("}\n\n")
	}
}

func writeBaseProtocols(b *strings.Builder) {
	b.WriteString(`protocol device {
	scan time 10;
}

# Connected routes, one per address configured on an interface. Without this
# BIRD has no route to its own subnets: it cannot announce them, and it cannot
# resolve a BGP next hop that sits on one. BIRD skips loopback by itself, so
# 127.0.0.0/8 never enters the table.
protocol direct direct1 {
	ipv4;
	ipv6;
}

# Export BIRD's best routes into the kernel FIB; never learn from it.
protocol kernel kernel4 {
	ipv4 {
		import none;
		export all;
	};
}

protocol kernel kernel6 {
	ipv6 {
		import none;
		export all;
	};
}

`)
}

// writeRawConfig appends the operator's escape hatch. It goes last so it can
// override nothing birdy generated by accident of ordering, and so a stray
// unclosed brace breaks the parse check rather than swallowing a real protocol.
func writeRawConfig(b *strings.Builder, in Input) {
	raw := strings.TrimSpace(in.RawConfig)
	if raw == "" {
		return
	}
	if in.MaskSecrets {
		raw = MaskPasswords(raw)
	}
	b.WriteString("# ---------------------------------------------------------------------\n" +
		"# Raw configuration, appended verbatim from Settings. birdy does not parse\n" +
		"# or understand any of this; `bird -p` is the only check it gets.\n" +
		"# ---------------------------------------------------------------------\n")
	b.WriteString(raw)
	b.WriteString("\n")
}

// writeOriginators emits a static protocol per originating set. You must
// originate what you announce: an export filter that permits a prefix does
// nothing unless some protocol actually puts that route in the table.
func writeOriginators(b *strings.Builder, sets []store.PrefixSet) error {
	for _, ps := range sets {
		if !ps.Originate || len(ps.Entries) == 0 {
			continue
		}
		channel := "ipv4"
		if ps.Family == store.FamilyV6 {
			channel = "ipv6"
		}
		action := ps.OriginateAction
		if action == "" {
			action = store.OriginateBlackhole
		}
		fmt.Fprintf(b, "# Originate the prefixes in %s. The anchor keeps each one in the table\n"+
			"# whether or not its more-specifics are up, so the announcement never flaps —\n"+
			"# and it swallows traffic for unassigned space instead of sending it back out\n"+
			"# the default route.\nprotocol static originate_%s {\n\t%s;\n", ps.Name, ps.Name, channel)
		for _, e := range ps.Entries {
			// The anchor route is the prefix itself, never the pattern: a "+"
			// modifier widens what the filter matches, not what we originate.
			fmt.Fprintf(b, "\troute %s %s;\n", e.Prefix, action)
		}
		b.WriteString("}\n\n")
	}
	return nil
}

// writePolicy emits one function per address family.
func writePolicy(b *strings.Builder, in Input, pol store.Policy, sets map[int64]store.PrefixSet, asSets map[int64]store.ASSet) error {
	for _, fam := range []family{familyV4, familyV6} {
		if pol.Description != "" {
			fmt.Fprintf(b, "# %s\n", pol.Description)
		}
		fmt.Fprintf(b, "function %s\n{\n", policyFunc(pol, fam))
		var err error
		if pol.IsImport() {
			err = writeImportBody(b, in, pol, fam, sets, asSets)
		} else {
			err = writeExportBody(b, pol, fam, sets)
		}
		if err != nil {
			return err
		}
		b.WriteString("}\n\n")
	}
	return nil
}

func policyFunc(pol store.Policy, fam family) string {
	prefix := "exp_"
	if pol.IsImport() {
		prefix = "imp_"
	}
	return prefix + pol.Name + "_" + fam.suffix + "()"
}

// writeImportBody emits only rejects. Falling off the end means "no objection";
// the caller decides what to do with a route no policy rejected.
func writeImportBody(b *strings.Builder, in Input, pol store.Policy, fam family, sets map[int64]store.PrefixSet, asSets map[int64]store.ASSet) error {
	// An accept-only set names the entire universe this policy permits. If it
	// belongs to the other address family, then for this family the policy
	// permits nothing — rejecting is the only safe reading. Silently emitting
	// no rule would turn "accept only my customer's prefixes" into "accept
	// anything" on the peer's other channel.
	if pol.AcceptOnlySetID.Valid {
		ps, ok := sets[pol.AcceptOnlySetID.Int64]
		if !ok {
			return fmt.Errorf("accept-only prefix set %d not found", pol.AcceptOnlySetID.Int64)
		}
		if len(ps.Entries) == 0 {
			return fmt.Errorf("accept-only prefix set %q is empty", ps.Name)
		}
		if familyOf(ps) != fam.suffix {
			fmt.Fprintf(b, "\t# %s is %s, so this policy permits nothing here.\n", ps.Name, ps.Family)
			fmt.Fprintf(b, "\treject \"no %s prefixes are permitted by this policy\";\n", fam.channel)
			return nil
		}
	}

	switch pol.DefaultRoute {
	case store.DefaultReject:
		fmt.Fprintf(b, "\tif net = %s then reject \"default route not accepted\";\n", fam.anyRoute)
	case store.DefaultOnly:
		fmt.Fprintf(b, "\tif net != %s then reject \"only the default route is accepted\";\n", fam.anyRoute)
	case store.DefaultAccept:
		fmt.Fprintf(b, "\t# the default route is accepted like any other prefix\n")
	}

	minLen, maxLen := pol.MinLenV4, pol.MaxLenV4
	if fam.suffix == "v6" {
		minLen, maxLen = pol.MinLenV6, pol.MaxLenV6
	}
	// The default route is shorter than any sane minimum, so a policy that
	// accepts it must not then reject it on length.
	guardLen := pol.DefaultRoute != store.DefaultOnly && (minLen > 0 || maxLen > 0)
	if guardLen {
		var conds []string
		if minLen > 0 {
			conds = append(conds, fmt.Sprintf("net.len < %d", minLen))
		}
		if maxLen > 0 {
			conds = append(conds, fmt.Sprintf("net.len > %d", maxLen))
		}
		cond := strings.Join(conds, " || ")
		if pol.DefaultRoute == store.DefaultAccept {
			fmt.Fprintf(b, "\tif net != %s && (%s) then reject \"prefix length out of bounds\";\n", fam.anyRoute, cond)
		} else {
			fmt.Fprintf(b, "\tif %s then reject \"prefix length out of bounds\";\n", cond)
		}
	}

	if pol.RejectBogonPrefixes {
		fmt.Fprintf(b, "\tif net ~ %s then reject \"bogon prefix\";\n", fam.bogonSet)
	}
	if pol.AcceptOnlySetID.Valid {
		name := sets[pol.AcceptOnlySetID.Int64].Name
		fmt.Fprintf(b, "\tif ! (net ~ %s) then reject \"not in %s\";\n", name, name)
	}
	// bgp_path.last is the AS that originated the route. Restricting it to the
	// members of an expanded IRR AS-SET is how a transit provider says "announce
	// your own prefixes and your downstreams', and nobody else's".
	if pol.OriginASSetID.Valid {
		as, ok := asSets[pol.OriginASSetID.Int64]
		if !ok {
			return fmt.Errorf("origin AS set %d not found", pol.OriginASSetID.Int64)
		}
		if len(as.Entries) == 0 {
			return fmt.Errorf("origin AS set %q is empty", as.Name)
		}
		fmt.Fprintf(b, "\tif ! (bgp_path.last ~ %s) then reject \"origin AS not in %s\";\n", as.Name, as.Name)
	}
	// RPKI route-origin validation. roa_check compares the route's origin AS
	// against the ROAs its prefix holder published. Invalid means the origin is
	// contradicted, not merely absent — "unknown" is the majority of the table
	// and must never be dropped.
	switch pol.ROV {
	case store.ROVReject:
		fmt.Fprintf(b, "\tif roa_check(%s, net, bgp_path.last) = ROA_INVALID then reject \"RPKI invalid\";\n", fam.roaTable)
	case store.ROVLog:
		fmt.Fprintf(b, "\t# log only: invalid routes are tagged, not dropped\n")
		fmt.Fprintf(b, "\tif roa_check(%s, net, bgp_path.last) = ROA_INVALID then bgp_large_community.add(RPKI_INVALID);\n", fam.roaTable)
	}
	if pol.MaxASPathLen > 0 {
		fmt.Fprintf(b, "\tif bgp_path.len > %d then reject \"AS path too long\";\n", pol.MaxASPathLen)
	}
	if pol.RejectOwnASN {
		fmt.Fprintf(b, "\tif bgp_path ~ [%d] then reject \"our own ASN in AS path\";\n", in.LocalASN)
	}
	switch pol.BogonASNs {
	case store.BogonASNsAll:
		b.WriteString("\tif bgp_path ~ BOGON_ASNS then reject \"bogon ASN in AS path\";\n")
	case store.BogonASNsExceptPrivate:
		b.WriteString("\tif bgp_path ~ BOGON_ASNS_EXCEPT_PRIVATE then reject \"bogon ASN in AS path\";\n")
	}
	if pol.SetLocalPref > 0 {
		fmt.Fprintf(b, "\tbgp_local_pref = %d;\n", pol.SetLocalPref)
	}
	return nil
}

// writeExportBody emits only accepts (plus an optional bogon veto). Falling off
// the end means "not permitted by this policy"; the caller rejects.
func writeExportBody(b *strings.Builder, pol store.Policy, fam family, sets map[int64]store.PrefixSet) error {
	if pol.RejectBogonPrefixes {
		fmt.Fprintf(b, "\tif net ~ %s then reject \"bogon prefix\";\n", fam.bogonSet)
	}
	if pol.AnnounceEverything {
		b.WriteString("\taccept;\n")
		return nil
	}
	if pol.AnnounceDefault {
		fmt.Fprintf(b, "\tif net = %s then accept;\n", fam.anyRoute)
	}
	for _, id := range pol.SetIDs {
		ps, ok := sets[id]
		if !ok {
			return fmt.Errorf("prefix set %d not found", id)
		}
		if familyOf(ps) != fam.suffix {
			continue // a v6 set says nothing about the v4 filter
		}
		// An empty set is not an error here: birdy ships ANNOUNCE_V4/V6 empty so
		// a fresh install has somewhere to put its aggregates. It simply permits
		// nothing until it is filled, and Lint says so.
		if len(ps.Entries) == 0 {
			fmt.Fprintf(b, "\t# %s is empty, so it permits nothing yet\n", ps.Name)
			continue
		}
		fmt.Fprintf(b, "\tif net ~ %s then accept;\n", ps.Name)
	}
	if pol.AnnounceFromUpstream {
		b.WriteString("\tif FROM_UPSTREAM ~ bgp_large_community then accept;\n")
	}
	if pol.AnnounceFromIX {
		b.WriteString("\tif FROM_IX ~ bgp_large_community then accept;\n")
	}
	if pol.AnnounceFromCustomer {
		b.WriteString("\tif FROM_CUSTOMER ~ bgp_large_community then accept;\n")
	}
	return nil
}

func familyOf(ps store.PrefixSet) string {
	if ps.Family == store.FamilyV6 {
		return "v6"
	}
	return "v4"
}

func writePeer(b *strings.Builder, in Input, p store.Peer) error {
	fam := familyV4
	if p.IsV6() {
		fam = familyV6
	}

	hasImport := len(p.ImportPolicies) > 0
	hasExport := len(p.ExportPolicies) > 0

	if !p.IsIBGP() {
		writePeerImportFilter(b, in, p, fam)
		if hasExport {
			writePeerExportFilter(b, p, fam)
		}
	} else if hasImport || hasExport {
		return fmt.Errorf("iBGP sessions do not take policies yet")
	}

	fmt.Fprintf(b, "protocol bgp %s {\n", p.Name)
	if !p.Enabled {
		b.WriteString("\tdisabled;\n")
	}
	if p.Description != "" {
		fmt.Fprintf(b, "\tdescription \"%s\";\n", p.Description)
	}
	if p.LocalIP != "" {
		fmt.Fprintf(b, "\tlocal %s as LOCAL_ASN;\n", p.LocalIP)
	} else {
		b.WriteString("\tlocal as LOCAL_ASN;\n")
	}
	fmt.Fprintf(b, "\tneighbor %s as %d;\n", p.NeighborIP, p.RemoteASN)
	if p.Multihop > 0 {
		fmt.Fprintf(b, "\tmultihop %d;\n", p.Multihop)
	}
	if p.Passive {
		b.WriteString("\tpassive;\n")
	}
	if p.Password != "" {
		pw := p.Password
		if in.MaskSecrets {
			pw = MaskedPassword
		}
		fmt.Fprintf(b, "\tpassword \"%s\";\n", pw)
		// BIRD warns "Missing authentication option, assuming MD5" if the
		// algorithm is left implicit. Say it, rather than lean on a default
		// that a future BIRD release is free to change.
		b.WriteString("\tauthentication md5;\n")
	}

	if p.IsIBGP() && p.RRClient {
		// Plain iBGP never readvertises an iBGP route to another iBGP peer, so a
		// mesh has to be full. A reflector is the alternative.
		b.WriteString("\trr client;\n")
		if in.RRClusterID != "" {
			fmt.Fprintf(b, "\trr cluster id %s;\n", in.RRClusterID)
		}
	}

	fmt.Fprintf(b, "\t%s {\n", fam.channel)
	if p.IsIBGP() {
		// iBGP peers are inside our own trust boundary and carry routes we
		// already filtered at the edge, tags and all.
		b.WriteString("\t\timport all;\n\t\texport all;\n")
		if p.NextHopSelf {
			// Without this, a route learned from an eBGP peer is readvertised
			// carrying that peer's address as its next hop. The router at the far
			// end of this session has no route to it, and the traffic is dropped.
			b.WriteString("\t\tnext hop self;\n")
		}
	} else {
		fmt.Fprintf(b, "\t\timport filter ebgp_in_%s;\n", p.Name)
		if hasExport {
			fmt.Fprintf(b, "\t\texport filter ebgp_out_%s;\n", p.Name)
		} else {
			// RFC 8212: no export policy means no announcements.
			b.WriteString("\t\texport none;\n")
		}
	}
	if p.ImportLimit > 0 {
		fmt.Fprintf(b, "\t\timport limit %d action %s;\n", p.ImportLimit, p.ImportLimitAction)
	}
	b.WriteString("\t};\n}\n\n")
	return nil
}

func writePeerImportFilter(b *strings.Builder, in Input, p store.Peer, fam family) {
	fmt.Fprintf(b, "filter ebgp_in_%s\n{\n", p.Name)
	if p.EnforceFirstAS {
		fmt.Fprintf(b, "\t# The peer must be the first AS in the path. Turn this off for an\n"+
			"\t# IXP route server, which does not prepend itself.\n"+
			"\tif bgp_path.first != %d then reject \"first AS is not the peer AS\";\n", p.RemoteASN)
	}
	// Transit for this peer, but not for anyone behind them: the route's origin
	// AS must be the peer itself. Prepending still works, because the origin is
	// the last ASN in the path, not the first.
	if p.OriginPeerOnly {
		fmt.Fprintf(b, "\tif bgp_path.last != %d then reject \"prefix not originated by this peer\";\n", p.RemoteASN)
	}
	// A peer must never be able to pretend a route came from somewhere else by
	// sending it to us pre-tagged with one of our own large communities.
	fmt.Fprintf(b, "\tbgp_large_community.delete([(%d, *, *)]);\n", in.LocalASN)

	for _, pol := range p.ImportPolicies {
		fmt.Fprintf(b, "\t%s;\n", policyFunc(pol, fam))
	}
	if tag, ok := roleTag[p.Role]; ok {
		fmt.Fprintf(b, "\tbgp_large_community.add(%s);\n", tag.name)
	}
	b.WriteString("\taccept;\n}\n\n")
}

func writePeerExportFilter(b *strings.Builder, p store.Peer, fam family) {
	fmt.Fprintf(b, "filter ebgp_out_%s\n{\n", p.Name)
	for _, pol := range p.ExportPolicies {
		fmt.Fprintf(b, "\t%s;\n", policyFunc(pol, fam))
	}
	b.WriteString("\treject \"not permitted by any export policy\";\n}\n\n")
}

// IsPrivateASN reports whether asn falls in one of the ranges the operator has
// marked private. Used to warn when a peer's own ASN would be rejected by its
// own import policy.
func IsPrivateASN(list []store.BogonASN, asn int64) bool {
	if len(list) == 0 {
		list = store.DefaultBogonASNs()
	}
	for _, r := range list {
		if r.Private && asn >= r.Low && asn <= r.High {
			return true
		}
	}
	return false
}

// checkBogonSets refuses to emit a filter that names a bogon set which is
// missing or empty. Generated filters reference BOGONS_V4/BOGONS_V6 by symbol,
// so an absent define is a config bird cannot parse — better to say so here.
func checkBogonSets(sets []store.PrefixSet, policies []store.Policy) error {
	needed := false
	for _, p := range policies {
		if p.RejectBogonPrefixes {
			needed = true
		}
	}
	if !needed {
		return nil
	}
	for _, name := range []string{store.BogonSetV4, store.BogonSetV6} {
		found := false
		for _, ps := range sets {
			if ps.Name != name {
				continue
			}
			found = true
			if len(ps.Entries) == 0 {
				return fmt.Errorf("render: %s is empty, but a policy rejects bogon prefixes; edit it under Settings", name)
			}
		}
		if !found {
			return fmt.Errorf("render: %s is missing, but a policy rejects bogon prefixes", name)
		}
	}
	return nil
}
