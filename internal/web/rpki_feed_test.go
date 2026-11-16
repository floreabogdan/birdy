package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/store"
)

// withLiveFeed puts an enabled RTR server "cf" in the model and a matching RPKI
// protocol in BIRD, stuck on session 25012 / serial 1, and re-polls so the
// poller has read it.
func withLiveFeed(t *testing.T, env *testEnv) {
	t.Helper()
	if _, err := env.store.CreateRPKIServer(store.RPKIServer{
		Name: "cf", Host: "rtr.rpki.cloudflare.com", Port: 8282, Enabled: true, Refresh: 900, Expire: 172800,
	}); err != nil {
		t.Fatal(err)
	}
	env.fc.protocols = append(env.fc.protocols,
		birdc.ProtocolSummary{Name: "cf", Proto: "RPKI", Table: "---", State: "up", Since: "2026-11-12", Info: "Established"})
	env.fc.details["cf"] = birdc.ProtocolDetail{
		RPKI: &birdc.RPKICache{Status: "Established", Version: 1, SessionID: 25012, Serial: 1, LastUpdate: "before 621.000 s"},
		Channels: []birdc.ChannelDetail{
			{AFI: "roa4", State: "UP", RoutesImported: 795569},
			{AFI: "roa6", State: "UP", RoutesImported: 237784},
		},
	}
	env.srv.poller.Run(cancelledCtx())
}

// The RPKI page answers "is this feed's data moving?" — session, serial, how
// long birdy has seen that serial stand still, and how many ROAs it holds (the
// number to compare with another router on the same cache).
func TestRPKIPageShowsFeedState(t *testing.T) {
	env := newTestEnv(t, false)
	withLiveFeed(t, env)
	body := env.do(t, "GET", "/rpki", nil).Body.String()
	for _, want := range []string{"session 25012", "serial 1", "795,569", "237,784", "RTR v1"} {
		if !strings.Contains(body, want) {
			t.Errorf("RPKI page is missing %q", want)
		}
	}
	if !strings.Contains(body, `action="/rpki/cf/restart"`) {
		t.Error("an enabled, live feed should offer a restart")
	}
}

func TestRPKIRestart(t *testing.T) {
	env := newTestEnv(t, false)
	withLiveFeed(t, env)
	rec := env.do(t, "POST", "/rpki/cf/restart", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("restart: %d %s", rec.Code, rec.Body.String())
	}
	if len(env.fc.restarts) != 1 || env.fc.restarts[0] != "cf" {
		t.Fatalf("restarts = %q, want [cf]", env.fc.restarts)
	}
	// It is an operator acting on the live router, so it belongs on the timeline.
	events, err := env.store.ListEvents(20, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == store.EventRPKIRestart && strings.Contains(e.Message, "cf") {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s event for the restart: %+v", store.EventRPKIRestart, events)
	}
}

// A viewer never issues write commands to BIRD, and "restart" is one.
func TestRPKIRestartRefusedReadOnly(t *testing.T) {
	env := newTestEnv(t, true)
	withLiveFeed(t, env)
	env.do(t, "POST", "/rpki/cf/restart", url.Values{})
	if len(env.fc.restarts) != 0 {
		t.Fatalf("read-only birdy restarted %q", env.fc.restarts)
	}
	if body := env.do(t, "GET", "/rpki", nil).Body.String(); strings.Contains(body, `action="/rpki/cf/restart"`) {
		t.Error("read-only birdy should not offer a restart")
	}
}

// Only feeds birdy manages can be restarted from here: the name in the URL must
// be an RTR server in the model.
func TestRPKIRestartUnknownServer(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, "POST", "/rpki/edge_v4/restart", url.Values{})
	if rec.Code != http.StatusNotFound {
		t.Errorf("restarting a non-RTR protocol: %d, want 404", rec.Code)
	}
	if len(env.fc.restarts) != 0 {
		t.Errorf("restarted %q", env.fc.restarts)
	}
}

func TestRPKIFormSavesMaxVersion(t *testing.T) {
	env := newTestEnv(t, false)
	form := url.Values{
		"name": {"cf"}, "host": {"rtr.rpki.cloudflare.com"}, "port": {"8282"},
		"refresh": {"900"}, "retry": {"90"}, "expire": {"172800"}, "enabled": {"on"}, "maxVersion": {"1"},
	}
	if rec := env.do(t, "POST", "/rpki/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	srv, err := env.store.GetRPKIServerByName("cf")
	if err != nil {
		t.Fatal(err)
	}
	if srv.MaxVersion != 1 {
		t.Errorf("MaxVersion = %d, want 1", srv.MaxVersion)
	}
	body := env.do(t, "GET", "/rpki/cf/edit", nil).Body.String()
	if !strings.Contains(body, `<option value="1" selected>`) {
		t.Error("the edit form should show the pinned version selected")
	}
}

func TestKernelNetlinkRxBufferSavedFromForm(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{RouterLabel: "rtr1", RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	identity := func(mb string) url.Values {
		return url.Values{"routerLabel": {"rtr1"}, "routerId": {"192.0.2.1"}, "localAsn": {"64500"}, "kernelNetlinkRxBufferMb": {mb}}
	}

	env.do(t, "POST", "/settings/identity", identity("32"))
	if got, _, _ := env.store.GetSettings(); got.KernelNetlinkRxBufferMB != 32 {
		t.Fatalf("KernelNetlinkRxBufferMB = %d, want 32", got.KernelNetlinkRxBufferMB)
	}

	rec := env.do(t, "POST", "/settings/identity", identity("100000"))
	if !strings.Contains(rec.Body.String(), "MiB between 1 and") {
		t.Error("an absurd buffer size should be refused on the form")
	}
	if got, _, _ := env.store.GetSettings(); got.KernelNetlinkRxBufferMB != 32 {
		t.Errorf("a refused value was saved: %d", got.KernelNetlinkRxBufferMB)
	}
}

// The feed numbers are what a monitoring system compares across routers.
func TestMetricsRPKIFeeds(t *testing.T) {
	env := newTestEnvMetrics(t)
	if err := env.store.SaveSettings(store.Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SaveAccessWhitelist("192.0.2.0/24\n127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	env.srv.reloadAccess()
	withLiveFeed(t, env)

	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`birdy_rpki_feed_established{name="cf"} 1`,
		`birdy_rpki_roas{name="cf",afi="ipv4"} 795569`,
		`birdy_rpki_roas{name="cf",afi="ipv6"} 237784`,
		`birdy_rpki_serial{name="cf"} 1`,
		`# TYPE birdy_rpki_serial_unchanged_seconds gauge`,
		`birdy_rpki_serial_unchanged_seconds{name="cf"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
}
