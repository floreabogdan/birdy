package store

import "testing"

func TestParseCommunities(t *testing.T) {
	good, errs := ParseCommunities("65000:666\n65551:1:2 # large\n\n#comment\n1:2, 3:4")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(good) != 4 {
		t.Fatalf("want 4 communities, got %d: %+v", len(good), good)
	}
	if good[0].BIRD() != "(65000, 666)" {
		t.Errorf("standard render = %q", good[0].BIRD())
	}
	if !good[1].Large || good[1].BIRD() != "(65551, 1, 2)" {
		t.Errorf("large render = %q", good[1].BIRD())
	}
}

func TestParseCommunitiesRejectsBad(t *testing.T) {
	cases := []string{"70000:1", "1", "a:b", "1:2:3:4"}
	for _, c := range cases {
		_, errs := ParseCommunities(c)
		if len(errs) == 0 {
			t.Errorf("%q should be rejected", c)
		}
	}
}
