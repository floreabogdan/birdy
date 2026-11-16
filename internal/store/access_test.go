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

// An entry written in IPv4-mapped IPv6 form matched no client in 0.7.0
// (clients are compared in plain IPv4 form), and it must go on matching none:
// reading ::ffff:0:0/96 as 0.0.0.0/0 would turn an inert line into allow-all
// on upgrade. Such entries are kept exactly as written.
func TestParseAccessWhitelistLeavesIPv4MappedPrefixesAlone(t *testing.T) {
	for in, want := range map[string]string{
		"::ffff:10.0.0.0/104": "::ffff:10.0.0.0/104",
		"::ffff:0.0.0.0/96":   "::ffff:0.0.0.0/96",
	} {
		list, errs := ParseAccessWhitelist(in)
		if len(errs) != 0 || len(list) != 1 || list[0].String() != want {
			t.Errorf("%s: list=%v errs=%v, want %s", in, list, errs, want)
			continue
		}
		for _, client := range []string{"10.9.8.7", "198.51.100.5", "2001:db8::1"} {
			if AccessAllowed(list, netip.MustParseAddr(client)) {
				t.Errorf("%s must not start admitting %s", in, client)
			}
		}
		if !AccessRestricted(list) {
			t.Errorf("%s must still count as a restricting list", in)
		}
	}
}
