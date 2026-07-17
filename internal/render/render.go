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

// bgpRoleName maps a peer's relationship role to the RFC 9234 role BIRD sends as
// `local role`. The role is our own position in the relationship: an upstream
// sells us transit, so to them we are a customer; a customer buys transit from
// us, so to them we are a provider; an IX peer is a lateral peer. iBGP has no
// entry — RFC 9234 roles are eBGP only.
var bgpRoleName = map[string]string{
	store.RoleUpstream: "customer",
	store.RoleCustomer: "provider",
	store.RoleIXPeer:   "peer",
}

// Input is everything the renderer needs from the model to produce a complete
// bird.conf: the router identity, the peers, the library (sets, policies), RPKI
// and BMP config, and rendering options like secret masking.
type Input struct {
	RouterID   string
	LocalASN   int64
	PrefixSets []store.PrefixSet
	ASSets     []store.ASSet
	Policies   []store.Policy
	Peers      []store.Peer
	// Communities are named community definitions from the library; each renders
	// to a BIRD `define`, available as a symbol in the raw block.
	Communities []store.CommunityDef
	RPKIServers []store.RPKIServer
	// BMPStations are RFC 7854 monitoring collectors BIRD streams session state
	// to. Rendered as standalone protocols; they reference nothing else birdy
	// generates.
	BMPStations []store.BMPStation
	// BogonASNs is editable model data, not a constant: IANA keeps handing out
	// ranges. Empty means "use the shipped defaults".
	BogonASNs []store.BogonASN

	// StaticRoutes is reachability no protocol discovers on its own.
	StaticRoutes []store.StaticRoute

	// RRClusterID is emitted beside "rr client". Empty lets BIRD use the router ID.
	RRClusterID string

	// KernelPrefSrcV4 and KernelPrefSrcV6 pin krt_prefsrc on Birdy-originated
	// static routes exported to kernel4 / kernel6. Imported BGP routes are never
	// installed into the host FIB by the generated kernel protocols.
	KernelPrefSrcV4   string
	KernelPrefSrcV6   string
	KernelExportBGPV4 bool
	KernelExportBGPV6 bool
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

// Section is one logical unit of the rendered bird.conf — the globals block, one
// policy's functions, one peer's filters-and-protocol, the raw block, and so on.
// Concatenating every section's Body in order reproduces exactly what Config
// returns; the split exists so the UI can show a per-unit diff of a large config
// instead of one undifferentiated file.
type Section struct {
	// Path is a stable identifier that doubles as a tree path: "globals",
	// "peers/edge1", "policies/IMPORT_SANITY". The part before the first slash
	// groups related sections in the UI.
	Path string
	// Title is the human label for the section.
	Title string
	// Body is the rendered text, already carrying the same trailing blank-line
	// spacing it had when it was one concatenated file.
	Body string
}

// Config renders the whole bird.conf. It is exactly the concatenation of every
// section's body, so anything that hashes or diffs the file sees identical bytes
// whether it goes through Config or Sections.
func Config(in Input) (string, error) {
	secs, err := Sections(in)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, s := range secs {
		b.WriteString(s.Body)
	}
	return b.String(), nil
}

// Sections renders the config as an ordered list of named units. Each writer is
// captured into its own buffer; because the writers only ever append (none reads
// the builder), the concatenation is byte-for-byte what a single shared builder
// produced. Sections with no output are dropped — an empty body contributes
// nothing to the file and would only be noise in the tree.
func Sections(in Input) ([]Section, error) {
	if err := validate(in); err != nil {
		return nil, err
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
		return nil, err
	}
	var rtr []store.RPKIServer
	for _, srv := range in.RPKIServers {
		if srv.Enabled {
			rtr = append(rtr, srv)
		}
	}
	slices.SortFunc(rtr, func(a, b store.RPKIServer) int { return strings.Compare(a.Name, b.Name) })
	if err := checkRPKI(policies, rtr); err != nil {
		return nil, err
	}
	statics := slices.Clone(in.StaticRoutes)
	slices.SortFunc(statics, func(a, b store.StaticRoute) int { return strings.Compare(a.Prefix, b.Prefix) })
	bmpStations := slices.Clone(in.BMPStations)
	slices.SortFunc(bmpStations, func(a, b store.BMPStation) int { return strings.Compare(a.Name, b.Name) })

	var secs []Section
	var ferr error
	add := func(path, title string, fn func(*strings.Builder) error) {
		if ferr != nil {
			return
		}
		var b strings.Builder
		if err := fn(&b); err != nil {
			ferr = err
			return
		}
		if b.Len() == 0 {
			return
		}
		secs = append(secs, Section{Path: path, Title: title, Body: b.String()})
	}

	add("header", "Header", func(b *strings.Builder) error { writeHeader(b, in); return nil })
	add("globals", "Router identity & logging", func(b *strings.Builder) error { writeGlobals(b, in); return nil })
	add("communities", "Communities", func(b *strings.Builder) error { writeCommunities(b, in); return nil })
	add("sets/prefixes", "Prefix sets", func(b *strings.Builder) error { writeSets(b, sets); return nil })
	add("sets/as", "AS sets", func(b *strings.Builder) error { writeASSets(b, asSets); return nil })
	add("bogons/asns", "Bogon AS numbers", func(b *strings.Builder) error { writeBogonASNs(b, bogons); return nil })
	add("rpki/tables", "RPKI ROA tables", func(b *strings.Builder) error { writeRPKITables(b, rtr); return nil })
	add("protocols/base", "Device, direct & kernel", func(b *strings.Builder) error { writeBaseProtocols(b, in); return nil })
	add("protocols/bfd", "BFD", func(b *strings.Builder) error { writeBFDProtocol(b, peers); return nil })
	add("protocols/bmp", "BMP monitoring stations", func(b *strings.Builder) error { writeBMP(b, bmpStations); return nil })
	add("rpki/servers", "RPKI RTR servers", func(b *strings.Builder) error { writeRPKIProtocols(b, rtr); return nil })
	add("sets/originators", "Originated prefixes", func(b *strings.Builder) error { return writeOriginators(b, sets) })
	add("static", "Static routes", func(b *strings.Builder) error { writeStaticRoutes(b, statics); return nil })
	for _, pol := range policies {
		add("policies/"+pol.Name, "Policy "+pol.Name, func(b *strings.Builder) error {
			if err := writePolicy(b, in, pol, setsByID, asSetsByID); err != nil {
				return fmt.Errorf("policy %q: %w", pol.Name, err)
			}
			return nil
		})
	}
	for _, p := range peers {
		add("peers/"+p.Name, "Peer "+p.Name, func(b *strings.Builder) error {
			if err := writePeer(b, in, p); err != nil {
				return fmt.Errorf("peer %q: %w", p.Name, err)
			}
			return nil
		})
	}
	add("raw", "Raw configuration", func(b *strings.Builder) error { writeRawConfig(b, in); return nil })

	if ferr != nil {
		return nil, ferr
	}
	return secs, nil
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
	// No generation timestamp: the rendered config must be a pure function of the
	// model, so re-rendering an unchanged model yields identical bytes. The apply
	// pipeline hashes this output to decide whether the router is in sync and
	// whether birdy still owns the file on disk — a wall-clock stamp would make
	// every render look different a second later.
	fmt.Fprintf(b, `#
# bird.conf generated by birdy %s
#
# birdy owns this file and rewrites it in full from its database on every
# apply. Hand edits will be lost — change the model in the birdy UI instead.
#

`, in.Version)
}

func writeGlobals(b *strings.Builder, in Input) {
	fmt.Fprintf(b, "log syslog all;\nrouter id %s;\n\ndefine LOCAL_ASN = %d;\n\n", in.RouterID, in.LocalASN)
}

func writeCommunities(b *strings.Builder, in Input) {
	localASN := in.LocalASN
	fmt.Fprintf(b, `# Where a route came from, stamped on import and read on export. Large
# communities because a 32-bit ASN does not fit in half of a standard one.
define FROM_UPSTREAM = (%d, 1, %d);
define FROM_IX       = (%d, 1, %d);
define FROM_CUSTOMER = (%d, 1, %d);

# Tagged onto routes an RPKI ROA contradicts, when a policy validates in
# log-only mode. Count them in the looking glass before you start dropping them.
define RPKI_INVALID  = (%d, 2, 1);

`, localASN, tagUpstream, localASN, tagIX, localASN, tagCustomer, localASN)

	// Named communities from the library. Defined once here, usable by name in
	// the raw-config block, and a single documented home for the operator's scheme.
	defs := slices.Clone(in.Communities)
	if len(defs) == 0 {
		return
	}
	slices.SortFunc(defs, func(a, c store.CommunityDef) int { return strings.Compare(a.Name, c.Name) })
	width := 0
	for _, d := range defs {
		if len(d.Name) > width {
			width = len(d.Name)
		}
	}
	b.WriteString("# Named communities from the library.\n")
	for _, d := range defs {
		fmt.Fprintf(b, "define %-*s = %s;", width, d.Name, d.Value().BIRD())
		if d.Description != "" {
			fmt.Fprintf(b, "\t# %s", d.Description)
		}
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}

func writeSets(b *strings.Builder, sets []store.PrefixSet) {
	for _, ps := range sets {
		if ps.Disabled {
			// Switched off in the library: the define is withheld on purpose. A
			// filter that still names it will fail the pre-apply `bird -p`, which
			// is the intended cue to remove the reference.
			fmt.Fprintf(b, "# prefix set %s is disabled and was not rendered\n\n", ps.Name)
			continue
		}
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

// writeBMP emits one protocol per enabled monitoring station. BIRD's BMP
// exporter picks up every BGP session by itself, so the station block only says
// where to send the stream and which RIB views to include. A disabled station is
// not rendered at all, matching how RPKI servers behave.
func writeBMP(b *strings.Builder, stations []store.BMPStation) {
	for _, st := range stations {
		if !st.Enabled {
			continue
		}
		if st.Description != "" {
			fmt.Fprintf(b, "# %s\n", st.Description)
		}
		fmt.Fprintf(b, "protocol bmp %s {\n", st.Name)
		fmt.Fprintf(b, "\tstation address ip %s port %d;\n", st.Address, st.Port)
		if st.PrePolicy {
			b.WriteString("\tmonitoring rib in pre_policy;\n")
		}
		if st.PostPolicy {
			b.WriteString("\tmonitoring rib in post_policy;\n")
		}
		if st.TxBufferLimit > 0 {
			fmt.Fprintf(b, "\ttx buffer limit %d;\n", st.TxBufferLimit)
		}
		b.WriteString("}\n\n")
	}
}

func writeBaseProtocols(b *strings.Builder, in Input) {
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

# Kernel route installation is opt-in. Never learn from the FIB, and never
# install imported BGP routes into it by default.
protocol kernel kernel4 {
	ipv4 {
		import none;
`)
	writeKernelExport(b, in.KernelPrefSrcV4, in.KernelExportBGPV4)
	b.WriteString(`	};
}

protocol kernel kernel6 {
	ipv6 {
		import none;
`)
	writeKernelExport(b, in.KernelPrefSrcV6, in.KernelExportBGPV6)
	b.WriteString(`	};
}

`)
}

// writeKernelExport admits only explicitly selected route sources. A preferred
// source opts in Birdy-originated static routes; exportBGP opts in the selected
// BGP route for each prefix. No mode emits a blanket export.
func writeKernelExport(b *strings.Builder, prefSrc string, exportBGP bool) {
	if prefSrc == "" && !exportBGP {
		b.WriteString("\t\texport none;\n")
		return
	}
	b.WriteString("\t\texport filter {\n")
	if exportBGP {
		b.WriteString("\t\t\tif source = RTS_BGP then {\n")
		if prefSrc != "" {
			fmt.Fprintf(b, "\t\t\t\tkrt_prefsrc = %s;\n", prefSrc)
		}
		b.WriteString("\t\t\t\taccept;\n\t\t\t}\n")
	}
	if prefSrc != "" {
		fmt.Fprintf(b, "\t\t\tif source = RTS_STATIC then {\n\t\t\t\tkrt_prefsrc = %s;\n\t\t\t\taccept;\n\t\t\t}\n", prefSrc)
	}
	b.WriteString("\t\t\treject;\n\t\t};\n")
}

// writeBFDProtocol emits a single BFD protocol when any enabled peer uses it.
// A bare protocol picks up every session that asks for BFD with "bfd;".
func writeBFDProtocol(b *strings.Builder, peers []store.Peer) {
	for _, p := range peers {
		if p.BFD {
			b.WriteString("# Bidirectional Forwarding Detection for the sessions that enable it.\nprotocol bfd bfd1 {\n}\n\n")
			return
		}
	}
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
		if ps.Disabled || !ps.Originate || len(ps.Entries) == 0 {
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

// writeStaticRoutes emits one static protocol per address family, holding the
// routes birdy cannot derive from anything else. A protocol with no routes is
// not rendered at all: BIRD accepts an empty static, but an operator reading the
// config should not have to wonder what it is for.
func writeStaticRoutes(b *strings.Builder, routes []store.StaticRoute) {
	for _, fam := range []family{familyV4, familyV6} {
		var mine []store.StaticRoute
		for _, r := range routes {
			if r.Enabled && r.Family() == fam.channel {
				mine = append(mine, r)
			}
		}
		if len(mine) == 0 {
			continue
		}
		fmt.Fprintf(b, "# Routes no protocol discovers on its own.\nprotocol static static_%s {\n\t%s;\n", fam.suffix, fam.channel)
		for _, r := range mine {
			if r.Description != "" {
				fmt.Fprintf(b, "\t# %s\n", r.Description)
			}
			if r.Action == store.StaticVia {
				// The route stays down until BIRD can resolve this next hop
				// against something else in the table.
				fmt.Fprintf(b, "\troute %s via %s;\n", r.Prefix, r.NextHop)
				continue
			}
			fmt.Fprintf(b, "\troute %s %s;\n", r.Prefix, r.Action)
		}
		b.WriteString("}\n\n")
	}
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
			err = writeExportBody(b, in, pol, fam, sets)
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
		switch {
		case ps.Disabled:
			// The allow-list this policy is built on is switched off. Fail closed —
			// permit nothing — rather than dropping the check, which would turn
			// "accept only these prefixes" into "accept anything" and leak the table.
			fmt.Fprintf(b, "\t# %s is disabled, so this policy permits nothing here.\n", ps.Name)
			fmt.Fprintf(b, "\treject \"%s is disabled; no prefixes are permitted\";\n", ps.Name)
			return nil
		case len(ps.Entries) == 0:
			return fmt.Errorf("accept-only prefix set %q is empty", ps.Name)
		case familyOf(ps) != fam.suffix:
			fmt.Fprintf(b, "\t# %s is %s, so this policy permits nothing here.\n", ps.Name, ps.Family)
			fmt.Fprintf(b, "\treject \"no %s prefixes are permitted by this policy\";\n", fam.channel)
			return nil
		}
	}

	// RFC 7999 blackhole: a host route tagged BLACKHOLE (65535, 666) is accepted
	// past the prefix-length filter and turned into a discard, so a customer can
	// null-route a single address under attack. It must come before the length
	// reject that would otherwise drop the /32 or /128.
	if pol.AcceptBlackhole {
		fmt.Fprintf(b, "\tif (65535, 666) ~ bgp_community && net.len = %d then {\n"+
			"\t\tdest = RTD_BLACKHOLE;\n\t\taccept;\n\t}\n", fam.bits)
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
	// An import policy rejects routes carrying the matched community — a blocklist
	// signal, e.g. a peer or customer tagging routes you agreed not to accept.
	if ref, ok, _ := store.ParseMatchCommunityRef(pol.MatchCommunity); ok {
		attr, expr := communityRefExpr(ref, in.communityByName())
		fmt.Fprintf(b, "\tif %s ~ %s then reject \"matched community %s\";\n", expr, attr, pol.MatchCommunity)
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
func writeExportBody(b *strings.Builder, in Input, pol store.Policy, fam family, sets map[int64]store.PrefixSet) error {
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
		// A disabled set is dropped from what this policy announces — the same as
		// if it were unlisted. Announcing less is fail-safe, so no error: the set's
		// own define is withheld too, so emitting the reference would break the parse.
		if ps.Disabled {
			fmt.Fprintf(b, "\t# %s is disabled, so it is not announced\n", ps.Name)
			continue
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
	// An export policy accepts routes carrying the matched community — how a
	// customer signals "announce this one" with a community you publish.
	if ref, ok, _ := store.ParseMatchCommunityRef(pol.MatchCommunity); ok {
		attr, expr := communityRefExpr(ref, in.communityByName())
		fmt.Fprintf(b, "\tif %s ~ %s then accept;\n", expr, attr)
	}
	return nil
}

// communityVar is the BIRD attribute a community of this width lives in.
func communityVar(c store.Community) string {
	if c.Large {
		return "bgp_large_community"
	}
	return "bgp_community"
}

// communityByName indexes the library's communities for name resolution.
func (in Input) communityByName() map[string]store.CommunityDef {
	m := make(map[string]store.CommunityDef, len(in.Communities))
	for _, cd := range in.Communities {
		m[cd.Name] = cd
	}
	return m
}

// communityRefExpr resolves one community reference to the BIRD attribute it
// lives in and the expression to use — a named reference renders as the define
// symbol (so the config reads by name), a literal as the tuple. A name that
// resolves to nothing defaults to the standard attribute; the apply pre-flight
// (bird -p) catches a symbol that is not defined, and referenced communities
// cannot be deleted.
func communityRefExpr(ref store.CommunityRef, comms map[string]store.CommunityDef) (attr, expr string) {
	if ref.Name != "" {
		attr = "bgp_community"
		if def, ok := comms[ref.Name]; ok && def.Large {
			attr = "bgp_large_community"
		}
		return attr, ref.Name
	}
	return communityVar(ref.Value), ref.Value.BIRD()
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
			writePeerExportFilter(b, in, p, fam)
		}
	} else {
		// An iBGP session without policies carries everything, which is the
		// conventional full-mesh config and stays byte-identical to what birdy
		// rendered before policies were allowed here. With a chain attached, it gets
		// filters — because "carry everything" is wrong the moment one router has an
		// upstream the other should not inherit.
		if hasImport {
			writePeerImportFilter(b, in, p, fam)
		}
		if hasExport {
			writePeerExportFilter(b, in, p, fam)
		}
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
	if p.Interface != "" {
		fmt.Fprintf(b, "\tinterface \"%s\";\n", p.Interface)
	}
	if p.BGPRole {
		if role, ok := bgpRoleName[p.Role]; ok {
			// RFC 9234: BIRD tags exported routes with the Only-To-Customer
			// attribute and rejects imports that carry it in a way that would be a
			// route leak — leak prevention negotiated in the protocol itself.
			fmt.Fprintf(b, "\tlocal role %s;\n", role)
		}
	}
	if p.Multihop > 0 {
		fmt.Fprintf(b, "\tmultihop %d;\n", p.Multihop)
	}
	if p.GTSM {
		// GTSM (RFC 5082): send with a maximal TTL and drop received packets whose
		// TTL is lower than expected, so an off-path attacker cannot spoof the
		// session. For a multihop peer the hop count above sets the expected TTL.
		b.WriteString("\tttl security on;\n")
	}
	if p.Passive {
		b.WriteString("\tpassive;\n")
	}
	if p.BFD {
		// Sub-second failure detection: BIRD tears the session down the moment BFD
		// stops hearing the neighbour, rather than waiting out the hold timer.
		b.WriteString("\tbfd;\n")
	}
	// graceful restart "aware" is BIRD's own default, so only the explicit
	// on/off choices are written.
	switch p.GracefulRestart {
	case store.GROn:
		b.WriteString("\tgraceful restart on;\n")
	case store.GROff:
		b.WriteString("\tgraceful restart off;\n")
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
		// Inside our own trust boundary the default is to carry everything: routes
		// crossing an internal session were already filtered at the edge, tags and
		// all. But "everything" includes a default route learned from an upstream —
		// and a router that reaches this one through a tunnel will then try to route
		// the tunnel's own endpoint through the tunnel. Attach a policy chain and
		// this session filters like any other.
		if hasImport {
			fmt.Fprintf(b, "\t\timport filter ibgp_in_%s;\n", p.Name)
		} else {
			b.WriteString("\t\timport all;\n")
		}
		if hasExport {
			fmt.Fprintf(b, "\t\texport filter ibgp_out_%s;\n", p.Name)
		} else {
			b.WriteString("\t\texport all;\n")
		}
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

// filterPrefix names a peer's generated filters. iBGP and eBGP filters differ in
// what they are allowed to do to a route, so they differ in name too — reading
// "ibgp_in_core" in a config tells you immediately that no re-tagging happened.
func filterPrefix(p store.Peer) string {
	if p.IsIBGP() {
		return "ibgp"
	}
	return "ebgp"
}

func writePeerImportFilter(b *strings.Builder, in Input, p store.Peer, fam family) {
	fmt.Fprintf(b, "filter %s_in_%s\n{\n", filterPrefix(p), p.Name)
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
	if p.IsIBGP() {
		// The opposite of the eBGP rule below, and the reason iBGP gets its own
		// filter: our large communities are how a route says where it came from
		// (FROM_UPSTREAM / FROM_IX / FROM_CUSTOMER). They are stamped once, at the
		// edge that accepted the route, and every export policy downstream reads
		// them. Stripping them on an internal session would silently unmake every
		// "announce what my customers sent me" decision on the far router.
		fmt.Fprintf(b, "\t# Our own large communities are kept: on an internal session they are the\n"+
			"\t# origin tags this route was stamped with at the edge, not something a peer\n"+
			"\t# is trying to forge.\n")
	} else {
		// A peer must never be able to pretend a route came from somewhere else by
		// sending it to us pre-tagged with one of our own large communities.
		fmt.Fprintf(b, "\tbgp_large_community.delete([(%d, *, *)]);\n", in.LocalASN)
	}

	// Draining: deprefer everything this peer sends, so we route around it while
	// its own traffic bleeds off. Part of RFC 8326 graceful shutdown.
	if p.Drained {
		b.WriteString("\tbgp_local_pref = 0;\t# draining: deprefer this peer (RFC 8326)\n")
	}

	for _, pol := range p.ImportPolicies {
		fmt.Fprintf(b, "\t%s;\n", policyFunc(pol, fam))
	}
	// Operator-defined ingress tags identify this specific neighbor (for
	// example, an IX route server or downstream) in addition to the broader
	// automatic relationship tag below.
	refs, _ := store.ParseCommunityRefs(p.ImportCommunities)
	comms := in.communityByName()
	for _, ref := range refs {
		attr, expr := communityRefExpr(ref, comms)
		fmt.Fprintf(b, "\t%s.add(%s);\n", attr, expr)
	}
	// Only eBGP stamps an origin tag; an iBGP route already carries the one it was
	// given at the edge, and roleTag has no entry for iBGP.
	if tag, ok := roleTag[p.Role]; ok {
		fmt.Fprintf(b, "\tbgp_large_community.add(%s);\n", tag.name)
	}
	b.WriteString("\taccept;\n}\n\n")
}

func writePeerExportFilter(b *strings.Builder, in Input, p store.Peer, fam family) {
	fmt.Fprintf(b, "filter %s_out_%s\n{\n", filterPrefix(p), p.Name)

	// Transforms run before the policy calls, because a policy function accepts
	// the route and terminates the filter — so the route must already carry these
	// modifications by the time it is accepted.

	// Draining: signal RFC 8326 graceful shutdown so a peer that honours it
	// deprefers what we announce and shifts its traffic off this session.
	if p.Drained {
		b.WriteString("\tbgp_community.add((65535, 0));\t# GRACEFUL_SHUTDOWN (RFC 8326) — draining\n")
	}
	// Prepend our own AS to make the path we advertise less preferred, steering
	// inbound traffic toward our other peers.
	for range p.PrependCount {
		b.WriteString("\tbgp_path.prepend(LOCAL_ASN);\n")
	}
	// Operator-defined communities, e.g. an upstream's "do not export" signal.
	// Each is a literal value or a reference to a named community in the library.
	refs, _ := store.ParseCommunityRefs(p.ExportCommunities)
	comms := in.communityByName()
	for _, ref := range refs {
		attr, expr := communityRefExpr(ref, comms)
		fmt.Fprintf(b, "\t%s.add(%s);\n", attr, expr)
	}

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
