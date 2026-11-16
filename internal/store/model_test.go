package store

import (
	"database/sql"
	"strings"
	"testing"
)

func validPeer() Peer {
	return Peer{
		Name: "edge_v4", Role: RoleUpstream, Enabled: true, EnforceFirstAS: true,
		NeighborIP: "198.51.100.1", RemoteASN: 64497, ImportLimitAction: "restart",
	}
}

func TestPeerValidateAcceptsGoodInput(t *testing.T) {
	p := validPeer()
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

// The peer name is interpolated straight into bird.conf as a protocol name and
// into generated filter names, so it must never carry BIRD syntax.
func TestPeerNameRejectsConfigInjection(t *testing.T) {
	bad := []string{
		"", "9lives", "has space", "has-dash", "quote\"", "semi;colon",
		"nl\nprotocol", "brace}", "edge_v4; protocol bgp evil {",
	}
	for _, name := range bad {
		p := validPeer()
		p.Name = name
		if errs := p.Validate(); errs["name"] == "" {
			t.Errorf("name %q should be rejected", name)
		}
	}
	for _, name := range []string{"edge_v4", "_x", "A1", "ibgp_core_2"} {
		p := validPeer()
		p.Name = name
		if errs := p.Validate(); errs["name"] != "" {
			t.Errorf("name %q should be accepted: %s", name, errs["name"])
		}
	}
}

func TestPeerDescriptionAndPasswordCannotEscapeTheirStrings(t *testing.T) {
	p := validPeer()
	p.Description = `x"; disabled; description "`
	if errs := p.Validate(); errs["description"] == "" {
		t.Error("a quote in the description must be rejected")
	}
	p = validPeer()
	p.Password = "a\"b"
	if errs := p.Validate(); errs["password"] == "" {
		t.Error("a quote in the password must be rejected")
	}
	p = validPeer()
	p.Description = "line\nbreak"
	if errs := p.Validate(); errs["description"] == "" {
		t.Error("a newline in the description must be rejected")
	}
}

func TestPeerASNBounds(t *testing.T) {
	cases := map[int64]bool{
		0: false, 1: true, 23456: false, 65535: false,
		65536: true, 4294967295: false, 4294967296: false, 65551: true,
	}
	for asn, ok := range cases {
		p := validPeer()
		p.RemoteASN = asn
		errs := p.Validate()
		if got := errs["remoteAsn"] == ""; got != ok {
			t.Errorf("AS%d: accepted=%v, want %v", asn, got, ok)
		}
	}
}

func TestPeerLocalIPMustMatchNeighborFamily(t *testing.T) {
	p := validPeer()
	p.LocalIP = "2001:db8::2" // neighbor is v4
	if errs := p.Validate(); errs["localIp"] == "" {
		t.Error("mixed-family local address must be rejected")
	}
	p.LocalIP = "198.51.100.2"
	if errs := p.Validate(); errs["localIp"] != "" {
		t.Errorf("same-family local address should be accepted: %v", errs)
	}
	p.LocalIP = ""
	if errs := p.Validate(); errs["localIp"] != "" {
		t.Error("blank local address should be accepted")
	}
}

func TestPeerIsV6(t *testing.T) {
	p := validPeer()
	if p.IsV6() {
		t.Error("v4 neighbor reported as v6")
	}
	p.NeighborIP = "2001:db8::1"
	if !p.IsV6() {
		t.Error("v6 neighbor reported as v4")
	}
}

func TestPeerValidatesTransportEndpoint(t *testing.T) {
	p := validPeer()
	p.TransportEndpoint = "not-an-address"
	if _, ok := p.Validate()["transportEndpoint"]; !ok {
		t.Fatal("invalid tunnel endpoint was accepted")
	}
	p = validPeer()
	p.TransportEndpoint = "2001:db8::200"
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("valid tunnel endpoint rejected: %v", errs)
	}
}

func TestPeerCRUD(t *testing.T) {
	s := openTest(t)

	id, err := s.CreatePeer(validPeer())
	if err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}
	got, err := s.GetPeer(id)
	if err != nil {
		t.Fatalf("GetPeer: %v", err)
	}
	if got.Name != "edge_v4" || got.RemoteASN != 64497 || !got.Enabled {
		t.Errorf("round trip mismatch: %+v", got)
	}

	got.Description = "transit"
	got.TransportEndpoint = "198.51.100.200"
	got.Enabled = false
	if err := s.UpdatePeer(got); err != nil {
		t.Fatalf("UpdatePeer: %v", err)
	}
	again, _ := s.GetPeer(id)
	if again.Description != "transit" || again.Enabled || again.TransportEndpoint != "198.51.100.200" {
		t.Errorf("update did not stick: %+v", again)
	}

	peers, err := s.ListPeers()
	if err != nil || len(peers) != 1 {
		t.Fatalf("ListPeers: %v, %d peers", err, len(peers))
	}

	if err := s.DeletePeer(id); err != nil {
		t.Fatalf("DeletePeer: %v", err)
	}
	if _, err := s.GetPeer(id); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if err := s.DeletePeer(id); err != ErrNotFound {
		t.Errorf("deleting twice should report ErrNotFound, got %v", err)
	}
}

func TestPeerNameIsUnique(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreatePeer(validPeer()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePeer(validPeer()); err == nil {
		t.Error("duplicate peer name should violate the unique constraint")
	}
}

func TestPrefixSetValidate(t *testing.T) {
	ps := PrefixSet{Name: "ANNOUNCE_V4", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "192.0.2.0/24"}}}
	if errs := ps.Validate(); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	// Empty set.
	empty := PrefixSet{Name: "X", Family: FamilyV4}
	if errs := empty.Validate(); errs["entries"] == "" {
		t.Error("an empty set must be rejected")
	}

	// Host bits set.
	hostBits := PrefixSet{Name: "X", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "10.0.0.1/8"}}}
	errs := hostBits.Validate()
	if errs["entry.0000"] == "" {
		t.Error("10.0.0.1/8 has host bits set and must be rejected")
	}

	// Family mismatch.
	mixed := PrefixSet{Name: "X", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "2001:db8::/32"}}}
	if errs := mixed.Validate(); errs["entry.0000"] == "" {
		t.Error("v6 prefix in a v4 set must be rejected")
	}

	// Surrounding whitespace is trimmed and the prefix re-serialised.
	norm := PrefixSet{Name: "X", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "  192.0.2.0/24  "}}}
	if errs := norm.Validate(); len(errs) != 0 {
		t.Fatalf("whitespace should be tolerated: %v", errs)
	}
	if norm.Entries[0].Prefix != "192.0.2.0/24" {
		t.Errorf("prefix should be normalised, got %q", norm.Entries[0].Prefix)
	}

	// Leading zeros are ambiguous (octal?) and netip rejects them; so do we.
	zeros := PrefixSet{Name: "X", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "192.000.2.0/24"}}}
	if errs := zeros.Validate(); errs["entry.0000"] == "" {
		t.Error("leading zeros in an address must be rejected")
	}
}

func TestPrefixEntryModifiers(t *testing.T) {
	good := []string{"", "+", "-", "{24,32}", "{8,8}"}
	for _, m := range good {
		ps := PrefixSet{Name: "X", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "10.0.0.0/8", Modifier: m}}}
		if errs := ps.Validate(); errs["entry.0000"] != "" {
			t.Errorf("modifier %q should be accepted: %s", m, errs["entry.0000"])
		}
	}
	bad := map[string]string{
		"++":      "garbage",
		"{32,24}": "low above high",
		"{8,33}":  "high beyond /32",
		"{4,24}":  "low shorter than the prefix",
		"; evil":  "injection",
		"{24}":    "malformed range",
	}
	for m, why := range bad {
		ps := PrefixSet{Name: "X", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "10.0.0.0/8", Modifier: m}}}
		if errs := ps.Validate(); errs["entry.0000"] == "" {
			t.Errorf("modifier %q (%s) should be rejected", m, why)
		}
	}
}

func TestPrefixEntryPattern(t *testing.T) {
	e := PrefixEntry{Prefix: "10.0.0.0/8", Modifier: "+"}
	if e.Pattern() != "10.0.0.0/8+" {
		t.Errorf("Pattern() = %q", e.Pattern())
	}
}

func TestStarterPackIsSeeded(t *testing.T) {
	s := openTest(t)
	sets, err := s.ListPrefixSets()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]PrefixSet{}
	for _, ps := range sets {
		byName[ps.Name] = ps
	}
	for _, name := range []string{"BOGONS_V4", "BOGONS_V6"} {
		ps, ok := byName[name]
		if !ok {
			t.Fatalf("%s not seeded", name)
		}
		if !ps.Builtin {
			t.Errorf("%s should be tagged builtin", name)
		}
		if len(ps.Entries) == 0 {
			t.Errorf("%s has no entries", name)
		}
		// Every seeded entry must survive its own validator.
		if errs := ps.Validate(); len(errs) != 0 {
			t.Errorf("seeded %s does not validate: %v", name, errs)
		}
	}
	if len(byName["BOGONS_V4"].Entries) != len(bogonsV4) {
		t.Error("v4 bogon entry count mismatch")
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	s := openTest(t)
	before, _ := s.ListPrefixSets()
	// migrate() runs on every Open; reopening must not duplicate the pack.
	if err := migrate(s.db); err != nil {
		t.Fatal(err)
	}
	after, _ := s.ListPrefixSets()
	if len(before) != len(after) {
		t.Errorf("seeding ran twice: %d -> %d sets", len(before), len(after))
	}
}

func TestPrefixSetCRUD(t *testing.T) {
	s := openTest(t)

	ps := PrefixSet{
		Name: "MY_AGGREGATES", Description: "ours", Family: FamilyV4, Originate: true,
		Entries: []PrefixEntry{{Prefix: "192.0.2.0/24"}, {Prefix: "198.51.100.0/24", Modifier: "+"}},
	}
	id, err := s.CreatePrefixSet(ps)
	if err != nil {
		t.Fatalf("CreatePrefixSet: %v", err)
	}
	got, err := s.GetPrefixSet(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 || got.Entries[1].Modifier != "+" || !got.Originate {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.Builtin {
		t.Error("user-created sets must not be builtin")
	}

	// Entries are replaced wholesale, and order is preserved.
	got.Entries = []PrefixEntry{{Prefix: "203.0.113.0/24"}}
	if err := s.UpdatePrefixSet(got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetPrefixSet(id)
	if len(again.Entries) != 1 || again.Entries[0].Prefix != "203.0.113.0/24" {
		t.Errorf("entries not replaced: %+v", again.Entries)
	}

	if err := s.DeletePrefixSet(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPrefixSet(id); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// Deleting a set a policy names would quietly turn "announce these prefixes"
// into "announce nothing" on the next render.
func TestDeletePrefixSetInUseIsRefused(t *testing.T) {
	s := openTest(t)
	setID, err := s.CreatePrefixSet(PrefixSet{
		Name: "MY_AGGREGATES", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "192.0.2.0/24"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	polID, err := s.CreatePolicy(Policy{
		Name: "EXPORT_MINE", Direction: DirExport, SetIDs: []int64{setID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePrefixSet(setID); err == nil {
		t.Fatal("deleting a set that a policy announces must be refused")
	}
	if err := s.DeletePolicy(polID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePrefixSet(setID); err != nil {
		t.Errorf("delete after last reference removed: %v", err)
	}
}

// An accept-only set is just as load-bearing: dropping it would turn
// "accept only these prefixes" into "accept anything".
func TestDeletePrefixSetUsedAsAcceptOnlyIsRefused(t *testing.T) {
	s := openTest(t)
	setID, err := s.CreatePrefixSet(PrefixSet{
		Name: "CUST_A", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "192.0.2.0/24"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePolicy(Policy{
		Name: "IMPORT_CUST_A", Direction: DirImport, DefaultRoute: DefaultReject,
		BogonASNs: BogonASNsOff, AcceptOnlySetID: sql.NullInt64{Int64: setID, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePrefixSet(setID); err == nil {
		t.Error("deleting an accept-only set must be refused")
	}
}

func TestPrefixEntriesCascadeOnDelete(t *testing.T) {
	s := openTest(t)
	id, err := s.CreatePrefixSet(PrefixSet{
		Name: "TMP", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "192.0.2.0/24"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePrefixSet(id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prefix_set_entries WHERE set_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d orphaned entries left behind", n)
	}
}

func TestPeerBFDTimersPersist(t *testing.T) {
	s := openTest(t)
	p := validPeer()
	p.BFD, p.BFDInterval, p.BFDMultiplier = true, 300, 10
	id, err := s.CreatePeer(p)
	if err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}
	got, err := s.GetPeer(id)
	if err != nil {
		t.Fatalf("GetPeer: %v", err)
	}
	if !got.BFD || got.BFDInterval != 300 || got.BFDMultiplier != 10 {
		t.Errorf("BFD timers did not round-trip: %+v", got)
	}

	got.BFDInterval, got.BFDMultiplier = 1000, 3
	if err := s.UpdatePeer(got); err != nil {
		t.Fatalf("UpdatePeer: %v", err)
	}
	peers, err := s.ListPeers()
	if err != nil || len(peers) != 1 {
		t.Fatalf("ListPeers: %v, %d peers", err, len(peers))
	}
	if peers[0].BFDInterval != 1000 || peers[0].BFDMultiplier != 3 {
		t.Errorf("BFD timer update did not stick: %+v", peers[0])
	}
}

func TestPeerValidatesBFDTimers(t *testing.T) {
	for _, tc := range []struct {
		interval, multiplier int
		wantErr              string
	}{
		{0, 0, ""},
		{10, 1, ""},
		{300, 10, ""},
		{10000, 255, ""},
		{9, 0, "bfdInterval"},
		{10001, 0, "bfdInterval"},
		{-1, 0, "bfdInterval"},
		{0, 256, "bfdMultiplier"},
		{0, -1, "bfdMultiplier"},
	} {
		p := validPeer()
		p.BFD, p.BFDInterval, p.BFDMultiplier = true, tc.interval, tc.multiplier
		errs := p.Validate()
		if tc.wantErr == "" && len(errs) != 0 {
			t.Errorf("interval %d, multiplier %d: unexpected errors %v", tc.interval, tc.multiplier, errs)
		}
		if _, ok := errs[tc.wantErr]; tc.wantErr != "" && !ok {
			t.Errorf("interval %d, multiplier %d: want an error on %s, got %v", tc.interval, tc.multiplier, tc.wantErr, errs)
		}
	}

	// Timers mean nothing without BFD. Normalise rather than reject — the form
	// submits them regardless — so a disabled session carries no hidden state.
	p := validPeer()
	p.BFD, p.BFDInterval, p.BFDMultiplier = false, 300, 10
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if p.BFDInterval != 0 || p.BFDMultiplier != 0 {
		t.Errorf("timers should be cleared when BFD is off: %+v", p)
	}
}

// netip accepts any non-empty zone after "%" and String() hands it back
// verbatim, so a zone is free text that every address validator would carry
// straight into bird.conf. The first value is a working route leak: it closes
// the neighbor line, opens an import-all/export-all channel and a second
// session. The second is the honest form, which still breaks the parse once
// the kernel filter quotes the address back. Every validator that writes an
// address into the config must refuse both.
func TestConfigAddressesRefuseZones(t *testing.T) {
	zoned := []string{
		"2001:db8::1%x as 64497;\n\tipv6 { import all; export all; };\n}\nprotocol bgp edge_shadow {",
		"fe80::1%eth0",
	}
	for _, z := range zoned {
		checks := []struct {
			name, field string
			errs        func() map[string]string
		}{
			{"peer neighbor", "neighborIp", func() map[string]string {
				p := validPeer()
				p.NeighborIP, p.Interface = z, "eth0"
				return p.Validate()
			}},
			{"peer local address", "localIp", func() map[string]string {
				p := validPeer()
				p.NeighborIP, p.LocalIP = "2001:db8::2", z
				return p.Validate()
			}},
			{"peer tunnel endpoint", "transportEndpoint", func() map[string]string {
				p := validPeer()
				p.TransportEndpoint = z
				return p.Validate()
			}},
			{"static next hop", "nextHop", func() map[string]string {
				r := StaticRoute{Prefix: "2001:db8:1::/48", Action: StaticVia, NextHop: z}
				return r.Validate()
			}},
			{"RPKI server", "host", func() map[string]string {
				s := RPKIServer{Name: "rtr", Host: z, Port: 323}
				return s.Validate()
			}},
			{"BMP station", "address", func() map[string]string {
				b := BMPStation{Name: "bmp", Address: z, Port: 1790}
				return b.Validate()
			}},
			{"kernel preferred source", "prefsrc", func() map[string]string {
				st := Settings{KernelPrefSrcV6: z}
				if msg := st.ValidateKernelPrefSrcV6(); msg != "" {
					return map[string]string{"prefsrc": msg}
				}
				return nil
			}},
		}
		for _, c := range checks {
			if msg := c.errs()[c.field]; msg == "" {
				t.Errorf("%s accepted the zoned address %q", c.name, z)
			} else if !strings.Contains(msg, "%zone") {
				t.Errorf("%s: the message should say the zone is the problem, got %q", c.name, msg)
			}
		}
	}

	// A link-local peer still works the supported way: the interface in its field.
	p := validPeer()
	p.NeighborIP, p.Interface = "fe80::1", "eth0"
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("a link-local neighbor with an interface should validate: %v", errs)
	}
}

// BIRD refuses a symbol longer than 64 characters, and birdy derives longer
// symbols from three kinds of name: a peer's filters (ebgp_out_NAME), a
// policy's functions (imp_NAME_v4) and a prefix set's originator
// (originate_NAME). A name that fits on its own but not inside what is derived
// from it saved fine and then failed every apply with "Symbol too long".
func TestNameLengthLeavesRoomForDerivedSymbols(t *testing.T) {
	name := func(n int) string { return "N" + strings.Repeat("x", n-1) }
	cases := []struct {
		kind     string
		max      int
		validate func(string) map[string]string
	}{
		{"peer", MaxPeerNameLen, func(n string) map[string]string { p := validPeer(); p.Name = n; return p.Validate() }},
		{"policy", MaxPolicyNameLen, func(n string) map[string]string {
			p := Policy{Name: n, Direction: DirImport, DefaultRoute: DefaultReject, BogonASNs: BogonASNsOff}
			return p.Validate()
		}},
		{"prefix set", MaxPrefixSetNameLen, func(n string) map[string]string {
			ps := PrefixSet{Name: n, Family: FamilyV4}
			return ps.Validate()
		}},
		// Rendered under their own name only, so the 63 they always had.
		{"AS set", 63, func(n string) map[string]string { as := ASSet{Name: n}; return as.Validate() }},
		{"community", 63, func(n string) map[string]string { cd := CommunityDef{Name: n, A: 65000, B: 1}; return cd.Validate() }},
		{"peer template", 63, func(n string) map[string]string {
			tp := PeerTemplate{Name: n, Role: RoleUpstream, ImportLimitAction: "restart"}
			return tp.Validate()
		}},
	}
	for _, c := range cases {
		if errs := c.validate(name(c.max)); errs["name"] != "" {
			t.Errorf("%s: a %d-character name should be accepted: %s", c.kind, c.max, errs["name"])
		}
		if errs := c.validate(name(c.max + 1)); errs["name"] == "" {
			t.Errorf("%s: a %d-character name should be refused", c.kind, c.max+1)
		}
	}
	// The derived symbols those caps leave room for.
	for _, sym := range []string{
		"ebgp_out_" + name(MaxPeerNameLen),
		"imp_" + name(MaxPolicyNameLen) + "_v4",
		"originate_" + name(MaxPrefixSetNameLen),
	} {
		if len(sym) != 64 {
			t.Errorf("%s is %d characters; the cap should leave it exactly at BIRD's 64", sym, len(sym))
		}
	}
}
