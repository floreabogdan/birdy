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
