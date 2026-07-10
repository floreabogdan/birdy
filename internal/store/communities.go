package store

import (
	"fmt"
	"strconv"
	"strings"
)

// A Community is a BGP community birdy attaches to routes. Standard communities
// are two 16-bit values (ASN:value); large communities are three 32-bit values
// (RFC 8092), which a 32-bit ASN needs.
type Community struct {
	Large   bool
	A, B, C int64
}

// BIRD renders the community as a tuple literal, e.g. "(65000, 666)" or
// "(65551, 1, 2)". Only ever built from validated integers, so there is nothing
// to escape.
func (c Community) BIRD() string {
	if c.Large {
		return fmt.Sprintf("(%d, %d, %d)", c.A, c.B, c.C)
	}
	return fmt.Sprintf("(%d, %d)", c.A, c.B)
}

// ParseCommunities reads a textarea of communities. Each entry is colon-
// separated — "65000:666" for a standard community, "65551:1:2" for a large one
// — one per line or comma-separated, with blank lines and # comments ignored.
// Returns the parsed communities and a list of human-readable errors.
func ParseCommunities(text string) ([]Community, []string) {
	var out []Community
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
			c, err := parseCommunity(tok)
			if err != "" {
				errs = append(errs, fmt.Sprintf("Line %d: %s", line, err))
				continue
			}
			out = append(out, c)
		}
	}
	return out, errs
}

func parseCommunity(tok string) (Community, string) {
	parts := strings.Split(tok, ":")
	nums := make([]int64, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return Community{}, fmt.Sprintf("%q is not a community (use ASN:value or ASN:x:y)", tok)
		}
		nums[i] = n
	}
	switch len(nums) {
	case 2:
		if bad := range16(nums); bad != "" {
			return Community{}, bad
		}
		return Community{A: nums[0], B: nums[1]}, ""
	case 3:
		if bad := range32(nums); bad != "" {
			return Community{}, bad
		}
		return Community{Large: true, A: nums[0], B: nums[1], C: nums[2]}, ""
	default:
		return Community{}, fmt.Sprintf("%q must have 2 parts (standard) or 3 (large)", tok)
	}
}

func range16(nums []int64) string {
	for _, n := range nums {
		if n < 0 || n > 65535 {
			return "a standard community's parts are each 0–65535; use a large community (ASN:x:y) for a 32-bit ASN"
		}
	}
	return ""
}

func range32(nums []int64) string {
	for _, n := range nums {
		if n < 0 || n > 4294967295 {
			return "a large community's parts are each 0–4294967295"
		}
	}
	return ""
}
