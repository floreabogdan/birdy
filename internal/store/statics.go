package store

import (
	"database/sql"
	"fmt"
	"net/netip"
	"strings"
)

// StaticVia forwards traffic to a next hop. The other actions discard it and
// are shared with an originated prefix set's anchor route.
const StaticVia = "via"

func staticActions() map[string]bool {
	m := map[string]bool{StaticVia: true}
	for a := range originateActions {
		m[a] = true
	}
	return m
}

// A StaticRoute is reachability that no protocol discovers on its own.
//
// Most internal subnets need nothing here: an address on an interface becomes a
// direct route, and iBGP carries it to your other routers. A static route is for
// what is left over — a subnet behind something that does not speak BGP, or a
// route to a far router's loopback so an iBGP session peering on loopbacks can
// resolve its next hop.
//
// It is deliberately not a prefix set. A prefix set is a thing filters match
// against; a static route is a thing in the routing table. Conflating them is how
// you end up blackholing a subnet you only meant to announce.
type StaticRoute struct {
	ID          int64
	Prefix      string
	Action      string
	NextHop     string // only for StaticVia
	Description string
	Enabled     bool
}

func (r StaticRoute) IsV6() bool {
	p, err := netip.ParsePrefix(r.Prefix)
	return err == nil && p.Addr().Is6()
}

func (r StaticRoute) Family() string {
	if r.IsV6() {
		return FamilyV6
	}
	return FamilyV4
}

func (r *StaticRoute) Validate() map[string]string {
	errs := map[string]string{}

	r.Description = strings.TrimSpace(r.Description)
	if strings.ContainsAny(r.Description, "\n\r") {
		errs["description"] = "Line breaks are not allowed."
	}
	if !staticActions()[r.Action] {
		errs["action"] = "Choose via, blackhole, unreachable or prohibit."
	}

	p, err := netip.ParsePrefix(strings.TrimSpace(r.Prefix))
	switch {
	case err != nil:
		errs["prefix"] = "Enter a valid prefix, e.g. 192.0.2.0/24 or 2001:db8::/48."
		return errs
	case p.Masked() != p:
		errs["prefix"] = fmt.Sprintf("%s has host bits set — did you mean %s?", p, p.Masked())
		return errs
	}
	r.Prefix = p.String()

	if r.Action != StaticVia {
		r.NextHop = ""
		return errs
	}

	hop, err := netip.ParseAddr(strings.TrimSpace(r.NextHop))
	switch {
	case err != nil:
		errs["nextHop"] = "Enter the address of the router that should receive this traffic."
	case hop.IsUnspecified() || hop.IsMulticast():
		errs["nextHop"] = "That is not a usable next hop."
	case hop.Is4() != p.Addr().Is4():
		errs["nextHop"] = "The next hop must be the same address family as the prefix."
	case p.Contains(hop):
		// BIRD resolves a next hop against the rest of the table. A next hop
		// inside this route's own prefix resolves to this route, which resolves
		// to itself. The route never comes up.
		errs["nextHop"] = fmt.Sprintf("%s is inside %s, so this route would have to resolve through itself. The next hop must be reachable some other way.", hop, p)
	default:
		r.NextHop = hop.String()
	}
	return errs
}

func (s *Store) ListStaticRoutes() ([]StaticRoute, error) {
	rows, err := s.db.Query(`SELECT id, prefix, action, next_hop, description, enabled FROM static_routes ORDER BY prefix`)
	if err != nil {
		return nil, fmt.Errorf("store: list static routes: %w", err)
	}
	defer rows.Close()
	var out []StaticRoute
	for rows.Next() {
		var r StaticRoute
		if err := rows.Scan(&r.ID, &r.Prefix, &r.Action, &r.NextHop, &r.Description, &r.Enabled); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetStaticRoute(id int64) (StaticRoute, error) {
	var r StaticRoute
	err := s.db.QueryRow(`SELECT id, prefix, action, next_hop, description, enabled FROM static_routes WHERE id = ?`, id).
		Scan(&r.ID, &r.Prefix, &r.Action, &r.NextHop, &r.Description, &r.Enabled)
	if err == sql.ErrNoRows {
		return StaticRoute{}, ErrNotFound
	}
	return r, err
}

func (s *Store) CreateStaticRoute(r StaticRoute) (int64, error) {
	ts := now()
	res, err := s.db.Exec(`
		INSERT INTO static_routes (prefix, action, next_hop, description, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, r.Prefix, r.Action, r.NextHop, r.Description, r.Enabled, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("store: create static route: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) UpdateStaticRoute(r StaticRoute) error {
	res, err := s.db.Exec(`
		UPDATE static_routes SET prefix = ?, action = ?, next_hop = ?, description = ?, enabled = ?, updated_at = ?
		WHERE id = ?`, r.Prefix, r.Action, r.NextHop, r.Description, r.Enabled, now(), r.ID)
	if err != nil {
		return fmt.Errorf("store: update static route: %w", err)
	}
	return affectedOne(res)
}

func (s *Store) DeleteStaticRoute(id int64) error {
	res, err := s.db.Exec(`DELETE FROM static_routes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete static route: %w", err)
	}
	return affectedOne(res)
}
