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

// controlKeywords are the words of the commands birdy sends over BIRD's control
// socket that stop working — or change meaning — once the running config
// defines a symbol of the same name. BIRD's lexer looks a word up as a symbol
// before it tries it as a keyword, and `bird -p` accepts most keywords as
// names, so a protocol or define called "configure" loads fine and then every
// "configure ..." command is a syntax error: the apply that brought it in can
// be neither confirmed nor undone, only left to time out. "all" is the worst of
// them: a protocol called "all" narrows "reload all" to itself, and while no
// such symbol is loaded, "restart all" on its behalf restarts every protocol.
//
// The list is what was shown to break on BIRD 2.17.5, one word at a time, and
// deliberately nothing more: it is not BIRD's keyword list. Words that merely
// fail to parse inside bird.conf are `bird -p`'s to refuse, and guessing at
// those would lock out names that work on someone's router today. Keywords are
// case-sensitive in BIRD, so only these exact lower-case spellings are held.
var controlKeywords = map[string]bool{
	"all":       true, // reload all, show protocols all, show route ... all; "restart all"
	"configure": true, "check": true, "confirm": true, "soft": true, "timeout": true, "undo": true,
	"reload": true, "restart": true,
	"show": true, "protocols": true, "route": true, "status": true,
}

// ControlKeywordError is the form error for a name that is one of
// controlKeywords, or "" for any other name. Exported so the web layer can
// recognise this one refusal: an object that already carries such a name (saved
// before the check existed) may keep it on edit, where every other name error
// still stands.
func ControlKeywordError(name string) string {
	if !controlKeywords[name] {
		return ""
	}
	return name + " is a BIRD keyword: BIRD's command line uses that word, and a config that defines it breaks the commands birdy sends. Choose another name."
}

// derivedSymbols are the names birdy builds from another object's name —
// policy functions (and an import policy's RFC 7999 blackhole form), per-peer
// filters, originator protocols. Such a name is taken only while the object it
// derives from exists.
var derivedSymbols = []struct {
	pattern *regexp.Regexp
	table   string
}{
	{regexp.MustCompile(`^(?:imp|exp|bh)_(.+)_v[46]$`), "policies"},
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
