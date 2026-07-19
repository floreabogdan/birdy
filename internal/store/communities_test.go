package store

import "testing"

func TestParseMatchCommunity(t *testing.T) {
	if _, set, msg := ParseMatchCommunity(""); set || msg != "" {
		t.Errorf("empty should be no-match: set=%v msg=%q", set, msg)
	}
	c, set, msg := ParseMatchCommunity("65000:666")
	if !set || msg != "" || c.BIRD() != "(65000, 666)" {
		t.Errorf("single standard: %+v set=%v msg=%q", c, set, msg)
	}
	if _, _, msg := ParseMatchCommunity("65000:1, 65000:2"); msg == "" {
		t.Error("multiple communities should be rejected for a single match")
	}
	if _, _, msg := ParseMatchCommunity("70000:1"); msg == "" {
		t.Error("out-of-range should be rejected")
	}
}
