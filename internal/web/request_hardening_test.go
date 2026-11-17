package web

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// postBody sends a form-encoded POST of exactly body. chunked hides the length
// from the server (ContentLength -1), the way a client that streams its body
// does, so only the reader's own limit can stop it.
func postBody(env *testEnv, path, body string, auth, chunked bool) *httptest.ResponseRecorder {
	var rd io.Reader = strings.NewReader(body)
	if chunked {
		rd = io.MultiReader(rd) // not a type NewRequest knows the length of
	}
	req := httptest.NewRequest(http.MethodPost, path, rd)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if auth {
		req.AddCookie(env.cookie)
	}
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	return rec
}

// The unauthenticated login form is two short fields. Before the cap, anyone
// who could reach the port could make birdy buffer net/http's default 10 MB of
// form per request, ahead of the login limiter.
func TestLoginBodyIsCapped(t *testing.T) {
	env := newTestEnv(t, false)
	big := "username=admin&password=" + strings.Repeat("a", 64<<10)

	rec := postBody(env, "/login", big, false, false)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized login with a declared length: code=%d, want 413", rec.Code)
	}
	// With no declared length the reader stops it; the form then fails to parse
	// and the login is refused like any other bad form — never a 500, never a
	// session.
	rec = postBody(env, "/login", big, false, true)
	if rec.Code >= 500 || findCookie(rec.Result().Cookies(), sessionCookieName) != nil {
		t.Errorf("oversized chunked login: code=%d cookies=%v", rec.Code, rec.Result().Cookies())
	}
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?error=1" {
		t.Errorf("oversized chunked login: code=%d location=%q, want the form error", rec.Code, rec.Header().Get("Location"))
	}

	// A real login is nowhere near the cap.
	form := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}
	if rec := postBody(env, "/login", form.Encode(), false, false); rec.Header().Get("Location") != "/" {
		t.Errorf("a normal login must still work: code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

// Every other form is capped too, well above the largest one birdy has.
func TestPostBodiesAreCapped(t *testing.T) {
	env := newTestEnv(t, false)
	big := "rawConfig=" + strings.Repeat("a", maxFormBody)

	if rec := postBody(env, "/settings/raw", big, true, false); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized form with a declared length: code=%d, want 413", rec.Code)
	}
	if rec := postBody(env, "/settings/raw", big, true, true); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized chunked form: code=%d, want 400", rec.Code)
	}
	if st, _, err := env.store.GetSettings(); err != nil || st.RawConfig != "" {
		t.Errorf("an oversized form must not be saved: %d bytes, err %v", len(st.RawConfig), err)
	}
}

// The largest legitimate forms must still go through after the cap: a raw
// config block at its own 64 KiB limit with every byte percent-encoded (three
// bytes on the wire for one), and a prefix set far larger than anyone types —
// the size an IRR expansion of a big customer cone produces, which the edit
// form posts back whole.
func TestLargestLegitimateFormsFit(t *testing.T) {
	env := newTestEnv(t, false)

	raw := strings.Repeat("#", maxRawConfig-1) + "\n" // '#' and '\n' are both escaped
	body := url.Values{"rawConfig": {raw}}.Encode()
	if len(body) < 3*maxRawConfig {
		t.Fatalf("test body is %d bytes, expected the worst-case encoding", len(body))
	}
	if rec := postBody(env, "/settings/raw", body, true, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("a raw config at its limit: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if st, _, err := env.store.GetSettings(); err != nil || st.RawConfig != raw {
		t.Fatalf("raw config not saved intact: %d bytes, err %v", len(st.RawConfig), err)
	}

	var entries strings.Builder
	const n = 60000
	for i := 0; i < n; i++ {
		entries.WriteString(netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, byte(i >> 8), byte(i)}), 48).String())
		entries.WriteString("{48,48}\r\n")
	}
	body = url.Values{"name": {"BIG_CONE"}, "family": {store.FamilyV6}, "entries": {entries.String()}}.Encode()
	if len(body) <= maxFormBody {
		t.Fatalf("test body is %d bytes, expected one above the general cap", len(body))
	}
	if rec := postBody(env, "/library/prefix-sets/new", body, true, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("a large prefix set: code=%d", rec.Code)
	}
	ps, err := env.store.GetPrefixSetByName("BIG_CONE")
	if err != nil || len(ps.Entries) != n {
		t.Fatalf("prefix set saved with %d entries, err %v", len(ps.Entries), err)
	}
	// The live preview posts the same form.
	if rec := postBody(env, "/library/prefix-sets/preview", body, true, false); rec.Code != http.StatusOK {
		t.Errorf("preview of a large prefix set: code=%d", rec.Code)
	}
	// ...but those routes are bounded as well.
	if rec := postBody(env, "/library/prefix-sets/new", "entries="+strings.Repeat("a", maxSetFormBody), true, false); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized prefix set form: code=%d, want 413", rec.Code)
	}
}

// Nothing birdy serves from behind the login may be kept by a browser or an
// intermediary: pages, the one-time token page, and the snapshot and backup
// downloads. Static assets keep their caching.
func TestResponsesAreNotStoredExceptStatic(t *testing.T) {
	env := newTestEnv(t, false)
	for _, path := range []string{"/", "/settings", "/peers", "/timeline", "/login", "/healthz",
		"/api/events", "/api/snapshot/download", "/api/backup/download", "/export/config"} {
		rec := env.do(t, http.MethodGet, path, nil)
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q (status %d), want no-store", path, got, rec.Code)
		}
	}
	// The page that shows a freshly generated token exactly once.
	rec := env.do(t, http.MethodPost, "/settings/instance-token", url.Values{"expiry": {"30"}})
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("token page: status %d Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	// Redirects to the login page and refusals as well.
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	anon := httptest.NewRecorder()
	env.srv.ServeHTTP(anon, req)
	if anon.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("redirect to login: Cache-Control %q", anon.Header().Get("Cache-Control"))
	}

	static := env.do(t, http.MethodGet, "/static/style.css", nil)
	if static.Code != http.StatusOK {
		t.Fatalf("static asset: status %d", static.Code)
	}
	if got := static.Header().Get("Cache-Control"); got != "" {
		t.Errorf("static assets must keep their caching, got Cache-Control %q", got)
	}
}

// A page that re-points its own name at birdy (DNS rebinding) runs in the
// operator's browser. If it has got hold of an instance token, the bearer
// exemption from the Host check would let it read the dashboard and timeline
// through that browser. A browser cannot hide what it is: it sends Fetch
// Metadata on every request and script cannot remove it, while a federating
// birdy sends none.
func TestHostCheckBearerExemptionIsNotForBrowsers(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveInstanceAPITokenHash(store.HashInstanceAPIToken("remote-secret")); err != nil {
		t.Fatal(err)
	}
	get := func(path, host string, hdr map[string]string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = host
		req.Header.Set("Authorization", "Bearer remote-secret")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, path := range []string{"/api/dashboard", "/api/events"} {
		for _, hdr := range []map[string]string{
			{"Sec-Fetch-Site": "same-origin"},
			{"Sec-Fetch-Mode": "cors"},
			{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors"},
		} {
			if code := get(path, "rebind.attacker.example:8080", hdr); code != http.StatusForbidden {
				t.Errorf("browser token read of %s by an unlisted name (%v): code=%d, want 403", path, hdr, code)
			}
			// By IP or a listed name the Host check has nothing to object to.
			if code := get(path, "192.0.2.1:8080", hdr); code != http.StatusOK {
				t.Errorf("browser token read of %s by IP (%v): code=%d, want 200", path, hdr, code)
			}
		}
		// A federating birdy, by any name: unchanged.
		if code := get(path, "router1.example.net:8080", nil); code != http.StatusOK {
			t.Errorf("machine token read of %s by name: code=%d, want 200", path, code)
		}
	}
}

// reloadAccess used to install an empty allow-list — which means allow all —
// whenever the settings could not be read or the stored list did not parse.
func TestReloadAccessKeepsTheListWhenSettingsCannotBeRead(t *testing.T) {
	var logs bytes.Buffer
	env := newTestEnv(t, false, func(c *Config) { c.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	if err := env.store.SaveAccessWhitelist("192.0.2.0/24"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SaveAllowedHosts("example.com\nrouter1.example.net"); err != nil {
		t.Fatal(err)
	}
	env.srv.reloadAccess()
	outsider := func() int { return fromIP(t, env, "GET", "/healthz", "198.51.100.7:4000", false).Code }
	if outsider() != http.StatusForbidden {
		t.Fatal("setup: an address outside the list should be blocked")
	}

	t.Run("read error", func(t *testing.T) {
		sabotageDB(t, env, `ALTER TABLE settings RENAME TO settings_gone`)
		defer sabotageDB(t, env, `ALTER TABLE settings_gone RENAME TO settings`)
		logs.Reset()
		env.srv.reloadAccess()
		if outsider() != http.StatusForbidden {
			t.Error("a settings read error opened birdy to every address")
		}
		if !strings.Contains(logs.String(), "level=ERROR") {
			t.Errorf("the failure should be logged as an error: %s", logs.String())
		}
	})
	t.Run("stored list does not parse", func(t *testing.T) {
		if err := env.store.SaveAccessWhitelist("not-an-address"); err != nil {
			t.Fatal(err)
		}
		if err := env.store.SaveAllowedHosts("bad host name!"); err != nil {
			t.Fatal(err)
		}
		logs.Reset()
		env.srv.reloadAccess()
		if outsider() != http.StatusForbidden {
			t.Error("an unparseable stored list opened birdy to every address")
		}
		if fromIP(t, env, "GET", "/healthz", "192.0.2.9:4000", false).Code != http.StatusOK {
			t.Error("the previously loaded list should still be in force")
		}
		env.srv.accessMu.RLock()
		kept := env.srv.allowedHosts["router1.example.net"]
		env.srv.accessMu.RUnlock()
		if !kept {
			t.Error("the previously loaded host names should still be in force")
		}
		if !strings.Contains(logs.String(), "level=ERROR") {
			t.Errorf("the failure should be logged as an error: %s", logs.String())
		}
	})
}

// At startup there is no earlier list to keep. birdy then answers loopback
// only — the SSH tunnel every refusal message points to — rather than everyone.
func TestStartupWithUnreadableAccessListIsLoopbackOnly(t *testing.T) {
	for name, breakIt := range map[string]func(*testing.T, *testEnv){
		"read error": func(t *testing.T, env *testEnv) {
			sabotageDB(t, env, `ALTER TABLE settings RENAME TO settings_gone`)
		},
		"stored list does not parse": func(t *testing.T, env *testEnv) {
			if err := env.store.SaveAccessWhitelist("not-an-address"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t, false)
			breakIt(t, env)
			var logs bytes.Buffer
			srv := New(Config{Store: env.store, Client: env.fc, Poller: env.srv.poller, Metrics: true,
				Log: slog.New(slog.NewTextHandler(&logs, nil))})

			for addr, want := range map[string]int{
				"198.51.100.7:4000": http.StatusForbidden,
				"10.1.2.3:4000":     http.StatusForbidden,
				"[2001:db8::1]:400": http.StatusForbidden,
				"127.0.0.1:4000":    http.StatusOK,
				"[::1]:4000":        http.StatusOK,
			} {
				req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
				req.Host = "127.0.0.1"
				req.RemoteAddr = addr
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, req)
				if rec.Code != want {
					t.Errorf("client %s: code=%d, want %d", addr, rec.Code, want)
				}
			}
			if srv.WideOpen() {
				t.Error("a failed load must not count as an allow-all list")
			}
			if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "loopback") {
				t.Errorf("the startup failure should be logged loudly: %s", logs.String())
			}
		})
	}
}

// A fresh database with no settings row yet (before `birdy init`) is not an
// error: there is no list, and that still means no restriction.
func TestStartupWithoutSettingsRowStillAllowsAll(t *testing.T) {
	env := newTestEnv(t, false)
	sabotageDB(t, env, `DELETE FROM settings`)
	srv := New(Config{Store: env.store, Client: env.fc, Poller: env.srv.poller})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1"
	req.RemoteAddr = "198.51.100.7:4000"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("no settings row: code=%d, want 200", rec.Code)
	}
}

// A link-local IPv6 client arrives with a zone ("fe80::1%eth0"), and
// netip.Prefix.Contains is false for any zoned address, so fe80::/10 in the
// allow-list never matched the clients it was written for.
func TestAllowListMatchesLinkLocalClients(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveAccessWhitelist("fe80::/10"); err != nil {
		t.Fatal(err)
	}
	env.srv.reloadAccess()
	for addr, want := range map[string]int{
		"[fe80::1%eth0]:4000":    http.StatusOK,
		"[fe80::1]:4000":         http.StatusOK,
		"[2001:db8::1%eth0]:400": http.StatusForbidden,
		"11.0.0.1:4000":          http.StatusForbidden,
	} {
		if rec := fromIP(t, env, "GET", "/healthz", addr, false); rec.Code != want {
			t.Errorf("client %s: code=%d, want %d", addr, rec.Code, want)
		}
	}
}

// bcrypt refuses a password over 72 bytes, which reached the operator as a
// bare "internal error". It is a form error like any other.
func TestPasswordChangeRejectsOver72Bytes(t *testing.T) {
	env := newTestEnv(t, false)
	change := func(next string) *httptest.ResponseRecorder {
		return env.do(t, http.MethodPost, "/profile/password", url.Values{
			"currentPassword": {"correct horse battery staple"}, "newPassword": {next}, "confirmPassword": {next},
		})
	}
	rec := change(strings.Repeat("a", 73))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "at most 72 bytes") {
		t.Fatalf("73-byte password: code=%d, want the form re-rendered with the limit", rec.Code)
	}
	// Bytes, not characters: 25 three-byte runes are 75 bytes.
	if rec := change(strings.Repeat("€", 25)); !strings.Contains(rec.Body.String(), "at most 72 bytes") {
		t.Fatalf("75-byte multibyte password: code=%d, want the form error", rec.Code)
	}
	if rec := change(strings.Repeat("a", 72)); rec.Code != http.StatusSeeOther {
		t.Fatalf("72-byte password must be accepted: code=%d", rec.Code)
	}
}
