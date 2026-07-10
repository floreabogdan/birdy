package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// A peer's role decides two things: whether the session is eBGP or iBGP, and
// which large community birdy stamps on the routes it sends us. Export policies
// then select routes by that tag.
const (
	RoleUpstream = "upstream" // transit provider
	RoleIXPeer   = "ix_peer"  // settlement-free peer, typically at an exchange
	RoleCustomer = "customer" // downstream who buys transit from us
	RoleIBGP     = "ibgp"     // inside our own AS
)

var peerRoles = map[string]bool{
	RoleUpstream: true, RoleIXPeer: true, RoleCustomer: true, RoleIBGP: true,
}

var limitActions = map[string]bool{
	"warn": true, "block": true, "restart": true, "disable": true,
}

// birdIdent is deliberately strict. Every peer name is interpolated straight
// into bird.conf as a protocol name and into generated filter names, so it is
// the one field that could smuggle syntax into the config. Anything that is
// not a plain BIRD symbol is rejected at the model boundary, not escaped.
var birdIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// ErrNotFound is returned by the Get/Update/Delete helpers.
var ErrNotFound = errors.New("store: not found")

type Peer struct {
	ID                int64
	Name              string
	Description       string
	Role              string
	Enabled           bool
	NeighborIP        string
	RemoteASN         int64
	LocalIP           string
	Multihop          int // 0 = direct
	Passive           bool
	Password          string
	ImportLimit       int // 0 = no limit
	ImportLimitAction string
	EnforceFirstAS    bool
	// OriginPeerOnly accepts only prefixes the peer itself originated: transit
	// for them, but not for anyone behind them. Per-peer because the check
	// compares the route's origin AS against this peer's own ASN.
	OriginPeerOnly bool

	// Ordered policy chains, filled by the caller from SetPeerPolicies/PeerPolicies.
	ImportPolicies []Policy
	ExportPolicies []Policy
}

// IsV6 reports whether the session carries an IPv6 channel. Callers must only
// use this on a validated peer.
func (p Peer) IsV6() bool {
	addr, err := netip.ParseAddr(p.NeighborIP)
	return err == nil && addr.Is6()
}

// IsIBGP reports whether the session is internal. iBGP peers are inside our
// trust boundary: birdy neither re-tags nor strips communities on them.
func (p Peer) IsIBGP() bool { return p.Role == RoleIBGP }

// Validate checks everything the renderer will trust. It returns a map keyed by
// form field so the UI can mark the offending input.
func (p *Peer) Validate() map[string]string {
	errs := map[string]string{}

	p.Name = strings.TrimSpace(p.Name)
	if !birdIdent.MatchString(p.Name) {
		errs["name"] = "Use letters, digits and underscore, starting with a letter or underscore (max 63)."
	}
	if strings.ContainsAny(p.Description, "\"\n\r") {
		errs["description"] = "Quotes and line breaks are not allowed."
	}
	if !peerRoles[p.Role] {
		errs["role"] = "Choose a role: upstream, IX peer, customer or iBGP."
	}

	neighbor, err := netip.ParseAddr(strings.TrimSpace(p.NeighborIP))
	if err != nil || !neighbor.IsValid() {
		errs["neighborIp"] = "Enter a valid IPv4 or IPv6 address."
	} else {
		p.NeighborIP = neighbor.String()
	}

	if p.LocalIP = strings.TrimSpace(p.LocalIP); p.LocalIP != "" {
		local, localErr := netip.ParseAddr(p.LocalIP)
		switch {
		case localErr != nil:
			errs["localIp"] = "Enter a valid IP address, or leave blank to let BIRD pick."
		case neighbor.IsValid() && local.Is4() != neighbor.Is4():
			errs["localIp"] = "Local address must be the same family as the neighbor."
		default:
			p.LocalIP = local.String()
		}
	}

	// BGP AS numbers are 32-bit. AS0 (RFC 7607), AS65535 and AS4294967295
	// (RFC 7300) are reserved and never valid on the wire; AS23456 is AS_TRANS
	// and must not appear as a real peer AS either.
	switch {
	case p.RemoteASN < 1 || p.RemoteASN > 4294967295:
		errs["remoteAsn"] = "Enter an AS number between 1 and 4294967295."
	case p.RemoteASN == 23456:
		errs["remoteAsn"] = "AS23456 is AS_TRANS, reserved for 4-byte AS translation."
	case p.RemoteASN == 65535 || p.RemoteASN == 4294967295:
		errs["remoteAsn"] = fmt.Sprintf("AS%d is reserved (RFC 7300).", p.RemoteASN)
	}

	if p.Multihop < 0 || p.Multihop > 255 {
		errs["multihop"] = "Enter a TTL between 1 and 255, or 0 for a directly connected peer."
	}
	if p.ImportLimit < 0 {
		errs["importLimit"] = "Enter a positive limit, or 0 for no limit."
	}
	if !limitActions[p.ImportLimitAction] {
		errs["importLimitAction"] = "Choose warn, block, restart or disable."
	}
	// The password lands inside a double-quoted BIRD string.
	if strings.ContainsAny(p.Password, "\"\n\r") {
		errs["password"] = "Quotes and line breaks are not allowed."
	}
	return errs
}

func (s *Store) ListPeers() ([]Peer, error) {
	rows, err := s.db.Query(`
		SELECT id, name, description, role, enabled, neighbor_ip, remote_asn, local_ip,
		       multihop, passive, password, import_limit, import_limit_action, enforce_first_as,
		       origin_peer_only
		FROM peers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list peers: %w", err)
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetPeer(id int64) (Peer, error) {
	row := s.db.QueryRow(`
		SELECT id, name, description, role, enabled, neighbor_ip, remote_asn, local_ip,
		       multihop, passive, password, import_limit, import_limit_action, enforce_first_as,
		       origin_peer_only
		FROM peers WHERE id = ?`, id)
	p, err := scanPeer(row)
	if err == sql.ErrNoRows {
		return Peer{}, ErrNotFound
	}
	return p, err
}

// GetPeerByName looks a peer up by its BIRD protocol name. Peers are addressed
// by name throughout the UI: the name is unique, stable and already the thing
// the operator recognises from BIRD's own output.
func (s *Store) GetPeerByName(name string) (Peer, error) {
	row := s.db.QueryRow(`
		SELECT id, name, description, role, enabled, neighbor_ip, remote_asn, local_ip,
		       multihop, passive, password, import_limit, import_limit_action, enforce_first_as,
		       origin_peer_only
		FROM peers WHERE name = ?`, name)
	p, err := scanPeer(row)
	if err == sql.ErrNoRows {
		return Peer{}, ErrNotFound
	}
	return p, err
}

type scanner interface{ Scan(...any) error }

func scanPeer(sc scanner) (Peer, error) {
	var p Peer
	err := sc.Scan(&p.ID, &p.Name, &p.Description, &p.Role, &p.Enabled, &p.NeighborIP,
		&p.RemoteASN, &p.LocalIP, &p.Multihop, &p.Passive, &p.Password,
		&p.ImportLimit, &p.ImportLimitAction, &p.EnforceFirstAS, &p.OriginPeerOnly)
	return p, err
}

func (s *Store) CreatePeer(p Peer) (int64, error) {
	ts := now()
	res, err := s.db.Exec(`
		INSERT INTO peers (name, description, role, enabled, neighbor_ip, remote_asn, local_ip,
		                   multihop, passive, password, import_limit, import_limit_action,
		                   enforce_first_as, origin_peer_only, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Description, p.Role, p.Enabled, p.NeighborIP, p.RemoteASN, p.LocalIP,
		p.Multihop, p.Passive, p.Password, p.ImportLimit, p.ImportLimitAction,
		p.EnforceFirstAS, p.OriginPeerOnly, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("store: create peer: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) UpdatePeer(p Peer) error {
	res, err := s.db.Exec(`
		UPDATE peers SET name = ?, description = ?, role = ?, enabled = ?, neighbor_ip = ?,
		                 remote_asn = ?, local_ip = ?, multihop = ?, passive = ?, password = ?,
		                 import_limit = ?, import_limit_action = ?, enforce_first_as = ?,
		                 origin_peer_only = ?, updated_at = ?
		WHERE id = ?`,
		p.Name, p.Description, p.Role, p.Enabled, p.NeighborIP, p.RemoteASN, p.LocalIP,
		p.Multihop, p.Passive, p.Password, p.ImportLimit, p.ImportLimitAction,
		p.EnforceFirstAS, p.OriginPeerOnly, now(), p.ID)
	if err != nil {
		return fmt.Errorf("store: update peer: %w", err)
	}
	return affectedOne(res)
}

func (s *Store) DeletePeer(id int64) error {
	res, err := s.db.Exec(`DELETE FROM peers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete peer: %w", err)
	}
	return affectedOne(res)
}

func affectedOne(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
