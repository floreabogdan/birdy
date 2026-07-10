package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The names of the two system prefix sets. Generated filters reference these
// symbols directly, which is why the sets cannot be renamed or deleted.
const (
	BogonSetV4 = "BOGONS_V4"
	BogonSetV6 = "BOGONS_V6"
)

// BogonASN is one reserved AS number or range.
type BogonASN struct {
	ID      int64
	Low     int64
	High    int64
	Private bool
	Note    string
}

// String renders the entry the way the settings editor shows it.
func (b BogonASN) String() string {
	var sb strings.Builder
	if b.Low == b.High {
		fmt.Fprintf(&sb, "%d", b.Low)
	} else {
		fmt.Fprintf(&sb, "%d-%d", b.Low, b.High)
	}
	if b.Private {
		sb.WriteString(" private")
	}
	if b.Note != "" {
		fmt.Fprintf(&sb, "  # %s", b.Note)
	}
	return sb.String()
}

// DefaultBogonASNs is the list birdy ships with: RFC 7607, RFC 4893, RFC 5398,
// RFC 6996, RFC 7300, plus the ranges IANA has reserved.
func DefaultBogonASNs() []BogonASN {
	return []BogonASN{
		{Low: 0, High: 0, Note: "RFC 7607 — reserved"},
		{Low: 23456, High: 23456, Note: "RFC 4893 — AS_TRANS"},
		{Low: 64496, High: 64511, Note: "RFC 5398 — documentation"},
		{Low: 64512, High: 65534, Private: true, Note: "RFC 6996 — private use"},
		{Low: 65535, High: 65535, Note: "RFC 7300 — last 16-bit ASN"},
		{Low: 65536, High: 65551, Note: "RFC 5398 — documentation"},
		{Low: 65552, High: 131071, Note: "IANA reserved"},
		{Low: 4200000000, High: 4294967294, Private: true, Note: "RFC 6996 — private use"},
		{Low: 4294967295, High: 4294967295, Note: "RFC 7300 — last 32-bit ASN"},
	}
}

// bogonLine matches "64512-65534 private # comment", with every part after the
// first number optional.
var bogonLine = regexp.MustCompile(`^(\d{1,10})(?:\s*-\s*(\d{1,10}))?(\s+private)?\s*(?:#\s*(.*))?$`)

// ParseBogonASNs reads the settings editor's textarea. Blank lines and lines
// that are only a comment are skipped. Errors are keyed "line.NNNN" and carry
// the line number in their text, the way prefix-set entries do.
func ParseBogonASNs(text string) ([]BogonASN, map[string]string) {
	errs := map[string]string{}
	var out []BogonASN
	n := 0
	for raw := range strings.Lines(text) {
		n++
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		field := fmt.Sprintf("line.%04d", n)
		m := bogonLine.FindStringSubmatch(line)
		if m == nil {
			errs[field] = fmt.Sprintf("Line %d: expected \"64512\" or \"64512-65534\", optionally followed by \"private\" and a # comment.", n)
			continue
		}
		low, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || low > 4294967295 {
			errs[field] = fmt.Sprintf("Line %d: %s is not a 32-bit AS number.", n, m[1])
			continue
		}
		high := low
		if m[2] != "" {
			high, err = strconv.ParseInt(m[2], 10, 64)
			if err != nil || high > 4294967295 {
				errs[field] = fmt.Sprintf("Line %d: %s is not a 32-bit AS number.", n, m[2])
				continue
			}
		}
		if low > high {
			errs[field] = fmt.Sprintf("Line %d: the range start must not exceed its end.", n)
			continue
		}
		out = append(out, BogonASN{
			Low: low, High: high,
			Private: strings.TrimSpace(m[3]) == "private",
			Note:    strings.TrimSpace(m[4]),
		})
	}
	if len(out) == 0 && len(errs) == 0 {
		errs["bogonAsns"] = "The list is empty. Every import policy that checks the AS path needs at least one entry — restore the defaults if you are unsure."
	}
	return out, errs
}

// FormatBogonASNs renders the list back into the editor.
func FormatBogonASNs(list []BogonASN) string {
	var sb strings.Builder
	for _, b := range list {
		sb.WriteString(b.String())
		sb.WriteString("\n")
	}
	return sb.String()
}

func (s *Store) ListBogonASNs() ([]BogonASN, error) {
	rows, err := s.db.Query(`SELECT id, asn_low, asn_high, is_private, note FROM bogon_asns ORDER BY position, asn_low`)
	if err != nil {
		return nil, fmt.Errorf("store: list bogon ASNs: %w", err)
	}
	defer rows.Close()
	var out []BogonASN
	for rows.Next() {
		var b BogonASN
		if err := rows.Scan(&b.ID, &b.Low, &b.High, &b.Private, &b.Note); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ReplaceBogonASNs rewrites the whole list in one transaction. Entries have no
// identity worth preserving across an edit.
func (s *Store) ReplaceBogonASNs(list []BogonASN) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceBogonASNs(tx, list); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceBogonASNs(tx *sql.Tx, list []BogonASN) error {
	if _, err := tx.Exec(`DELETE FROM bogon_asns`); err != nil {
		return fmt.Errorf("store: clear bogon ASNs: %w", err)
	}
	for i, b := range list {
		if _, err := tx.Exec(`
			INSERT INTO bogon_asns (asn_low, asn_high, is_private, note, position)
			VALUES (?, ?, ?, ?, ?)`, b.Low, b.High, b.Private, b.Note, i); err != nil {
			return fmt.Errorf("store: insert bogon ASN: %w", err)
		}
	}
	return nil
}

// GetBogonSet returns one of the two system prefix sets.
func (s *Store) GetBogonSet(family string) (PrefixSet, error) {
	name := BogonSetV4
	if family == FamilyV6 {
		name = BogonSetV6
	}
	return s.GetPrefixSetByName(name)
}
