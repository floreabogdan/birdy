package web

import (
	"database/sql"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func withIdentity(t *testing.T, env *testEnv) {
	t.Helper()
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: sql.NullInt64{Int64: 65551, Valid: true},
		BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "127.0.0.1:8080",
	}); err != nil {
		t.Fatal(err)
	}
}

func ibgpForm() url.Values {
	return url.Values{
		"name": {"ibgp_core"}, "role": {"ibgp"}, "enabled": {"on"},
		"neighborIp": {"192.0.2.9"}, "remoteAsn": {"65551"},
		"importLimit": {"0"}, "importLimitAction": {"restart"},
		"nextHopSelf": {"on"},
	}
}

func TestIBGPPeerStoresNextHopSelfAndReflection(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	form := ibgpForm()
	form.Set("rrClient", "on")
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create failed: %d %s", rec.Code, rec.Body.String())
	}

	p, err := env.store.GetPeerByName("ibgp_core")
	if err != nil {
		t.Fatal(err)
	}
	if !p.NextHopSelf || !p.RRClient {
		t.Fatalf("iBGP options not persisted: %+v", p)
	}

	body := env.do(t, "GET", "/changes", nil).Body.String()
	if !strings.Contains(body, "next hop self;") {
		t.Error("the candidate config should rewrite the next hop on iBGP")
	}
	if !strings.Contains(body, "rr client;") {
		t.Error("the candidate config should mark this peer a reflector client")
	}
}

// The two options are iBGP concepts. An eBGP peer that somehow submits them
// must not carry them into the config.
func TestEBGPPeerDropsIBGPOptions(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	form := peerForm()
	form.Set("nextHopSelf", "on")
	form.Set("rrClient", "on")
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create failed: %d", rec.Code)
	}

	p, err := env.store.GetPeerByName("transit_v4")
	if err != nil {
		t.Fatal(err)
	}
	if p.NextHopSelf || p.RRClient {
		t.Errorf("eBGP peer kept iBGP options: %+v", p)
	}
}

// An iBGP peer whose remote AS is not our own is an eBGP session mislabelled.
func TestLintSurfacesIBGPASNMismatchOnChanges(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	form := ibgpForm()
	form.Set("remoteAsn", "64496") // not ours
	env.do(t, "POST", "/peers/new", form)

	body := env.do(t, "GET", "/changes", nil).Body.String()
	if !strings.Contains(body, "marked iBGP but its remote AS") {
		t.Error("the changes page should warn about a role/ASN mismatch")
	}
}

func TestChangesRendersDirectProtocol(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	body := env.do(t, "GET", "/changes", nil).Body.String()
	if !strings.Contains(body, "protocol direct direct1") {
		t.Error("the candidate config must import connected routes")
	}
}

func TestRawConfigRoundTrips(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	// No bird binary in the test environment, so the parse check is skipped and
	// the block saves unverified — which is exactly what a dev box should do.
	raw := "protocol bfd {\n\tinterface \"eno1\";\n}"
	rec := env.do(t, "POST", "/settings/raw", url.Values{"rawConfig": {raw}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save failed: %d %s", rec.Code, rec.Body.String())
	}

	st, _, err := env.store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.RawConfig != raw {
		t.Errorf("raw config = %q", st.RawConfig)
	}

	body := env.do(t, "GET", "/changes", nil).Body.String()
	if !strings.Contains(body, "protocol bfd") {
		t.Error("the raw block should reach the candidate config")
	}
	if !strings.Contains(body, "raw block that birdy does not understand") {
		t.Error("lint should say the raw block is unchecked")
	}
}

func TestRawConfigRejectsOversizeAndNul(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	big := strings.Repeat("a", maxRawConfig+1)
	rec := env.do(t, "POST", "/settings/raw", url.Values{"rawConfig": {big}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Too long") {
		t.Errorf("oversize raw config should be refused, got %d", rec.Code)
	}

	rec = env.do(t, "POST", "/settings/raw", url.Values{"rawConfig": {"protocol \x00 bfd"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "NUL byte") {
		t.Errorf("a NUL byte should be refused, got %d", rec.Code)
	}

	st, _, _ := env.store.GetSettings()
	if st.RawConfig != "" {
		t.Error("nothing should have been saved")
	}
}

// A raw block holding a password must not be echoed back into the browser.
func TestRawConfigPasswordIsMaskedOnChanges(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	env.do(t, "POST", "/settings/raw", url.Values{
		"rawConfig": {"protocol bgp x {\n\tpassword \"hunter2\";\n}"},
	})
	body := env.do(t, "GET", "/changes", nil).Body.String()
	if strings.Contains(body, "hunter2") {
		t.Error("a password in the raw block leaked into the rendered page")
	}
}

func TestRRClusterIDValidation(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	good := url.Values{"routerId": {"192.0.2.1"}, "localAsn": {"65551"}, "rrClusterId": {"192.0.2.5"}}
	if rec := env.do(t, "POST", "/settings/identity", good); rec.Code != http.StatusSeeOther {
		t.Fatalf("valid cluster id refused: %d", rec.Code)
	}
	st, _, _ := env.store.GetSettings()
	if st.RRClusterID != "192.0.2.5" {
		t.Errorf("cluster id = %q", st.RRClusterID)
	}

	// A cluster ID is a 32-bit value, so BIRD only takes the IPv4 form.
	bad := url.Values{"routerId": {"192.0.2.1"}, "localAsn": {"65551"}, "rrClusterId": {"2001:db8::1"}}
	rec := env.do(t, "POST", "/settings/identity", bad)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Enter an IPv4 address") {
		t.Errorf("an IPv6 cluster id should be refused, got %d", rec.Code)
	}
}
