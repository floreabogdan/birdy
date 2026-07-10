package store

import (
	"database/sql"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

const (
	FamilyV4 = "ipv4"
	FamilyV6 = "ipv6"
)

// What an originated aggregate does with the traffic it attracts. The anchor
// only ever catches addresses inside the aggregate that no longer prefix
// matches — unassigned space — because the kernel forwards on longest match,
// not on BIRD's route preference.
//
// Dropping that traffic here is the point. Without an anchor it would match
// your default route and go straight back to your upstream, which sends it
// back to you: a loop that lasts until the TTL runs out.
const (
	OriginateBlackhole   = "blackhole"   // drop it silently
	OriginateUnreachable = "unreachable" // drop it, send ICMP unreachable
	OriginateProhibit    = "prohibit"    // drop it, send ICMP admin-prohibited
)

var originateActions = map[string]bool{
	OriginateBlackhole: true, OriginateUnreachable: true, OriginateProhibit: true,
}

// rangeModifier matches BIRD's "{low,high}" prefix pattern suffix.
var rangeModifier = regexp.MustCompile(`^\{(\d{1,3}),(\d{1,3})\}$`)

type PrefixSet struct {
	ID          int64
	Name        string
	Description string
	Family      string // ipv4 | ipv6
	Originate   bool   // render a static protocol announcing these prefixes
	// OriginateAction is what the anchor route does with traffic it attracts:
	// blackhole (silent), unreachable or prohibit. Ignored unless Originate.
	OriginateAction string
	Builtin         bool
	// System sets (the bogon lists) are named directly by generated filters.
	// They live under Settings, never appear in a prefix-set picker, and
	// cannot be renamed, re-familied or deleted.
	System  bool
	Entries []PrefixEntry
}

type PrefixEntry struct {
	ID       int64
	Prefix   string
	Modifier string // "", "+", "-", "{low,high}"
}

// Pattern renders the entry the way BIRD writes it, e.g. "10.0.0.0/8+".
func (e PrefixEntry) Pattern() string { return e.Prefix + e.Modifier }

func (ps *PrefixSet) Validate() map[string]string {
	errs := map[string]string{}

	ps.Name = strings.TrimSpace(ps.Name)
	if !birdIdent.MatchString(ps.Name) {
		errs["name"] = "Use letters, digits and underscore, starting with a letter or underscore (max 63)."
	}
	if strings.ContainsAny(ps.Description, "\"\n\r") {
		errs["description"] = "Quotes and line breaks are not allowed."
	}
	if ps.Family != FamilyV4 && ps.Family != FamilyV6 {
		errs["family"] = "Choose IPv4 or IPv6."
	}
	// An anchor route has to do something with the traffic it attracts.
	if ps.OriginateAction == "" {
		ps.OriginateAction = OriginateBlackhole
	}
	if !originateActions[ps.OriginateAction] {
		errs["originateAction"] = "Choose blackhole, unreachable or prohibit."
	}
	if len(ps.Entries) == 0 {
		errs["entries"] = "Add at least one prefix."
	}

	// Errors are keyed entry.N but also carry the line number in their text:
	// the form shows them all under one textarea, where "which line?" is the
	// first thing the operator needs to know.
	wantV4 := ps.Family == FamilyV4
	for i, e := range ps.Entries {
		field := fmt.Sprintf("entry.%04d", i)
		line := func(format string, args ...any) string {
			return fmt.Sprintf("Line %d: %s", i+1, fmt.Sprintf(format, args...))
		}
		p, err := netip.ParsePrefix(strings.TrimSpace(e.Prefix))
		if err != nil {
			errs[field] = line("%q is not a valid prefix (e.g. 192.0.2.0/24).", e.Prefix)
			continue
		}
		if p.Addr().Is4() != wantV4 {
			errs[field] = line("%s does not match the set's address family.", p)
			continue
		}
		// 10.0.0.1/8 is a host address wearing a prefix length. BIRD rejects
		// it; catch it here where we can say why.
		if p.Masked() != p {
			errs[field] = line("%s has host bits set — did you mean %s?", p, p.Masked())
			continue
		}
		ps.Entries[i].Prefix = p.String()

		if msg := validModifier(e.Modifier, p); msg != "" {
			errs[field] = line("%s", msg)
		}
	}
	return errs
}

// validModifier returns "" when the BIRD prefix-pattern suffix is usable with
// the given prefix, otherwise a message explaining the problem.
func validModifier(mod string, p netip.Prefix) string {
	switch mod {
	case "", "+", "-":
		return ""
	}
	m := rangeModifier.FindStringSubmatch(mod)
	if m == nil {
		return `Modifier must be empty, "+", "-" or "{low,high}".`
	}
	low, _ := strconv.Atoi(m[1])
	high, _ := strconv.Atoi(m[2])
	maxBits := p.Addr().BitLen()
	switch {
	case low > high:
		return "Range low must not exceed high."
	case high > maxBits:
		return fmt.Sprintf("Range high must not exceed /%d.", maxBits)
	case low < p.Bits():
		return fmt.Sprintf("Range low must be at least the prefix length (/%d).", p.Bits())
	}
	return ""
}

func (s *Store) ListPrefixSets() ([]PrefixSet, error) {
	rows, err := s.db.Query(`
		SELECT id, name, description, family, originate, originate_action, builtin, system
		FROM prefix_sets ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list prefix sets: %w", err)
	}
	defer rows.Close()
	var out []PrefixSet
	for rows.Next() {
		var ps PrefixSet
		if err := rows.Scan(&ps.ID, &ps.Name, &ps.Description, &ps.Family, &ps.Originate, &ps.OriginateAction, &ps.Builtin, &ps.System); err != nil {
			return nil, err
		}
		out = append(out, ps)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		entries, err := s.listEntries(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Entries = entries
	}
	return out, nil
}

// ListSelectablePrefixSets returns the sets an operator may attach to a policy.
// System sets are excluded: the bogon lists are wired into the "reject bogons"
// checkbox, and offering them in a picker would invite "announce BOGONS_V4".
func (s *Store) ListSelectablePrefixSets() ([]PrefixSet, error) {
	all, err := s.ListPrefixSets()
	if err != nil {
		return nil, err
	}
	out := make([]PrefixSet, 0, len(all))
	for _, ps := range all {
		if !ps.System {
			out = append(out, ps)
		}
	}
	return out, nil
}

func (s *Store) GetPrefixSet(id int64) (PrefixSet, error) {
	var ps PrefixSet
	row := s.db.QueryRow(`
		SELECT id, name, description, family, originate, originate_action, builtin, system
		FROM prefix_sets WHERE id = ?`, id)
	if err := row.Scan(&ps.ID, &ps.Name, &ps.Description, &ps.Family, &ps.Originate, &ps.OriginateAction, &ps.Builtin, &ps.System); err != nil {
		if err == sql.ErrNoRows {
			return PrefixSet{}, ErrNotFound
		}
		return PrefixSet{}, fmt.Errorf("store: get prefix set: %w", err)
	}
	entries, err := s.listEntries(id)
	if err != nil {
		return PrefixSet{}, err
	}
	ps.Entries = entries
	return ps, nil
}

func (s *Store) GetPrefixSetByName(name string) (PrefixSet, error) {
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM prefix_sets WHERE name = ?`, name).Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return PrefixSet{}, ErrNotFound
		}
		return PrefixSet{}, fmt.Errorf("store: get prefix set by name: %w", err)
	}
	return s.GetPrefixSet(id)
}

func (s *Store) listEntries(setID int64) ([]PrefixEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, prefix, modifier FROM prefix_set_entries
		WHERE set_id = ? ORDER BY position, id`, setID)
	if err != nil {
		return nil, fmt.Errorf("store: list prefix entries: %w", err)
	}
	defer rows.Close()
	var out []PrefixEntry
	for rows.Next() {
		var e PrefixEntry
		if err := rows.Scan(&e.ID, &e.Prefix, &e.Modifier); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) CreatePrefixSet(ps PrefixSet) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`
		INSERT INTO prefix_sets (name, description, family, originate, originate_action, builtin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?)`, ps.Name, ps.Description, ps.Family, ps.Originate, ps.OriginateAction, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("store: create prefix set: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := replaceEntries(tx, id, ps.Entries); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdatePrefixSet rewrites the set and all of its entries. Entries have no
// identity worth preserving across an edit, so the whole list is replaced
// inside one transaction rather than diffed.
func (s *Store) UpdatePrefixSet(ps PrefixSet) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`
		UPDATE prefix_sets SET name = ?, description = ?, family = ?, originate = ?, originate_action = ?, updated_at = ?
		WHERE id = ?`, ps.Name, ps.Description, ps.Family, ps.Originate, ps.OriginateAction, now(), ps.ID)
	if err != nil {
		return fmt.Errorf("store: update prefix set: %w", err)
	}
	if err := affectedOne(res); err != nil {
		return err
	}
	if err := replaceEntries(tx, ps.ID, ps.Entries); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceEntries(tx *sql.Tx, setID int64, entries []PrefixEntry) error {
	if _, err := tx.Exec(`DELETE FROM prefix_set_entries WHERE set_id = ?`, setID); err != nil {
		return fmt.Errorf("store: clear prefix entries: %w", err)
	}
	for i, e := range entries {
		if _, err := tx.Exec(`
			INSERT INTO prefix_set_entries (set_id, prefix, modifier, position)
			VALUES (?, ?, ?, ?)`, setID, e.Prefix, e.Modifier, i); err != nil {
			return fmt.Errorf("store: insert prefix entry: %w", err)
		}
	}
	return nil
}

// DeletePrefixSet refuses to remove a set a policy still names: silently
// dropping it would turn "announce these prefixes" into "announce nothing", or
// "accept only these" into "accept anything", on the next render.
func (s *Store) DeletePrefixSet(id int64) error {
	ps, err := s.GetPrefixSet(id)
	if err != nil {
		return err
	}
	// Every generated filter that rejects bogons names this set literally.
	if ps.System {
		return fmt.Errorf("store: %s is a system set and cannot be deleted; edit it under Settings", ps.Name)
	}
	uses, err := s.PrefixSetUsage(id)
	if err != nil {
		return err
	}
	if uses > 0 {
		noun := "policies"
		if uses == 1 {
			noun = "policy"
		}
		return fmt.Errorf("store: prefix set is used by %d %s", uses, noun)
	}
	res, err := s.db.Exec(`DELETE FROM prefix_sets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete prefix set: %w", err)
	}
	return affectedOne(res)
}
