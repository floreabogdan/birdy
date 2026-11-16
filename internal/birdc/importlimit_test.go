package birdc

import "testing"

// Captured from BIRD 2.17.5 once a channel's import limit (action warn) had
// triggered, trimmed to the lines that matter, addresses rewritten to the
// documentation ranges as in fixtures_test.go.
const fixtureShowProtocolsAllLimitHit = "2002-Name       Proto      Table      State  Since         Info\n" +
	"1002-edge_v4    BGP        ---        up     12:22:55.700  Established   \n" +
	"1006-  BGP state:          Established\n" +
	"     Neighbor address: 203.0.113.57\n" +
	"     Neighbor AS:      64496\n" +
	"     Local AS:         65551\n" +
	"   Channel ipv4\n" +
	"     State:          UP\n" +
	"     Table:          master4\n" +
	"     Preference:     100\n" +
	"     Input filter:   ACCEPT\n" +
	"     Output filter:  REJECT\n" +
	"     Import limit:   10 [HIT]\n" +
	"       Action:       warn\n" +
	"     Routes:         15 imported, 0 exported, 15 preferred\n" +
	"0000 \n"

func TestParseImportLimitHitFromBIRD(t *testing.T) {
	d, err := ParseProtocolDetail(mustFrame(t, fixtureShowProtocolsAllLimitHit))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Channels) != 1 {
		t.Fatalf("got %d channels, want 1", len(d.Channels))
	}
	limit, hit := ParseImportLimit(d.Channels[0].ImportLimit)
	if limit != 10 || !hit {
		t.Fatalf("ParseImportLimit(%q) = %d, %v; want 10, true", d.Channels[0].ImportLimit, limit, hit)
	}
}

func TestParseImportLimit(t *testing.T) {
	for _, tc := range []struct {
		in    string
		limit int
		hit   bool
	}{
		{"2000000", 2000000, false},
		{"1000 [HIT]", 1000, true},
		{"  1000   [HIT] ", 1000, true},
		{"", 0, false},
		{"0", 0, false},
		{"off", 0, false},
		{"[HIT]", 0, false},
		{"-5 [HIT]", 0, false},
	} {
		limit, hit := ParseImportLimit(tc.in)
		if limit != tc.limit || hit != tc.hit {
			t.Errorf("ParseImportLimit(%q) = %d, %v; want %d, %v", tc.in, limit, hit, tc.limit, tc.hit)
		}
	}
}
