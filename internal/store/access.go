package store

import (
	"fmt"
	"net/netip"
	"strings"
)

// ParseAccessWhitelist reads the access-whitelist textarea — one IP or CIDR per
// line or comma-separated, blank lines and # comments ignored — into prefixes.
// A bare address becomes a host prefix (/32 or /128). Returns the prefixes and a
// list of human-readable errors.
func ParseAccessWhitelist(text string) ([]netip.Prefix, []string) {
	var out []netip.Prefix
	var errs []string
	line := 0
	for raw := range strings.Lines(text) {
		line++
		s := strings.TrimSpace(raw)
		if i := strings.IndexByte(s, '#'); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
		if s == "" {
			continue
		}
		for _, tok := range strings.Split(s, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			p, msg := parseCIDROrIP(tok)
			if msg != "" {
				errs = append(errs, fmt.Sprintf("Line %d: %s", line, msg))
				continue
			}
			out = append(out, p)
		}
	}
	return out, errs
}

func parseCIDROrIP(tok string) (netip.Prefix, string) {
	if p, err := netip.ParsePrefix(tok); err == nil {
		return p.Masked(), ""
	}
	if a, err := netip.ParseAddr(tok); err == nil {
		return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), ""
	}
	return netip.Prefix{}, fmt.Sprintf("%q is not an IP address or CIDR (e.g. 203.0.113.4 or 10.0.0.0/8).", tok)
}

// AccessRestricted reports whether the whitelist actually narrows who can reach
// birdy — i.e. it is non-empty and does not contain a default route. It is the
// condition the unauthenticated /metrics endpoint is gated on: an endpoint no
// cookie can protect must not be readable by the whole internet just because
// birdy binds every interface out of the box.
func AccessRestricted(prefixes []netip.Prefix) bool {
	if len(prefixes) == 0 {
		return false
	}
	for _, p := range prefixes {
		if p.Bits() == 0 { // 0.0.0.0/0 or ::/0 — allow all
			return false
		}
	}
	return true
}

// AccessAllowed reports whether ip may reach birdy given the whitelist. It fails
// open in the ways that prevent a lock-out: loopback is always allowed (an SSH
// tunnel must never be blocked), an empty list means no restriction, and a
// default route (/0, i.e. 0.0.0.0/0 or ::/0) in the list allows everything.
func AccessAllowed(prefixes []netip.Prefix, ip netip.Addr) bool {
	if !ip.IsValid() || ip.IsLoopback() {
		return true
	}
	if len(prefixes) == 0 {
		return true
	}
	ip = ip.Unmap()
	for _, p := range prefixes {
		if p.Bits() == 0 { // 0.0.0.0/0 or ::/0 — allow all
			return true
		}
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
