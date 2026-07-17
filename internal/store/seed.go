package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// The starter pack: martian / special-use space that should never be accepted
// from or announced to a BGP peer. Lists follow RFC 6890 and the NLNOG BGP
// filter guide. The "+" modifier matches the prefix and everything longer, so
// a single entry covers all more-specifics inside the block.
//
// These are seeded as ordinary rows tagged builtin, not hardcoded in the
// renderer: they are meant to be inspected, cloned and edited.
var (
	bogonsV4 = []string{
		"0.0.0.0/8",       // "this network"
		"10.0.0.0/8",      // RFC 1918 private
		"100.64.0.0/10",   // RFC 6598 carrier-grade NAT
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local
		"172.16.0.0/12",   // RFC 1918 private
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1 documentation
		"192.88.99.0/24",  // deprecated 6to4 relay anycast
		"192.168.0.0/16",  // RFC 1918 private
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2 documentation
		"203.0.113.0/24",  // TEST-NET-3 documentation
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved (includes 255.255.255.255/32)
	}
	bogonsV6 = []string{
		"::/8",           // includes ::/128 unspecified and ::1/128 loopback
		"100::/64",       // discard-only
		"100:0:0:1::/64", // dummy IPv6 prefix
		"2001:2::/48",    // benchmarking
		"2001:10::/28",   // deprecated ORCHID
		"2001:db8::/32",  // documentation
		"2002::/16",      // deprecated 6to4
		"3fff::/20",      // documentation
		"3ffe::/16",      // former 6bone
		"5f00::/16",      // segment-routing SIDs, not globally reachable
		"fc00::/7",       // unique local
		"fe80::/10",      // link-local
		"fec0::/10",      // deprecated site-local
		"ff00::/8",       // multicast
	}
)

// DefaultBogonPrefixes returns the shipped list for one address family, so the
// settings editor can offer "restore defaults" without duplicating it.
func DefaultBogonPrefixes(family string) []string {
	if family == FamilyV6 {
		return bogonsV6
	}
	return bogonsV4
}

// seedPolicies ships the handful of policies that cover the common cases, so a
// fresh birdy can configure a real session without anyone first learning the
// policy model. They are ordinary editable rows tagged builtin.
//
// The prefix-length windows (8..24 v4, 12..48 v6) and the AS-path cap follow
// the NLNOG BGP filter guide.
func seedPolicies(tx *sql.Tx) error {
	type seed struct {
		name, description string
		cols              map[string]any
	}
	seeds := []seed{
		{"IMPORT_SANITY", "Standard eBGP hygiene: no default route, no bogons, sane prefix lengths, no AS-path nonsense.", map[string]any{
			"direction": DirImport, "default_route": DefaultReject, "reject_bogon_prefixes": 1,
			"min_len_v4": 8, "max_len_v4": 24, "min_len_v6": 12, "max_len_v6": 48,
			"reject_own_asn": 1, "max_as_path_len": 64, "bogon_asns": BogonASNsAll,
		}},
		{"IMPORT_SANITY_PRIVATE_AS", "Like IMPORT_SANITY, but tolerates private AS numbers in the path — for peers that legitimately use one.", map[string]any{
			"direction": DirImport, "default_route": DefaultReject, "reject_bogon_prefixes": 1,
			"min_len_v4": 8, "max_len_v4": 24, "min_len_v6": 12, "max_len_v6": 48,
			"reject_own_asn": 1, "max_as_path_len": 64, "bogon_asns": BogonASNsExceptPrivate,
		}},
		{"IMPORT_DEFAULT_ONLY", "Accept nothing but the default route. For an upstream that only sends you 0.0.0.0/0.", map[string]any{
			"direction": DirImport, "default_route": DefaultOnly, "reject_bogon_prefixes": 0,
			"reject_own_asn": 1, "bogon_asns": BogonASNsOff,
		}},
		{"EXPORT_OWN", "Announce only our own prefixes and nothing else. Attach the prefix set holding your aggregates — until you do, this policy announces nothing.", map[string]any{
			"direction": DirExport, "reject_bogon_prefixes": 1,
		}},
		{"EXPORT_FULL_TABLE", "Announce everything we have. For a customer buying full transit.", map[string]any{
			"direction": DirExport, "announce_everything": 1, "reject_bogon_prefixes": 1,
		}},
		{"EXPORT_DEFAULT_ONLY", "Announce nothing but the default route.", map[string]any{
			"direction": DirExport, "announce_default": 1, "reject_bogon_prefixes": 1,
		}},
		{"EXPORT_OWN_AND_CUSTOMERS", "Announce everything our customers send us. Attach your own prefix set to also announce your aggregates. The safe default towards upstreams and IX peers.", map[string]any{
			"direction": DirExport, "announce_from_customer": 1, "reject_bogon_prefixes": 1,
		}},
		{"EXPORT_DOWNSTREAM", "Announce the default route plus what we learned from IX peers and customers, but not the full transit table. Attach your own prefix set to also announce your aggregates.", map[string]any{
			"direction": DirExport, "announce_default": 1, "announce_from_ix": 1,
			"announce_from_customer": 1, "reject_bogon_prefixes": 1,
		}},
	}

	ts := now()
	for _, s := range seeds {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM policies WHERE name = ?`, s.name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		cols := []string{"name", "description", "builtin", "created_at", "updated_at"}
		args := []any{s.name, s.description, 1, ts, ts}
		for k, v := range s.cols {
			cols = append(cols, k)
			args = append(args, v)
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
		q := fmt.Sprintf(`INSERT INTO policies (%s) VALUES (%s)`, strings.Join(cols, ", "), placeholders)
		if _, err := tx.Exec(q, args...); err != nil {
			return fmt.Errorf("seed policy %s: %w", s.name, err)
		}
	}
	return nil
}

// seedAnnounceSets creates the two prefix sets an operator will fill with their
// own aggregates, and points EXPORT_OWN at them.
//
// They ship EMPTY on purpose: birdy cannot know your address space, and an
// empty set announces nothing, which is the safe direction to be wrong in. The
// renderer skips an empty set with a comment rather than failing, and Lint tells
// you a session announcing nothing is probably not what you meant.
func seedAnnounceSets(tx *sql.Tx) error {
	ts := now()
	ids := map[string]int64{}
	for _, ps := range []struct{ name, family, description string }{
		{"ANNOUNCE_V4", FamilyV4, "Your own IPv4 aggregates. Add them here, then announce them with EXPORT_OWN."},
		{"ANNOUNCE_V6", FamilyV6, "Your own IPv6 aggregates. Add them here, then announce them with EXPORT_OWN."},
	} {
		var id int64
		err := tx.QueryRow(`SELECT id FROM prefix_sets WHERE name = ?`, ps.name).Scan(&id)
		switch err {
		case nil:
			ids[ps.name] = id // an operator already made one; leave it alone
			continue
		case sql.ErrNoRows:
		default:
			return err
		}
		// originate: you must originate what you announce, so a static protocol
		// blackholes these aggregates once prefixes are added.
		res, err := tx.Exec(`
			INSERT INTO prefix_sets (name, description, family, originate, builtin, system, created_at, updated_at)
			VALUES (?, ?, ?, 1, 1, 0, ?, ?)`, ps.name, ps.description, ps.family, ts, ts)
		if err != nil {
			return fmt.Errorf("seed %s: %w", ps.name, err)
		}
		if ids[ps.name], err = res.LastInsertId(); err != nil {
			return err
		}
	}

	// Attach both to EXPORT_OWN, but only if nobody has curated it already.
	var policyID int64
	err := tx.QueryRow(`SELECT id FROM policies WHERE name = 'EXPORT_OWN'`).Scan(&policyID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var attached int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM policy_prefix_sets WHERE policy_id = ?`, policyID).Scan(&attached); err != nil {
		return err
	}
	if attached > 0 {
		return nil
	}
	for i, name := range []string{"ANNOUNCE_V4", "ANNOUNCE_V6"} {
		if _, err := tx.Exec(`INSERT INTO policy_prefix_sets (policy_id, set_id, position) VALUES (?, ?, ?)`,
			policyID, ids[name], i); err != nil {
			return fmt.Errorf("attach %s to EXPORT_OWN: %w", name, err)
		}
	}
	return nil
}

// seedRPKIServers ships Cloudflare's public RTR endpoint, disabled.
//
// Disabled matters: an enabled server means birdy renders a protocol that dials
// out to a third party, and that is a decision for the operator, not a default.
// Enabling it is one click; a local validator is the better answer.
func seedRPKIServers(tx *sql.Tx) error {
	const name = "cloudflare"
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM rpki_servers WHERE name = ?`, name).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	ts := now()
	_, err := tx.Exec(`
		INSERT INTO rpki_servers (name, description, host, port, enabled, refresh, retry, expire, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		name, "Cloudflare's public RTR endpoint. Enable it to get going; run your own validator for production.",
		"rtr.rpki.cloudflare.com", 8282, 900, 90, 172800, ts, ts)
	if err != nil {
		return fmt.Errorf("seed RPKI server %s: %w", name, err)
	}
	return nil
}

func seedStarterPack(tx *sql.Tx) error {
	packs := []struct {
		name, description, family string
		prefixes                  []string
	}{
		{"BOGONS_V4", "IPv4 martians and special-use space (RFC 6890). Never accept or announce.", FamilyV4, bogonsV4},
		{"BOGONS_V6", "IPv6 martians and special-use space (RFC 6890). Never accept or announce.", FamilyV6, bogonsV6},
	}
	ts := now()
	for _, p := range packs {
		// A name collision means an operator already made a set by this name;
		// leave theirs alone rather than clobbering it.
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM prefix_sets WHERE name = ?`, p.name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		res, err := tx.Exec(`
			INSERT INTO prefix_sets (name, description, family, originate, builtin, system, created_at, updated_at)
			VALUES (?, ?, ?, 0, 1, 1, ?, ?)`, p.name, p.description, p.family, ts, ts)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for i, prefix := range p.prefixes {
			if _, err := tx.Exec(`
				INSERT INTO prefix_set_entries (set_id, prefix, modifier, position)
				VALUES (?, ?, '+', ?)`, id, prefix, i); err != nil {
				return err
			}
		}
	}
	return nil
}
