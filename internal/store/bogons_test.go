package store

import (
	"slices"
	"strings"
	"testing"
)

func TestParseBogonASNs(t *testing.T) {
	text := `# reserved and special-purpose AS numbers
0            # RFC 7607
23456        # AS_TRANS

64512-65534 private   # RFC 6996
4200000000 - 4294967294  private
131072
`
	list, errs := ParseBogonASNs(text)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(list) != 5 {
		t.Fatalf("want 5 entries, got %d: %+v", len(list), list)
	}
	if list[0].Low != 0 || list[0].High != 0 || list[0].Note != "RFC 7607" {
		t.Errorf("single value parsed wrong: %+v", list[0])
	}
	if list[2].Low != 64512 || list[2].High != 65534 || !list[2].Private || list[2].Note != "RFC 6996" {
		t.Errorf("private range parsed wrong: %+v", list[2])
	}
	if !list[3].Private || list[3].Low != 4200000000 || list[3].High != 4294967294 {
		t.Errorf("spaces around the dash should be tolerated: %+v", list[3])
	}
	if list[4].Private || list[4].Note != "" {
		t.Errorf("bare value should be neither private nor annotated: %+v", list[4])
	}
}

func TestParseBogonASNsRejectsGarbage(t *testing.T) {
	cases := map[string]string{
		"not a number\n":      "words",
		"65534-64512\n":       "reversed range",
		"4294967296\n":        "beyond 32 bits",
		"100-4294967296\n":    "range end beyond 32 bits",
		"64512 privat\n":      "misspelled keyword",
		"64512-65534; drop\n": "injection attempt",
	}
	for text, why := range cases {
		if _, errs := ParseBogonASNs(text); len(errs) == 0 {
			t.Errorf("%q (%s) should be rejected", strings.TrimSpace(text), why)
		}
	}
}

func TestParseBogonASNsRejectsEmptyList(t *testing.T) {
	if _, errs := ParseBogonASNs("# only a comment\n\n"); errs["bogonAsns"] == "" {
		t.Error("an empty list must be rejected: every AS-path check needs entries")
	}
}

func TestBogonASNErrorsCarryLineNumbers(t *testing.T) {
	_, errs := ParseBogonASNs("0\nnonsense\n")
	found := false
	for _, msg := range errs {
		if strings.Contains(msg, "Line 2:") {
			found = true
		}
	}
	if !found {
		t.Errorf("the offending line number should be reported, got %v", errs)
	}
}

// The editor round-trips: what we render must parse back to the same list.
func TestBogonASNsRoundTrip(t *testing.T) {
	orig := DefaultBogonASNs()
	back, errs := ParseBogonASNs(FormatBogonASNs(orig))
	if len(errs) != 0 {
		t.Fatalf("our own defaults do not parse: %v", errs)
	}
	if len(back) != len(orig) {
		t.Fatalf("round trip lost entries: %d -> %d", len(orig), len(back))
	}
	for i := range orig {
		if back[i].Low != orig[i].Low || back[i].High != orig[i].High ||
			back[i].Private != orig[i].Private || back[i].Note != orig[i].Note {
			t.Errorf("entry %d changed: %+v -> %+v", i, orig[i], back[i])
		}
	}
}

func TestBogonASNsSeededAndReplaceable(t *testing.T) {
	s := openTest(t)

	list, err := s.ListBogonASNs()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(DefaultBogonASNs()) {
		t.Fatalf("defaults not seeded: %d entries", len(list))
	}
	var private int
	for _, b := range list {
		if b.Private {
			private++
		}
	}
	if private != 2 {
		t.Errorf("want 2 private ranges seeded, got %d", private)
	}

	if err := s.ReplaceBogonASNs([]BogonASN{{Low: 7, High: 9, Note: "mine"}}); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListBogonASNs()
	if len(list) != 1 || list[0].Low != 7 || list[0].Note != "mine" {
		t.Errorf("replace did not stick: %+v", list)
	}
}

// The bogon sets are named by generated filters; deleting one would produce a
// config that references a define which does not exist.
func TestSystemPrefixSetsCannotBeDeleted(t *testing.T) {
	s := openTest(t)
	for _, name := range []string{BogonSetV4, BogonSetV6} {
		ps, err := s.GetPrefixSetByName(name)
		if err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
		if !ps.System {
			t.Errorf("%s should be a system set", name)
		}
		if err := s.DeletePrefixSet(ps.ID); err == nil {
			t.Errorf("%s must not be deletable", name)
		}
	}
}

func TestSystemPrefixSetsAreNotSelectable(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreatePrefixSet(PrefixSet{
		Name: "MY_AGGREGATES", Family: FamilyV4, Entries: []PrefixEntry{{Prefix: "192.0.2.0/24"}},
	}); err != nil {
		t.Fatal(err)
	}
	sets, err := s.ListSelectablePrefixSets()
	if err != nil {
		t.Fatal(err)
	}
	for _, ps := range sets {
		if ps.System {
			t.Errorf("%s is a system set and must not be offered in a picker", ps.Name)
		}
	}
	// The seeded ANNOUNCE_* sets are selectable too; only the bogons are hidden.
	var names []string
	for _, ps := range sets {
		names = append(names, ps.Name)
	}
	for _, want := range []string{"MY_AGGREGATES", "ANNOUNCE_V4", "ANNOUNCE_V6"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s should be selectable, got %v", want, names)
		}
	}
}

func TestGetBogonSet(t *testing.T) {
	s := openTest(t)
	v4, err := s.GetBogonSet(FamilyV4)
	if err != nil || v4.Name != BogonSetV4 || len(v4.Entries) == 0 {
		t.Errorf("v4 bogon set: %+v %v", v4, err)
	}
	v6, err := s.GetBogonSet(FamilyV6)
	if err != nil || v6.Name != BogonSetV6 || len(v6.Entries) == 0 {
		t.Errorf("v6 bogon set: %+v %v", v6, err)
	}
}
