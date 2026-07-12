package birdc

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ParseStatus parses the reply to "show status".
func ParseStatus(r Reply) (Status, error) {
	var st Status
	for _, line := range r.Lines() {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "BIRD "):
			st.Version = strings.TrimPrefix(line, "BIRD ")
		case strings.HasPrefix(line, "Router ID is "):
			st.RouterID = strings.TrimPrefix(line, "Router ID is ")
		case strings.HasPrefix(line, "Hostname is "):
			st.Hostname = strings.TrimPrefix(line, "Hostname is ")
		case strings.HasPrefix(line, "Current server time is "):
			st.CurrentTime = strings.TrimPrefix(line, "Current server time is ")
		case strings.HasPrefix(line, "Last reboot on "):
			st.LastReboot = strings.TrimPrefix(line, "Last reboot on ")
		case strings.HasPrefix(line, "Last reconfiguration on "):
			st.LastReconfig = strings.TrimPrefix(line, "Last reconfiguration on ")
		}
	}
	st.Message = strings.TrimSpace(r.Terminal.Lines[0])
	return st, nil
}

// headerColumns finds the byte offset of each column name within a
// fixed-width table header line, e.g. "Name       Proto      Table ...".
func headerColumns(header string, cols []string) map[string]int {
	idx := make(map[string]int, len(cols))
	for _, c := range cols {
		idx[c] = strings.Index(header, c)
	}
	return idx
}

func sliceColumn(line string, start, end int) string {
	if start < 0 {
		return ""
	}
	if start >= len(line) {
		return ""
	}
	if end < 0 || end > len(line) {
		end = len(line)
	}
	if end < start {
		end = start
	}
	return strings.TrimSpace(line[start:end])
}

// ParseProtocols parses the reply to "show protocols" into one row per protocol.
func ParseProtocols(r Reply) ([]ProtocolSummary, error) {
	if len(r.Blocks) < 2 {
		return nil, fmt.Errorf("birdc: unexpected show protocols reply shape")
	}
	header := r.Blocks[0].Lines[0]
	cols := []string{"Name", "Proto", "Table", "State", "Since", "Info"}
	idx := headerColumns(header, cols)

	var out []ProtocolSummary
	for _, blk := range r.Blocks[1:] {
		for _, row := range blk.Lines {
			if strings.TrimSpace(row) == "" {
				continue
			}
			out = append(out, ProtocolSummary{
				Name:  sliceColumn(row, idx["Name"], idx["Proto"]),
				Proto: sliceColumn(row, idx["Proto"], idx["Table"]),
				Table: sliceColumn(row, idx["Table"], idx["State"]),
				State: sliceColumn(row, idx["State"], idx["Since"]),
				Since: sliceColumn(row, idx["Since"], idx["Info"]),
				Info:  sliceColumn(row, idx["Info"], -1),
			})
		}
	}
	return out, nil
}

var (
	reHold      = regexp.MustCompile(`^Hold timer:\s*(\S+)`)
	reKeepalive = regexp.MustCompile(`^Keepalive timer:\s*(\S+)`)
	reChannel   = regexp.MustCompile(`^Channel (\S+)`)
	reRoutes    = regexp.MustCompile(`^Routes:\s*(\d+) imported(?:,\s*(\d+) filtered)?,\s*(\d+) exported,\s*(\d+) preferred`)
)

// ParseProtocolDetail parses the reply to "show protocols all <name>". It
// works for any protocol type: fields that don't apply (e.g. BGP fields on a
// static protocol) are left zero-valued, and RawLines always holds the full
// detail text so nothing is silently lost.
func ParseProtocolDetail(r Reply) (ProtocolDetail, error) {
	var d ProtocolDetail
	if len(r.Blocks) < 2 {
		return d, fmt.Errorf("birdc: unexpected show protocols all reply shape")
	}
	header := r.Blocks[0].Lines[0]
	idx := headerColumns(header, []string{"Name", "Proto", "Table", "State", "Since", "Info"})
	summaryRow := r.Blocks[1].Lines[0]
	d.Summary = ProtocolSummary{
		Name:  sliceColumn(summaryRow, idx["Name"], idx["Proto"]),
		Proto: sliceColumn(summaryRow, idx["Proto"], idx["Table"]),
		Table: sliceColumn(summaryRow, idx["Table"], idx["State"]),
		State: sliceColumn(summaryRow, idx["State"], idx["Since"]),
		Since: sliceColumn(summaryRow, idx["Since"], idx["Info"]),
		Info:  sliceColumn(summaryRow, idx["Info"], -1),
	}

	var curChannel *ChannelDetail
	flush := func() {
		if curChannel != nil {
			d.Channels = append(d.Channels, *curChannel)
			curChannel = nil
		}
	}

	for _, blk := range r.Blocks[2:] {
		for _, raw := range blk.Lines {
			line := strings.TrimRight(raw, " ")
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			d.RawLines = append(d.RawLines, raw)

			switch {
			case strings.HasPrefix(trimmed, "BGP state:"):
				d.BGPState = strings.TrimSpace(strings.TrimPrefix(trimmed, "BGP state:"))
			case strings.HasPrefix(trimmed, "Neighbor address:"):
				d.NeighborAddress = strings.TrimSpace(strings.TrimPrefix(trimmed, "Neighbor address:"))
			case strings.HasPrefix(trimmed, "Neighbor AS:"):
				d.NeighborAS = strings.TrimSpace(strings.TrimPrefix(trimmed, "Neighbor AS:"))
			case strings.HasPrefix(trimmed, "Local AS:"):
				d.LocalAS = strings.TrimSpace(strings.TrimPrefix(trimmed, "Local AS:"))
			case strings.HasPrefix(trimmed, "Neighbor ID:"):
				d.NeighborID = strings.TrimSpace(strings.TrimPrefix(trimmed, "Neighbor ID:"))
			case strings.HasPrefix(trimmed, "Session:"):
				d.SessionType = strings.TrimSpace(strings.TrimPrefix(trimmed, "Session:"))
			case strings.HasPrefix(trimmed, "Source address:"):
				d.SourceAddress = strings.TrimSpace(strings.TrimPrefix(trimmed, "Source address:"))
			case reHold.MatchString(trimmed):
				d.HoldTimer = reHold.FindStringSubmatch(trimmed)[1]
			case reKeepalive.MatchString(trimmed):
				d.KeepaliveTimer = reKeepalive.FindStringSubmatch(trimmed)[1]
			case reChannel.MatchString(trimmed):
				flush()
				curChannel = &ChannelDetail{AFI: reChannel.FindStringSubmatch(trimmed)[1]}
			case curChannel != nil && strings.HasPrefix(trimmed, "State:"):
				curChannel.State = strings.TrimSpace(strings.TrimPrefix(trimmed, "State:"))
			case curChannel != nil && strings.HasPrefix(trimmed, "Table:"):
				curChannel.Table = strings.TrimSpace(strings.TrimPrefix(trimmed, "Table:"))
			case curChannel != nil && strings.HasPrefix(trimmed, "Preference:"):
				curChannel.Preference = strings.TrimSpace(strings.TrimPrefix(trimmed, "Preference:"))
			case curChannel != nil && strings.HasPrefix(trimmed, "Input filter:"):
				curChannel.ImportFilter = strings.TrimSpace(strings.TrimPrefix(trimmed, "Input filter:"))
			case curChannel != nil && strings.HasPrefix(trimmed, "Output filter:"):
				curChannel.ExportFilter = strings.TrimSpace(strings.TrimPrefix(trimmed, "Output filter:"))
			case curChannel != nil && strings.HasPrefix(trimmed, "Import limit:"):
				curChannel.ImportLimit = strings.TrimSpace(strings.TrimPrefix(trimmed, "Import limit:"))
			case curChannel != nil && strings.HasPrefix(trimmed, "Action:"):
				curChannel.ImportLimitAction = strings.TrimSpace(strings.TrimPrefix(trimmed, "Action:"))
			case curChannel != nil && reRoutes.MatchString(trimmed):
				m := reRoutes.FindStringSubmatch(trimmed)
				curChannel.RoutesImported, _ = strconv.Atoi(m[1])
				curChannel.RoutesFiltered, _ = strconv.Atoi(m[2]) // "" when BIRD omits it
				curChannel.RoutesExported, _ = strconv.Atoi(m[3])
				curChannel.RoutesPreferred, _ = strconv.Atoi(m[4])
			}
		}
	}
	flush()
	return d, nil
}

var reRouteCount = regexp.MustCompile(`^(\d+) of (\d+) routes for (\d+) networks in table (\S+)`)

// ParseRouteCount parses the reply to "show route count": per-table counts
// plus the grand total.
func ParseRouteCount(r Reply) ([]RouteCountEntry, error) {
	var out []RouteCountEntry
	for _, line := range r.Lines() {
		m := reRouteCount.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		routes, _ := strconv.Atoi(m[1])
		nets, _ := strconv.Atoi(m[3])
		out = append(out, RouteCountEntry{Table: m[4], Routes: routes, Networks: nets})
	}
	return out, nil
}

// ParseRoutes parses any "show route ..." reply (plain, protocol, export,
// noexport, for, with or without "all") into per-table route lists.
func ParseRoutes(r Reply) ([]RouteTable, error) {
	var tables []RouteTable
	var cur *RouteTable
	var lastEntry *RouteEntry
	lastNetwork := ""

	for _, raw := range r.Lines() {
		line := strings.TrimRight(raw, " ")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "Table ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			name := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, "Table ")), ":")
			tables = append(tables, RouteTable{Name: name})
			cur = &tables[len(tables)-1]
			lastEntry = nil
			lastNetwork = ""
			continue
		}
		if strings.HasPrefix(line, "\t") {
			attachRouteDetail(lastEntry, line)
			continue
		}
		if entry, network, ok := parseRouteLine(line, lastNetwork); ok {
			if cur == nil {
				tables = append(tables, RouteTable{Name: "master"})
				cur = &tables[len(tables)-1]
			}
			cur.Routes = append(cur.Routes, entry)
			lastEntry = &cur.Routes[len(cur.Routes)-1]
			lastNetwork = network
			continue
		}
		// Unrecognized line (e.g. an extra attribute row from "show route all"
		// that doesn't start with a tab) — attach to the last route so nothing
		// is silently dropped.
		attachRouteDetail(lastEntry, line)
	}
	return tables, nil
}

func attachRouteDetail(e *RouteEntry, line string) {
	if e == nil {
		return
	}
	trimmed := strings.TrimSpace(line)
	fields := strings.Fields(trimmed)
	switch {
	case len(fields) >= 4 && fields[0] == "via" && fields[2] == "on":
		e.NextHop = trimmed
	case len(fields) >= 2 && fields[0] == "dev":
		e.NextHop = trimmed
	case strings.HasPrefix(trimmed, "BGP.community:"):
		e.Communities = append(e.Communities, parseCommunityList(afterColon(trimmed), false)...)
	case strings.HasPrefix(trimmed, "BGP.large_community:"):
		e.Communities = append(e.Communities, parseCommunityList(afterColon(trimmed), true)...)
	case strings.HasPrefix(trimmed, "BGP.local_pref:"):
		e.LocalPref = afterColon(trimmed)
	case strings.HasPrefix(trimmed, "BGP.origin:"):
		e.Origin = afterColon(trimmed)
	case strings.HasPrefix(trimmed, "BGP.med:"):
		e.MED = afterColon(trimmed)
	default:
		e.Attrs = append(e.Attrs, trimmed)
	}
}

// afterColon returns the trimmed text following the first colon in s.
func afterColon(s string) string {
	if _, after, ok := strings.Cut(s, ":"); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

// parseCommunityList parses a whitespace-separated list of parenthesised
// community tuples, e.g. "(65000,100) (65000, 200)" or "(64496, 1, 1000)".
// Tuples whose arity does not match large (3) / standard (2) are skipped
// rather than guessed at.
func parseCommunityList(s string, large bool) []Community {
	var out []Community
	for len(s) > 0 {
		i := strings.IndexByte(s, '(')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], ')')
		if j < 0 {
			break
		}
		body := s[i+1 : i+j]
		s = s[i+j+1:]

		parts := strings.Split(body, ",")
		nums := make([]int64, 0, len(parts))
		ok := true
		for _, p := range parts {
			n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
			if err != nil {
				ok = false
				break
			}
			nums = append(nums, n)
		}
		if !ok {
			continue
		}
		switch {
		case large && len(nums) == 3:
			out = append(out, Community{Large: true, A: nums[0], B: nums[1], C: nums[2]})
		case !large && len(nums) == 2:
			out = append(out, Community{A: nums[0], B: nums[1]})
		}
	}
	return out
}

var routeTypes = map[string]bool{"unicast": true, "unreachable": true, "blackhole": true, "prohibit": true}

// parseRouteLine parses one route summary line, e.g.:
//
//	192.0.2.0/24      unicast [direct1 2026-07-08] * (240)
//	                     unreachable [anchors4 2026-07-08] (200)
//	0.0.0.0/0            unicast [edge_v4 2026-07-08] * (100) [AS64496i]
//
// A blank network field means "same network as the previous entry"; lastNetwork
// supplies it. Returns the resolved network alongside the entry.
func parseRouteLine(line string, lastNetwork string) (RouteEntry, string, bool) {
	i1 := strings.IndexByte(line, '[')
	if i1 < 0 {
		return RouteEntry{}, "", false
	}
	j1 := strings.IndexByte(line[i1:], ']')
	if j1 < 0 {
		return RouteEntry{}, "", false
	}
	j1 += i1

	head := strings.Fields(strings.TrimSpace(line[:i1]))
	var network, typ string
	switch len(head) {
	case 1:
		typ = head[0]
		network = lastNetwork
	case 2:
		network, typ = head[0], head[1]
	default:
		return RouteEntry{}, "", false
	}
	if !routeTypes[typ] {
		return RouteEntry{}, "", false
	}

	b1 := strings.Fields(line[i1+1 : j1])
	var protocol, since, from string
	if len(b1) >= 2 {
		protocol, since = b1[0], b1[1]
	}
	for i, f := range b1 {
		if f == "from" && i+1 < len(b1) {
			from = b1[i+1]
		}
	}

	rest := strings.TrimSpace(line[j1+1:])
	primary := strings.HasPrefix(rest, "*")
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "*"))

	var pref int
	if strings.HasPrefix(rest, "(") {
		if end := strings.IndexByte(rest, ')'); end > 0 {
			pref, _ = strconv.Atoi(rest[1:end])
			rest = strings.TrimSpace(rest[end+1:])
		}
	}
	var aspath string
	if strings.HasPrefix(rest, "[") && strings.HasSuffix(rest, "]") {
		aspath = rest[1 : len(rest)-1]
	}

	return RouteEntry{
		Network:    network,
		Type:       typ,
		Protocol:   protocol,
		Since:      since,
		From:       from,
		Primary:    primary,
		Preference: pref,
		ASPath:     aspath,
	}, network, true
}
