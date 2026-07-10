package irr

import (
	"context"
	"fmt"
	"testing"
)

// bgpq4 -j output for a small AS-SET, both an exact prefix and a length range.
const sampleJSON = `{"data":[
  {"prefix":"192.0.2.0/24","exact":true},
  {"prefix":"198.51.100.0/23","greater-equal":24,"less-equal":24},
  {"prefix":"203.0.113.0/24"}
]}`

func fakeRunner(out string, err error) Runner {
	return func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return []byte(out), err
	}
}

func TestPrefixesParsesBgpq4JSON(t *testing.T) {
	c := &Client{Bin: "bgpq4", Run: fakeRunner(sampleJSON, nil)}
	prefixes, err := c.Prefixes(context.Background(), "AS-EXAMPLE", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) != 3 {
		t.Fatalf("want 3 prefixes, got %d: %+v", len(prefixes), prefixes)
	}
	if prefixes[0].Prefix != "192.0.2.0/24" || prefixes[0].Modifier != "" {
		t.Errorf("exact prefix wrong: %+v", prefixes[0])
	}
	if prefixes[1].Modifier != "{24,24}" {
		t.Errorf("range modifier wrong: %+v", prefixes[1])
	}
}

func TestPrefixesPassesFamilyFlag(t *testing.T) {
	var gotArgs []string
	c := &Client{Bin: "bgpq4", Run: func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"data":[]}`), nil
	}}
	c.Prefixes(context.Background(), "AS-X", true)
	if len(gotArgs) == 0 || gotArgs[0] != "-6" {
		t.Errorf("v6 should pass -6, got %v", gotArgs)
	}
}

func TestPrefixesRejectsBadSource(t *testing.T) {
	c := &Client{Bin: "bgpq4", Run: fakeRunner("", nil)}
	if _, err := c.Prefixes(context.Background(), "AS-X; rm -rf /", false); err == nil {
		t.Error("a source with shell metacharacters must be refused")
	}
}

func TestPrefixesReportsRunnerError(t *testing.T) {
	c := &Client{Bin: "bgpq4", Run: fakeRunner("", fmt.Errorf("exit status 1"))}
	if _, err := c.Prefixes(context.Background(), "AS-X", false); err == nil {
		t.Error("a bgpq4 failure should surface")
	}
}
