//go:build integration

// Package web integration tests run only under `-tags integration` and only
// where the real `bird` binary is installed; they are excluded from the normal
// suite. CI runs them in a dedicated job that installs bird2.
package web

import (
	"context"
	"testing"

	birdconf "github.com/floreabogdan/birdy/internal/render"
)

// The whole point of birdy is that the config it renders actually loads in BIRD.
// Render a representative model — the seeded starter pack (bogons, functions,
// filters, default policies) plus an eBGP peer — and run the real `bird -p`
// parser over it. This catches the class of bug bird -p exists for: birdy
// emitting syntax or references BIRD rejects.
func TestIntegrationRenderedConfigParsesInBird(t *testing.T) {
	env := applyReady(t)

	f := peerForm()
	f.Set("name", "edge_v4")
	f.Set("neighborIp", "198.51.100.1")
	f.Set("remoteAsn", "64500")
	if rec := env.do(t, "POST", "/peers/new", f); rec.Code != 303 {
		t.Fatalf("peer create: %d %s", rec.Code, rec.Body.String())
	}

	in, reason, err := env.srv.renderInput(false)
	if err != nil {
		t.Fatalf("build render input: %v", err)
	}
	if reason != "" {
		t.Fatalf("model cannot render: %s", reason)
	}
	cfg, err := birdconf.Config(in)
	if err != nil {
		t.Fatalf("render config: %v", err)
	}

	res := birdconf.Check(context.Background(), "bird", cfg)
	if res.Skipped != "" {
		t.Skipf("bird not available: %s", res.Skipped)
	}
	if !res.OK {
		t.Fatalf("bird -p rejected birdy's rendered config:\n%s\n\n--- config ---\n%s", res.Output, cfg)
	}
}
