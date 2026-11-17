package federation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestFetchDashboardUsesBearerAndBoundsResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"statusOK":true}`))
	}))
	defer srv.Close()
	body, err := (Client{BaseURL: srv.URL, Token: "secret", HTTP: loopbackClient}).FetchDashboard(context.Background())
	if err != nil || !strings.Contains(string(body), "statusOK") {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

func TestFetchDashboardDoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := (Client{BaseURL: srv.URL, Token: "secret", HTTP: loopbackClient}).FetchDashboard(context.Background()); err == nil {
		t.Fatal("expected redirect status error")
	}
}

func TestFetchEventsUsesReadOnlyEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" || r.URL.Query().Get("limit") != "8" {
			t.Fatalf("request = %s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"events":[{"id":7,"ts":"2026-07-18T10:00:00Z","kind":"session_up","message":"peer up"}]}`))
	}))
	defer srv.Close()
	events, err := (Client{BaseURL: srv.URL, Token: "secret", HTTP: loopbackClient}).FetchEvents(context.Background(), 8)
	if err != nil || len(events) != 1 || events[0].Message != "peer up" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

// BlockedAddr is the one predicate both the save-time URL check and the
// dial-time check use, so the two cannot drift.
func TestBlockedAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1":              true,
		"127.8.9.10":             true,
		"::1":                    true,
		"169.254.169.254":        true, // cloud metadata
		"fe80::1":                true,
		"fe80::1%eth0":           true,
		"0.0.0.0":                true,
		"::":                     true,
		"224.0.0.1":              true,
		"ff02::1":                true,
		"::ffff:127.0.0.1":       true, // IPv4-mapped forms of the above
		"::ffff:169.254.169.254": true,
		"::ffff:0.0.0.0":         true,
		"10.0.0.5":               false, // private management networks stay reachable
		"172.16.3.4":             false,
		"192.168.1.2":            false,
		"fd00::1":                false,
		"::ffff:10.0.0.5":        false,
		"192.0.2.10":             false,
		"2001:db8::1":            false,
	} {
		if got := BlockedAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("BlockedAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

// The save-time check only sees the URL the operator typed. The production
// client refuses the address it is actually about to connect to, whatever a
// name resolved to at that moment — here the httptest server on loopback
// stands in for a name that was re-pointed after it was saved.
func TestDefaultClientRefusesBlockedAddressAtDialTime(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]

	// By literal, and by a name that resolves to loopback without any DNS query.
	for _, base := range []string{srv.URL, "http://localhost" + port} {
		c := Client{BaseURL: base, Token: "secret"}
		if _, err := c.FetchDashboard(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot be a remote Birdy target") {
			t.Errorf("FetchDashboard(%s) err = %v, want the dial-time refusal", base, err)
		}
		if _, err := c.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "cannot be a remote Birdy target") {
			t.Errorf("Check(%s) err = %v, want the dial-time refusal", base, err)
		}
		if _, err := c.FetchEvents(context.Background(), 8); err == nil || !strings.Contains(err.Error(), "cannot be a remote Birdy target") {
			t.Errorf("FetchEvents(%s) err = %v, want the dial-time refusal", base, err)
		}
	}
	if reached {
		t.Fatal("the default client connected to a loopback address")
	}
}

// The dial check must not get in the way of an address that is allowed: the
// same guarded client, told (as only a test can) that loopback is fine, still
// completes the request.
func TestGuardedClientReachesAnAllowedAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"statusOK":true}`))
	}))
	defer srv.Close()
	seen := 0
	guarded := newHTTPClient(func(addr netip.Addr) bool {
		seen++
		return !addr.IsLoopback() && BlockedAddr(addr)
	})
	if _, err := (Client{BaseURL: srv.URL, Token: "secret", HTTP: guarded}).FetchDashboard(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("the dial check never ran")
	}
}

// loopbackClient is the production client with the one difference a test
// needs: httptest servers listen on 127.0.0.1, which the dial check refuses.
// Everything else about it (timeouts, no redirects, the check itself for every
// other blocked range) is what production runs.
var loopbackClient = newHTTPClient(func(addr netip.Addr) bool {
	return !addr.Unmap().IsLoopback() && BlockedAddr(addr)
})
