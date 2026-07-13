// Package irr expands an IRR AS-SET into prefixes using bgpq4, so a prefix set
// can be refreshed from the registry instead of maintained by hand. bgpq4 dials
// out to IRR/RPKI mirrors, so birdy only runs it when the operator opts in.
package irr

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"time"
)

// A Prefix is one entry expanded from the AS-SET: the CIDR plus an optional
// birdy prefix-pattern modifier ("" exact, or "{ge,le}" for a length range).
type Prefix struct {
	Prefix   string
	Modifier string
}

// Runner executes bgpq4 and returns its stdout. Injectable so tests drive the
// parser with captured output instead of a real binary.
type Runner func(ctx context.Context, bin string, args ...string) ([]byte, error)

// Client runs bgpq4. Bin is the binary (name or path); Run defaults to exec.
type Client struct {
	Bin string
	Run Runner
}

func New(bin string) *Client {
	if bin == "" {
		bin = "bgpq4"
	}
	return &Client{Bin: bin, Run: execRunner}
}

func execRunner(ctx context.Context, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, bin, args...).Output()
}

// Available reports whether the bgpq4 binary can be found, so the UI can guide
// the operator to install it rather than failing cryptically.
func (c *Client) Available() bool {
	_, err := exec.LookPath(c.Bin)
	return err == nil
}

// sourceRe bounds an IRR object name: AS-SETs look like AS-FOO, AS64500:AS-BAR,
// or a bare ASN. Everything else is refused before it becomes a bgpq4 argument.
var sourceRe = regexp.MustCompile(`^[A-Za-z0-9:_.\-]{1,128}$`)

// bgpq4 JSON: {"data":[{"prefix":"1.2.3.0/24","exact":true}, {"prefix":"1.2.0.0/16","greater-equal":17,"less-equal":24}]}
type bgpq4JSON struct {
	Data []struct {
		Prefix       string `json:"prefix"`
		Exact        bool   `json:"exact"`
		GreaterEqual int    `json:"greater-equal"`
		LessEqual    int    `json:"less-equal"`
	} `json:"data"`
}

// Prefixes expands source into the prefixes bgpq4 finds for one address family.
func (c *Client) Prefixes(ctx context.Context, source string, v6 bool) ([]Prefix, error) {
	if !sourceRe.MatchString(source) {
		return nil, fmt.Errorf("irr: %q is not a valid IRR object name", source)
	}
	fam := "-4"
	if v6 {
		fam = "-6"
	}
	// -j JSON, -A aggregate, -l names the list "data" so the parser knows the key.
	out, err := c.Run(ctx, c.Bin, fam, "-j", "-A", "-l", "data", source)
	if err != nil {
		return nil, fmt.Errorf("irr: bgpq4 failed: %w", err)
	}
	var parsed bgpq4JSON
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("irr: could not parse bgpq4 output: %w", err)
	}
	out2 := make([]Prefix, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		if d.Prefix == "" {
			continue
		}
		p := Prefix{Prefix: d.Prefix}
		// A length range widens what the prefix matches; birdy writes it as
		// "{ge,le}". An exact entry (or none given) is just the prefix.
		if !d.Exact && (d.GreaterEqual > 0 || d.LessEqual > 0) {
			ge, le := d.GreaterEqual, d.LessEqual
			if ge == 0 {
				ge = prefixLen(d.Prefix)
			}
			if le == 0 {
				le = maxLen(v6)
			}
			p.Modifier = fmt.Sprintf("{%d,%d}", ge, le)
		}
		out2 = append(out2, p)
	}
	return out2, nil
}

// bgpq4 -t JSON: {"data": [64500, 64501]} — a flat list of the member ASNs.
type bgpq4ASNJSON struct {
	Data []int64 `json:"data"`
}

// ASNs expands source into its member AS numbers, which is what an origin filter
// compares a route's last AS-path hop against. Unlike Prefixes this is
// family-independent: an AS-SET's members are the same for v4 and v6.
//
// bgpq4 reports an unknown or empty AS-SET as an empty list with exit status 0,
// so an empty result is not an error here — callers must decide what to do with
// it rather than assume a failure.
func (c *Client) ASNs(ctx context.Context, source string) ([]int64, error) {
	if !sourceRe.MatchString(source) {
		return nil, fmt.Errorf("irr: %q is not a valid IRR object name", source)
	}
	// -t asks for the AS list rather than prefixes, -j JSON, -l names the list.
	out, err := c.Run(ctx, c.Bin, "-j", "-t", "-l", "data", source)
	if err != nil {
		return nil, fmt.Errorf("irr: bgpq4 failed: %w", err)
	}
	var parsed bgpq4ASNJSON
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("irr: could not parse bgpq4 output: %w", err)
	}
	asns := make([]int64, 0, len(parsed.Data))
	for _, a := range parsed.Data {
		if a < 1 || a > 4294967295 {
			continue
		}
		asns = append(asns, a)
	}
	return asns, nil
}

func maxLen(v6 bool) int {
	if v6 {
		return 128
	}
	return 32
}

// prefixLen reads the /len off a CIDR string, defaulting to the family max.
func prefixLen(cidr string) int {
	for i := len(cidr) - 1; i >= 0; i-- {
		if cidr[i] == '/' {
			n := 0
			for _, ch := range cidr[i+1:] {
				if ch < '0' || ch > '9' {
					return 0
				}
				n = n*10 + int(ch-'0')
			}
			return n
		}
	}
	return 0
}
