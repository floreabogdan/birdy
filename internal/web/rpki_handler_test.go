package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/store"
)

func TestRPKIPageShowsSeededDisabledServer(t *testing.T) {
	env := newTestEnv(t, false)
	body := env.do(t, "GET", "/rpki", nil).Body.String()
	if !strings.Contains(body, "rtr.rpki.cloudflare.com") {
		t.Error("the seeded RTR server should be listed")
	}
	if !strings.Contains(body, "disabled") {
		t.Error("it should be shown as disabled")
	}
	// Policies that validate nothing are listed too, rather than being invisible:
	// "which of my policies is not checking origins, and who rides on it" is the
	// question this page exists to answer.
	if !strings.Contains(body, "IMPORT_SANITY") {
		t.Error("the seeded import policies should be listed, validating or not")
	}
	if !strings.Contains(body, "Accepted, unchecked") {
		t.Error("a policy with RPKI off should say plainly that it checks nothing")
	}
	if strings.Contains(body, "reject invalid</span>") {
		t.Error("nothing is set to reject, so no policy should claim to")
	}
}

// A disabled server renders nothing, so validating against it would leave every
// route "unknown". The Changes page must refuse rather than pretend.
func TestValidatingWithOnlyADisabledServerBlocksTheRender(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: nullInt(65551),
		BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "127.0.0.1:8080",
	}); err != nil {
		t.Fatal(err)
	}

	sanity, err := env.store.GetPolicyByName("IMPORT_SANITY")
	if err != nil {
		t.Fatal(err)
	}
	sanity.ROV = store.ROVReject
	if err := env.store.UpdatePolicy(sanity); err != nil {
		t.Fatal(err)
	}

	body := env.do(t, "GET", "/changes", nil).Body.String()
	if !strings.Contains(body, "no RTR server is enabled") {
		t.Errorf("the render should refuse and explain:\n%s", body)
	}

	// Enabling the seeded server resolves it.
	srv, _ := env.store.GetRPKIServerByName("cloudflare")
	srv.Enabled = true
	if err := env.store.UpdateRPKIServer(srv); err != nil {
		t.Fatal(err)
	}
	body = env.do(t, "GET", "/changes", nil).Body.String()
	if strings.Contains(body, "no RTR server is enabled") {
		t.Error("with a server enabled the config should render")
	}
	if !strings.Contains(body, "protocol rpki cloudflare") {
		t.Error("the enabled server should be rendered into the config")
	}
}

// Turning the last enabled server off while a policy validates would produce a
// config that checks nothing.
func TestDisablingTheLastServerWhileValidatingIsRefused(t *testing.T) {
	env := newTestEnv(t, false)
	srv, _ := env.store.GetRPKIServerByName("cloudflare")
	srv.Enabled = true
	if err := env.store.UpdateRPKIServer(srv); err != nil {
		t.Fatal(err)
	}
	sanity, _ := env.store.GetPolicyByName("IMPORT_SANITY")
	sanity.ROV = store.ROVReject
	if err := env.store.UpdatePolicy(sanity); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"name": {"cloudflare"}, "host": {"rtr.rpki.cloudflare.com"}, "port": {"8282"},
		"refresh": {"900"}, "retry": {"90"}, "expire": {"172800"},
		// "enabled" absent = unchecked
	}
	rec := env.do(t, "POST", "/rpki/cloudflare/edit", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the form back, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "last enabled RTR server") {
		t.Error("disabling the last server while a policy validates must be refused")
	}
	again, _ := env.store.GetRPKIServerByName("cloudflare")
	if !again.Enabled {
		t.Error("it must stay enabled")
	}

	// Deleting it is refused for the same reason.
	del := env.do(t, "POST", "/rpki/cloudflare/delete", nil)
	if !strings.Contains(flashOf(del), "Could not delete") {
		t.Errorf("expected a refusal flash, got %q", flashOf(del))
	}
}

func TestRPKIServerValidation(t *testing.T) {
	env := newTestEnv(t, false)
	form := url.Values{
		"name": {"bad"}, "host": {"not a hostname"}, "port": {"0"},
		"refresh": {"900"}, "expire": {"300"}, "enabled": {"on"},
	}
	body := env.do(t, "POST", "/rpki/new", form).Body.String()
	for _, want := range []string{"valid hostname", "port between 1 and 65535", "longer than refresh"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing validation message %q", want)
		}
	}
}

// "not running" conflated three different situations. An enabled server BIRD is
// not running means "not applied"; a disabled one means "not rendered".
func TestInBirdColumnDistinguishesRenderedFromApplied(t *testing.T) {
	env := newTestEnv(t, false)

	// Seeded and disabled: nothing to run, by design.
	body := env.do(t, "GET", "/rpki", nil).Body.String()
	if !strings.Contains(body, "not rendered") {
		t.Error("a disabled server should read \"not rendered\"")
	}
	if strings.Contains(body, "not applied") && !strings.Contains(body, "reads <span") {
		t.Error("a disabled server must not read \"not applied\"")
	}

	// Enabled, but birdy has applied nothing, so BIRD has no such protocol.
	srv, _ := env.store.GetRPKIServerByName("cloudflare")
	srv.Enabled = true
	if err := env.store.UpdateRPKIServer(srv); err != nil {
		t.Fatal(err)
	}
	body = env.do(t, "GET", "/rpki", nil).Body.String()
	if !strings.Contains(body, "not applied") {
		t.Error("an enabled server BIRD is not running should read \"not applied\"")
	}
	if !strings.Contains(body, "has never written") {
		t.Error("the page should explain why nothing is running")
	}
}

// A configured peer BIRD is not running is "not applied", not "not running".
func TestPeersListSaysNotApplied(t *testing.T) {
	env := newTestEnv(t, false)
	form := peerForm()
	form.Set("name", "not_in_bird")
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", rec.Code)
	}
	body := env.do(t, "GET", "/peers", nil).Body.String()
	if !strings.Contains(body, "not applied") {
		t.Error("a configured peer absent from BIRD should read \"not applied\"")
	}
	if strings.Contains(body, "not running") {
		t.Error(`"not running" reads like a failure; it is an unapplied config`)
	}
}

// With a policy in log-only mode, the RPKI page lists the routes BIRD is
// currently tagging invalid — the dry run before switching to reject.
func TestRPKIInvalidsDryRun(t *testing.T) {
	env := applyReady(t)
	if _, err := env.store.CreatePolicy(store.Policy{
		Name: "IMPORT_LOG", Direction: store.DirImport, ROV: store.ROVLog,
		DefaultRoute: store.DefaultReject, BogonASNs: store.BogonASNsAll,
	}); err != nil {
		t.Fatal(err)
	}
	env.fc.routes["rpki-invalid"] = []birdc.RouteTable{{
		Name:   "master4",
		Routes: []birdc.RouteEntry{{Network: "203.0.113.0/24", Protocol: "edge_v4", ASPath: "AS64500"}},
	}}

	// BIRD counts the invalids itself; the ROA tables are not routes and must not
	// be added into the total.
	env.fc.invalidCounts = []birdc.RouteCountEntry{
		{Table: "master4", Routes: 742},
		{Table: "master6", Routes: 116},
		{Table: "rpki4", Routes: 0},
	}

	body := env.do(t, "GET", "/rpki", nil).Body.String()
	if !strings.Contains(body, "RPKI-invalid routes") {
		t.Error("a log-only policy should show the invalids dry-run panel")
	}
	if !strings.Contains(body, "203.0.113.0/24") {
		t.Error("the live invalid route should be listed")
	}
	// The number is the answer the dry run exists to give: how many would I lose?
	if !strings.Contains(body, "858 would be dropped") {
		t.Error("the total invalid count (742 + 116) should be stated outright")
	}
	if !strings.Contains(body, "master6") {
		t.Error("the per-table breakdown should say how many are v6")
	}
}

// The table names the peers each policy carries. That is the blast radius of
// switching it to reject, and a bare list of policy names could never show it.
func TestRPKIPolicyTableShowsTheBlastRadius(t *testing.T) {
	env := applyReady(t)
	id, err := env.store.CreatePolicy(store.Policy{
		Name: "IMPORT_LOG", Direction: store.DirImport, ROV: store.ROVLog,
		DefaultRoute: store.DefaultReject, BogonASNs: store.BogonASNsAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := peerForm()
	f.Set("name", "edge_v4")
	f.Set("importPolicyIds", strconv.FormatInt(id, 10))
	env.do(t, "POST", "/peers/new", f)

	body := env.do(t, "GET", "/rpki", nil).Body.String()
	if !strings.Contains(body, "IMPORT_LOG") {
		t.Fatal("the validating policy should be listed")
	}
	if !strings.Contains(body, "edge_v4") {
		t.Error("the peers riding on the policy are what tells you what a switch to reject would affect")
	}
}

// Without a log-only policy, the dry-run panel is absent (nothing tags invalids).
func TestRPKINoInvalidsPanelWithoutLogOnly(t *testing.T) {
	env := applyReady(t)
	if body := env.do(t, "GET", "/rpki", nil).Body.String(); strings.Contains(body, "dry run") {
		t.Error("the invalids panel should not appear without a log-only policy")
	}
}
