package render

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// A peer linked to a template whose block is not in the input renders in
// full — a comment names the template, nothing else changes. An unlinked peer
// renders byte-for-byte as before: the authorship hash and the "in sync" check
// depend on an unchanged model producing unchanged bytes.
func TestLinkedPeerCarriesTemplateComment(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()
	in.Peers = []store.Peer{ebgpPeer()}
	plain := mustRender(t, in)
	if strings.Contains(plain, "peer template") {
		t.Fatal("an unlinked peer must not mention templates")
	}

	linked := ebgpPeer()
	linked.TemplateID, linked.TemplateName = sql.NullInt64{Int64: 3, Valid: true}, "IX_PEERS"
	in.Peers = []store.Peer{linked}
	out := mustRender(t, in)
	want := "protocol bgp edge_v4 {\n\t# Shape inherited from peer template IX_PEERS; edit the template to change it.\n"
	if !strings.Contains(out, want) {
		t.Fatalf("linked peer should open with the template comment:\n%s", out)
	}
	// The comment is the only difference.
	if strings.Replace(out, "\t# Shape inherited from peer template IX_PEERS; edit the template to change it.\n", "", 1) != plain {
		t.Error("linking must change nothing but the comment line")
	}
}

// Identical findings about peers on the same template fold into one line,
// attributed to the template — the fix is there, once. Findings that differ
// per peer, and findings about unlinked peers, stay as they are.
func TestLintFoldsIdenticalFindingsByTemplate(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()
	in.Policies = []store.Policy{sanityPolicy()}
	linked := func(name, ip string, asn int64) store.Peer {
		p := ebgpPeer()
		p.Name, p.NeighborIP, p.RemoteASN = name, ip, asn
		p.ImportLimit = 0 // "No import limit" on every one of them
		p.TemplateID, p.TemplateName = sql.NullInt64{Int64: 3, Valid: true}, "IX_PEERS"
		return p
	}
	own := ebgpPeer()
	own.Name, own.NeighborIP, own.ImportLimit = "own_v4", "198.51.100.9", 0
	// rs3 uses a private ASN against the strict bogon policy: a finding only it has.
	in.Peers = []store.Peer{linked("rs1_v4", "198.51.100.10", 64500), linked("rs2_v4", "198.51.100.11", 64500), linked("rs3_v4", "198.51.100.12", 64512), own}
	for i := range in.Peers {
		in.Peers[i].ImportPolicies = []store.Policy{sanityPolicy()}
	}

	ws := Lint(in)
	var folded, perPeer, ownLines, private int
	for _, w := range ws {
		switch {
		case w.Peer == "IX_PEERS (3 peers)" && strings.HasPrefix(w.Message, "No import limit"):
			folded++
		case strings.HasPrefix(w.Peer, "rs") && strings.HasPrefix(w.Message, "No import limit"):
			perPeer++
		case w.Peer == "own_v4" && strings.HasPrefix(w.Message, "No import limit"):
			ownLines++
		case w.Peer == "rs3_v4" && strings.Contains(w.Message, "private AS number"):
			private++
		}
	}
	if folded != 1 || perPeer != 0 {
		t.Errorf("three identical findings on one template should fold into one: folded=%d perPeer=%d\n%+v", folded, perPeer, ws)
	}
	if ownLines != 1 {
		t.Errorf("an unlinked peer's finding must stay its own: %d", ownLines)
	}
	if private != 1 {
		t.Errorf("a finding only one linked peer has must not fold: %d", private)
	}
	// Nothing folds when nothing is linked.
	for i := range in.Peers {
		in.Peers[i].TemplateID, in.Peers[i].TemplateName = sql.NullInt64{}, ""
	}
	var plain int
	for _, w := range Lint(in) {
		if strings.HasPrefix(w.Message, "No import limit") {
			plain++
		}
	}
	if plain != 4 {
		t.Errorf("unlinked peers keep one finding each, got %d", plain)
	}
}

// ixTemplate is an IX route-server template: first-AS off, GTSM, a limit, an
// RFC 9234 role, and a multihop — enough to populate the template block.
func ixTemplate() store.PeerTemplate {
	return store.PeerTemplate{
		ID: 3, Name: "IX_PEERS", Description: "LONAP route servers", Role: store.RoleIXPeer,
		ImportLimit: 50000, ImportLimitAction: "restart", GTSM: true, BGPRole: true, Multihop: 2,
		GracefulRestart: store.GROn, BFD: true, Passive: true,
	}
}

func linkedTo(t store.PeerTemplate, name, ip string) store.Peer {
	p := ebgpPeer()
	p.Name, p.NeighborIP, p.LocalIP = name, ip, ""
	t.ApplyTo(&p)
	return p
}

// With the template in the input, linked peers are declared "from" a BIRD
// template block that carries the shared session options once, and their own
// blocks drop those options. Everything a peer differs in — identity,
// password, filters, the channel with its limit — stays in the protocol.
func TestLinkedPeersRenderFromATemplateBlock(t *testing.T) {
	tmpl := ixTemplate()
	in := baseInput()
	in.PrefixSets, in.Policies, in.Templates = bogonSets(), []store.Policy{sanityPolicy()}, []store.PeerTemplate{tmpl}
	rs1, rs2 := linkedTo(tmpl, "rs1_v4", "198.51.100.10"), linkedTo(tmpl, "rs2_v4", "198.51.100.11")
	rs2.LocalIP, rs2.Password = "198.51.100.2", "s3cret"
	rs2.ImportLimit, rs2.TemplateOverrides = 300000, store.OverrideImportLimit
	for _, p := range []*store.Peer{&rs1, &rs2} {
		p.ImportPolicies = []store.Policy{sanityPolicy()}
	}
	own := ebgpPeer()
	in.Peers = []store.Peer{rs1, rs2, own}
	out := mustRender(t, in)

	wantTemplate := "template bgp IX_PEERS {\n" +
		"\tlocal as LOCAL_ASN;\n" +
		"\tlocal role peer;\n" +
		"\tmultihop 2;\n" +
		"\tttl security on;\n" +
		"\tpassive;\n" +
		"\tbfd;\n" +
		"\tgraceful restart on;\n" +
		"}\n"
	if !strings.Contains(out, wantTemplate) {
		t.Errorf("template block wrong:\n%s", out)
	}
	if !strings.Contains(out, "# Peer template IX_PEERS: LONAP route servers\n") {
		t.Error("the template block should carry its description")
	}
	// The template precedes the peers that use it.
	if strings.Index(out, "template bgp IX_PEERS {") > strings.Index(out, "protocol bgp rs1_v4 from IX_PEERS {") {
		t.Error("a template must be declared before the protocols that inherit it")
	}

	blk := block(t, out, "protocol bgp rs1_v4 from IX_PEERS {")
	for _, inherited := range []string{"local as LOCAL_ASN;", "local role", "multihop", "ttl security", "passive;", "bfd;", "graceful restart"} {
		if strings.Contains(blk, inherited) {
			t.Errorf("a linked peer must leave %q to the template:\n%s", inherited, blk)
		}
	}
	for _, own := range []string{"# Shape inherited from peer template IX_PEERS", "neighbor 198.51.100.10 as 64497;", "import filter ebgp_in_rs1_v4;", "import limit 50000 action restart;"} {
		if !strings.Contains(blk, own) {
			t.Errorf("a linked peer keeps %q in its own block:\n%s", own, blk)
		}
	}
	// A pinned source address and a password stay with the peer and override
	// the template's "local as"; an overridden limit is the peer's own too.
	blk = block(t, out, "protocol bgp rs2_v4 from IX_PEERS {")
	for _, own := range []string{"local 198.51.100.2 as LOCAL_ASN;", "password \"s3cret\";", "authentication md5;", "import limit 300000 action restart;"} {
		if !strings.Contains(blk, own) {
			t.Errorf("rs2 should carry %q:\n%s", own, blk)
		}
	}
	// The unlinked peer is untouched and still states everything itself.
	blk = block(t, out, "protocol bgp edge_v4 {")
	if !strings.Contains(blk, "local as LOCAL_ASN;") || strings.Contains(blk, " from ") {
		t.Errorf("unlinked peer should render as before:\n%s", blk)
	}

	// Sections: one template section, filed with the policies, before the peers.
	secs, err := Sections(in)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, s := range secs {
		paths = append(paths, s.Path)
	}
	ti, pi := indexOf(paths, "templates/IX_PEERS"), indexOf(paths, "peers/edge_v4")
	if ti < 0 || pi < 0 || ti > pi {
		t.Errorf("template section should exist and precede the peers: %v", paths)
	}
	if name := includeFileName("templates/IX_PEERS"); name != "birdy.d/08-templates-IX_PEERS.conf" {
		t.Errorf("template file name = %q", name)
	}
}

// A template nobody links to is not written: it changes nothing on the
// router, so it must change nothing in the file. A template a peer names but
// the input lacks leaves that peer rendering in full, as an unlinked one.
func TestUnusedTemplateIsNotRenderedAndMissingTemplateFallsBack(t *testing.T) {
	in := baseInput()
	in.PrefixSets, in.Templates = bogonSets(), []store.PeerTemplate{ixTemplate()}
	in.Peers = []store.Peer{ebgpPeer()}
	if out := mustRender(t, in); strings.Contains(out, "template bgp") {
		t.Error("an unused template must not be rendered")
	}

	orphan := linkedTo(store.PeerTemplate{ID: 9, Name: "GONE", Role: store.RoleIXPeer, GTSM: true, ImportLimitAction: "restart"}, "rs9_v4", "198.51.100.19")
	in.Peers = []store.Peer{orphan}
	out := mustRender(t, in)
	if strings.Contains(out, "from GONE") {
		t.Error("a peer whose template is not in the input must not be declared from it")
	}
	if blk := block(t, out, "protocol bgp rs9_v4 {"); !strings.Contains(blk, "ttl security on;") || !strings.Contains(blk, "# Shape inherited from peer template GONE") {
		t.Errorf("the orphan should render in full, with the comment:\n%s", blk)
	}
}

// An iBGP template carries the reflector options, and its peers keep the
// channel-level next-hop-self themselves.
func TestIBGPTemplateCarriesReflectorOptions(t *testing.T) {
	tmpl := store.PeerTemplate{ID: 4, Name: "RR_CLIENTS", Role: store.RoleIBGP, RRClient: true, NextHopSelf: true, ImportLimitAction: "restart", GracefulRestart: store.GRAware}
	in := baseInput()
	in.PrefixSets, in.Templates, in.RRClusterID = bogonSets(), []store.PeerTemplate{tmpl}, "192.0.2.99"
	p := ebgpPeer()
	p.Name, p.RemoteASN, p.NeighborIP = "core2", in.LocalASN, "10.0.0.2"
	tmpl.ApplyTo(&p)
	in.Peers = []store.Peer{p}
	out := mustRender(t, in)
	if blk := block(t, out, "template bgp RR_CLIENTS {"); !strings.Contains(blk, "\trr client;\n\trr cluster id 192.0.2.99;") {
		t.Errorf("template should carry the reflector options:\n%s", blk)
	}
	blk := block(t, out, "protocol bgp core2 from RR_CLIENTS {")
	if strings.Contains(blk, "rr client") || !strings.Contains(blk, "next hop self;") {
		t.Errorf("the peer leaves rr client to the template and keeps next hop self:\n%s", blk)
	}
}

func indexOf(list []string, want string) int {
	for i, s := range list {
		if s == want {
			return i
		}
	}
	return -1
}
