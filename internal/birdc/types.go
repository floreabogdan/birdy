package birdc

import "fmt"

// Status is the result of "show status".
type Status struct {
	Version      string
	RouterID     string
	Hostname     string
	CurrentTime  string
	LastReboot   string
	LastReconfig string
	Message      string // terminal line, e.g. "Daemon is up and running"
}

// ProtocolSummary is one row of "show protocols".
type ProtocolSummary struct {
	Name  string
	Proto string
	Table string
	State string
	Since string
	Info  string
}

// ChannelDetail is one address-family channel of a protocol (e.g. ipv4/ipv6 on a BGP session).
type ChannelDetail struct {
	AFI               string // "ipv4" or "ipv6"
	State             string
	Table             string
	Preference        string
	ImportFilter      string
	ExportFilter      string
	ImportLimit       string
	ImportLimitAction string
	RoutesImported    int
	// RoutesFiltered is what the import filter rejected. BIRD only prints it
	// when the channel keeps filtered routes, so zero can mean "none" or
	// "not reported".
	RoutesFiltered  int
	RoutesExported  int
	RoutesPreferred int
}

// ProtocolDetail is the result of "show protocols all <name>", covering any
// protocol type. BGP-specific fields are empty for non-BGP protocols.
type ProtocolDetail struct {
	Summary ProtocolSummary

	// BGP-specific
	BGPState        string
	NeighborAddress string
	NeighborAS      string
	LocalAS         string
	NeighborID      string
	SessionType     string // e.g. "external multihop AS4"
	SourceAddress   string
	HoldTimer       string
	KeepaliveTimer  string

	Channels []ChannelDetail

	// RawLines holds every detail line verbatim, for anything the
	// structured fields above don't capture yet.
	RawLines []string
}

// RouteEntry is one route within a "show route" family reply.
type RouteEntry struct {
	Network    string
	Type       string // unicast, unreachable, blackhole, prohibit
	Protocol   string
	Since      string
	From       string // present for e.g. route-reflected / multihop routes
	Primary    bool   // marked '*' — the best/selected route
	Preference int
	ASPath     string // raw bracketed AS-path/origin text, e.g. "AS64496i"
	NextHop    string // "via <ip> on <iface>" or "dev <iface>", raw

	// The fields below are only populated by "show route ... all", which emits
	// per-route BGP attribute detail lines. They are zero for a summary query.
	Origin      string      // BGP.origin, e.g. "IGP" / "Incomplete"
	LocalPref   string      // BGP.local_pref, kept as text (absent on eBGP-learned)
	MED         string      // BGP.med
	Communities []Community // BGP.community + BGP.large_community, in wire order

	Attrs []string
}

// Community is a BGP community read off a route's detail lines. A standard
// community has two parts (A:B); a large community has three (A:B:C).
type Community struct {
	Large   bool
	A, B, C int64
}

// String renders the tuple the way BIRD writes it, e.g. "(65535, 666)".
func (c Community) String() string {
	if c.Large {
		return fmt.Sprintf("(%d, %d, %d)", c.A, c.B, c.C)
	}
	return fmt.Sprintf("(%d, %d)", c.A, c.B)
}

// RouteTable groups routes returned for one BIRD routing table.
type RouteTable struct {
	Name   string
	Routes []RouteEntry
}

// RouteCountEntry is one line of "show route count".
type RouteCountEntry struct {
	Table    string
	Routes   int
	Networks int
}
