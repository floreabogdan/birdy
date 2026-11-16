package store

import "testing"

// A name that is also a word of BIRD's command line changes what birdy's own
// commands mean once it is a symbol in the running config. Every kind of object
// whose name is rendered as a bare symbol refuses those words.
func TestControlKeywordsRefusedAsNames(t *testing.T) {
	validators := map[string]func(name string) map[string]string{
		"peer":          func(n string) map[string]string { p := Peer{Name: n}; return p.Validate() },
		"peer template": func(n string) map[string]string { p := PeerTemplate{Name: n}; return p.Validate() },
		"prefix set":    func(n string) map[string]string { p := PrefixSet{Name: n}; return p.Validate() },
		"AS set":        func(n string) map[string]string { p := ASSet{Name: n}; return p.Validate() },
		"community":     func(n string) map[string]string { p := CommunityDef{Name: n}; return p.Validate() },
		"RPKI server":   func(n string) map[string]string { p := RPKIServer{Name: n}; return p.Validate() },
		"BMP station":   func(n string) map[string]string { p := BMPStation{Name: n}; return p.Validate() },
	}
	for kind, validate := range validators {
		for _, word := range []string{"all", "configure", "check", "confirm", "soft", "timeout", "undo",
			"reload", "restart", "show", "protocols", "route", "status"} {
			msg := validate(word)["name"]
			if msg == "" {
				t.Errorf("%s named %q was accepted", kind, word)
				continue
			}
			if msg != ControlKeywordError(word) {
				t.Errorf("%s named %q: error %q, want the keyword message", kind, word, msg)
			}
		}
		// Keywords are case-sensitive in BIRD, and only the whole name matters.
		for _, name := range []string{"ALL", "All", "all_v4", "transit_all", "Status", "edge1"} {
			if msg := validate(name)["name"]; msg != "" {
				t.Errorf("%s named %q was refused: %s", kind, name, msg)
			}
		}
	}
	if ControlKeywordError("edge1") != "" || ControlKeywordError("ALL") != "" {
		t.Error("ControlKeywordError must be empty for a name that is not a keyword")
	}
}

// A policy's name never reaches BIRD on its own — only inside imp_NAME_v4 and
// its siblings — so no keyword can come of it, and existing habits like a
// policy called "all" keep working.
func TestPolicyNamesAreNotKeywordChecked(t *testing.T) {
	p := Policy{Name: "all", Direction: DirExport}
	if msg := p.Validate()["name"]; msg != "" {
		t.Errorf("policy named all was refused: %s", msg)
	}
}
