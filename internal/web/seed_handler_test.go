package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/birdc"
)

// seedDetail is the fake client's ProtocolDetail for a discovered session.
func seedDetail(name, neighbor, remoteAS, localAS, session string) birdc.ProtocolDetail {
	return birdc.ProtocolDetail{
		Summary:         birdc.ProtocolSummary{Name: name, Proto: "BGP", State: "up", Info: "Established"},
		NeighborAddress: neighbor,
		NeighborAS:      remoteAS,
		LocalAS:         localAS,
		SessionType:     session,
	}
}

// The seed page lists live BGP sessions birdy does not yet model, with the
// values it read off the socket.
func TestSeedDiscoversUnmodelledSessions(t *testing.T) {
	env := applyReady(t) // fake BIRD runs edge_v4, and the model has no peers
	env.fc.details["edge_v4"] = seedDetail("edge_v4", "198.51.100.1", "64500", "65551", "external")

	body := env.do(t, "GET", "/peers/seed", nil).Body.String()
	for _, want := range []string{"edge_v4", "198.51.100.1", "64500"} {
		if !strings.Contains(body, want) {
			t.Errorf("seed page should show %q, body:\n%s", want, body)
		}
	}
}

// Importing a checked session creates a model peer built from what BIRD reported.
func TestSeedCreatesPeers(t *testing.T) {
	env := applyReady(t)
	env.fc.details["edge_v4"] = seedDetail("edge_v4", "198.51.100.1", "64500", "65551", "external")

	f := url.Values{"include": {"edge_v4"}, "role_edge_v4": {"upstream"}}
	if rec := env.do(t, "POST", "/peers/seed", f); rec.Code != 303 {
		t.Fatalf("seed save: code=%d body=%s", rec.Code, rec.Body.String())
	}

	p, err := env.store.GetPeerByName("edge_v4")
	if err != nil {
		t.Fatalf("seeded peer not created: %v", err)
	}
	if p.NeighborIP != "198.51.100.1" || p.RemoteASN != 64500 || p.Role != "upstream" {
		t.Errorf("seeded peer has wrong values: %+v", p)
	}
	if p.BGPRole {
		t.Error("seeding must not enable RFC 9234 roles — adopting a live session must not risk resetting it")
	}
}

// A session already in the model is not offered for import (no duplicates).
func TestSeedSkipsModelledSessions(t *testing.T) {
	env := applyReady(t)
	env.fc.details["edge_v4"] = seedDetail("edge_v4", "198.51.100.1", "64500", "65551", "external")

	// Import it once.
	env.do(t, "POST", "/peers/seed", url.Values{"include": {"edge_v4"}, "role_edge_v4": {"upstream"}})

	body := env.do(t, "GET", "/peers/seed", nil).Body.String()
	if strings.Contains(body, "edge_v4") {
		t.Error("a session already modelled must not appear on the seed page")
	}
	if !strings.Contains(body, "already models every BGP session") {
		t.Error("with nothing left to import the page should say so")
	}
}

// An internal session (neighbor AS == local AS) is proposed as iBGP.
func TestSeedDetectsIBGP(t *testing.T) {
	p, _ := seedPeerFromDetail("rr1", seedDetail("rr1", "10.0.0.2", "65551", "65551", "internal"))
	if p.Role != "ibgp" {
		t.Errorf("a session inside our own AS should seed as iBGP, got %q", p.Role)
	}
	if !p.NextHopSelf {
		t.Error("a seeded iBGP peer should default to next-hop-self")
	}
}
