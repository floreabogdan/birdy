package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/birdc"
)

func TestPeersListShowsLiveState(t *testing.T) {
	env := newTestEnv(t, false)

	form := peerForm()
	form.Set("name", "edge_v4") // live in the fake client
	env.do(t, "POST", "/peers/new", form)
	form = peerForm()
	form.Set("name", "not_running")
	form.Set("neighborIp", "198.51.100.2")
	env.do(t, "POST", "/peers/new", form)

	body := env.do(t, "GET", "/peers", nil).Body.String()
	if !strings.Contains(body, `href="/peers/edge_v4"`) {
		t.Error("a peer name should link to its detail page")
	}
	if !strings.Contains(body, "not applied") {
		t.Error("a configured peer BIRD has no protocol for should say so")
	}
}

// /sessions/{name} was the live view before it moved beside the peer.
func TestLegacySessionDetailRedirects(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, "GET", "/sessions/edge_v4", nil)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("want 301, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/peers/edge_v4" {
		t.Errorf("Location = %q", got)
	}
}

// The literal routes must win over the {name} wildcard.
func TestPeerEditNotShadowedByDetailRoute(t *testing.T) {
	env := newTestEnv(t, false)
	form := peerForm()
	env.do(t, "POST", "/peers/new", form)

	rec := env.do(t, "GET", "/peers/transit_v4/edit", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="neighborIp"`) {
		t.Error("/peers/{name}/edit should render the peer form")
	}
	if rec := env.do(t, "GET", "/peers/new", nil); rec.Code != http.StatusOK {
		t.Fatalf("/peers/new should render the create form, got %d", rec.Code)
	}
}

// liveDetail makes the fake BIRD answer "show protocols all edge_v4", which is
// what turns the detail page from "not applied" into the live view.
func liveDetail(env *testEnv) {
	env.fc.details["edge_v4"] = birdc.ProtocolDetail{
		Summary:  birdc.ProtocolSummary{Proto: "BGP", State: "up"},
		BGPState: "Established",
		RawLines: []string{"edge_v4     BGP    ---    up    2026-07-08    Established"},
	}
}

func TestPeerDetailTabs(t *testing.T) {
	env := newTestEnv(t, false)
	liveDetail(env)

	body := env.do(t, "GET", "/peers/edge_v4", nil).Body.String()
	for _, want := range []string{`data-tab="general"`, `data-tab="bird"`, `show protocols all edge_v4`} {
		if !strings.Contains(body, want) {
			t.Errorf("peer detail is missing %q", want)
		}
	}
	// General is the default, so the BIRD output panel ships hidden.
	if !strings.Contains(body, `data-tab-panel="bird" hidden`) {
		t.Error("the BIRD output panel should be hidden until selected")
	}

	body = env.do(t, "GET", "/peers/edge_v4?tab=bird", nil).Body.String()
	if !strings.Contains(body, `data-tab-panel="general" hidden`) {
		t.Error("?tab=bird should hide the General panel, so the tab survives a reload")
	}
	if strings.Contains(body, `data-tab-panel="bird" hidden`) {
		t.Error("?tab=bird should show the BIRD output panel")
	}
}

// An unknown ?tab= is a stale bookmark, not a 404.
func TestUnknownTabFallsBackToTheFirst(t *testing.T) {
	env := newTestEnv(t, false)
	liveDetail(env)
	rec := env.do(t, "GET", "/peers/edge_v4?tab=nonsense", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `data-tab-panel="bird" hidden`) {
		t.Error("an unknown tab should land on General")
	}
}

// A peer birdy knows and BIRD does not is the normal read-only state, not an error.
func TestPeerDetailOfAnUnappliedPeerExplainsItself(t *testing.T) {
	env := newTestEnv(t, false)
	form := peerForm()
	form.Set("name", "not_running")
	env.do(t, "POST", "/peers/new", form)

	body := env.do(t, "GET", "/peers/not_running", nil).Body.String()
	if !strings.Contains(body, "Not applied") {
		t.Error("a configured peer BIRD has no protocol for should say it is not applied")
	}
	if !strings.Contains(body, `href="/changes"`) {
		t.Error("it should point at the config that would create it")
	}
}

func TestClonePeerPrefillsShapeNotIdentity(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	// Make a source peer with a distinctive shape.
	form := peerForm()
	form.Set("name", "cust_a")
	form.Set("role", "customer")
	form.Set("neighborIp", "198.51.100.20")
	form.Set("remoteAsn", "64512")
	form.Set("password", "s3cr3t")
	form.Set("prependCount", "2")
	form.Set("importPolicyIds", "") // no chains, keep it simple
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create source: %d %s", rec.Code, rec.Body.String())
	}

	body := env.do(t, "GET", "/peers/new?from=cust_a", nil).Body.String()
	// The shape carries: role customer, prepend 2.
	if !strings.Contains(body, `value="customer" selected`) {
		t.Error("clone should carry the role")
	}
	if !strings.Contains(body, `Cloned from`) {
		t.Error("clone should note its source")
	}
	// The identity and secret do NOT carry.
	if strings.Contains(body, "198.51.100.20") || strings.Contains(body, "64512") {
		t.Error("clone must not carry the neighbor or ASN")
	}
	if strings.Contains(body, "s3cr3t") {
		t.Error("clone must never carry the password")
	}
}

// Cloning a peer that no longer exists falls back to a blank form, not a 404.
func TestCloneMissingPeerIsBlankForm(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)
	rec := env.do(t, "GET", "/peers/new?from=ghost", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want a blank form, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Cloned from") {
		t.Error("a missing source should not claim to be a clone")
	}
}
