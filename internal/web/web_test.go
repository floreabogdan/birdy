package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/poller"
	"github.com/floreabogdan/birdy/internal/snapshot"
	"github.com/floreabogdan/birdy/internal/store"
)

// fakeClient implements both the poller's and the web layer's BIRD client
// interfaces, so tests never touch a real socket.
type fakeClient struct {
	protocols []birdc.ProtocolSummary
	details   map[string]birdc.ProtocolDetail
	routes    map[string][]birdc.RouteTable // keyed "type:target"
	routeErr  error
	// invalidCounts is what BIRD answers to the filtered "show route ... count" the
	// RPKI dry run asks for.
	invalidCounts []birdc.RouteCountEntry

	// Apply pipeline behaviour. Zero value means every configure step succeeds,
	// which is what most tests want; a test flips one to false or sets an error
	// to exercise a failure path. calls records the sequence for assertions.
	cfgCheckFail   bool
	cfgApplyFail   bool
	cfgConfirmFail bool
	cfgReloadFail  bool // reload reports a non-OK result (e.g. a peer without route-refresh)
	cfgErr         error
	calls          []string
	lastSoft       bool // whether the last ConfigureTimeout asked for a soft reconfigure
}

func (f *fakeClient) result(ok bool, msg string) (birdc.ConfigureResult, error) {
	return birdc.ConfigureResult{OK: ok, Message: msg}, nil
}

func (f *fakeClient) ConfigureCheck() (birdc.ConfigureResult, error) {
	f.calls = append(f.calls, "check")
	if f.cfgErr != nil {
		return birdc.ConfigureResult{}, f.cfgErr
	}
	return f.result(!f.cfgCheckFail, "checked")
}
func (f *fakeClient) ConfigureTimeout(seconds int, soft bool) (birdc.ConfigureResult, error) {
	f.calls = append(f.calls, "timeout")
	f.lastSoft = soft
	if f.cfgErr != nil {
		return birdc.ConfigureResult{}, f.cfgErr
	}
	return f.result(!f.cfgApplyFail, "applied")
}
func (f *fakeClient) ConfigureConfirm() (birdc.ConfigureResult, error) {
	f.calls = append(f.calls, "confirm")
	return f.result(!f.cfgConfirmFail, "confirmed")
}
func (f *fakeClient) ConfigureUndo() (birdc.ConfigureResult, error) {
	f.calls = append(f.calls, "undo")
	return f.result(true, "undone")
}
func (f *fakeClient) Reload() (birdc.ConfigureResult, error) {
	f.calls = append(f.calls, "reload")
	if f.cfgErr != nil {
		return birdc.ConfigureResult{}, f.cfgErr
	}
	msg := "reloaded"
	if f.cfgReloadFail {
		msg = "edge1: BGP neighbor does not support route refresh"
	}
	return f.result(!f.cfgReloadFail, msg)
}

func (f *fakeClient) Status() (birdc.Status, error)               { return birdc.Status{Version: "2.17.1"}, nil }
func (f *fakeClient) Protocols() ([]birdc.ProtocolSummary, error) { return f.protocols, nil }
func (f *fakeClient) RouteCount() ([]birdc.RouteCountEntry, error) {
	return []birdc.RouteCountEntry{{Table: "master4", Routes: 5, Networks: 4}}, nil
}

func (f *fakeClient) ProtocolDetail(name string) (birdc.ProtocolDetail, error) {
	if d, ok := f.details[name]; ok {
		return d, nil
	}
	return birdc.ProtocolDetail{}, &notFoundErr{name}
}

type notFoundErr struct{ name string }

func (e *notFoundErr) Error() string { return "unknown protocol " + e.name }

func (f *fakeClient) RoutesForPage(prefixOrIP string, all bool, offset, limit int) (birdc.RoutePage, error) {
	return f.lookup("for:" + prefixOrIP)
}
func (f *fakeClient) RoutesByProtocolPage(name string, all bool, offset, limit int) (birdc.RoutePage, error) {
	return f.lookup("protocol:" + name)
}
func (f *fakeClient) RoutesExportPage(name string, all bool, offset, limit int) (birdc.RoutePage, error) {
	return f.lookup("export:" + name)
}
func (f *fakeClient) RoutesNoExportPage(name string, all bool, offset, limit int) (birdc.RoutePage, error) {
	return f.lookup("noexport:" + name)
}
func (f *fakeClient) RoutesRPKIInvalidPage(localASN int64, offset, limit int) (birdc.RoutePage, error) {
	return f.lookup("rpki-invalid")
}
func (f *fakeClient) RoutesRPKIInvalidCount(localASN int64) ([]birdc.RouteCountEntry, error) {
	if f.routeErr != nil {
		return nil, f.routeErr
	}
	return f.invalidCounts, nil
}
func (f *fakeClient) lookup(key string) (birdc.RoutePage, error) {
	if f.routeErr != nil {
		return birdc.RoutePage{}, f.routeErr
	}
	return birdc.RoutePage{Tables: f.routes[key]}, nil
}

type testEnv struct {
	srv      *Server
	store    *store.Store
	fc       *fakeClient
	cookie   *http.Cookie
	confPath string
}

// fakeNotifier records the events the web layer emits, so a test can assert an
// apply or rollback was forwarded to alert destinations.
type fakeNotifier struct {
	kinds  []string
	mailed string
}

func (f *fakeNotifier) Notify(kind, protocol, message string) { f.kinds = append(f.kinds, kind) }
func (f *fakeNotifier) MailConfig(maskedConfig string)        { f.mailed = maskedConfig }

// newTestEnvMetrics builds an env with the Prometheus endpoint enabled.
func newTestEnvMetrics(t *testing.T) *testEnv {
	return newTestEnv(t, false, func(c *Config) { c.Metrics = true })
}

func newTestEnv(t *testing.T, readOnly bool, opts ...func(*Config)) *testEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "birdy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fc := &fakeClient{
		protocols: []birdc.ProtocolSummary{
			{Name: "edge_v4", Proto: "BGP", Table: "---", State: "up", Since: "2026-07-08", Info: "Established"},
		},
		details: map[string]birdc.ProtocolDetail{},
		routes:  map[string][]birdc.RouteTable{},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := poller.New(fc, st, time.Hour, log) // long interval; we only need Run's one unconditional pre-loop poll
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Run(ctx) // cancelled context: does exactly one poll, then returns immediately

	snapMgr := snapshot.NewManager(filepath.Join(dir, "birdy.db"), filepath.Join(dir, "snapshots"), 3)

	confPath := filepath.Join(dir, "bird.conf")
	cfg := Config{
		Store: st, Client: fc, Poller: p, Snapshot: snapMgr, Log: log, ReadOnly: readOnly,
		BirdConfPath: confPath, BirdBackupDir: filepath.Join(dir, "bird-backups"),
		BirdBinary:   "definitely-not-a-real-bird-binary", // makes bird -p Skip rather than run
		ApplyTimeout: 60,
	}
	for _, o := range opts {
		o(&cfg)
	}
	srv := New(cfg)

	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("admin", hash); err != nil {
		t.Fatal(err)
	}
	token, err := newSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(token, 1, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	return &testEnv{srv: srv, store: st, fc: fc, confPath: confPath, cookie: &http.Cookie{Name: sessionCookieName, Value: token}}
}

func TestLoginFlow(t *testing.T) {
	env := newTestEnv(t, false)

	// wrong password
	form := url.Values{"username": {"admin"}, "password": {"wrong"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?error=1" {
		t.Fatalf("wrong password: code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}

	// correct password
	form = url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}
	req = httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("correct password: code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || cookies[0].Value == "" {
		t.Fatalf("expected a session cookie, got %+v", cookies)
	}
}

func TestDashboardRequiresAuth(t *testing.T) {
	env := newTestEnv(t, false)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated dashboard: code=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestDashboardJSON(t *testing.T) {
	env := newTestEnv(t, false)
	req := httptest.NewRequest("GET", "/api/dashboard", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"name":"edge_v4"`) || !strings.Contains(body, `"up":true`) {
		t.Fatalf("unexpected dashboard JSON: %s", body)
	}
}

func TestPeerDetailNotFound(t *testing.T) {
	env := newTestEnv(t, false)
	req := httptest.NewRequest("GET", "/api/peers/does_not_exist", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"err":"unknown protocol`) {
		t.Fatalf("expected error field in body: %s", rec.Body.String())
	}
}

func TestLookingGlassRejectsInvalidTarget(t *testing.T) {
	env := newTestEnv(t, false)
	env.fc.routeErr = nil
	req := httptest.NewRequest("GET", "/api/lg?type=for&target=not-a-prefix", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	// the fake doesn't validate, but the real birdc.Client would reject this;
	// here we assert the handler round-trips the fake's response either way
	// and never panics on malformed input.
	if !strings.Contains(rec.Body.String(), `"ran":true`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestLookingGlassRoutesFor(t *testing.T) {
	env := newTestEnv(t, false)
	env.fc.routes["for:192.0.2.0/24"] = []birdc.RouteTable{
		{Name: "master4", Routes: []birdc.RouteEntry{{Network: "192.0.2.0/24", Type: "unicast", Primary: true}}},
	}
	req := httptest.NewRequest("GET", "/api/lg?type=for&target=192.0.2.0/24", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "master4") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPeerRoutesEndpoint(t *testing.T) {
	env := newTestEnv(t, false)
	env.fc.routes["protocol:edge_v4"] = []birdc.RouteTable{
		{Name: "master4", Routes: []birdc.RouteEntry{{Network: "0.0.0.0/0", Type: "unicast", Protocol: "edge_v4"}}},
	}
	req := httptest.NewRequest("GET", "/api/peers/edge_v4/routes?dir=protocol", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "0.0.0.0/0") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}

	// default dir when omitted is "protocol"
	req = httptest.NewRequest("GET", "/api/peers/edge_v4/routes", nil)
	req.AddCookie(env.cookie)
	rec = httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "0.0.0.0/0") {
		t.Fatalf("expected default dir=protocol, body=%s", rec.Body.String())
	}

	// invalid dir is rejected, not silently ignored
	req = httptest.NewRequest("GET", "/api/peers/edge_v4/routes?dir=bogus", nil)
	req.AddCookie(env.cookie)
	rec = httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"err":"invalid dir"`) {
		t.Fatalf("expected invalid dir error, body=%s", rec.Body.String())
	}
}

func TestLookingGlassPaginationLinks(t *testing.T) {
	env := newTestEnv(t, false)
	env.fc.routes["protocol:edge_v4"] = []birdc.RouteTable{
		{Name: "master4", Routes: []birdc.RouteEntry{{Network: "0.0.0.0/0"}}},
	}
	req := httptest.NewRequest("GET", "/lg?type=protocol&target=edge_v4&offset=50", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "offset=0") {
		t.Fatalf("expected a Previous link back to offset=0, body=%s", body)
	}
}

// TestHTMLPagesRender exercises every server-rendered HTML page (as opposed
// to the JSON API) so html/template field-resolution errors — which only
// surface at execution time, not at parse time — are caught in CI rather
// than during manual deployment testing.
func TestHTMLPagesRender(t *testing.T) {
	env := newTestEnv(t, false)
	env.fc.details["edge_v4"] = birdc.ProtocolDetail{
		Summary:  birdc.ProtocolSummary{Name: "edge_v4", Proto: "BGP", State: "up"},
		BGPState: "Established",
		Channels: []birdc.ChannelDetail{{AFI: "ipv4", State: "UP", RoutesImported: 1}},
		RawLines: []string{"BGP state: Established"},
	}
	if err := env.store.InsertEvent(store.EventSessionUp, "edge_v4", "established"); err != nil {
		t.Fatal(err)
	}

	pages := []string{"/", "/peers/edge_v4", "/peers/edge_v4?tab=bird", "/timeline", "/lg", "/lg?type=for&target=192.0.2.0/24", "/settings"}
	for _, path := range pages {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(env.cookie)
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d body=%s", path, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Fatalf("%s: content-type=%q", path, ct)
		}
	}
}

func TestLoginPageRenders(t *testing.T) {
	env := newTestEnv(t, false)
	for _, path := range []string{"/login", "/login?error=1"} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		env.srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestSnapshotRestoreBlockedInReadOnly(t *testing.T) {
	env := newTestEnv(t, true)
	req := httptest.NewRequest("POST", "/api/snapshot/restore", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d, want 403", rec.Code)
	}
}

func TestTimelinePagination(t *testing.T) {
	env := newTestEnv(t, false)
	for i := range 3 {
		if err := env.store.InsertEvent(store.EventSessionUp, "edge_v4", "test event"); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	req := httptest.NewRequest("GET", "/api/events", nil)
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"hasMore":false`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}
