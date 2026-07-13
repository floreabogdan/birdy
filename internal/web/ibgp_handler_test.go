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

func TestPeerTrafficEngineeringPersists(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	form := peerForm()
	form.Set("prependCount", "3")
	form.Set("exportCommunities", "65000:666\n65551:1:2")
	form.Set("drained", "on")
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	p, err := env.store.GetPeerByName("transit_v4")
	if err != nil {
		t.Fatal(err)
	}
	if p.PrependCount != 3 || !p.Drained || p.ExportCommunities == "" {
		t.Fatalf("TE fields not persisted: %+v", p)
	}

	// A bad community is a form error, and nothing is saved.
	bad := peerForm()
	bad.Set("name", "bad_comm")
	bad.Set("exportCommunities", "70000:1")
	rec := env.do(t, "POST", "/peers/new", bad)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "standard community") {
		t.Errorf("a bad community should be rejected on the form, got %d", rec.Code)
	}
}

func TestAlertDestinationCRUD(t *testing.T) {
	env := newTestEnv(t, false)

	// Create a Slack destination.
	form := url.Values{"name": {"noc-slack"}, "type": {"slack"}, "enabled": {"on"},
		"url": {"https://hooks.slack.com/services/x"}}
	if rec := env.do(t, "POST", "/alerts/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	dests, _ := env.store.ListAlertDestinations()
	if len(dests) != 1 || dests[0].Type != "slack" {
		t.Fatalf("destination not saved: %+v", dests)
	}

	// It shows on the Settings → Alerts tab.
	body := env.do(t, "GET", "/settings?tab=alerts", nil).Body.String()
	if !strings.Contains(body, "noc-slack") || !strings.Contains(body, "Slack") {
		t.Error("the destination should appear on the Settings alerts tab")
	}

	// A Slack destination with a non-http URL is refused.
	bad := url.Values{"name": {"bad"}, "type": {"discord"}, "url": {"ftp://nope"}}
	if rec := env.do(t, "POST", "/alerts/new", bad); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "webhook URL") {
		t.Errorf("a bad URL should be refused, got %d", rec.Code)
	}

	// Delete it.
	env.do(t, "POST", "/alerts/"+itoa(dests[0].ID)+"/delete", nil)
	if d, _ := env.store.ListAlertDestinations(); len(d) != 0 {
		t.Error("destination was not deleted")
	}
}

func TestAlertEmailValidation(t *testing.T) {
	env := newTestEnv(t, false)

	// Email needs host, from and to.
	form := url.Values{"name": {"mail"}, "type": {"email"}, "enabled": {"on"},
		"smtpPort": {"587"}, "smtpSecurity": {"starttls"}, "smtpFrom": {"nope"}}
	rec := env.do(t, "POST", "/alerts/new", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("want the form back with errors, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "SMTP server host") {
		t.Error("a missing host should be reported")
	}

	// A valid email destination saves.
	good := url.Values{"name": {"mail"}, "type": {"email"}, "enabled": {"on"},
		"smtpHost": {"smtp.example.com"}, "smtpPort": {"587"}, "smtpSecurity": {"starttls"},
		"smtpFrom": {"birdy@example.com"}, "smtpTo": {"noc@example.com, oncall@example.com"}}
	if rec := env.do(t, "POST", "/alerts/new", good); rec.Code != http.StatusSeeOther {
		t.Fatalf("valid email save: %d %s", rec.Code, rec.Body.String())
	}
	d, _ := env.store.ListAlertDestinations()
	if len(d) != 1 || d[0].SMTPHost != "smtp.example.com" {
		t.Fatalf("email destination not saved: %+v", d)
	}
}

// The SMTP password is never rendered back to the browser, like a BGP password.
func TestAlertEmailPasswordNotLeaked(t *testing.T) {
	env := newTestEnv(t, false)
	id, err := env.store.CreateAlertDestination(store.Destination{
		Name: "mail", Type: store.AlertEmail, Enabled: true, SMTPHost: "smtp.example.com",
		SMTPPort: 587, SMTPSecurity: store.SMTPStartTLS, SMTPFrom: "a@b.com", SMTPTo: "c@d.com",
		SMTPUsername: "user", SMTPPassword: "s3cr3t",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := env.do(t, "GET", "/alerts/"+itoa(id)+"/edit", nil).Body.String()
	if strings.Contains(body, "s3cr3t") {
		t.Error("the SMTP password must never be rendered to the browser")
	}
	if !strings.Contains(body, "unchanged") {
		t.Error("a stored password should show as 'unchanged'")
	}
}

func TestBFDAndBlackholePersist(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	form := peerForm()
	form.Set("bfd", "on")
	env.do(t, "POST", "/peers/new", form)
	if p, _ := env.store.GetPeerByName("transit_v4"); !p.BFD {
		t.Error("BFD should persist on the peer")
	}

	pol := url.Values{"name": {"CUST_IN"}, "direction": {"import"}, "defaultRoute": {"reject"},
		"bogonAsns": {"off"}, "rov": {"off"}, "acceptBlackhole": {"on"}}
	if rec := env.do(t, "POST", "/policies/new", pol); rec.Code != http.StatusSeeOther {
		t.Fatalf("policy save: %d %s", rec.Code, rec.Body.String())
	}
	if p, _ := env.store.GetPolicyByName("CUST_IN"); !p.AcceptBlackhole {
		t.Error("accept-blackhole should persist on the policy")
	}
}
