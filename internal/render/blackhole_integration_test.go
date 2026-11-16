//go:build integration

package render

import (
	"context"
	"testing"
)

// The blackhole branch is the one place the generated import filter nests an
// if/else and the one place a policy has a second form: both must load in BIRD,
// for an IPv4 and an IPv6 session.
func TestIntegrationBlackholeChainParsesInBird(t *testing.T) {
	in, p := customerChain()
	v6 := p
	v6.ID, v6.Name, v6.NeighborIP = 2, "edge_v6", "2001:db8::1"
	in.Peers = append(in.Peers, v6)
	cfg := mustRender(t, in)

	res := Check(context.Background(), "bird", cfg)
	if res.Skipped != "" {
		t.Skipf("bird not available: %s", res.Skipped)
	}
	if !res.OK {
		t.Fatalf("bird -p rejected the blackhole chain:\n%s\n\n--- config ---\n%s", res.Output, cfg)
	}
}
