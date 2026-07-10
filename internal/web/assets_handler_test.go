package web

import (
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// The preview only renders once the local ASN is known, since it appears in the
// generated loop guard and communities.
func withLocalASN(t *testing.T, env *testEnv) {
	t.Helper()
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: nullInt(65551),
		BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "127.0.0.1:8080",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestASSetCRUDAndBgpq4Paste(t *testing.T) {
	env := newTestEnv(t, false)

	// bgpq4-style output: AS-prefixed, comma-terminated, with comments.
	form := url.Values{
		"name":        {"AS_CUSTOMER_A"},
		"description": {"Customer A and downstreams"},
		"source":      {"AS-CUSTOMER-A"},
		"entries":     {"# expanded 2026-07-10\nAS64600,\n65010-65020  # downstreams\n\n"},
	}
	if rec := env.do(t, "POST", "/library/as-sets/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	as, err := env.store.GetASSetByName("AS_CUSTOMER_A")
	if err != nil {
		t.Fatal(err)
	}
	if len(as.Entries) != 2 {
		t.Fatalf("comments, blank lines and trailing commas should be ignored, got %+v", as.Entries)
	}
	if as.Entries[0].Low != 64600 || as.Entries[0].High != 64600 {
		t.Errorf("AS-prefixed number parsed wrong: %+v", as.Entries[0])
	}
	if as.Entries[1].Low != 65010 || as.Entries[1].High != 65020 || as.Entries[1].Note != "downstreams" {
		t.Errorf("range parsed wrong: %+v", as.Entries[1])
	}
	if as.Source != "AS-CUSTOMER-A" {
		t.Error("the IRR source should be recorded")
	}

	body := env.do(t, "GET", "/library/as-sets", nil).Body.String()
	if !strings.Contains(body, "AS_CUSTOMER_A") || !strings.Contains(body, "AS-CUSTOMER-A") {
		t.Error("the list should show the set and its source")
	}

	if rec := env.do(t, "POST", "/library/as-sets/AS_CUSTOMER_A/delete", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := env.store.GetASSetByName("AS_CUSTOMER_A"); err != store.ErrNotFound {
		t.Error("the set should be gone")
	}
}

func TestASSetRejectsGarbageWithLineNumbers(t *testing.T) {
	env := newTestEnv(t, false)
	form := url.Values{"name": {"X"}, "entries": {"64600\nnot-an-asn\n"}}
	rec := env.do(t, "POST", "/library/as-sets/new", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the form back, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Line 2:") {
		t.Error("the offending line should be reported")
	}
	if sets, _ := env.store.ListASSets(); len(sets) != 0 {
		t.Error("nothing should be stored")
	}
}

func TestEmptyASSetIsRefused(t *testing.T) {
	env := newTestEnv(t, false)
	form := url.Values{"name": {"X"}, "entries": {"# nothing here\n"}}
	rec := env.do(t, "POST", "/library/as-sets/new", form)
	if !strings.Contains(rec.Body.String(), "Add at least one AS number") {
		t.Error("BIRD has no empty-set syntax; an empty AS set must be refused")
	}
}

// Deleting an AS set a policy filters through would turn "accept only these
// origins" into "accept any origin".
func TestDeleteASSetInUseIsRefused(t *testing.T) {
	env := newTestEnv(t, false)
	id, err := env.store.CreateASSet(store.ASSet{
		Name: "AS_CUST", Entries: []store.ASNRange{{Low: 64600, High: 64600}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreatePolicy(store.Policy{
		Name: "IMPORT_CUST", Direction: store.DirImport, DefaultRoute: store.DefaultReject,
		BogonASNs: store.BogonASNsOff, OriginASSetID: nullInt(id),
	}); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, "POST", "/library/as-sets/AS_CUST/delete", nil)
	if !strings.Contains(rec.Header().Get("Location"), "Could+not+delete") {
		t.Errorf("expected a refusal flash, got %q", rec.Header().Get("Location"))
	}
	if _, err := env.store.GetASSetByName("AS_CUST"); err != nil {
		t.Error("the AS set must survive a refused delete")
	}
}

// "Transit for you, not your downstreams" is a per-peer switch, because the
// check compares against that peer's own ASN.
func TestOriginPeerOnlyRoundTrip(t *testing.T) {
	env := newTestEnv(t, false)
	withLocalASN(t, env)
	form := peerForm()
	form.Set("role", "customer")
	form.Set("originPeerOnly", "on")
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	p, err := env.store.GetPeerByName("transit_v4")
	if err != nil {
		t.Fatal(err)
	}
	if !p.OriginPeerOnly {
		t.Fatal("originPeerOnly not stored")
	}

	// The generated preview must carry the origin check with the peer's ASN.
	body := env.do(t, "GET", "/peers/transit_v4/edit", nil).Body.String()
	if !strings.Contains(body, "bgp_path.last != 64497") {
		t.Error("the preview should show the origin-AS guard")
	}

	form.Del("originPeerOnly")
	if rec := env.do(t, "POST", "/peers/transit_v4/edit", form); rec.Code != http.StatusSeeOther {
		t.Fatal("edit failed")
	}
	p, _ = env.store.GetPeerByName("transit_v4")
	if p.OriginPeerOnly {
		t.Error("unchecking the box should clear it")
	}
}

func TestPolicyOriginASSetRoundTrip(t *testing.T) {
	env := newTestEnv(t, false)
	withLocalASN(t, env)
	id, err := env.store.CreateASSet(store.ASSet{
		Name: "AS_CUST", Entries: []store.ASNRange{{Low: 64600, High: 64600}},
	})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"name": {"IMPORT_CUST"}, "direction": {"import"}, "defaultRoute": {"reject"},
		"bogonAsns": {"except_private"}, "originAsSetId": {itoa(id)},
	}
	if rec := env.do(t, "POST", "/policies/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	pol, err := env.store.GetPolicyByName("IMPORT_CUST")
	if err != nil {
		t.Fatal(err)
	}
	if !pol.OriginASSetID.Valid || pol.OriginASSetID.Int64 != id {
		t.Fatalf("origin AS set not stored: %+v", pol.OriginASSetID)
	}

	body := env.do(t, "GET", "/policies/IMPORT_CUST/edit", nil).Body.String()
	if !strings.Contains(body, "bgp_path.last ~ AS_CUST") {
		t.Error("the preview should show the origin-AS-set guard")
	}
}

func nullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
func itoa(v int64) string           { return strconv.FormatInt(v, 10) }
