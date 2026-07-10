package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// The bogon sets are named directly by generated filters. Offering them in a
// picker invites "announce BOGONS_V4 to my customer".
func TestBogonSetsAreNotOfferedInPickers(t *testing.T) {
	env := newTestEnv(t, false)
	if _, err := env.store.CreatePrefixSet(store.PrefixSet{
		Name: "MY_AGGREGATES", Family: store.FamilyV4, Entries: []store.PrefixEntry{{Prefix: "192.0.2.0/24"}},
	}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/policies/new?direction=export", "/policies/new?direction=import"} {
		body := env.do(t, "GET", path, nil).Body.String()
		if !strings.Contains(body, "MY_AGGREGATES") {
			t.Errorf("%s should offer the operator's own sets", path)
		}
		for _, name := range []string{store.BogonSetV4, store.BogonSetV6} {
			// The word may appear in help text; an <option> for it must not.
			if strings.Contains(body, ">"+name+" (") {
				t.Errorf("%s must not offer %s as a selectable set", path, name)
			}
		}
	}
}

func TestBogonSetsAreNotListedInTheLibrary(t *testing.T) {
	env := newTestEnv(t, false)
	body := env.do(t, "GET", "/library/prefix-sets", nil).Body.String()
	if !strings.Contains(body, "Settings") {
		t.Error("the library should point at Settings for the bogon lists")
	}
	if strings.Contains(body, `/library/prefix-sets/BOGONS_V4/edit`) {
		t.Error("a system set must not appear as a library row")
	}
}

func TestBogonSetCannotBeDeletedOverHTTP(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, "POST", "/library/prefix-sets/BOGONS_V4/delete", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "Could+not+delete") {
		t.Errorf("expected a refusal flash, got %q", rec.Header().Get("Location"))
	}
	if _, err := env.store.GetPrefixSetByName(store.BogonSetV4); err != nil {
		t.Error("the bogon set must survive")
	}
}

// The set is reachable by URL even though the Library does not link it. Its
// name and family must survive an edit that tries to change them.
func TestBogonSetIdentityIsLocked(t *testing.T) {
	env := newTestEnv(t, false)
	form := url.Values{
		"name": {"RENAMED"}, "family": {"ipv6"}, "originate": {"on"},
		"entries": {"10.0.0.0/8+\n"},
	}
	if rec := env.do(t, "POST", "/library/prefix-sets/BOGONS_V4/edit", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	ps, err := env.store.GetPrefixSetByName(store.BogonSetV4)
	if err != nil {
		t.Fatalf("the set was renamed: %v", err)
	}
	if ps.Family != store.FamilyV4 {
		t.Error("family must not change")
	}
	if ps.Originate {
		t.Error("a bogon set must never originate routes")
	}
	if len(ps.Entries) != 1 {
		t.Error("contents should still be editable")
	}
}

func TestSettingsBogonsRoundTrip(t *testing.T) {
	env := newTestEnv(t, false)

	page := env.do(t, "GET", "/settings", nil).Body.String()
	for _, want := range []string{"IPv4 bogon prefixes", "Bogon AS numbers", "64512-65534 private"} {
		if !strings.Contains(page, want) {
			t.Errorf("settings page missing %q", want)
		}
	}

	form := url.Values{
		"bogonsV4":  {"10.0.0.0/8+\n192.168.0.0/16+\n"},
		"bogonsV6":  {"fc00::/7+\n"},
		"bogonAsns": {"0  # reserved\n64512-65534 private\n"},
	}
	if rec := env.do(t, "POST", "/settings/bogons", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	v4, _ := env.store.GetBogonSet(store.FamilyV4)
	if len(v4.Entries) != 2 || v4.Entries[0].Pattern() != "10.0.0.0/8+" {
		t.Errorf("v4 bogons not saved: %+v", v4.Entries)
	}
	asns, _ := env.store.ListBogonASNs()
	if len(asns) != 2 || !asns[1].Private || asns[0].Note != "reserved" {
		t.Errorf("bogon ASNs not saved: %+v", asns)
	}
}

// Nothing may be saved unless all three lists parse: generated filters name
// these sets, so a half-applied edit is worse than a rejected one.
func TestSettingsBogonsRejectBadInputWholesale(t *testing.T) {
	env := newTestEnv(t, false)
	before, _ := env.store.GetBogonSet(store.FamilyV4)

	form := url.Values{
		"bogonsV4":  {"10.0.0.0/8+\n"},
		"bogonsV6":  {"fc00::/7+\n"},
		"bogonAsns": {"0\nnot-a-number\n"},
	}
	rec := env.do(t, "POST", "/settings/bogons", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the page back, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Line 2:") {
		t.Error("the offending line should be reported")
	}
	after, _ := env.store.GetBogonSet(store.FamilyV4)
	if len(after.Entries) != len(before.Entries) {
		t.Error("the v4 list must not be saved when the ASN list is invalid")
	}
}

func TestSettingsBogonsCannotBeEmptied(t *testing.T) {
	env := newTestEnv(t, false)
	form := url.Values{"bogonsV4": {""}, "bogonsV6": {"fc00::/7+\n"}, "bogonAsns": {"0\n"}}
	rec := env.do(t, "POST", "/settings/bogons", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the page back, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Add at least one prefix") {
		t.Error("an empty bogon list should be refused: every filter names it")
	}
}

func TestSettingsBogonsRestoreDefaults(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.ReplaceBogonASNs([]store.BogonASN{{Low: 1, High: 1}}); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"restore": {"defaults"}, "bogonsV4": {""}, "bogonsV6": {""}, "bogonAsns": {""}}
	if rec := env.do(t, "POST", "/settings/bogons", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	asns, _ := env.store.ListBogonASNs()
	if len(asns) != len(store.DefaultBogonASNs()) {
		t.Errorf("defaults not restored: %d entries", len(asns))
	}
	v4, _ := env.store.GetBogonSet(store.FamilyV4)
	if len(v4.Entries) != len(store.DefaultBogonPrefixes(store.FamilyV4)) {
		t.Errorf("v4 defaults not restored: %d entries", len(v4.Entries))
	}
}

// A fresh install ships EXPORT_OWN already pointing at two empty prefix sets, so
// the operator has an obvious place to put their aggregates.
func TestExportOwnIsSeededWithAnnounceSets(t *testing.T) {
	env := newTestEnv(t, false)
	p, err := env.store.GetPolicyByName("EXPORT_OWN")
	if err != nil {
		t.Fatalf("EXPORT_OWN not seeded: %v", err)
	}
	if !p.Builtin || p.IsImport() {
		t.Errorf("EXPORT_OWN should be a builtin export policy: %+v", p)
	}
	if p.AnnounceEverything || p.AnnounceFromCustomer {
		t.Error("EXPORT_OWN announces only our own prefixes")
	}
	if len(p.SetIDs) != 2 {
		t.Fatalf("EXPORT_OWN should name ANNOUNCE_V4 and ANNOUNCE_V6, got %d sets", len(p.SetIDs))
	}

	for _, name := range []string{"ANNOUNCE_V4", "ANNOUNCE_V6"} {
		ps, err := env.store.GetPrefixSetByName(name)
		if err != nil {
			t.Fatalf("%s not seeded: %v", name, err)
		}
		if ps.System {
			t.Errorf("%s must stay editable and selectable", name)
		}
		if !ps.Originate {
			t.Errorf("%s should originate: you must originate what you announce", name)
		}
		// Empty on purpose — birdy cannot know your address space.
		if len(ps.Entries) != 0 {
			t.Errorf("%s should ship empty, got %d entries", name, len(ps.Entries))
		}
		if !slices.Contains(p.SetIDs, ps.ID) {
			t.Errorf("%s should be attached to EXPORT_OWN", name)
		}
	}

	// Empty sets still count as "announces something" for validation; the
	// renderer skips them and Lint explains the consequence.
	if errs := p.Validate(); errs["announce"] != "" {
		t.Errorf("EXPORT_OWN should be saveable as shipped: %v", errs)
	}
}

// Both sets appear in the Library so an operator can find and fill them.
func TestAnnounceSetsAppearInTheLibrary(t *testing.T) {
	env := newTestEnv(t, false)
	body := env.do(t, "GET", "/library/prefix-sets", nil).Body.String()
	for _, name := range []string{"ANNOUNCE_V4", "ANNOUNCE_V6"} {
		if !strings.Contains(body, name) {
			t.Errorf("%s should be listed in the library", name)
		}
	}
}
