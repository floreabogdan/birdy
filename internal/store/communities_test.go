package store

import (
	"errors"
	"strings"
	"testing"
)

func TestParseMatchCommunity(t *testing.T) {
	if _, set, msg := ParseMatchCommunity(""); set || msg != "" {
		t.Errorf("empty should be no-match: set=%v msg=%q", set, msg)
	}
	c, set, msg := ParseMatchCommunity("65000:666")
	if !set || msg != "" || c.BIRD() != "(65000, 666)" {
		t.Errorf("single standard: %+v set=%v msg=%q", c, set, msg)
	}
	if _, _, msg := ParseMatchCommunity("65000:1, 65000:2"); msg == "" {
		t.Error("multiple communities should be rejected for a single match")
	}
	if _, _, msg := ParseMatchCommunity("70000:1"); msg == "" {
		t.Error("out-of-range should be rejected")
	}
}

// A community renders `define NAME`, so it cannot take any name birdy defines
// itself — the same list the template check uses, not a shorter copy of it.
func TestCommunityCannotTakeABuiltinName(t *testing.T) {
	for _, name := range []string{"LOCAL_ASN", "BOGON_ASNS", "rpki4", "master6"} {
		cd := CommunityDef{Name: name, A: 65000, B: 1}
		if _, bad := cd.Validate()["name"]; !bad {
			t.Errorf("a community named %s should be refused", name)
		}
	}
}

// A peer, a peer template and a policy each name a community; renaming it
// would leave the reference pointing at nothing. The check runs inside the
// update's transaction, so a reference saved meanwhile cannot slip past it.
func TestCommunityRenameRefusedWhileReferenced(t *testing.T) {
	s := openTest(t)
	cd := CommunityDef{Name: "TAG", A: 65000, B: 7}
	id, err := s.CreateCommunityDef(cd)
	if err != nil {
		t.Fatal(err)
	}
	cd.ID = id

	p := validPeer()
	p.ImportCommunities = "65000:1, TAG # tag what this peer sends"
	if _, err := s.CreatePeer(p); err != nil {
		t.Fatal(err)
	}
	tmpl := PeerTemplate{Name: "IX", Role: RoleIXPeer, ImportLimitAction: "restart", ExportCommunities: "TAG"}
	if _, err := s.CreatePeerTemplate(tmpl, nil, nil); err != nil {
		t.Fatal(err)
	}
	pol := Policy{Name: "DROP_TAGGED", Direction: DirImport, DefaultRoute: DefaultReject, BogonASNs: BogonASNsOff, ROV: ROVOff, MatchCommunity: "TAG"}
	if _, err := s.CreatePolicy(pol); err != nil {
		t.Fatal(err)
	}

	renamed := cd
	renamed.Name = "TAG2"
	err = s.UpdateCommunityDef(renamed)
	var inUse *CommunityInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("want a CommunityInUseError, got %v", err)
	}
	want := []string{"peer edge_v4", "peer template IX", "policy DROP_TAGGED"}
	if strings.Join(inUse.Users, ", ") != strings.Join(want, ", ") {
		t.Errorf("users = %v, want %v", inUse.Users, want)
	}
	if got, _ := s.GetCommunityDef(id); got.Name != "TAG" {
		t.Errorf("the refused rename was written: %q", got.Name)
	}

	// Its value may still change, and a name nothing uses may be renamed.
	cd.B = 8
	if err := s.UpdateCommunityDef(cd); err != nil {
		t.Errorf("an edit that keeps the name should save: %v", err)
	}
	spare := CommunityDef{Name: "SPARE", A: 65000, B: 9}
	if spare.ID, err = s.CreateCommunityDef(spare); err != nil {
		t.Fatal(err)
	}
	spare.Name = "SPARE2"
	if err := s.UpdateCommunityDef(spare); err != nil {
		t.Errorf("an unreferenced community should rename: %v", err)
	}
}
