package web

import (
	"bytes"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// newestEvent is the latest row on the timeline, or a zero Event if empty.
func newestEvent(t *testing.T, env *testEnv) store.Event {
	t.Helper()
	events, err := env.store.ListEvents(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		return store.Event{}
	}
	return events[0]
}

// auditStep runs one operator action and checks that it left exactly the audit
// row described: attributed to the signed-in operator, of the given kind, and
// saying what was touched.
type auditStep struct {
	name string
	do   func(t *testing.T, env *testEnv) *httptest.ResponseRecorder
	kind string
	want []string // substrings of the message
}

func post(path string, form url.Values) func(*testing.T, *testEnv) *httptest.ResponseRecorder {
	return func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
		return env.do(t, http.MethodPost, path, form)
	}
}

func runAuditSteps(t *testing.T, env *testEnv, actor string, steps []auditStep) {
	t.Helper()
	for _, step := range steps {
		before := newestEvent(t, env).ID
		rec := step.do(t, env)
		if rec.Code >= 400 {
			t.Errorf("%s: request failed with %d: %s", step.name, rec.Code, rec.Body.String())
			continue
		}
		got := newestEvent(t, env)
		if got.ID == before {
			t.Errorf("%s: no audit event was recorded", step.name)
			continue
		}
		if got.Kind != step.kind || got.Actor != actor {
			t.Errorf("%s: event kind=%q actor=%q, want kind=%q actor=%q (message %q)", step.name, got.Kind, got.Actor, step.kind, actor, got.Message)
		}
		for _, want := range step.want {
			if !strings.Contains(got.Message, want) {
				t.Errorf("%s: message %q should mention %q", step.name, got.Message, want)
			}
		}
	}
}

// Every handler that changes what the router will be configured with leaves a
// row on the timeline saying who did it.
func TestConfigChangesAreAudited(t *testing.T) {
	// A stand-in bgpq4, so the operator-triggered AS-set refresh has something
	// to run.
	bgpq4 := filepath.Join(t.TempDir(), "bgpq4")
	if err := os.WriteFile(bgpq4, []byte("#!/bin/sh\necho '{\"data\": [64600, 64601]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t, false, func(c *Config) { c.Bgpq4Bin = bgpq4 })
	withIdentity(t, env)
	env.fc.details["edge_v4"] = seedDetail("edge_v4", "198.51.100.1", "64500", "65551", "external")

	staticID := func(t *testing.T, env *testEnv) string {
		routes, err := env.store.ListStaticRoutes()
		if err != nil || len(routes) != 1 {
			t.Fatalf("static routes: %+v, err %v", routes, err)
		}
		return fmt.Sprint(routes[0].ID)
	}
	static := url.Values{"prefix": {"198.51.100.0/26"}, "action": {"blackhole"}, "enabled": {"on"}}
	rpki := url.Values{"name": {"rtr1"}, "host": {"rtr1.example.net"}, "port": {"8282"}, "refresh": {"900"}, "expire": {"172800"}, "enabled": {"on"}}
	bmp := url.Values{"name": {"collector"}, "address": {"203.0.113.9"}, "port": {"1790"}, "enabled": {"on"}}

	runAuditSteps(t, env, "admin", []auditStep{
		{"raw config", post("/settings/raw", url.Values{"rawConfig": {"ipv4 table extra4;\n"}}),
			store.EventModelChange, []string{"raw config", "19 bytes"}},
		{"router identity", post("/settings/identity", url.Values{"routerId": {"192.0.2.2"}, "localAsn": {"64500"}, "kernelExportBgpV4": {"on"}}),
			store.EventModelChange, []string{"router settings", "router ID 192.0.2.1 → 192.0.2.2", "local ASN 65551 → 64500", "kernel export of BGP routes (IPv4)"}},
		{"router identity, nothing changed", post("/settings/identity", url.Values{"routerId": {"192.0.2.2"}, "localAsn": {"64500"}, "kernelExportBgpV4": {"on"}}),
			store.EventModelChange, []string{"router settings", "no changes"}},
		{"bogons", post("/settings/bogons", url.Values{"bogonsV4": {"10.0.0.0/8+\n"}, "bogonsV6": {"fc00::/7+\n"}, "bogonAsns": {"0\n"}}),
			store.EventModelChange, []string{"bogon lists", "1 IPv4", "1 IPv6", "1 ASN"}},
		{"bogons restored", post("/settings/bogons", url.Values{"restore": {"defaults"}}),
			store.EventModelChange, []string{"restored the default bogon lists"}},

		{"prefix set create", post("/library/prefix-sets/new", url.Values{"name": {"CUST_V4"}, "family": {"ipv4"}, "entries": {"198.51.100.0/24\n203.0.113.0/24\n"}}),
			store.EventModelChange, []string{"created prefix set CUST_V4", "2 entries"}},
		{"prefix set edit", post("/library/prefix-sets/CUST_V4/edit", url.Values{"name": {"CUST_V4"}, "family": {"ipv4"}, "entries": {"198.51.100.0/24\n"}}),
			store.EventModelChange, []string{"updated prefix set CUST_V4", "1 entries"}},
		{"prefix set delete", post("/library/prefix-sets/CUST_V4/delete", nil),
			store.EventModelChange, []string{"deleted prefix set CUST_V4"}},

		{"AS set create", post("/library/as-sets/new", url.Values{"name": {"AS_CUST"}, "source": {"AS-CUST"}, "entries": {"64600\n"}}),
			store.EventModelChange, []string{"created AS set AS_CUST"}},
		{"AS set edit", post("/library/as-sets/AS_CUST/edit", url.Values{"name": {"AS_CUST"}, "source": {"AS-CUST"}, "entries": {"64600\n64602\n"}}),
			store.EventModelChange, []string{"updated AS set AS_CUST"}},
		{"AS set refresh", post("/library/as-sets/AS_CUST/refresh", nil),
			store.EventModelChange, []string{"refreshed AS set AS_CUST", "AS-CUST"}},
		{"AS set delete", post("/library/as-sets/AS_CUST/delete", nil),
			store.EventModelChange, []string{"deleted AS set AS_CUST"}},

		{"static route create", post("/library/static-routes/new", static),
			store.EventModelChange, []string{"created static route 198.51.100.0/26"}},
		{"static route edit", func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			return env.do(t, http.MethodPost, "/library/static-routes/"+staticID(t, env)+"/edit", static)
		}, store.EventModelChange, []string{"updated static route 198.51.100.0/26"}},
		{"static route delete", func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			return env.do(t, http.MethodPost, "/library/static-routes/"+staticID(t, env)+"/delete", nil)
		}, store.EventModelChange, []string{"deleted static route 198.51.100.0/26"}},

		{"RPKI server create", post("/rpki/new", rpki), store.EventModelChange, []string{"created RPKI server rtr1"}},
		{"RPKI server edit", post("/rpki/rtr1/edit", rpki), store.EventModelChange, []string{"updated RPKI server rtr1"}},
		{"RPKI server delete", post("/rpki/rtr1/delete", nil), store.EventModelChange, []string{"deleted RPKI server rtr1"}},

		{"BMP station create", post("/bmp/new", bmp), store.EventModelChange, []string{"created BMP station collector"}},
		{"BMP station edit", post("/bmp/collector/edit", bmp), store.EventModelChange, []string{"updated BMP station collector"}},
		{"BMP station delete", post("/bmp/collector/delete", nil), store.EventModelChange, []string{"deleted BMP station collector"}},

		{"seed import", post("/peers/seed", url.Values{"include": {"edge_v4"}, "role_edge_v4": {"upstream"}}),
			store.EventModelChange, []string{"imported 1 peer(s) from BIRD", "edge_v4"}},
	})
}

// A save the form rejected changed nothing, so it must not claim it did.
func TestRejectedSavesAreNotAudited(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)
	before := newestEvent(t, env).ID
	env.do(t, http.MethodPost, "/library/prefix-sets/new", url.Values{"name": {"bad name!"}, "family": {"ipv4"}, "entries": {"not-a-prefix\n"}})
	env.do(t, http.MethodPost, "/settings/identity", url.Values{"routerId": {"2001:db8::1"}, "localAsn": {"0"}})
	env.do(t, http.MethodPost, "/rpki/new", url.Values{"name": {""}})
	env.do(t, http.MethodPost, "/peers/seed", url.Values{}) // nothing ticked
	if got := newestEvent(t, env); got.ID != before {
		t.Errorf("a rejected save was audited: %+v", got)
	}
}

// Alert destinations decide where news of the router goes, and a Slack or
// Discord webhook URL is itself the credential: every change is audited by
// name, and no URL or SMTP password is ever written to the timeline.
func TestAlertDestinationChangesAreAuditedWithoutSecrets(t *testing.T) {
	env := newTestEnv(t, false)
	hook := httptest.NewServer(http.NotFoundHandler())
	defer hook.Close()
	const secret = "not-a-real-secret"
	form := func(name string) url.Values {
		return url.Values{"name": {name}, "type": {store.AlertSlack}, "enabled": {"on"},
			"url": {hook.URL + "/services/T/B/" + secret}, "events": {store.EventSessionDown}}
	}
	mail := url.Values{"name": {"ops-mail"}, "type": {store.AlertEmail}, "enabled": {"on"},
		"smtpHost": {"127.0.0.1"}, "smtpPort": {"25"}, "smtpFrom": {"birdy@example.net"}, "smtpTo": {"noc@example.net"},
		"smtpUsername": {"birdy"}, "smtpPassword": {secret}, "smtpSecurity": {"none"}, "events": {store.EventSessionDown}}
	id := func(t *testing.T, env *testEnv) string {
		dests, err := env.store.ListAlertDestinations()
		if err != nil || len(dests) == 0 {
			t.Fatalf("destinations: %+v, err %v", dests, err)
		}
		for _, d := range dests {
			if d.Type == store.AlertSlack {
				return fmt.Sprint(d.ID)
			}
		}
		t.Fatal("no slack destination")
		return ""
	}
	on := func(suffix string, form url.Values) func(*testing.T, *testEnv) *httptest.ResponseRecorder {
		return func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			return env.do(t, http.MethodPost, "/alerts/"+id(t, env)+suffix, form)
		}
	}
	runAuditSteps(t, env, "admin", []auditStep{
		{"create", post("/alerts/new", form("ops-slack")), store.EventModelChange, []string{"created alert destination ops-slack", "slack"}},
		{"create email", post("/alerts/new", mail), store.EventModelChange, []string{"created alert destination ops-mail", "email"}},
		{"edit", on("/edit", form("ops-chat")), store.EventModelChange, []string{"updated alert destination ops-chat"}},
		// The receiver answers 404, so this is the failing branch of the test send.
		{"test", on("/test", url.Values{}), store.EventModelChange, []string{"test alert", "ops-chat", "failed"}},
		{"delete", on("/delete", nil), store.EventModelChange, []string{"deleted alert destination ops-chat"}},
	})

	events, err := env.store.ListEvents(50, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if strings.Contains(e.Message, secret) || strings.Contains(e.Message, "/services/") || strings.Contains(e.Message, hook.URL) {
			t.Errorf("audit message carries a webhook URL or password: %q", e.Message)
		}
	}
}

// Taking a copy of the database off the router, and staging one to replace it,
// are audited and also written to the server log with who and from where: a
// restore replaces the very table the audit row is in.
func TestDataTransfersAreAuditedAndLogged(t *testing.T) {
	var logs bytes.Buffer
	env := newTestEnv(t, false, func(c *Config) { c.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	withIdentity(t, env)

	get := func(path string) func(*testing.T, *testEnv) *httptest.ResponseRecorder {
		return func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "192.0.2.77:5000"
			req.AddCookie(env.cookie)
			rec := httptest.NewRecorder()
			env.srv.ServeHTTP(rec, req)
			return rec
		}
	}
	restore := func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, err := mw.CreateFormFile("db", "birdy.db")
		if err != nil {
			t.Fatal(err)
		}
		// A real database, as a restore validates what it is given: the one
		// this server hands out as a snapshot.
		snap := get("/api/snapshot/download")(t, env)
		if snap.Code != http.StatusOK {
			t.Fatalf("snapshot download for the restore body: %d", snap.Code)
		}
		_, _ = part.Write(snap.Body.Bytes())
		mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/snapshot/restore", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.RemoteAddr = "192.0.2.77:5000"
		req.AddCookie(env.cookie)
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		return rec
	}

	for _, step := range []auditStep{
		{"snapshot download", get("/api/snapshot/download"), store.EventBackup, []string{"downloaded a database snapshot"}},
		{"backup download", get("/api/backup/download"), store.EventBackup, []string{"downloaded a full backup"}},
		{"restore staged", restore, store.EventBackup, []string{"staged a database restore"}},
	} {
		logs.Reset()
		// Snapshot files are named to the second, and a second one inside the
		// same second cannot be written; start each step with none.
		if err := os.RemoveAll(filepath.Join(filepath.Dir(env.confPath), "snapshots")); err != nil {
			t.Fatal(err)
		}
		runAuditSteps(t, env, "admin", []auditStep{step})
		line := logs.String()
		for _, want := range []string{"level=INFO", "actor=admin", "client=192.0.2.77", step.want[0]} {
			if !strings.Contains(line, want) {
				t.Errorf("%s: server log should carry %q, got: %s", step.name, want, line)
			}
		}
	}
}

// Sign-ins, sign-outs and credential changes are on the timeline, attributed
// to the account, without anything secret in them.
func TestAccountEventsAreAudited(t *testing.T) {
	env := newTestEnv(t, false)

	login := func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
		form := url.Values{"username": {"admin"}, "password": {goodPassword}}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		if c := findCookie(rec.Result().Cookies(), sessionCookieName); c != nil {
			env.cookie = c // the rest of the test acts as this new session
		}
		return rec
	}
	const newPassword = "a brand new passphrase"
	runAuditSteps(t, env, "admin", []auditStep{
		{"login", login, store.EventAuth, []string{"signed in"}},
		{"password change", func(t *testing.T, env *testEnv) *httptest.ResponseRecorder {
			rec := env.do(t, http.MethodPost, "/profile/password", url.Values{
				"currentPassword": {goodPassword}, "newPassword": {newPassword}, "confirmPassword": {newPassword}})
			if c := findCookie(rec.Result().Cookies(), sessionCookieName); c != nil {
				env.cookie = c // the password change rotates the session
			}
			return rec
		}, store.EventModelChange, []string{"changed the account password"}},
	})
	// From here on the account has its new name, and so do the audit rows.
	runAuditSteps(t, env, "operator", []auditStep{
		{"username change", post("/profile/identity", url.Values{"username": {"operator"}, "currentPassword": {newPassword}}),
			store.EventModelChange, []string{"changed the username from admin to operator"}},
		{"logout", post("/logout", nil), store.EventAuth, []string{"signed out"}},
	})

	events, err := env.store.ListEvents(50, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if strings.Contains(e.Message, goodPassword) || strings.Contains(e.Message, newPassword) {
			t.Errorf("audit message carries a password: %q", e.Message)
		}
	}
}

// A failed sign-in is not an operator action, so it is not on the timeline (a
// guessing bot would bury it) — but it is in the server log, with where it came
// from and why it was refused, and never with the password. The username is
// attacker-controlled text: it is logged cut short and with control characters
// removed, so it cannot forge log lines or carry a mistyped password whole.
func TestRefusedLoginsAreLogged(t *testing.T) {
	var logs bytes.Buffer
	env := newTestEnv(t, false, func(c *Config) { c.Log = slog.New(slog.NewJSONHandler(&logs, nil)) })
	attempt := func(username, password string) {
		form := url.Values{"username": {username}, "password": {password}}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "198.51.100.7:4000"
		env.srv.ServeHTTP(httptest.NewRecorder(), req)
	}
	before := newestEvent(t, env).ID

	attempt("admin", "wrong-password-1")
	line := logs.String()
	for _, want := range []string{`"level":"WARN"`, `"client":"198.51.100.7"`, `"verdict":"wrong username or password"`, `"username":"admin"`} {
		if !strings.Contains(line, want) {
			t.Errorf("failed login log should carry %s, got: %s", want, line)
		}
	}

	logs.Reset()
	attempt("root\x1b[31m\r\nlevel=INFO forged\x00"+strings.Repeat("x", 500), "wrong-password-2")
	line = logs.String()
	if strings.Contains(line, `\u001b`) || strings.Contains(line, `\r`) || strings.Contains(line, `\n`+"level") || strings.Contains(line, `\u0000`) {
		t.Errorf("control characters from the username reached the log: %s", line)
	}
	if strings.Contains(line, strings.Repeat("x", 100)) {
		t.Errorf("the username was logged at full length: %s", line)
	}
	if !strings.Contains(line, `"username":"root[31mlevel=INFO forged`) {
		t.Errorf("the sanitised username should still be recognisable: %s", line)
	}

	// Five failures lock the address out; the refusals after that are logged
	// with their own verdict.
	for i := 0; i < 4; i++ {
		attempt("admin", "wrong-password-3")
	}
	logs.Reset()
	attempt("admin", goodPassword)
	if line := logs.String(); !strings.Contains(line, `"verdict":"locked"`) || !strings.Contains(line, `"client":"198.51.100.7"`) {
		t.Errorf("a locked-out attempt should be logged as such: %s", line)
	}

	if strings.Contains(logs.String(), "wrong-password") || strings.Contains(logs.String(), goodPassword) {
		t.Errorf("a password reached the log: %s", logs.String())
	}
	if got := newestEvent(t, env); got.ID != before {
		t.Errorf("a refused login must not be written to the timeline: %+v", got)
	}
}

// The router-wide budget refusing a client is logged the same way.
func TestThrottledLoginIsLogged(t *testing.T) {
	var logs bytes.Buffer
	env := newTestEnv(t, false, func(c *Config) { c.Log = slog.New(slog.NewJSONHandler(&logs, nil)) })
	env.srv.login.tokens = 0 // the shared budget is spent
	form := url.Values{"username": {"admin"}, "password": {goodPassword}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.50:4000"
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Header().Get("Location") != "/login?error=throttled" {
		t.Fatalf("setup: expected a throttled refusal, got %q", rec.Header().Get("Location"))
	}
	if line := logs.String(); !strings.Contains(line, `"verdict":"throttled"`) || !strings.Contains(line, `"client":"203.0.113.50"`) || !strings.Contains(line, `"level":"WARN"`) {
		t.Errorf("a throttled attempt should be logged as such: %s", line)
	}
}
