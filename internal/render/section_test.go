package render

import (
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// The whole point of the split: concatenating every section must reproduce
// exactly what Config returns, or the apply pipeline's hash and diff would drift.
func TestSectionsConcatEqualsConfig(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()
	in.Policies = []store.Policy{sanityPolicy()}
	p := ebgpPeer()
	p.ImportPolicies = []store.Policy{sanityPolicy()}
	in.Peers = []store.Peer{p}

	cfg := mustRender(t, in)
	secs, err := Sections(in)
	if err != nil {
		t.Fatalf("Sections: %v", err)
	}
	var b strings.Builder
	for _, s := range secs {
		b.WriteString(s.Body)
	}
	if b.String() != cfg {
		t.Fatal("concatenated section bodies must equal Config output byte-for-byte")
	}

	// The peer and policy each get their own addressable unit.
	paths := map[string]bool{}
	for _, s := range secs {
		paths[s.Path] = true
	}
	for _, want := range []string{"globals", "communities", "policies/IMPORT_SANITY", "peers/edge_v4"} {
		if !paths[want] {
			t.Errorf("missing section %q (have %v)", want, paths)
		}
	}
}

// Against an empty baseline the whole candidate is new, so every section is
// "added" with nothing removed.
func TestSectionDiffFromEmptyIsAllAdded(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()
	in.Peers = []store.Peer{ebgpPeer()}

	files, err := SectionDiff("", in, 3)
	if err != nil {
		t.Fatalf("SectionDiff: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected sections")
	}
	for _, f := range files {
		if f.Status != "added" {
			t.Errorf("%s: status = %q, want added", f.Path, f.Status)
		}
		if f.Removed != 0 || f.Added == 0 {
			t.Errorf("%s: added=%d removed=%d, want added>0 removed=0", f.Path, f.Added, f.Removed)
		}
	}
}

// A change confined to one peer must be reported on that peer's file alone.
func TestSectionDiffAttributesPeerChange(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()
	p := ebgpPeer()
	in.Peers = []store.Peer{p}
	before := mustRender(t, in)

	p.Description = "changed"
	in.Peers = []store.Peer{p}

	files, err := SectionDiff(before, in, 3)
	if err != nil {
		t.Fatalf("SectionDiff: %v", err)
	}
	var changed []string
	for _, f := range files {
		if f.Status != "unchanged" {
			changed = append(changed, f.Path)
		}
	}
	if len(changed) != 1 || changed[0] != "peers/edge_v4" {
		t.Fatalf("expected only peers/edge_v4 to change, got %v", changed)
	}
	for _, f := range files {
		if f.Path == "peers/edge_v4" && f.Added == 0 {
			t.Error("peers/edge_v4 should show an added line for the new description")
		}
	}
}
