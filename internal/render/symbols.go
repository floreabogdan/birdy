package render

import (
	"fmt"
	"regexp"
)

// symbolDecl matches a line that declares a BIRD symbol: a define, function,
// filter, BGP template, named protocol or table. Every writer starts these at
// the beginning of a line; a body line never does (it starts with a keyword
// such as "import" or "if", or a comment).
var symbolDecl = regexp.MustCompile(`(?m)^[ \t]*(define|function|filter|template bgp|protocol [a-z]+|roa[46] table|ipv[46] table)[ \t]+([A-Za-z_][A-Za-z0-9_]*)`)

// birdSymbols are names BIRD defines on its own, before reading the file.
var birdSymbols = map[string]string{"master4": "BIRD's own master4 table", "master6": "BIRD's own master6 table"}

// checkSymbols refuses a model whose rendered sections declare one name twice.
// BIRD keeps defines, functions, filters, templates and protocols in a single
// namespace, so such a config fails `bird -p` with "Symbol already defined" —
// every per-object check passes, and only here are all symbols in one place.
// Raw configuration is left to BIRD: it is free text, and a commented-out block
// there is not a clash.
func checkSymbols(secs []Section) error {
	seen := map[string]string{}
	for name, what := range birdSymbols {
		seen[name] = what
	}
	for _, s := range secs {
		if s.Path == "raw" {
			continue
		}
		for _, m := range symbolDecl.FindAllStringSubmatch(s.Body, -1) {
			what, name := m[1], m[2]
			if first, dup := seen[name]; dup {
				return fmt.Errorf("%q is declared twice, by %s and by %s: BIRD keeps every name in one namespace, so rename one of them", name, first, what)
			}
			seen[name] = what
		}
	}
	return nil
}
