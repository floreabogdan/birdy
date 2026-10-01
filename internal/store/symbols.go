package store

import (
	"database/sql"
	"fmt"
	"regexp"
)

// SymbolUse is one thing in the model that renders a BIRD symbol of a given
// name. BIRD keeps protocols, templates, defines, functions and filters in a
// single namespace, so two of them sharing a name fail `bird -p` with "Symbol
// already defined" — the whole config, not just the one block.
type SymbolUse struct {
	Kind string // "peer", "peer template", "prefix set", "AS set", "community", "RPKI server", "BMP station" or "built-in"
	ID   int64  // 0 for a built-in
	Name string
}

func (u SymbolUse) String() string {
	if u.Kind == "built-in" {
		return "a name birdy generates itself"
	}
	return fmt.Sprintf("%s %q", u.Kind, u.Name)
}

// builtinSymbols are the names birdy renders on its own, outside any table,
// and the tables BIRD defines before reading the file.
var builtinSymbols = map[string]bool{
	"LOCAL_ASN": true, "FROM_UPSTREAM": true, "FROM_IX": true, "FROM_CUSTOMER": true, "RPKI_INVALID": true,
	"BOGON_ASNS": true, "BOGON_ASNS_EXCEPT_PRIVATE": true,
	"rpki4": true, "rpki6": true,
	"device1": true, "direct1": true, "kernel4": true, "kernel6": true, "bfd1": true,
	"static_v4": true, "static_v6": true,
	"master4": true, "master6": true,
}

// derivedSymbols are the names birdy builds from another object's name —
// policy functions, per-peer filters, originator protocols. Such a name is
// taken only while the object it derives from exists.
var derivedSymbols = []struct {
	pattern *regexp.Regexp
	table   string
}{
	{regexp.MustCompile(`^(?:imp|exp)_(.+)_v[46]$`), "policies"},
	{regexp.MustCompile(`^(?:ebgp|ibgp)_(?:in|out)_(.+)$`), "peers"},
	{regexp.MustCompile(`^originate_(.+)$`), "prefix_sets"},
}

// symbolTables are the tables whose rows each render a symbol named by their
// name column.
var symbolTables = []struct{ table, kind string }{
	{"peers", "peer"},
	{"peer_templates", "peer template"},
	{"prefix_sets", "prefix set"},
	{"as_sets", "AS set"},
	{"communities", "community"},
	{"rpki_servers", "RPKI server"},
	{"bmp_stations", "BMP station"},
}

// SymbolUses lists everything that already renders a BIRD symbol called name,
// so a save can refuse a name the config could not load with. The caller
// ignores its own row when renaming in place.
func (s *Store) SymbolUses(name string) ([]SymbolUse, error) {
	var out []SymbolUse
	if builtinSymbols[name] {
		out = append(out, SymbolUse{Kind: "built-in", Name: name})
	}
	for _, d := range derivedSymbols {
		m := d.pattern.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		var one int
		// The table name comes from the fixed list above, never from input.
		err := s.db.QueryRow(`SELECT 1 FROM `+d.table+` WHERE name = ?`, m[1]).Scan(&one)
		if err == nil {
			out = append(out, SymbolUse{Kind: "built-in", Name: name})
			break
		}
		if err != sql.ErrNoRows {
			return nil, fmt.Errorf("store: symbol uses in %s: %w", d.table, err)
		}
	}
	for _, st := range symbolTables {
		var id int64
		// The table name comes from the fixed list above, never from input.
		err := s.db.QueryRow(`SELECT id FROM `+st.table+` WHERE name = ?`, name).Scan(&id)
		if err == nil {
			out = append(out, SymbolUse{Kind: st.kind, ID: id, Name: name})
			continue
		}
		if err != sql.ErrNoRows {
			return nil, fmt.Errorf("store: symbol uses in %s: %w", st.table, err)
		}
	}
	return out, nil
}
