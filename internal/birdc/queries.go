package birdc

import (
	"context"
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
func (c *Client) Status(ctx context.Context) (Status, error) {
	r, err := c.Command(ctx, "show status")
	if err != nil {
		return Status{}, err
	}
	if r.IsError() {
		return Status{}, fmt.Errorf("birdc: %s", r.Terminal.Lines[0])
	}
	return ParseStatus(r)
}

// Protocols runs "show protocols".
func (c *Client) Protocols(ctx context.Context) ([]ProtocolSummary, error) {
	r, err := c.Command(ctx, "show protocols")
	if err != nil {
		return nil, err
	}
	if r.IsError() {
		return nil, fmt.Errorf("birdc: %s", r.Terminal.Lines[0])
	}
	return ParseProtocols(r)
}

// ProtocolDetail runs "show protocols all <name>".
func (c *Client) ProtocolDetail(ctx context.Context, name string) (ProtocolDetail, error) {
	if err := validIdent(name); err != nil {
		return ProtocolDetail{}, err
	}
	r, err := c.Command(ctx, fmt.Sprintf("show protocols all %s", name))
	if err != nil {
		return ProtocolDetail{}, err
	}
	if r.IsError() {
		return ProtocolDetail{}, fmt.Errorf("birdc: %s", r.Terminal.Lines[0])
	}
	return ParseProtocolDetail(r)
}

// RouteCount runs "show route count".
func (c *Client) RouteCount(ctx context.Context) ([]RouteCountEntry, error) {
	r, err := c.Command(ctx, "show route count")
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
func (c *Client) RoutesFor(ctx context.Context, prefixOrIP string, all bool) ([]RouteTable, error) {
	if err := validPrefixOrIP(prefixOrIP); err != nil {
		return nil, err
	}
	cmd := fmt.Sprintf("show route for %s", prefixOrIP)
	if all {
		cmd += " all"
	}
	return c.runRouteQuery(ctx, cmd)
}

// RoutesByProtocol runs "show route protocol <name>" — the routes learned
// (imported) from that protocol/peer.
func (c *Client) RoutesByProtocol(ctx context.Context, name string) ([]RouteTable, error) {
	if err := validIdent(name); err != nil {
		return nil, err
	}
	return c.runRouteQuery(ctx, fmt.Sprintf("show route protocol %s", name))
}

// RoutesExport runs "show route export <name>" — the routes that pass the
// export filter toward that protocol/peer (what is actually being sent).
func (c *Client) RoutesExport(ctx context.Context, name string) ([]RouteTable, error) {
	if err := validIdent(name); err != nil {
		return nil, err
	}
	return c.runRouteQuery(ctx, fmt.Sprintf("show route export %s", name))
}

// RoutesNoExport runs "show route noexport <name>" — routes that would be
// rejected by the export filter toward that protocol/peer.
func (c *Client) RoutesNoExport(ctx context.Context, name string) ([]RouteTable, error) {
	if err := validIdent(name); err != nil {
		return nil, err
	}
	return c.runRouteQuery(ctx, fmt.Sprintf("show route noexport %s", name))
}

func (c *Client) runRouteQuery(ctx context.Context, cmd string) ([]RouteTable, error) {
	r, err := c.Command(ctx, cmd)
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
func (c *Client) RoutesForPage(ctx context.Context, prefixOrIP string, all bool, offset, limit int) (RoutePage, error) {
	if err := validPrefixOrIP(prefixOrIP); err != nil {
		return RoutePage{}, err
	}
	cmd := fmt.Sprintf("show route for %s", prefixOrIP)
	if all {
		cmd += " all"
	}
	return paginate(ctx, c.path, pageQueryTimeout, cmd, offset, limit)
}

// RoutesByProtocolPage is the paginated form of RoutesByProtocol. With all=true
// it requests per-route attributes ("show route protocol X all"), so the caller
// can see communities and path attributes.
func (c *Client) RoutesByProtocolPage(ctx context.Context, name string, all bool, offset, limit int) (RoutePage, error) {
	if err := validIdent(name); err != nil {
		return RoutePage{}, err
	}
	return paginate(ctx, c.path, pageQueryTimeout, routeCmd("show route protocol %s", name, all), offset, limit)
}

// RoutesExportPage is the paginated form of RoutesExport.
func (c *Client) RoutesExportPage(ctx context.Context, name string, all bool, offset, limit int) (RoutePage, error) {
	if err := validIdent(name); err != nil {
		return RoutePage{}, err
	}
	return paginate(ctx, c.path, pageQueryTimeout, routeCmd("show route export %s", name, all), offset, limit)
}

// RoutesRPKIInvalidPage pages the routes carrying the RPKI_INVALID large
// community birdy tags in log-only mode — i.e. what a policy would drop if it
// were switched from log-only to reject. The community must match the
// RPKI_INVALID define the renderer emits, (localASN, 2, 1).
func (c *Client) RoutesRPKIInvalidPage(ctx context.Context, localASN int64, offset, limit int) (RoutePage, error) {
	if localASN < 1 || localASN > 4294967295 {
		return RoutePage{}, fmt.Errorf("birdc: invalid local ASN %d", localASN)
	}
	cmd := fmt.Sprintf("show route where (%d, 2, 1) ~ bgp_large_community", localASN)
	return paginate(ctx, c.path, pageQueryTimeout, cmd, offset, limit)
}

// RoutesRPKIInvalidCount asks BIRD how many routes carry the RPKI_INVALID tag —
// the number a policy would drop if it moved from log-only to reject, which is
// the whole point of the dry run.
//
// BIRD does the counting and answers with one line per table, so this is cheap
// even on a full-table router: nothing walks 2.6M routes across the socket. The
// paginated listing exists to show *which* routes; this says *how many*.
func (c *Client) RoutesRPKIInvalidCount(ctx context.Context, localASN int64) ([]RouteCountEntry, error) {
	if localASN < 1 || localASN > 4294967295 {
		return nil, fmt.Errorf("birdc: invalid local ASN %d", localASN)
	}
	r, err := c.Command(ctx, fmt.Sprintf("show route where (%d, 2, 1) ~ bgp_large_community count", localASN))
	if err != nil {
		return nil, err
	}
	if r.IsError() {
		return nil, fmt.Errorf("birdc: %s", r.Terminal.Lines[0])
	}
	return ParseRouteCount(r)
}

// RoutesNoExportPage is the paginated form of RoutesNoExport.
func (c *Client) RoutesNoExportPage(ctx context.Context, name string, all bool, offset, limit int) (RoutePage, error) {
	if err := validIdent(name); err != nil {
		return RoutePage{}, err
	}
	return paginate(ctx, c.path, pageQueryTimeout, routeCmd("show route noexport %s", name, all), offset, limit)
}

// routeCmd formats a "show route ..." command, appending " all" to request the
// per-route attribute block when wanted.
func routeCmd(format, name string, all bool) string {
	cmd := fmt.Sprintf(format, name)
	if all {
		cmd += " all"
	}
	return cmd
}
