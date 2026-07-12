package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ASSet is a named list of AS numbers.
//
// It exists because BIRD has no concept of an IRR AS-SET. An operator gives you
// "AS-CUSTOMER"; a tool like bgpq4 expands that, recursively, into the member
// ASNs; those members land here and render as a BIRD integer set. Source keeps
// the AS-SET name so the expansion can be re-run later without guessing.
type ASSet struct {
	ID          int64
	Name        string
	Description string
	Source      string // e.g. "AS-CUSTOMER", the IRR object this was expanded from
	Entries     []ASNRange
}

// ASNRange is one AS number, or a contiguous block of them.
type ASNRange struct {
	ID   int64
	Low  int64
	High int64
	Note string
}

func (a ASNRange) String() string {
	var sb strings.Builder
	if a.Low == a.High {
		fmt.Fprintf(&sb, "%d", a.Low)
	} else {
		fmt.Fprintf(&sb, "%d-%d", a.Low, a.High)
	}
	if a.Note != "" {
		fmt.Fprintf(&sb, "  # %s", a.Note)
	}
	return sb.String()
}

// asnLine matches "64512", "64512-65534" or "AS64512", each with an optional
// trailing comment. The AS prefix is accepted because that is how humans and
// IRR tooling write AS numbers.
var asnLine = regexp.MustCompile(`^(?i:AS)?(\d{1,10})(?:\s*-\s*(?:AS)?(\d{1,10}))?\s*(?:#\s*(.*))?$`)

// ParseASNRanges reads one AS number or range per line. Blank lines and
// comment-only lines are skipped, so bgpq4 output pastes in unchanged.
func ParseASNRanges(text string) ([]ASNRange, map[string]string) {
	errs := map[string]string{}
	var out []ASNRange
	n := 0
	for raw := range strings.Lines(text) {
		n++
		line := strings.TrimSpace(raw)
		// Tolerate a trailing comma: bgpq4's plain output is comma-separated.
		line = strings.TrimSuffix(line, ",")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		field := fmt.Sprintf("line.%04d", n)
		m := asnLine.FindStringSubmatch(line)
		if m == nil {
			errs[field] = fmt.Sprintf("Line %d: expected an AS number like 64500, AS64500 or 64500-64510.", n)
			continue
		}
		low, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || low < 1 || low > 4294967295 {
			errs[field] = fmt.Sprintf("Line %d: %s is not a valid AS number.", n, m[1])
			continue
		}
		high := low
		if m[2] != "" {
			high, err = strconv.ParseInt(m[2], 10, 64)
			if err != nil || high > 4294967295 {
				errs[field] = fmt.Sprintf("Line %d: %s is not a valid AS number.", n, m[2])
				continue
			}
		}
		if low > high {
			errs[field] = fmt.Sprintf("Line %d: the range start must not exceed its end.", n)
			continue
		}
		out = append(out, ASNRange{Low: low, High: high, Note: strings.TrimSpace(m[3])})
	}
	return out, errs
}

func FormatASNRanges(list []ASNRange) string {
	var sb strings.Builder
	for _, a := range list {
		sb.WriteString(a.String())
		sb.WriteString("\n")
	}
	return sb.String()
}

func (as *ASSet) Validate() map[string]string {
	var errs map[string]string
	as.Name, errs = validateNameDesc(as.Name, as.Description)
	if strings.ContainsAny(as.Source, "\"\n\r") {
		errs["source"] = "Quotes and line breaks are not allowed."
	}
	// BIRD has no syntax for an empty integer set, and a policy that permits no
	// origin at all would reject every route.
	if len(as.Entries) == 0 {
		errs["entries"] = "Add at least one AS number."
	}
	return errs
}

func (s *Store) ListASSets() ([]ASSet, error) {
	rows, err := s.db.Query(`SELECT id, name, description, source FROM as_sets ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list AS sets: %w", err)
	}
	defer rows.Close()
	var out []ASSet
	for rows.Next() {
		var as ASSet
		if err := rows.Scan(&as.ID, &as.Name, &as.Description, &as.Source); err != nil {
			return nil, err
		}
		out = append(out, as)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Entries, err = s.asSetEntries(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) GetASSetByName(name string) (ASSet, error) {
	var as ASSet
	row := s.db.QueryRow(`SELECT id, name, description, source FROM as_sets WHERE name = ?`, name)
	if err := row.Scan(&as.ID, &as.Name, &as.Description, &as.Source); err != nil {
		if err == sql.ErrNoRows {
			return ASSet{}, ErrNotFound
		}
		return ASSet{}, fmt.Errorf("store: get AS set: %w", err)
	}
	entries, err := s.asSetEntries(as.ID)
	if err != nil {
		return ASSet{}, err
	}
	as.Entries = entries
	return as, nil
}

func (s *Store) asSetEntries(setID int64) ([]ASNRange, error) {
	rows, err := s.db.Query(`SELECT id, asn_low, asn_high, note FROM as_set_entries WHERE set_id = ? ORDER BY position, asn_low`, setID)
	if err != nil {
		return nil, fmt.Errorf("store: list AS set entries: %w", err)
	}
	defer rows.Close()
	var out []ASNRange
	for rows.Next() {
		var a ASNRange
		if err := rows.Scan(&a.ID, &a.Low, &a.High, &a.Note); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CreateASSet(as ASSet) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`INSERT INTO as_sets (name, description, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		as.Name, as.Description, as.Source, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("store: create AS set: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := replaceASSetEntries(tx, id, as.Entries); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) UpdateASSet(as ASSet) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE as_sets SET name = ?, description = ?, source = ?, updated_at = ? WHERE id = ?`,
		as.Name, as.Description, as.Source, now(), as.ID)
	if err != nil {
		return fmt.Errorf("store: update AS set: %w", err)
	}
	if err := affectedOne(res); err != nil {
		return err
	}
	if err := replaceASSetEntries(tx, as.ID, as.Entries); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceASSetEntries(tx *sql.Tx, setID int64, entries []ASNRange) error {
	if _, err := tx.Exec(`DELETE FROM as_set_entries WHERE set_id = ?`, setID); err != nil {
		return err
	}
	for i, a := range entries {
		if _, err := tx.Exec(`INSERT INTO as_set_entries (set_id, asn_low, asn_high, note, position) VALUES (?, ?, ?, ?, ?)`,
			setID, a.Low, a.High, a.Note, i); err != nil {
			return fmt.Errorf("store: insert AS set entry: %w", err)
		}
	}
	return nil
}

// ASSetUsage counts the policies filtering origins through this set.
func (s *Store) ASSetUsage(setID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM policies WHERE origin_as_set_id = ?`, setID).Scan(&n)
	return n, err
}

// DeleteASSet refuses while a policy still filters through it: dropping the set
// would turn "accept only these origins" into "accept any origin".
func (s *Store) DeleteASSet(id int64) error {
	uses, err := s.ASSetUsage(id)
	if err != nil {
		return err
	}
	if uses > 0 {
		return fmt.Errorf("store: AS set is used by %d %s", uses, pluralize(uses, "policy", "policies"))
	}
	res, err := s.db.Exec(`DELETE FROM as_sets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete AS set: %w", err)
	}
	return affectedOne(res)
}

// Contains reports whether asn is a member. Used to warn when a peer's own ASN
// is missing from the set its policy filters on.
func (as ASSet) Contains(asn int64) bool {
	for _, e := range as.Entries {
		if asn >= e.Low && asn <= e.High {
			return true
		}
	}
	return false
}
