package store

import (
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// CommunityDef is a named BGP community in the library: define the value once,
// give it a readable name, and reuse it. It renders to a BIRD `define`, so the
// name is available as a symbol in the raw-config block and documents the
// operator's community scheme in one place.
type CommunityDef struct {
	ID          int64
	Name        string
	Description string
	Large       bool
	A, B, C     int64
	Builtin     bool
}

// Value is the underlying community tuple.
func (cd CommunityDef) Value() Community {
	return Community{Large: cd.Large, A: cd.A, B: cd.B, C: cd.C}
}

// Pattern renders the value the way BIRD writes it, e.g. "(65535, 666)".
func (cd CommunityDef) Pattern() string { return cd.Value().BIRD() }

// Validate checks the name and the community value, returning field-keyed errors.
func (cd *CommunityDef) Validate() map[string]string {
	name, errs := validateNameDesc(cd.Name, cd.Description)
	cd.Name = name
	// A library community must not shadow a name birdy or BIRD defines itself,
	// or the rendered config declares it twice.
	if builtinSymbols[name] {
		errs["name"] = name + " is a name birdy or BIRD defines itself; pick another."
	}

	parts := []int64{cd.A, cd.B}
	if cd.Large {
		parts = append(parts, cd.C)
		if bad := range32(parts); bad != "" {
			errs["value"] = bad
		}
	} else {
		cd.C = 0
		if bad := range16(parts); bad != "" {
			errs["value"] = bad
		}
	}
	return errs
}

func (s *Store) ListCommunityDefs() ([]CommunityDef, error) {
	rows, err := s.db.Query(`SELECT id, name, description, large, a, b, c, builtin FROM communities ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list communities: %w", err)
	}
	defer rows.Close()
	var out []CommunityDef
	for rows.Next() {
		var cd CommunityDef
		if err := rows.Scan(&cd.ID, &cd.Name, &cd.Description, &cd.Large, &cd.A, &cd.B, &cd.C, &cd.Builtin); err != nil {
			return nil, err
		}
		out = append(out, cd)
	}
	return out, rows.Err()
}

func (s *Store) GetCommunityDef(id int64) (CommunityDef, error) {
	var cd CommunityDef
	row := s.db.QueryRow(`SELECT id, name, description, large, a, b, c, builtin FROM communities WHERE id = ?`, id)
	if err := row.Scan(&cd.ID, &cd.Name, &cd.Description, &cd.Large, &cd.A, &cd.B, &cd.C, &cd.Builtin); err != nil {
		if err == sql.ErrNoRows {
			return CommunityDef{}, ErrNotFound
		}
		return CommunityDef{}, fmt.Errorf("store: get community: %w", err)
	}
	return cd, nil
}

func (s *Store) GetCommunityDefByName(name string) (CommunityDef, error) {
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM communities WHERE name = ?`, name).Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return CommunityDef{}, ErrNotFound
		}
		return CommunityDef{}, fmt.Errorf("store: get community by name: %w", err)
	}
	return s.GetCommunityDef(id)
}

func (s *Store) CreateCommunityDef(cd CommunityDef) (int64, error) {
	ts := now()
	res, err := s.db.Exec(`INSERT INTO communities (name, description, large, a, b, c, builtin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`, cd.Name, cd.Description, cd.Large, cd.A, cd.B, cd.C, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("store: create community: %w", err)
	}
	return res.LastInsertId()
}

// CommunityInUseError is UpdateCommunityDef refusing to rename a community
// that peers, templates or policies still reference by name.
type CommunityInUseError struct{ Users []string }

func (e *CommunityInUseError) Error() string {
	return "store: community is referenced by " + strings.Join(e.Users, ", ")
}

// UpdateCommunityDef saves a community. A rename is refused while anything
// still names the community: references are by name, not id, so a rename is
// a delete as far as they are concerned, and would leave each pointing at a
// symbol the next render no longer defines. That is the rule deletes follow.
// The check and the write share one transaction, so a reference saved in
// between cannot slip past.
func (s *Store) UpdateCommunityDef(cd CommunityDef) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Write before reading, as AttachPeers does: it takes SQLite's write lock
	// now, so the check below sees every reference committed before it.
	res, err := tx.Exec(`UPDATE communities SET updated_at = updated_at WHERE id = ?`, cd.ID)
	if err != nil {
		return fmt.Errorf("store: lock community: %w", err)
	}
	if err := affectedOne(res); err != nil {
		return err
	}
	var current string
	if err := tx.QueryRow(`SELECT name FROM communities WHERE id = ?`, cd.ID).Scan(&current); err != nil {
		return fmt.Errorf("store: get community: %w", err)
	}
	if current != cd.Name {
		users, err := communityUsers(tx, current)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return &CommunityInUseError{Users: users}
		}
	}
	if _, err := tx.Exec(`UPDATE communities SET name = ?, description = ?, large = ?, a = ?, b = ?, c = ?, updated_at = ?
		WHERE id = ?`, cd.Name, cd.Description, cd.Large, cd.A, cd.B, cd.C, now(), cd.ID); err != nil {
		return fmt.Errorf("store: update community: %w", err)
	}
	return tx.Commit()
}

// CommunityUsers lists the peers, peer templates and policies that reference a
// community by name, as "peer NAME", "peer template NAME" and "policy NAME". A
// template counts even with no peers linked: linking one later copies its
// references onto the peer.
func (s *Store) CommunityUsers(name string) ([]string, error) {
	return communityUsers(s.db, name)
}

func communityUsers(q querier, name string) ([]string, error) {
	var users []string
	// The tables and columns come from this fixed list, never from input.
	for _, src := range []struct{ kind, query string }{
		{"peer", `SELECT name, import_communities || char(10) || export_communities FROM peers ORDER BY name`},
		{"peer template", `SELECT name, import_communities || char(10) || export_communities FROM peer_templates ORDER BY name`},
		{"policy", `SELECT name, match_community FROM policies ORDER BY name`},
	} {
		rows, err := q.Query(src.query)
		if err != nil {
			return nil, fmt.Errorf("store: community users: %w", err)
		}
		for rows.Next() {
			var owner, refs string
			if err := rows.Scan(&owner, &refs); err != nil {
				rows.Close()
				return nil, err
			}
			if slices.Contains(NamedCommunityRefs(refs), name) {
				users = append(users, src.kind+" "+owner)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return users, nil
}

func (s *Store) DeleteCommunityDef(id int64) error {
	res, err := s.db.Exec(`DELETE FROM communities WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete community: %w", err)
	}
	return affectedOne(res)
}

// seedCommunities plants the near-universal well-known communities as builtin
// rows, idempotently (INSERT OR IGNORE keys on the unique name), so a fresh
// library is not empty and the feature is discoverable.
func seedCommunities(tx *sql.Tx) error {
	defs := []CommunityDef{
		{Name: "BLACKHOLE", Description: "RFC 7999 — ask the neighbor to blackhole this prefix", A: 65535, B: 666},
		{Name: "GRACEFUL_SHUTDOWN", Description: "RFC 8326 — deprefer routes during maintenance", A: 65535, B: 0},
	}
	ts := now()
	for _, d := range defs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO communities (name, description, large, a, b, c, builtin, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, d.Name, d.Description, d.Large, d.A, d.B, d.C, ts, ts); err != nil {
			return fmt.Errorf("seed communities: %w", err)
		}
	}
	return nil
}

// A Community is a BGP community birdy attaches to routes. Standard communities
// are two 16-bit values (ASN:value); large communities are three 32-bit values
// (RFC 8092), which a 32-bit ASN needs.
type Community struct {
	Large   bool
	A, B, C int64
}

// BIRD renders the community as a tuple literal, e.g. "(65000, 666)" or
// "(65551, 1, 2)". Only ever built from validated integers, so there is nothing
// to escape.
func (c Community) BIRD() string {
	if c.Large {
		return fmt.Sprintf("(%d, %d, %d)", c.A, c.B, c.C)
	}
	return fmt.Sprintf("(%d, %d)", c.A, c.B)
}

// A CommunityRef is one entry in a peer-export or policy-match field: either a
// literal community value, or a reference to a named community in the library
// (rendered as the define symbol, so the config reads by name).
type CommunityRef struct {
	Name  string    // non-empty for a named reference
	Value Community // the literal, when Name is empty
}

// parseCommunityRef reads one token. A BIRD identifier (letter-led) is a named
// reference; a digit-led token is a literal community.
func parseCommunityRef(tok string) (CommunityRef, string) {
	tok = strings.TrimSpace(tok)
	if birdIdent.MatchString(tok) {
		return CommunityRef{Name: tok}, ""
	}
	c, msg := parseCommunity(tok)
	if msg != "" {
		return CommunityRef{}, msg
	}
	return CommunityRef{Value: c}, ""
}

// ParseCommunityRefs reads a textarea of community references — names and/or
// literals, one per line or comma-separated, blank lines and # comments ignored.
func ParseCommunityRefs(text string) ([]CommunityRef, []string) {
	var out []CommunityRef
	var errs []string
	line := 0
	for raw := range strings.Lines(text) {
		line++
		s := strings.TrimSpace(raw)
		if i := strings.IndexByte(s, '#'); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
		if s == "" {
			continue
		}
		for _, tok := range strings.Split(s, ",") {
			if strings.TrimSpace(tok) == "" {
				continue
			}
			ref, msg := parseCommunityRef(tok)
			if msg != "" {
				errs = append(errs, fmt.Sprintf("Line %d: %s", line, msg))
				continue
			}
			out = append(out, ref)
		}
	}
	return out, errs
}

// ParseMatchCommunityRef parses a single policy-match community reference (a name
// or a literal). Empty is a valid "no match".
func ParseMatchCommunityRef(text string) (ref CommunityRef, set bool, errMsg string) {
	text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), ","))
	if text == "" {
		return CommunityRef{}, false, ""
	}
	if strings.ContainsAny(text, ",\n") {
		return CommunityRef{}, false, "Enter a single community or name, e.g. 65000:666 or BLACKHOLE."
	}
	ref, msg := parseCommunityRef(text)
	if msg != "" {
		return CommunityRef{}, false, msg
	}
	return ref, true, ""
}

// NamedCommunityRefs returns the names referenced in a community-refs textarea,
// for existence and usage checks.
func NamedCommunityRefs(text string) []string {
	refs, _ := ParseCommunityRefs(text)
	var names []string
	for _, r := range refs {
		if r.Name != "" {
			names = append(names, r.Name)
		}
	}
	return names
}

// ParseMatchCommunity parses a single community for a policy match. Empty is a
// valid "no match"; anything else must be exactly one community.
func ParseMatchCommunity(text string) (c Community, set bool, errMsg string) {
	text = strings.TrimSpace(text)
	if text = strings.TrimSuffix(text, ","); text == "" {
		return Community{}, false, ""
	}
	if strings.ContainsAny(text, ",\n") {
		return Community{}, false, "Enter a single community, e.g. 65000:666 or 65551:1:2."
	}
	c, msg := parseCommunity(text)
	if msg != "" {
		return Community{}, false, msg
	}
	return c, true, ""
}

func parseCommunity(tok string) (Community, string) {
	parts := strings.Split(tok, ":")
	nums := make([]int64, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return Community{}, fmt.Sprintf("%q is not a community (use ASN:value or ASN:x:y)", tok)
		}
		nums[i] = n
	}
	switch len(nums) {
	case 2:
		if bad := range16(nums); bad != "" {
			return Community{}, bad
		}
		return Community{A: nums[0], B: nums[1]}, ""
	case 3:
		if bad := range32(nums); bad != "" {
			return Community{}, bad
		}
		return Community{Large: true, A: nums[0], B: nums[1], C: nums[2]}, ""
	default:
		return Community{}, fmt.Sprintf("%q must have 2 parts (standard) or 3 (large)", tok)
	}
}

func range16(nums []int64) string {
	for _, n := range nums {
		if n < 0 || n > 65535 {
			return "a standard community's parts are each 0–65535; use a large community (ASN:x:y) for a 32-bit ASN"
		}
	}
	return ""
}

func range32(nums []int64) string {
	for _, n := range nums {
		if n < 0 || n > 4294967295 {
			return "a large community's parts are each 0–4294967295"
		}
	}
	return ""
}
