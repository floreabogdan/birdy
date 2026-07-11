package web

import (
	"strings"
	"testing"
)

// A BGP session BIRD is running that the model does not include must be flagged
// as would-be-removed on the Changes page — the adoption trap made visible.
func TestChangesFlagsSessionsWouldRemove(t *testing.T) {
	env := applyReady(t) // model has no peers; the fake BIRD runs edge_v4 (Established)
	body := env.do(t, "GET", "/changes", nil).Body.String()
	if !strings.Contains(body, "Applying would remove") {
		t.Fatal("a live session with no model peer should be flagged")
	}
	if !strings.Contains(body, "edge_v4") {
		t.Error("the at-risk session should be named")
	}
	if !strings.Contains(body, "1 established") {
		t.Error("the established at-risk session should be counted")
	}
}

// Once the session is modelled as a peer of the same name, it is no longer at
// risk and the warning goes away.
func TestChangesModelledSessionNotFlagged(t *testing.T) {
	env := applyReady(t)
	f := peerForm()
	f.Set("name", "edge_v4")
	f.Set("neighborIp", "198.51.100.1")
	env.do(t, "POST", "/peers/new", f)

	body := env.do(t, "GET", "/changes", nil).Body.String()
	if strings.Contains(body, "Applying would remove") {
		t.Error("a modelled session must not be flagged as would-remove")
	}
}
