package web

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func (e *testEnv) do(t *testing.T, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if form == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func peerForm() url.Values {
	return url.Values{
		"name": {"transit_v4"}, "role": {"upstream"}, "enabled": {"on"},
		"neighborIp": {"198.51.100.1"}, "remoteAsn": {"64497"},
		"importLimit": {"1000000"}, "importLimitAction": {"restart"},
		"enforceFirstAs": {"on"},
	}
}

func TestPeerCreateEditDeleteRoundTrip(t *testing.T) {
	env := newTestEnv(t, false)

	if rec := env.do(t, "POST", "/peers/new", peerForm()); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d body=%s", rec.Code, rec.Body)
	}
	p, err := env.store.GetPeerByName("transit_v4")
	if err != nil {
		t.Fatalf("peer not stored: %v", err)
	}
	if p.RemoteASN != 64497 || !p.Enabled || p.ImportLimit != 1000000 {
		t.Errorf("stored peer wrong: %+v", p)
	}

	// The list page shows it.
	rec := env.do(t, "GET", "/peers", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "transit_v4") {
		t.Errorf("list: code=%d", rec.Code)
	}

	// Edit: rename and disable.
	form := peerForm()
	form.Set("name", "transit_v4_renamed")
	form.Del("enabled")
	if rec := env.do(t, "POST", "/peers/transit_v4/edit", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: code=%d body=%s", rec.Code, rec.Body)
	}
	p, err = env.store.GetPeerByName("transit_v4_renamed")
	if err != nil {
		t.Fatalf("renamed peer missing: %v", err)
	}
	if p.Enabled {
		t.Error("peer should be disabled after unchecking the box")
	}

	if rec := env.do(t, "POST", "/peers/transit_v4_renamed/delete", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: code=%d", rec.Code)
	}
	if _, err := env.store.GetPeerByName("transit_v4_renamed"); err != store.ErrNotFound {
		t.Errorf("peer should be gone, got %v", err)
	}
}

// /peers/new is a literal that must outrank the /peers/{name} wildcard used by
// the live session view, otherwise "Add peer" renders a protocol detail page.
func TestPeerNewOutranksSessionWildcard(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, "GET", "/peers/new", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="neighborIp"`) {
		t.Error("/peers/new should render the create form")
	}
	// "Raw BIRD output" only exists on the live session detail page.
	if strings.Contains(body, "Raw BIRD output") {
		t.Error("/peers/new rendered the live session detail page instead")
	}
}

func TestPeerInvalidInputRedisplaysFormWithErrors(t *testing.T) {
	env := newTestEnv(t, false)

	form := peerForm()
	form.Set("name", "9bad name")
	form.Set("neighborIp", "not-an-ip")
	form.Set("remoteAsn", "23456")

	rec := env.do(t, "POST", "/peers/new", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the form to be redisplayed, got code=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"starting with a letter", "valid IPv4 or IPv6 address", "AS_TRANS"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing error message %q", want)
		}
	}
	if peers, _ := env.store.ListPeers(); len(peers) != 0 {
		t.Error("an invalid peer must not be stored")
	}
}

func TestDuplicatePeerNameShowsFormError(t *testing.T) {
	env := newTestEnv(t, false)
	if rec := env.do(t, "POST", "/peers/new", peerForm()); rec.Code != http.StatusSeeOther {
		t.Fatal("first create should succeed")
	}
	rec := env.do(t, "POST", "/peers/new", peerForm())
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate name should redisplay the form, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already exists") {
		t.Error("duplicate name should be reported on the form, not as a 500")
	}
}

// The edit form never renders the stored secret, so a blank password field has
// to mean "keep it" rather than "clear it".
func TestBlankPasswordKeepsExistingSecret(t *testing.T) {
	env := newTestEnv(t, false)
	form := peerForm()
	form.Set("password", "hunter2")
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
		t.Fatal("create failed")
	}

	form = peerForm()
	form.Set("description", "now with a description")
	form.Set("password", "") // blank
	if rec := env.do(t, "POST", "/peers/transit_v4/edit", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("edit failed: %s", rec.Body)
	}
	p, err := env.store.GetPeerByName("transit_v4")
	if err != nil {
		t.Fatal(err)
	}
	if p.Password != "hunter2" {
		t.Errorf("password should survive an edit that leaves the field blank, got %q", p.Password)
	}
}

func TestPeerFormNeverRendersThePassword(t *testing.T) {
	env := newTestEnv(t, false)
	form := peerForm()
	form.Set("password", "hunter2")
	env.do(t, "POST", "/peers/new", form)

	rec := env.do(t, "GET", "/peers/transit_v4/edit", nil)
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Error("the stored password must never reach the browser")
	}
}

// A chain's order is the document order of the posted selects. If that ever
// stops holding, filters silently run their policies in the wrong sequence.
func TestPolicyChainPreservesPostedOrder(t *testing.T) {
	env := newTestEnv(t, false)
	sanity, err := env.store.GetPolicyByName("IMPORT_SANITY")
	if err != nil {
		t.Fatal(err)
	}
	defaultOnly, err := env.store.GetPolicyByName("IMPORT_DEFAULT_ONLY")
	if err != nil {
		t.Fatal(err)
	}

	form := peerForm()
	form["importPolicyIds"] = []string{
		strconv.FormatInt(defaultOnly.ID, 10),
		strconv.FormatInt(sanity.ID, 10),
	}
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	p, err := env.store.GetPeerByName("transit_v4")
	if err != nil {
		t.Fatal(err)
	}
	imports, _, err := env.store.PeerPolicies(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(imports) != 2 {
		t.Fatalf("want 2 import policies, got %d", len(imports))
	}
	if imports[0].Name != "IMPORT_DEFAULT_ONLY" || imports[1].Name != "IMPORT_SANITY" {
		t.Errorf("chain order not preserved: %s, %s", imports[0].Name, imports[1].Name)
	}
}

// An export policy in the import chain would render a call to a function that
// does not exist in that position, or worse, one that accepts.
func TestPolicyChainRejectsWrongDirection(t *testing.T) {
	env := newTestEnv(t, false)
	exp, err := env.store.GetPolicyByName("EXPORT_FULL_TABLE")
	if err != nil {
		t.Fatal(err)
	}
	form := peerForm()
	form["importPolicyIds"] = []string{strconv.FormatInt(exp.ID, 10)}

	rec := env.do(t, "POST", "/peers/new", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the form back, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cannot be used as an import policy") {
		t.Errorf("direction mismatch should be explained: %s", rec.Body)
	}
	if peers, _ := env.store.ListPeers(); len(peers) != 0 {
		t.Error("peer must not be stored")
	}
}

func TestIBGPPeerTakesNoPolicies(t *testing.T) {
	env := newTestEnv(t, false)
	sanity, _ := env.store.GetPolicyByName("IMPORT_SANITY")

	form := peerForm()
	form.Set("role", "ibgp")
	form.Set("neighborIp", "10.0.0.2")
	form["importPolicyIds"] = []string{strconv.FormatInt(sanity.ID, 10)}

	rec := env.do(t, "POST", "/peers/new", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the form back, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "do not take policies") {
		t.Error("attaching a policy to an iBGP session should be explained")
	}
}

// Deleting a policy a peer still names would silently change what that session
// imports or announces.
func TestDeleteAttachedPolicyIsRefused(t *testing.T) {
	env := newTestEnv(t, false)
	sanity, _ := env.store.GetPolicyByName("IMPORT_SANITY")

	form := peerForm()
	form["importPolicyIds"] = []string{strconv.FormatInt(sanity.ID, 10)}
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
		t.Fatal("create failed")
	}

	rec := env.do(t, "POST", "/policies/IMPORT_SANITY/delete", nil)
	if !strings.Contains(rec.Header().Get("Location"), "Could+not+delete") {
		t.Errorf("expected a refusal flash, got %q", rec.Header().Get("Location"))
	}
	if _, err := env.store.GetPolicyByName("IMPORT_SANITY"); err != nil {
		t.Error("the policy must survive a refused delete")
	}
}

func TestPolicySeedsAreUsable(t *testing.T) {
	env := newTestEnv(t, false)
	for _, name := range []string{
		"IMPORT_SANITY", "IMPORT_SANITY_PRIVATE_AS", "IMPORT_DEFAULT_ONLY",
		"EXPORT_FULL_TABLE", "EXPORT_DEFAULT_ONLY", "EXPORT_DOWNSTREAM", "EXPORT_OWN_AND_CUSTOMERS",
	} {
		p, err := env.store.GetPolicyByName(name)
		if err != nil {
			t.Errorf("%s not seeded: %v", name, err)
			continue
		}
		if !p.Builtin {
			t.Errorf("%s should be tagged builtin", name)
		}
		if errs := p.Validate(); len(errs) != 0 {
			t.Errorf("seeded %s does not validate: %v", name, errs)
		}
	}
}

func TestPrefixSetCreateAndDeleteGuard(t *testing.T) {
	env := newTestEnv(t, false)

	form := url.Values{
		"name": {"MY_AGGREGATES"}, "family": {"ipv4"}, "originate": {"on"},
		"entries": {"# our space\n192.0.2.0/24\n\n198.51.100.0/24+\n"},
	}
	if rec := env.do(t, "POST", "/library/prefix-sets/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d body=%s", rec.Code, rec.Body)
	}
	ps, err := env.store.GetPrefixSetByName("MY_AGGREGATES")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.Entries) != 2 {
		t.Fatalf("comments and blank lines should be skipped, got %d entries", len(ps.Entries))
	}
	if ps.Entries[1].Modifier != "+" {
		t.Errorf("pattern suffix lost: %+v", ps.Entries[1])
	}
	if !ps.Originate {
		t.Error("originate flag lost")
	}

	// Point a policy at it, then try to delete the set.
	if _, err := env.store.CreatePolicy(store.Policy{
		Name: "EXPORT_MINE", Direction: store.DirExport, SetIDs: []int64{ps.ID},
	}); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, "POST", "/library/prefix-sets/MY_AGGREGATES/delete", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "Could+not+delete") {
		t.Errorf("expected a refusal flash, got %q", rec.Header().Get("Location"))
	}
	if _, err := env.store.GetPrefixSetByName("MY_AGGREGATES"); err != nil {
		t.Error("the set must survive a refused delete")
	}
}

func TestPrefixSetInvalidEntryReportsLineNumber(t *testing.T) {
	env := newTestEnv(t, false)
	form := url.Values{
		"name": {"X"}, "family": {"ipv4"},
		"entries": {"192.0.2.0/24\n10.0.0.1/8\n"}, // line 2 has host bits set
	}
	rec := env.do(t, "POST", "/library/prefix-sets/new", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the form back, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Line 2:") {
		t.Errorf("the offending line number should be shown:\n%s", rec.Body)
	}
}

func TestChangesRendersWithoutBirdInstalled(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: sql.NullInt64{Int64: 65551, Valid: true},
		BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "127.0.0.1:8080",
	}); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, "GET", "/changes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := rec.Body.String()
	// No bird binary and no /etc/bird/bird.conf in the test environment: the
	// page must degrade to "skipped" rather than error.
	if !strings.Contains(body, "Nothing here is applied") {
		t.Error("the not-applied warning must always be shown")
	}
	if !strings.Contains(body, "define BOGON_ASNS") {
		t.Error("the candidate config should be rendered")
	}
}

// Without this form, a router initialized before M2 has no way to set a router
// id, and the Changes page can never render anything.
func TestSettingsIdentityRoundTrip(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{BirdSocketPath: "/x", ListenAddr: "y"}); err != nil {
		t.Fatal(err)
	}

	bad := url.Values{"routerId": {"2001:db8::1"}, "localAsn": {"0"}}
	rec := env.do(t, "POST", "/settings/identity", bad)
	if rec.Code != http.StatusOK {
		t.Fatalf("bad input should redisplay the page, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "32-bit value") || !strings.Contains(body, "between 1 and 4294967295") {
		t.Error("both errors should be reported")
	}

	good := url.Values{"routerId": {"192.0.2.1"}, "localAsn": {"65551"}}
	if rec := env.do(t, "POST", "/settings/identity", good); rec.Code != http.StatusSeeOther {
		t.Fatalf("save: code=%d body=%s", rec.Code, rec.Body)
	}
	st, _, err := env.store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.RouterID != "192.0.2.1" || !st.LocalASN.Valid || st.LocalASN.Int64 != 65551 {
		t.Errorf("identity not saved: %+v", st)
	}
	// Saving identity must not wipe the rest of the settings row.
	if st.BirdSocketPath != "/x" || st.ListenAddr != "y" {
		t.Errorf("other settings clobbered: %+v", st)
	}
}

// Initialized, but the router id was never set: BIRD needs one, so the page
// must say so rather than render a config that cannot parse.
func TestChangesNeedsRouterIDAndASN(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{
		LocalASN:       sql.NullInt64{Int64: 65551, Valid: true}, // no RouterID
		BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "127.0.0.1:8080",
	}); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, "GET", "/changes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "router ID and local ASN") {
		t.Error("a router with no router id should be told what is missing")
	}
	if strings.Contains(body, "define BOGON_ASNS") {
		t.Error("no config should be rendered when the router id is unknown")
	}
}

func TestChangesTabs(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: sql.NullInt64{Int64: 65551, Valid: true},
		BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "127.0.0.1:8080",
	}); err != nil {
		t.Fatal(err)
	}

	body := env.do(t, "GET", "/changes", nil).Body.String()
	for _, want := range []string{`data-tab="config"`, `data-tab="diff"`, `id="candidate-config"`} {
		if !strings.Contains(body, want) {
			t.Errorf("changes page is missing %q", want)
		}
	}
	// The config tab is first, so the diff panel ships hidden.
	if !strings.Contains(body, `data-tab-panel="diff" hidden`) {
		t.Error("the diff panel should be hidden until selected")
	}
	// Every config line is wrapped for the numbered gutter.
	if !strings.Contains(body, `<span class="cl">define BOGON_ASNS`) {
		t.Error("candidate config lines should be wrapped for line numbering")
	}

	body = env.do(t, "GET", "/changes?tab=diff", nil).Body.String()
	if !strings.Contains(body, `data-tab-panel="config" hidden`) {
		t.Error("?tab=diff should hide the config panel, so the tab survives a reload")
	}
	if strings.Contains(body, `data-tab-panel="diff" hidden`) {
		t.Error("?tab=diff should show the diff panel")
	}
}
