package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// Policy directions. An import policy only ever rejects a route; an export
// policy only ever accepts one. That asymmetry is what makes chaining work:
// a peer's import policies compose with AND (any one may veto), its export
// policies with OR (any one may permit).
const (
	DirImport = "import"
	DirExport = "export"
)

// How an import policy treats the default route.
const (
	DefaultReject = "reject" // 0.0.0.0/0 and ::/0 are dropped
	DefaultAccept = "accept" // the default is accepted alongside everything else
	DefaultOnly   = "only"   // nothing but the default is accepted
)

// How an import policy treats reserved AS numbers in the received path.
const (
	BogonASNsOff           = "off"
	BogonASNsAll           = "all"
	BogonASNsExceptPrivate = "except_private" // for peers that legitimately use a private ASN
)

var (
	defaultRouteModes = map[string]bool{DefaultReject: true, DefaultAccept: true, DefaultOnly: true}
	bogonASNModes     = map[string]bool{BogonASNsOff: true, BogonASNsAll: true, BogonASNsExceptPrivate: true}
)

// Policy is one reusable import or export rule set. Peers attach policies in
// ordered chains; imports compose with AND, exports with OR.
type Policy struct {
	ID          int64
	Name        string
	Description string
	Direction   string
	Builtin     bool

	// import
	DefaultRoute    string
	MinLenV4        int
	MaxLenV4        int
	MinLenV6        int
	MaxLenV6        int
	RejectOwnASN    bool
	MaxASPathLen    int
	BogonASNs       string
	AcceptOnlySetID sql.NullInt64
	// OriginASSetID accepts only prefixes whose origin AS is a member of this
	// AS set — how an IRR AS-SET, once expanded, is actually enforced.
	OriginASSetID sql.NullInt64
	// ROV is RPKI route-origin validation: off, log, or reject.
	ROV          string
	SetLocalPref int64

	// export
	AnnounceEverything   bool
	AnnounceDefault      bool
	AnnounceFromUpstream bool
	AnnounceFromIX       bool
	AnnounceFromCustomer bool
	SetIDs               []int64 // prefix sets this policy announces

	// both
	RejectBogonPrefixes bool
	// MatchCommunity matches a single BGP community on the route. Its action
	// follows the direction: an import policy rejects a route that carries it, an
	// export policy accepts one that does. Empty = no community match.
	MatchCommunity string
	// AcceptBlackhole honours the RFC 7999 BLACKHOLE community (65535:666) on an
	// import policy: a host route (a /32 or /128) tagged with it is accepted —
	// past the normal prefix-length filter — and turned into a discard, so a
	// customer can null-route a host under attack. Import only.
	AcceptBlackhole bool
}

func (p Policy) IsImport() bool { return p.Direction == DirImport }

func (p *Policy) Validate() map[string]string {
	var errs map[string]string
	p.Name, errs = validateNameDesc(p.Name, p.Description)

	if p.Direction != DirImport && p.Direction != DirExport {
		errs["direction"] = "Choose import or export."
		return errs
	}
	p.MatchCommunity = strings.TrimSpace(p.MatchCommunity)
	if _, _, msg := ParseMatchCommunity(p.MatchCommunity); msg != "" {
		errs["matchCommunity"] = msg
	}

	if p.IsImport() {
		p.zeroExportFields()
		if !defaultRouteModes[p.DefaultRoute] {
			errs["defaultRoute"] = "Choose reject, accept or accept-only."
		}
		if !bogonASNModes[p.BogonASNs] {
			errs["bogonAsns"] = "Choose off, all, or all except private."
		}
		if p.ROV == "" {
			p.ROV = ROVOff
		}
		if !rovModes[p.ROV] {
			errs["rov"] = "Choose off, log only, or reject invalid."
		}
		if msg := checkLenBounds(p.MinLenV4, p.MaxLenV4, 32); msg != "" {
			errs["lenV4"] = msg
		}
		if msg := checkLenBounds(p.MinLenV6, p.MaxLenV6, 128); msg != "" {
			errs["lenV6"] = msg
		}
		if p.MaxASPathLen < 0 || p.MaxASPathLen > 255 {
			errs["maxAsPathLen"] = "Enter a length between 1 and 255, or 0 to not check."
		}
		if p.SetLocalPref < 0 || p.SetLocalPref > 4294967295 {
			errs["setLocalPref"] = "Enter a local preference, or 0 to leave it alone."
		}
		return errs
	}

	p.zeroImportFields()
	// An export policy that permits nothing renders a filter that rejects every
	// route. That is a valid thing to want ("receive only"), but not by accident.
	permits := p.AnnounceEverything || p.AnnounceDefault || p.AnnounceFromUpstream ||
		p.AnnounceFromIX || p.AnnounceFromCustomer || len(p.SetIDs) > 0 || p.MatchCommunity != ""
	if !permits {
		errs["announce"] = "This policy announces nothing. Choose at least one source, or attach no export policy at all to make the session receive-only."
	}
	return errs
}

// checkLenBounds validates a prefix-length window. 0/0 means "no bound".
func checkLenBounds(minLen, maxLen, bits int) string {
	if minLen == 0 && maxLen == 0 {
		return ""
	}
	switch {
	case minLen < 0 || maxLen < 0 || minLen > bits || maxLen > bits:
		return fmt.Sprintf("Prefix lengths must be between 0 and %d.", bits)
	case maxLen != 0 && minLen > maxLen:
		return "The shortest prefix length must not exceed the longest."
	}
	return ""
}

// zeroExportFields and zeroImportFields keep the row honest: this is one table
// serving two shapes, and a stale value in the unused half would be a trap for
// whoever reads the database next.
func (p *Policy) zeroExportFields() {
	p.AnnounceEverything = false
	p.AnnounceDefault = false
	p.AnnounceFromUpstream = false
	p.AnnounceFromIX = false
	p.AnnounceFromCustomer = false
	p.SetIDs = nil
}

func (p *Policy) zeroImportFields() {
	p.DefaultRoute = DefaultReject
	p.MinLenV4, p.MaxLenV4, p.MinLenV6, p.MaxLenV6 = 0, 0, 0, 0
	p.RejectOwnASN = false
	p.MaxASPathLen = 0
	p.BogonASNs = BogonASNsOff
	p.AcceptOnlySetID = sql.NullInt64{}
	p.OriginASSetID = sql.NullInt64{}
	p.ROV = ROVOff
	p.SetLocalPref = 0
	p.AcceptBlackhole = false
}

const policyCols = `id, name, description, direction, builtin,
	default_route, min_len_v4, max_len_v4, min_len_v6, max_len_v6,
	reject_own_asn, max_as_path_len, bogon_asns, accept_only_set_id, origin_as_set_id, rov, set_local_pref,
	announce_everything, announce_default, announce_from_upstream, announce_from_ix,
	announce_from_customer, reject_bogon_prefixes, match_community, accept_blackhole`

func scanPolicy(sc scanner) (Policy, error) {
	var p Policy
	err := sc.Scan(&p.ID, &p.Name, &p.Description, &p.Direction, &p.Builtin,
		&p.DefaultRoute, &p.MinLenV4, &p.MaxLenV4, &p.MinLenV6, &p.MaxLenV6,
		&p.RejectOwnASN, &p.MaxASPathLen, &p.BogonASNs, &p.AcceptOnlySetID, &p.OriginASSetID, &p.ROV, &p.SetLocalPref,
		&p.AnnounceEverything, &p.AnnounceDefault, &p.AnnounceFromUpstream, &p.AnnounceFromIX,
		&p.AnnounceFromCustomer, &p.RejectBogonPrefixes, &p.MatchCommunity, &p.AcceptBlackhole)
	return p, err
}

func (s *Store) ListPolicies() ([]Policy, error) {
	rows, err := s.db.Query(`SELECT ` + policyCols + ` FROM policies ORDER BY direction, name`)
	if err != nil {
		return nil, fmt.Errorf("store: list policies: %w", err)
	}
	defer rows.Close()
	var out []Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].IsImport() {
			continue
		}
		ids, err := s.policySetIDs(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].SetIDs = ids
	}
	return out, nil
}

func (s *Store) GetPolicyByName(name string) (Policy, error) {
	row := s.db.QueryRow(`SELECT `+policyCols+` FROM policies WHERE name = ?`, name)
	p, err := scanPolicy(row)
	if err == sql.ErrNoRows {
		return Policy{}, ErrNotFound
	}
	if err != nil {
		return Policy{}, fmt.Errorf("store: get policy: %w", err)
	}
	if !p.IsImport() {
		if p.SetIDs, err = s.policySetIDs(p.ID); err != nil {
			return Policy{}, err
		}
	}
	return p, nil
}

func (s *Store) policySetIDs(policyID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT set_id FROM policy_prefix_sets WHERE policy_id = ? ORDER BY position, set_id`, policyID)
	if err != nil {
		return nil, fmt.Errorf("store: policy prefix sets: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) CreatePolicy(p Policy) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`
		INSERT INTO policies (name, description, direction, builtin,
			default_route, min_len_v4, max_len_v4, min_len_v6, max_len_v6,
			reject_own_asn, max_as_path_len, bogon_asns, accept_only_set_id, origin_as_set_id, rov, set_local_pref,
			announce_everything, announce_default, announce_from_upstream, announce_from_ix,
			announce_from_customer, reject_bogon_prefixes, match_community, accept_blackhole, created_at, updated_at)
		VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Description, p.Direction,
		p.DefaultRoute, p.MinLenV4, p.MaxLenV4, p.MinLenV6, p.MaxLenV6,
		p.RejectOwnASN, p.MaxASPathLen, p.BogonASNs, p.AcceptOnlySetID, p.OriginASSetID, p.ROV, p.SetLocalPref,
		p.AnnounceEverything, p.AnnounceDefault, p.AnnounceFromUpstream, p.AnnounceFromIX,
		p.AnnounceFromCustomer, p.RejectBogonPrefixes, p.MatchCommunity, p.AcceptBlackhole, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("store: create policy: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := replacePolicySets(tx, id, p.SetIDs); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) UpdatePolicy(p Policy) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`
		UPDATE policies SET name = ?, description = ?, direction = ?,
			default_route = ?, min_len_v4 = ?, max_len_v4 = ?, min_len_v6 = ?, max_len_v6 = ?,
			reject_own_asn = ?, max_as_path_len = ?, bogon_asns = ?, accept_only_set_id = ?,
			origin_as_set_id = ?, rov = ?, set_local_pref = ?,
			announce_everything = ?, announce_default = ?, announce_from_upstream = ?, announce_from_ix = ?,
			announce_from_customer = ?, reject_bogon_prefixes = ?, match_community = ?, accept_blackhole = ?, updated_at = ?
		WHERE id = ?`,
		p.Name, p.Description, p.Direction,
		p.DefaultRoute, p.MinLenV4, p.MaxLenV4, p.MinLenV6, p.MaxLenV6,
		p.RejectOwnASN, p.MaxASPathLen, p.BogonASNs, p.AcceptOnlySetID, p.OriginASSetID, p.ROV, p.SetLocalPref,
		p.AnnounceEverything, p.AnnounceDefault, p.AnnounceFromUpstream, p.AnnounceFromIX,
		p.AnnounceFromCustomer, p.RejectBogonPrefixes, p.MatchCommunity, p.AcceptBlackhole, now(), p.ID)
	if err != nil {
		return fmt.Errorf("store: update policy: %w", err)
	}
	if err := affectedOne(res); err != nil {
		return err
	}
	if err := replacePolicySets(tx, p.ID, p.SetIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func replacePolicySets(tx *sql.Tx, policyID int64, setIDs []int64) error {
	if _, err := tx.Exec(`DELETE FROM policy_prefix_sets WHERE policy_id = ?`, policyID); err != nil {
		return err
	}
	for i, id := range setIDs {
		if _, err := tx.Exec(`INSERT INTO policy_prefix_sets (policy_id, set_id, position) VALUES (?, ?, ?)`, policyID, id, i); err != nil {
			return fmt.Errorf("store: attach prefix set to policy: %w", err)
		}
	}
	return nil
}

// DeletePolicy refuses while any peer still names it: silently detaching would
// change what that session imports or announces.
func (s *Store) DeletePolicy(id int64) error {
	var peers int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM peer_policies WHERE policy_id = ?`, id).Scan(&peers); err != nil {
		return err
	}
	if peers > 0 {
		return fmt.Errorf("store: policy is attached to %d peer(s)", peers)
	}
	res, err := s.db.Exec(`DELETE FROM policies WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete policy: %w", err)
	}
	return affectedOne(res)
}

// PeerPolicies returns the peer's chains, each already in application order.
func (s *Store) PeerPolicies(peerID int64) (imports, exports []Policy, err error) {
	rows, err := s.db.Query(`
		SELECT `+withPrefix(policyCols, "p.")+`
		FROM peer_policies pp JOIN policies p ON p.id = pp.policy_id
		WHERE pp.peer_id = ? ORDER BY pp.position, p.name`, peerID)
	if err != nil {
		return nil, nil, fmt.Errorf("store: peer policies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, nil, err
		}
		if p.IsImport() {
			imports = append(imports, p)
		} else {
			exports = append(exports, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for i := range exports {
		if exports[i].SetIDs, err = s.policySetIDs(exports[i].ID); err != nil {
			return nil, nil, err
		}
	}
	return imports, exports, nil
}

// withPrefix qualifies a column list for a join.
func withPrefix(cols, prefix string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// SetPeerPolicies replaces both chains. Positions are assigned from the slice
// order: imports first, then exports, so the two never collide on a position.
func (s *Store) SetPeerPolicies(peerID int64, importIDs, exportIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM peer_policies WHERE peer_id = ?`, peerID); err != nil {
		return err
	}
	pos := 0
	for _, ids := range [][]int64{importIDs, exportIDs} {
		for _, id := range ids {
			if _, err := tx.Exec(`INSERT INTO peer_policies (peer_id, policy_id, position) VALUES (?, ?, ?)`, peerID, id, pos); err != nil {
				return fmt.Errorf("store: attach policy to peer: %w", err)
			}
			pos++
		}
	}
	return tx.Commit()
}

// PrefixSetUsage counts every reference to a prefix set, so a delete can
// explain itself instead of tripping a foreign key.
func (s *Store) PrefixSetUsage(setID int64) (int, error) {
	var n, m int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM policy_prefix_sets WHERE set_id = ?`, setID).Scan(&n); err != nil {
		return 0, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM policies WHERE accept_only_set_id = ?`, setID).Scan(&m); err != nil {
		return 0, err
	}
	return n + m, nil
}
