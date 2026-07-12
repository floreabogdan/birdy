package store

import "testing"

func TestParseCommunityRefs(t *testing.T) {
	refs, errs := ParseCommunityRefs("BLACKHOLE\n65000:666\n65551:1:2, LEARNT_TRANSIT")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(refs) != 4 {
		t.Fatalf("want 4 refs, got %d: %+v", len(refs), refs)
	}
	if refs[0].Name != "BLACKHOLE" {
		t.Errorf("a letter-led token should be a name, got %+v", refs[0])
	}
	if refs[1].Name != "" || refs[1].Value.A != 65000 || refs[1].Value.B != 666 {
		t.Errorf("a digit-led token should be a literal, got %+v", refs[1])
	}
	if !refs[2].Value.Large {
		t.Errorf("three parts should parse as a large community, got %+v", refs[2])
	}
	if refs[3].Name != "LEARNT_TRANSIT" {
		t.Errorf("comma-separated name should parse, got %+v", refs[3])
	}

	// A malformed literal is an error; a name is never one.
	if _, errs := ParseCommunityRefs("not:a:community:really"); len(errs) == 0 {
		t.Error("a malformed literal should be reported")
	}
}

func TestParseMatchCommunityRef(t *testing.T) {
	ref, set, msg := ParseMatchCommunityRef("BLACKHOLE")
	if !set || msg != "" || ref.Name != "BLACKHOLE" {
		t.Errorf("a single name should parse: set=%v msg=%q ref=%+v", set, msg, ref)
	}
	if _, set, _ := ParseMatchCommunityRef(""); set {
		t.Error("empty is a valid no-match")
	}
	if _, _, msg := ParseMatchCommunityRef("A, B"); msg == "" {
		t.Error("more than one entry should be rejected")
	}
}
