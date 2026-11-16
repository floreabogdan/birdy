package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// hostRequest builds a request addressed to host, from remoteAddr, optionally
// carrying the session cookie.
func hostRequest(env *testEnv, method, path, host, remoteAddr string, form url.Values, auth bool) *httptest.ResponseRecorder {
	var req *http.Request
	if form == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Host = host
	req.RemoteAddr = remoteAddr
	if auth {
		req.AddCookie(env.cookie)
	}
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	return rec
}

// DNS rebinding: a hostile page re-points its own name at birdy, and the
// operator's browser then talks to birdy from a trusted address (here the SSH
// tunnel's loopback) with the attacker's name as Host and Origin. Both match each
// other, so the same-origin check passes; only the Host itself gives it away.
func TestDNSRebindingLoginRefused(t *testing.T) {
	env := newTestEnv(t, false)
	form := url.Values{"username": {"admin"}, "password": {goodPassword}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "rebind.attacker.example:8080"
	req.Header.Set("Origin", "http://rebind.attacker.example:8080")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("a login addressed to an unknown name: code=%d, want 403", rec.Code)
	}
	if c := findCookie(rec.Result().Cookies(), sessionCookieName); c != nil {
		t.Fatal("a refused request must not get a session")
	}
	body := rec.Body.String()
	for _, want := range []string{"rebind.attacker.example", "IP address", "SSH tunnel", "Settings", "Access control"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("the refusal should be plain text, got %q", ct)
	}
}

func TestHostCheckCoversSessionPages(t *testing.T) {
	env := newTestEnv(t, false)
	for _, path := range []string{"/", "/settings", "/api/dashboard", "/static/style.css", "/healthz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "rebind.attacker.example"
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.AddCookie(env.cookie)
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s addressed to an unknown name: code=%d, want 403", path, rec.Code)
		}
	}
}

// An IP literal cannot be rebound — the browser connects to exactly that address
// — and neither can localhost, so both always work, with or without a port.
func TestHostCheckAcceptsIPLiteralsAndLocalhost(t *testing.T) {
	env := newTestEnv(t, false)
	for _, host := range []string{
		"192.0.2.10", "192.0.2.10:8080", "[2001:db8::1]", "[2001:db8::1]:8080",
		"127.0.0.1:8080", "[::1]:8080", "localhost", "LOCALHOST:8080", "localhost.:8080",
	} {
		if rec := hostRequest(env, http.MethodGet, "/", host, "192.0.2.1:1234", nil, true); rec.Code != http.StatusOK {
			t.Errorf("Host %q: code=%d, want 200", host, rec.Code)
		}
	}
	for _, host := range []string{"router1.example.net", "192.0.2.10.attacker.example", "localhost.attacker.example", "evil.localhost"} {
		if rec := hostRequest(env, http.MethodGet, "/", host, "192.0.2.1:1234", nil, true); rec.Code != http.StatusForbidden {
			t.Errorf("Host %q: code=%d, want 403", host, rec.Code)
		}
	}
}

// The upgrade path: an operator who browsed by name is refused, reaches birdy
// by IP, adds the name under Settings → Access control, and the name works.
func TestHostCheckAllowedNamesFromSettings(t *testing.T) {
	env := applyReady(t)
	if rec := hostRequest(env, http.MethodGet, "/", "router1.example.net:8080", "192.0.2.1:1234", nil, true); rec.Code != http.StatusForbidden {
		t.Fatalf("an unlisted name: code=%d, want 403", rec.Code)
	}
	page := hostRequest(env, http.MethodGet, "/settings?tab=access", "192.0.2.1:8080", "192.0.2.1:1234", nil, true)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `name="allowedHosts"`) {
		t.Fatalf("the access settings, reached by IP, should offer the host-name list: code=%d", page.Code)
	}
	save := hostRequest(env, http.MethodPost, "/settings/hosts", "192.0.2.1:8080", "192.0.2.1:1234",
		url.Values{"allowedHosts": {"example.com\nRouter1.Example.NET\n# the NOC's name for it\nbirdy.noc.example.net"}}, true)
	if save.Code != http.StatusSeeOther {
		t.Fatalf("save: code=%d body=%s", save.Code, save.Body.String())
	}
	for _, host := range []string{"router1.example.net:8080", "ROUTER1.example.net.", "birdy.noc.example.net"} {
		if rec := hostRequest(env, http.MethodGet, "/", host, "192.0.2.1:1234", nil, true); rec.Code != http.StatusOK {
			t.Errorf("listed name %q: code=%d, want 200", host, rec.Code)
		}
	}
	if rec := hostRequest(env, http.MethodGet, "/", "other.example.net", "192.0.2.1:1234", nil, true); rec.Code != http.StatusForbidden {
		t.Errorf("an unlisted name after saving: code=%d, want 403", rec.Code)
	}
}

func TestAllowedHostsRejectsWhatIsNotAName(t *testing.T) {
	env := applyReady(t)
	for _, entry := range []string{"http://router1.example.net", "router1.example.net:8080", "*.example.net", "192.0.2.7", "bad name", "router1..example.net"} {
		rec := env.do(t, http.MethodPost, "/settings/hosts", url.Values{"allowedHosts": {"example.com\n" + entry}})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Line 2") {
			t.Errorf("%q should be refused with a line number: code=%d", entry, rec.Code)
		}
	}
	settings, _, err := env.store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.AllowedHosts != "example.com" {
		t.Errorf("a rejected list must not be saved, stored %q", settings.AllowedHosts)
	}
}

// Other birdy instances read the dashboard API with a bearer token, often by
// host name. Rebinding cannot supply that token, so those calls skip the check —
// but a Bearer header on any other route earns nothing.
func TestHostCheckLetsTokenReadsThrough(t *testing.T) {
	env := applyReady(t)
	if err := env.store.SaveInstanceAPITokenHash(store.HashInstanceAPIToken("remote-secret")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/dashboard", "/api/events"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "router1.example.net:8080"
		req.Header.Set("Authorization", "Bearer remote-secret")
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("token read of %s by name: code=%d, want 200", path, rec.Code)
		}
	}
	// A bad token by name is still a 401, never a fall-back to the cookie.
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	req.Host = "rebind.attacker.example"
	req.Header.Set("Authorization", "Bearer guess")
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong token by name: code=%d, want 401", rec.Code)
	}

	form := url.Values{"username": {"admin"}, "password": {goodPassword}}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login.Header.Set("Authorization", "Bearer anything")
	login.Host = "rebind.attacker.example"
	lrec := httptest.NewRecorder()
	env.srv.ServeHTTP(lrec, login)
	if lrec.Code != http.StatusForbidden {
		t.Errorf("a Bearer header must not exempt the login form: code=%d, want 403", lrec.Code)
	}
}

// Prometheus scrapes by name and sends no Fetch Metadata; a browser always sends
// it, and page script cannot strip it. So /metrics keeps working for scrapers by
// any name while a rebound page cannot read it.
func TestHostCheckMetricsScrapeByName(t *testing.T) {
	env := newTestEnvMetrics(t)
	if err := env.store.SaveAccessWhitelist("192.0.2.0/24"); err != nil {
		t.Fatal(err)
	}
	env.srv.reloadAccess()

	scrape := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	scrape.Host = "router1.example.net:8080"
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, scrape)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "birdy_up 1") {
		t.Fatalf("a scrape by name: code=%d", rec.Code)
	}

	browser := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	browser.Host = "rebind.attacker.example:8080"
	browser.Header.Set("Sec-Fetch-Site", "same-origin")
	browser.Header.Set("Sec-Fetch-Mode", "cors")
	brec := httptest.NewRecorder()
	env.srv.ServeHTTP(brec, browser)
	if brec.Code != http.StatusForbidden || strings.Contains(brec.Body.String(), "birdy_up") {
		t.Fatalf("a browser fetch of /metrics by an unknown name: code=%d", brec.Code)
	}
}
