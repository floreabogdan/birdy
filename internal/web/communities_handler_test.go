package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// A community created in the library renders as a BIRD define in the candidate
// config, and the seeded well-known ones are present.
func TestCommunityLibraryFlow(t *testing.T) {
	env := applyReady(t)

	body := env.do(t, "GET", "/library/communities", nil).Body.String()
	if !strings.Contains(body, "BLACKHOLE") {
		t.Error("the seeded built-in BLACKHOLE should appear in the library")
	}

	f := url.Values{"name": {"LEARNT_TRANSIT"}, "value": {"65000:100"}, "description": {"learned from transit"}}
	if rec := env.do(t, "POST", "/library/communities/new", f); rec.Code != 303 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	cd, err := env.store.GetCommunityDefByName("LEARNT_TRANSIT")
	if err != nil || cd.A != 65000 || cd.B != 100 || cd.Large {
		t.Fatalf("community stored wrong: %+v (err %v)", cd, err)
	}

	// Rendered as a define in the candidate config (alignment padding varies, so
	// assert on the name and the value assignment, not exact spacing).
	changes := env.do(t, "GET", "/changes", nil).Body.String()
	if !strings.Contains(changes, "LEARNT_TRANSIT") || !strings.Contains(changes, "= (65000, 100);") {
		t.Error("the community should render as a define in the candidate config")
	}
	if !strings.Contains(changes, "= (65535, 666);") {
		t.Error("the seeded BLACKHOLE define should be in the candidate config")
	}

	env.do(t, "POST", "/library/communities/LEARNT_TRANSIT/delete", nil)
	if _, err := env.store.GetCommunityDefByName("LEARNT_TRANSIT"); err != store.ErrNotFound {
		t.Errorf("community should be deleted, got err=%v", err)
	}
}

// The peer and policy forms surface the defined community names for discovery —
// a hint on the peer export textarea, a datalist on the policy match input.
func TestCommunityFormsShowDefinedNames(t *testing.T) {
	env := applyReady(t)
	env.do(t, "POST", "/library/communities/new", url.Values{"name": {"MY_TAG"}, "value": {"65000:7"}})

	if body := env.do(t, "GET", "/peers/new", nil).Body.String(); !strings.Contains(body, "MY_TAG") {
		t.Error("peer form should list the defined community names")
	}
	body := env.do(t, "GET", "/policies/new?direction=import", nil).Body.String()
	if !strings.Contains(body, `id="community-names"`) || !strings.Contains(body, `value="MY_TAG"`) {
		t.Error("policy form should offer the defined communities in an autocomplete datalist")
	}
}

// A large community round-trips through the form.
func TestCommunityLarge(t *testing.T) {
	env := applyReady(t)
	f := url.Values{"name": {"BIG"}, "value": {"65551:1:2"}}
	env.do(t, "POST", "/library/communities/new", f)
	cd, err := env.store.GetCommunityDefByName("BIG")
	if err != nil || !cd.Large || cd.A != 65551 || cd.B != 1 || cd.C != 2 {
		t.Fatalf("large community stored wrong: %+v (err %v)", cd, err)
	}
}

// A peer referencing an undefined community by name is rejected at save; one
// referencing a defined community saves and blocks the community's deletion.
func TestCommunityReferenceGuards(t *testing.T) {
	env := applyReady(t)

	// Unknown name: the peer save is rejected and says why.
	f := peerForm()
	f.Set("name", "edge_v4")
	f.Set("neighborIp", "198.51.100.1")
	f.Set("remoteAsn", "64500")
	f.Set("exportCommunities", "DOES_NOT_EXIST")
	rec := env.do(t, "POST", "/peers/new", f)
	if rec.Code == 303 {
		t.Error("a peer referencing an undefined community must not save")
	}
	if !strings.Contains(rec.Body.String(), "Unknown community") {
		t.Error("the error should name the unknown community")
	}

	// Define it, then the same peer saves.
	env.do(t, "POST", "/library/communities/new", url.Values{"name": {"DOES_NOT_EXIST"}, "value": {"65000:1"}})
	if rec := env.do(t, "POST", "/peers/new", f); rec.Code != 303 {
		t.Fatalf("peer should save once the community exists: %d %s", rec.Code, rec.Body.String())
	}

	// Now the community cannot be deleted while the peer references it.
	env.do(t, "POST", "/library/communities/DOES_NOT_EXIST/delete", nil)
	if _, err := env.store.GetCommunityDefByName("DOES_NOT_EXIST"); err != nil {
		t.Error("a referenced community must not be deletable")
	}
}

// A reserved name and a malformed value are rejected without creating anything.
func TestCommunityValidation(t *testing.T) {
	env := applyReady(t)

	body := env.do(t, "POST", "/library/communities/new", url.Values{"name": {"FROM_UPSTREAM"}, "value": {"65551:1"}}).Body.String()
	if !strings.Contains(body, "built-in define") {
		t.Error("a reserved name should be rejected")
	}
	if _, err := env.store.GetCommunityDefByName("FROM_UPSTREAM"); err != store.ErrNotFound {
		t.Error("a rejected community must not be created")
	}

	env.do(t, "POST", "/library/communities/new", url.Values{"name": {"OK_NAME"}, "value": {"nonsense"}})
	if _, err := env.store.GetCommunityDefByName("OK_NAME"); err != store.ErrNotFound {
		t.Error("a community with a malformed value must not be created")
	}
}
