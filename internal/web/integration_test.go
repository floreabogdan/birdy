//go:build integration

// Package web integration tests run only under `-tags integration` and only
// where the real `bird` binary is installed; they are excluded from the normal
// suite. CI runs them in a dedicated job that installs bird2.
package web

import (
	"context"
	"strconv"
	"strings"
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

// Per-session BFD timers render as a `bfd { ... };` block — on a standalone
// peer, and on a template whose linked peers inherit it through BIRD's own
// `template bgp`. Both must load.
func TestIntegrationBFDTimersParseInBird(t *testing.T) {
	env := applyReady(t)

	tf := templateForm(env, t)
	tf.Set("bfd", "on")
	tf.Set("bfdInterval", "300")
	tf.Set("bfdMultiplier", "10")
	if rec := env.do(t, "POST", "/peers/templates/new", tf); rec.Code != 303 {
		t.Fatalf("template create: %d %s", rec.Code, rec.Body.String())
	}
	tmpl, err := env.store.GetPeerTemplateByName("IX_PEERS")
	if err != nil {
		t.Fatal(err)
	}

	linked := peerForm()
	linked.Set("name", "rs1_v4")
	linked.Set("neighborIp", "198.51.100.10")
	linked.Set("remoteAsn", "64500")
	linked.Set("templateId", strconv.FormatInt(tmpl.ID, 10))
	if rec := env.do(t, "POST", "/peers/new", linked); rec.Code != 303 {
		t.Fatalf("linked peer create: %d %s", rec.Code, rec.Body.String())
	}

	own := peerForm()
	own.Set("name", "tunnel_v4")
	own.Set("neighborIp", "198.51.100.20")
	own.Set("remoteAsn", "64501")
	own.Set("bfd", "on")
	own.Set("bfdInterval", "1000")
	own.Set("bfdMultiplier", "3")
	if rec := env.do(t, "POST", "/peers/new", own); rec.Code != 303 {
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
	for _, want := range []string{"interval 300 ms;", "multiplier 10;", "interval 1000 ms;", "multiplier 3;"} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("rendered config lacks %q:\n%s", want, cfg)
		}
	}

	res := birdconf.Check(context.Background(), "bird", cfg)
	if res.Skipped != "" {
		t.Skipf("bird not available: %s", res.Skipped)
	}
	if !res.OK {
		t.Fatalf("bird -p rejected the BFD timers:\n%s\n\n--- config ---\n%s", res.Output, cfg)
	}
}
