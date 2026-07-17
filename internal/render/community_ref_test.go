package render

import (
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// A peer export and a policy match can reference a library community by name; the
// rendered config uses the define symbol (and the right community attribute for
// its width), while a literal still renders inline.
func TestCommunityReferenceByName(t *testing.T) {
	in := baseInput()
	in.Communities = []store.CommunityDef{
		{Name: "NO_EXPORT_X", A: 65000, B: 100},              // standard
		{Name: "BIG_TAG", Large: true, A: 65551, B: 1, C: 2}, // large
	}
	imp := store.Policy{ID: 1, Name: "BLOCK_TAGGED", Direction: store.DirImport,
		DefaultRoute: store.DefaultReject, MatchCommunity: "BIG_TAG"}
	in.Policies = []store.Policy{imp}

	p := ebgpPeer()
	p.ImportPolicies = []store.Policy{imp}
	p.ImportCommunities = "BIG_TAG\n65000:200"
	p.ExportCommunities = "NO_EXPORT_X\n65535:777" // one named, one literal
	p.ExportPolicies = []store.Policy{{ID: 2, Name: "EXPORT_MINE", Direction: store.DirExport, AnnounceEverything: true}}
	in.Peers = []store.Peer{p}

	out := mustRender(t, in)

	// The library defines are emitted.
	if !strings.Contains(out, "NO_EXPORT_X") || !strings.Contains(out, "= (65000, 100)") {
		t.Error("the named community should be rendered as a define")
	}

	// The peer export references the name for the named one and the literal for
	// the literal, each with the attribute matching its width.
	seg := out[strings.Index(out, "filter ebgp_out_"+p.Name):]
	if !strings.Contains(seg, "bgp_community.add(NO_EXPORT_X);") {
		t.Errorf("export should reference the standard community by name:\n%s", seg)
	}
	if !strings.Contains(seg, "bgp_community.add((65535, 777));") {
		t.Error("a literal community should still render inline")
	}
	inbound := block(t, out, "filter ebgp_in_"+p.Name)
	if !strings.Contains(inbound, "bgp_large_community.add(BIG_TAG);") {
		t.Errorf("import should add the named large community:\n%s", inbound)
	}
	if !strings.Contains(inbound, "bgp_community.add((65000, 200));") {
		t.Errorf("import should add the literal standard community:\n%s", inbound)
	}

	// The policy import match references the large community by name and looks it
	// up in the large-community attribute.
	if !strings.Contains(out, "if BIG_TAG ~ bgp_large_community then reject") {
		t.Errorf("policy match should reference the large community by name with the large attribute:\n%s", out)
	}
}
