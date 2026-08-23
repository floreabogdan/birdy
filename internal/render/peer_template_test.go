package render

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// A peer linked to a template says so in its protocol block, and nowhere
// else: the filters and the rest of the block are whatever its (copied)
// shape renders to. An unlinked peer renders byte-for-byte as before — the
// authorship hash and the "in sync" check depend on an unchanged model
// producing unchanged bytes.
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
