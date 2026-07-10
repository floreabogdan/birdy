package birdc

import (
	"fmt"
	"net"
	"regexp"
	"time"
)

// pageQueryTimeout is generous compared to the Client's usual short command
// timeout: a paginated query opens its own connection and, for a deep offset
// into a large table, may need BIRD to walk and transmit a lot of lines
// before reaching the requested page — even though none of them are held in
// memory once skipped.
const pageQueryTimeout = 20 * time.Second

// identRe matches safe BIRD symbol names (protocol names, table names) —
// used to guard against injecting extra commands via user-supplied input,
// since the wire protocol is plain newline-delimited text.
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validIdent(name string) error {
	if !identRe.MatchString(name) {
		return fmt.Errorf("birdc: invalid identifier %q", name)
	}
	return nil
}

func validPrefixOrIP(s string) error {
	if _, _, err := net.ParseCIDR(s); err == nil {
		return nil
	}
	if ip := net.ParseIP(s); ip != nil {
		return nil
	}
	return fmt.Errorf("birdc: invalid prefix or IP %q", s)
}

// Status runs "show status".
func (c *Client) Status() (Status, error) {
	r, err := c.Command("show status")
	if err != nil {
		return Status{}, err
	}
	if r.IsError() {
		return Status{}, fmt.Errorf("birdc: %s", r.Terminal.Lines[0])
	}
	return ParseStatus(r)
}

// Protocols runs "show protocols".
func (c *Client) Protocols() ([]ProtocolSummary, error) {
	r, err := c.Command("show protocols")
	if err != nil {
		return nil, err
	}
	if r.IsError() {
		return nil, fmt.Errorf("birdc: %s", r.Terminal.Lines[0])
	}
	return ParseProtocols(r)
}

// ProtocolDetail runs "show protocols all <name>".
func (c *Client) ProtocolDetail(name string) (ProtocolDetail, error) {
	if err := validIdent(name); err != nil {
		return ProtocolDetail{}, err
	}
	r, err := c.Command(fmt.Sprintf("show protocols all %s", name))
	if err != nil {
		return ProtocolDetail{}, err
	}
	if r.IsError() {
		return ProtocolDetail{}, fmt.Errorf("birdc: %s", r.Terminal.Lines[0])
	}
	return ParseProtocolDetail(r)
}

// RouteCount runs "show route count".
func (c *Client) RouteCount() ([]RouteCountEntry, error) {
	r, err := c.Command("show route count")
	if err != nil {
		return nil, err
	}
	if r.IsError() {
		return nil, fmt.Errorf("birdc: %s", r.Terminal.Lines[0])
	}
	return ParseRouteCount(r)
}

// RoutesFor runs "show route for <prefix-or-ip>" (longest-prefix match), or
// with all=true, "show route for <prefix-or-ip> all" (includes non-best routes
// and per-route attributes).
func (c *Client) RoutesFor(prefixOrIP string, all bool) ([]RouteTable, error) {
	if err := validPrefixOrIP(prefixOrIP); err != nil {
		return nil, err
	}
	cmd := fmt.Sprintf("show route for %s", prefixOrIP)
	if all {
		cmd += " all"
	}
	return c.runRouteQuery(cmd)
}

// RoutesByProtocol runs "show route protocol <name>" — the routes learned
// (imported) from that protocol/peer.
func (c *Client) RoutesByProtocol(name string) ([]RouteTable, error) {
	if err := validIdent(name); err != nil {
		return nil, err
	}
	return c.runRouteQuery(fmt.Sprintf("show route protocol %s", name))
}

// RoutesExport runs "show route export <name>" — the routes that pass the
// export filter toward that protocol/peer (what is actually being sent).
func (c *Client) RoutesExport(name string) ([]RouteTable, error) {
	if err := validIdent(name); err != nil {
		return nil, err
	}
	return c.runRouteQuery(fmt.Sprintf("show route export %s", name))
}

// RoutesNoExport runs "show route noexport <name>" — routes that would be
// rejected by the export filter toward that protocol/peer.
func (c *Client) RoutesNoExport(name string) ([]RouteTable, error) {
	if err := validIdent(name); err != nil {
		return nil, err
	}
	return c.runRouteQuery(fmt.Sprintf("show route noexport %s", name))
}

func (c *Client) runRouteQuery(cmd string) ([]RouteTable, error) {
	r, err := c.Command(cmd)
	if err != nil {
		return nil, err
	}
	if r.IsError() {
		return nil, fmt.Errorf("birdc: %s", r.Terminal.Lines[0])
	}
	return ParseRoutes(r)
}

// The Page variants below back paginated UI (peer route listings, looking
// glass). Each opens its own short-lived connection via streamRoutes and
// stops reading as soon as it has the requested page — a peer carrying a
// full table (millions of routes) never gets loaded into memory just to
// show page one.

// RoutesForPage is the paginated form of RoutesFor.
func (c *Client) RoutesForPage(prefixOrIP string, all bool, offset, limit int) (RoutePage, error) {
	if err := validPrefixOrIP(prefixOrIP); err != nil {
		return RoutePage{}, err
	}
	cmd := fmt.Sprintf("show route for %s", prefixOrIP)
	if all {
		cmd += " all"
	}
	return paginate(c.path, pageQueryTimeout, cmd, offset, limit)
}

// RoutesByProtocolPage is the paginated form of RoutesByProtocol.
func (c *Client) RoutesByProtocolPage(name string, offset, limit int) (RoutePage, error) {
	if err := validIdent(name); err != nil {
		return RoutePage{}, err
	}
	return paginate(c.path, pageQueryTimeout, fmt.Sprintf("show route protocol %s", name), offset, limit)
}

// RoutesExportPage is the paginated form of RoutesExport.
func (c *Client) RoutesExportPage(name string, offset, limit int) (RoutePage, error) {
	if err := validIdent(name); err != nil {
		return RoutePage{}, err
	}
	return paginate(c.path, pageQueryTimeout, fmt.Sprintf("show route export %s", name), offset, limit)
}

// RoutesNoExportPage is the paginated form of RoutesNoExport.
func (c *Client) RoutesNoExportPage(name string, offset, limit int) (RoutePage, error) {
	if err := validIdent(name); err != nil {
		return RoutePage{}, err
	}
	return paginate(c.path, pageQueryTimeout, fmt.Sprintf("show route noexport %s", name), offset, limit)
}
