package render

import (
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func TestPeerGTSMAndGracefulRestart(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()

	on := ebgpPeer()
	on.Name = "with_opts"
	on.GTSM = true
	on.GracefulRestart = store.GROn

	off := ebgpPeer()
	off.Name = "gr_off"
	off.NeighborIP = "198.51.100.2"
	off.GracefulRestart = store.GROff

	aware := ebgpPeer()
	aware.Name = "gr_aware"
	aware.NeighborIP = "198.51.100.3"
	aware.GracefulRestart = store.GRAware // BIRD default → emitted as nothing

	in.Peers = []store.Peer{on, off, aware}
	cfg := mustRender(t, in)

	withOpts := block(t, cfg, "protocol bgp with_opts")
	if !strings.Contains(withOpts, "ttl security on;") {
		t.Errorf("GTSM peer missing ttl security:\n%s", withOpts)
	}
	if !strings.Contains(withOpts, "graceful restart on;") {
		t.Errorf("graceful-restart-on peer missing the line:\n%s", withOpts)
	}

	grOff := block(t, cfg, "protocol bgp gr_off")
	if !strings.Contains(grOff, "graceful restart off;") {
		t.Errorf("graceful-restart-off peer missing the line:\n%s", grOff)
	}
	if strings.Contains(grOff, "ttl security") {
		t.Error("gr_off peer should not have GTSM")
	}

	grAware := block(t, cfg, "protocol bgp gr_aware")
	if strings.Contains(grAware, "graceful restart") {
		t.Error("an 'aware' peer is BIRD's default and should emit no graceful restart line")
	}
}

// GTSM is an eBGP protection; Validate strips it from an iBGP session.
func TestGTSMStrippedForIBGP(t *testing.T) {
	p := store.Peer{
		Name: "ibgp1", Role: store.RoleIBGP, Enabled: true,
		NeighborIP: "192.0.2.9", RemoteASN: 65551, ImportLimitAction: "restart",
		GTSM: true,
	}
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
	if p.GTSM {
		t.Error("GTSM should be cleared for an iBGP peer")
	}
	if p.GracefulRestart != store.GRAware {
		t.Errorf("graceful restart should default to aware, got %q", p.GracefulRestart)
	}
}
