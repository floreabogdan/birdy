package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestIRRPrefixSetSourcePersists(t *testing.T) {
	env := newTestEnv(t, false)
	form := url.Values{"name": {"CUST_V4"}, "family": {"ipv4"}, "entries": {"192.0.2.0/24"}, "source": {"AS-CUSTOMER"}}
	if rec := env.do(t, "POST", "/library/prefix-sets/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	ps, err := env.store.GetPrefixSetByName("CUST_V4")
	if err != nil || ps.Source != "AS-CUSTOMER" {
		t.Fatalf("source not persisted: %v %+v", err, ps)
	}
}

func TestIRREndpointGated(t *testing.T) {
	// Disabled by default: endpoint absent, button hidden.
	env := newTestEnv(t, false)
	req := httptest.NewRequest("GET", "/api/irr/prefixes?source=AS-X&family=ipv4", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("IRR endpoint should be absent when disabled, got %d", rec.Code)
	}
	if strings.Contains(env.do(t, "GET", "/library/prefix-sets/new", nil).Body.String(), "irr-refresh") {
		t.Error("the IRR button should be hidden when disabled")
	}

	// Enabled but bgpq4 missing: a clear install message, and the button shows.
	on := newTestEnv(t, false, func(c *Config) { c.Bgpq4Bin = "definitely-not-bgpq4" })
	body := on.do(t, "GET", "/library/prefix-sets/new", nil).Body.String()
	if !strings.Contains(body, "irr-refresh") {
		t.Error("the IRR button should show when enabled")
	}
	req = httptest.NewRequest("GET", "/api/irr/prefixes?source=AS-X&family=ipv4", nil)
	req.AddCookie(on.cookie)
	rec = httptest.NewRecorder()
	on.srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "not installed") {
		t.Errorf("a missing bgpq4 should say so: %s", rec.Body.String())
	}
}
