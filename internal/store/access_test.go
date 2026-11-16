package store

import (
	"net/netip"
	"testing"
)

func TestParseAccessWhitelist(t *testing.T) {
	prefixes, errs := ParseAccessWhitelist("203.0.113.4\n10.0.0.0/8, 2001:db8::/32\n# a comment\n\n")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(prefixes) != 3 {
		t.Fatalf("want 3 prefixes, got %d: %v", len(prefixes), prefixes)
	}
	if prefixes[0].String() != "203.0.113.4/32" {
		t.Errorf("a bare IP should become a host prefix, got %s", prefixes[0])
	}
	if _, errs := ParseAccessWhitelist("not-an-ip"); len(errs) == 0 {
		t.Error("a malformed entry should be reported")
	}
}

func TestAccessAllowed(t *testing.T) {
	a := func(s string) netip.Addr { addr, _ := netip.ParseAddr(s); return addr }
	list, _ := ParseAccessWhitelist("203.0.113.0/24")

	// Loopback is always allowed (an SSH tunnel must never be blocked).
	if !AccessAllowed(list, a("127.0.0.1")) || !AccessAllowed(list, a("::1")) {
		t.Error("loopback must always be allowed")
	}
	if !AccessAllowed(list, a("203.0.113.9")) {
		t.Error("an in-range address must be allowed")
	}
	if AccessAllowed(list, a("198.51.100.5")) {
		t.Error("an out-of-range address must be blocked")
	}
	// An empty list means no restriction.
	if !AccessAllowed(nil, a("198.51.100.5")) {
		t.Error("an empty whitelist must allow everything")
	}
	// A default route (/0) allows everything, across both families.
	all, _ := ParseAccessWhitelist("0.0.0.0/0")
	if !AccessAllowed(all, a("198.51.100.5")) || !AccessAllowed(all, a("2001:db8::1")) {
		t.Error("0.0.0.0/0 must allow all addresses")
	}
	// A v4-mapped v6 address matches a v4 prefix.
	if !AccessAllowed(list, a("::ffff:203.0.113.9")) {
		t.Error("a v4-mapped address should match a v4 prefix")
	}
}

// A zoned address never matches a netip.Prefix, so the zone a link-local
// client arrives with is dropped before matching.
func TestAccessAllowedIgnoresZone(t *testing.T) {
	list, errs := ParseAccessWhitelist("fe80::/10")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if !AccessAllowed(list, netip.MustParseAddr("fe80::1%eth0")) {
		t.Error("a zoned link-local client must match fe80::/10")
	}
	if AccessAllowed(list, netip.MustParseAddr("2001:db8::1%eth0")) {
		t.Error("the zone must not make an outside address match")
	}
}

// Clients are matched in their plain IPv4 form, so an entry written in
// IPv4-mapped IPv6 form is stored as the IPv4 prefix it means.
func TestParseAccessWhitelistUnmapsIPv4MappedPrefixes(t *testing.T) {
	for in, want := range map[string]string{
		"::ffff:10.0.0.0/104":    "10.0.0.0/8",
		"::ffff:203.0.113.4/128": "203.0.113.4/32",
		"::ffff:203.0.113.4":     "203.0.113.4/32",
		"::ffff:10.1.2.3/104":    "10.0.0.0/8", // host bits masked as for any prefix
		"::ffff:0.0.0.0/96":      "0.0.0.0/0",
		"::ffff:0:0/90":          "::ffc0:0:0/90", // wider than the mapped range: stays IPv6 (masked as ever)
		"2001:db8::/32":          "2001:db8::/32",
	} {
		list, errs := ParseAccessWhitelist(in)
		if len(errs) != 0 || len(list) != 1 {
			t.Errorf("%s: list=%v errs=%v", in, list, errs)
			continue
		}
		if list[0].String() != want {
			t.Errorf("%s parsed as %s, want %s", in, list[0], want)
		}
	}
	list, _ := ParseAccessWhitelist("::ffff:10.0.0.0/104")
	if !AccessAllowed(list, netip.MustParseAddr("10.9.8.7")) || AccessAllowed(list, netip.MustParseAddr("11.0.0.1")) {
		t.Error("::ffff:10.0.0.0/104 should allow exactly 10.0.0.0/8")
	}
}
