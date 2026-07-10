package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPeerIdentFromName(t *testing.T) {
	cases := map[string]string{
		"Example Networks, Inc.": "example_networks_inc",
		"  AS-FOO / bar ":        "as_foo_bar",
		"":                       "as64500",
		"123 Numeric Lead":       "123_numeric_lead", // starts with digit after clean? -> falls back
	}
	for in, want := range cases {
		got := peerIdentFromName(in, 64500)
		if in == "123 Numeric Lead" {
			// leading digit -> fallback
			if got != "as64500" {
				t.Errorf("%q -> %q, want as64500", in, got)
			}
			continue
		}
		if got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestPeeringDBEndpointGated(t *testing.T) {
	// Default env has PeeringDB off.
	env := newTestEnv(t, false)
	req := httptest.NewRequest("GET", "/api/peeringdb/64500", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("peeringdb endpoint should be absent when disabled, got %d", rec.Code)
	}

	// The button is hidden on the peer form when disabled.
	body := env.do(t, "GET", "/peers/new", nil).Body.String()
	if strings.Contains(body, "pdb-lookup") {
		t.Error("the PeeringDB button should be hidden when disabled")
	}

	// Enabled: the button appears (the endpoint would dial out, not tested here).
	on := newTestEnv(t, false, func(c *Config) { c.PeeringDB = true })
	body = on.do(t, "GET", "/peers/new", nil).Body.String()
	if !strings.Contains(body, "pdb-lookup") {
		t.Error("the PeeringDB button should show when enabled")
	}
}
