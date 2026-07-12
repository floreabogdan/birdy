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
