package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func enabledPeer(t *testing.T, env *testEnv, name string) store.Peer {
	t.Helper()
	f := peerForm()
	f.Set("name", name)
	env.do(t, "POST", "/peers/new", f)
	p, err := env.store.GetPeerByName(name)
	if err != nil {
		t.Fatalf("peer %s should exist: %v", name, err)
	}
	if !p.Enabled {
		t.Fatalf("peer %s should start enabled", name)
	}
	return p
}

// Shutting a session is something you reach for in a hurry, so it lives on the
// list — not behind a form and a checkbox.
func TestPeerToggleFromTheList(t *testing.T) {
	env := newTestEnv(t, false)
	p := enabledPeer(t, env, "edge_v4")

	rec := env.do(t, "POST", "/peers/"+p.Name+"/toggle", url.Values{})
	if rec.Code != 303 {
		t.Fatalf("toggle should redirect, got %d", rec.Code)
	}
	got, _ := env.store.GetPeerByName(p.Name)
	if got.Enabled {
		t.Error("the peer should now be disabled")
	}
	// It is a model change, not a router command: the operator has to apply it.
	if loc := flashOf(rec); !strings.Contains(loc, "apply") {
		t.Errorf("the flash should say an apply is needed, got %q", loc)
	}

	// And back again.
	env.do(t, "POST", "/peers/"+p.Name+"/toggle", url.Values{})
	if got, _ = env.store.GetPeerByName(p.Name); !got.Enabled {
		t.Error("toggling again should re-enable the peer")
	}
}

// A disabled peer renders BIRD's "disabled", which is what stops BIRD from making
// any connection attempt at all. Without it, disabling would be cosmetic.
func TestDisabledPeerRendersDisabledInBird(t *testing.T) {
	env := applyReady(t) // /changes needs a router ID and ASN to render anything
	p := enabledPeer(t, env, "edge_v4")
	if err := env.store.SetPeerEnabled(p.ID, false); err != nil {
		t.Fatal(err)
	}

	cfg := env.do(t, "GET", "/changes", nil).Body.String()
	if !strings.Contains(cfg, "disabled;") {
		t.Error("a disabled peer must render BIRD's disabled keyword, or BIRD keeps dialling")
	}
}

// The peers list shows "disabled", not the live "down" badge — and flags that the
// session is still running until the change is applied.
func TestPeersListShowsDisabled(t *testing.T) {
	env := newTestEnv(t, false)
	p := enabledPeer(t, env, "edge_v4") // the fake BIRD reports edge_v4 as Established
	if err := env.store.SetPeerEnabled(p.ID, false); err != nil {
		t.Fatal(err)
	}

	body := env.do(t, "GET", "/peers", nil).Body.String()
	if !strings.Contains(body, ">disabled<") {
		t.Error("a disabled peer should read as disabled on the list")
	}
	if !strings.Contains(body, ">pending apply<") {
		t.Error("BIRD still has the session up, so the list should say the disable is not applied yet")
	}
}
