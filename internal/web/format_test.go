package web

import "testing"

func TestComma(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{7, "7"},
		{999, "999"},
		{1000, "1,000"},
		{2600141, "2,600,141"},
		{1000000, "1,000,000"},
		{-4321, "-4,321"},
	}
	for _, c := range cases {
		if got := comma(c.in); got != c.want {
			t.Errorf("comma(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRatio(t *testing.T) {
	cases := []struct {
		part, total int
		want        float64
	}{
		{0, 0, 0},   // no protocols: no division by zero
		{3, 0, 0},   // nonsensical input still must not panic
		{0, 2, 0},   // nothing down
		{2, 2, 100}, // all up
		{1, 4, 25},
		{5, 4, 100}, // clamped, never overflows the track
	}
	for _, c := range cases {
		if got := ratio(c.part, c.total); got != c.want {
			t.Errorf("ratio(%d, %d) = %v, want %v", c.part, c.total, got, c.want)
		}
	}
}

func TestSessionVerdict(t *testing.T) {
	cases := []struct {
		name     string
		pollErr  string
		total    int
		down     int
		disabled int
		wantText string
		wantOK   bool
	}{
		{"poll error outranks counts", "dial unix: no such file", 2, 0, 0, "BIRD unreachable", false},
		{"no sessions", "", 0, 0, 0, "No BGP sessions", false},
		{"all up", "", 2, 0, 0, "All 2 sessions up", true},
		{"single session up", "", 1, 0, 0, "All 1 session up", true},
		{"some down", "", 4, 1, 0, "1 of 4 sessions down", false},
		{"single session down", "", 1, 1, 0, "1 of 1 session down", false},
		// A peer switched off on purpose is not a fault: it is named, but it neither
		// counts as down nor turns the verdict red.
		{"disabled is not down", "", 2, 0, 1, "All 2 sessions up · 1 disabled", true},
		{"disabled alongside a real failure", "", 2, 1, 1, "1 of 2 sessions down · 1 disabled", false},
		{"everything disabled", "", 0, 0, 2, "No active BGP sessions (2 disabled)", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, ok := sessionVerdict(c.pollErr, c.total, c.down, c.disabled)
			if text != c.wantText || ok != c.wantOK {
				t.Errorf("sessionVerdict(%q, %d, %d, %d) = (%q, %v), want (%q, %v)",
					c.pollErr, c.total, c.down, c.disabled, text, ok, c.wantText, c.wantOK)
			}
		})
	}
}
