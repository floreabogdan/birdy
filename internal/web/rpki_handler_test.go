package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

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
	if !strings.Contains(body, "No import policy validates") {
		t.Error("with nothing validating, the page should say so")
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
	if !strings.Contains(del.Header().Get("Location"), "Could+not+delete") {
		t.Errorf("expected a refusal flash, got %q", del.Header().Get("Location"))
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
