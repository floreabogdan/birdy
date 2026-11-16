package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "birdy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SaveSettings(store.Settings{RouterLabel: "rtr1", BirdSocketPath: "/x", ListenAddr: "y"}); err != nil {
		t.Fatal(err)
	}
	return st
}

// Config apply/revert are subscribable event kinds the apply pipeline really
// dispatches, so they must render a specific title and severity rather than the
// generic "birdy alert"/info fallthrough.
func TestConfigEventTitleAndSeverity(t *testing.T) {
	cases := []struct {
		kind     string
		title    string
		severity string
	}{
		{store.EventConfigApply, "Config applied", "info"},
		{store.EventConfigRevert, "Config reverted", "warning"},
	}
	for _, c := range cases {
		a := alert{Kind: c.kind}
		if got := a.title(); got != c.title {
			t.Errorf("%s title = %q, want %q", c.kind, got, c.title)
		}
		if got := a.severity(); got != c.severity {
			t.Errorf("%s severity = %q, want %q", c.kind, got, c.severity)
		}
	}
}

// A generic webhook gets text (Slack-ish) and content (Discord-ish) plus fields.
func TestWebhookPayload(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	st := openStore(t)
	if _, err := st.CreateAlertDestination(store.Destination{Name: "w", Type: store.AlertWebhook, Enabled: true, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(st, nil, 0)
	if err := d.SendTest(mustGet(t, st, "w")); err != nil {
		t.Fatal(err)
	}
	if got["text"] == "" || got["text"] != got["content"] {
		t.Errorf("text/content = %v / %v", got["text"], got["content"])
	}
	if got["severity"] == nil {
		t.Error("payload should carry severity")
	}
}

// Slack gets attachments with a colour; Discord gets embeds with a colour.
func TestSlackAndDiscordShape(t *testing.T) {
	for _, tc := range []struct {
		typ, wantKey string
	}{{store.AlertSlack, "attachments"}, {store.AlertDiscord, "embeds"}} {
		var got map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &got)
			w.WriteHeader(204)
		}))
		st := openStore(t)
		if _, err := st.CreateAlertDestination(store.Destination{Name: "d", Type: tc.typ, Enabled: true, URL: srv.URL}); err != nil {
			t.Fatal(err)
		}
		if err := NewDispatcher(st, nil, 0).SendTest(mustGet(t, st, "d")); err != nil {
			t.Fatal(err)
		}
		if _, ok := got[tc.wantKey]; !ok {
			t.Errorf("%s payload missing %q: %v", tc.typ, tc.wantKey, got)
		}
		srv.Close()
	}
}

// Notify fans out to every enabled destination and skips disabled ones.
func TestNotifyFansOutToEnabledOnly(t *testing.T) {
	hits := make(chan string, 4)
	on := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits <- "on"; w.WriteHeader(200) }))
	defer on.Close()
	off := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits <- "off"; w.WriteHeader(200) }))
	defer off.Close()

	st := openStore(t)
	st.CreateAlertDestination(store.Destination{Name: "on", Type: store.AlertWebhook, Enabled: true, URL: on.URL})
	st.CreateAlertDestination(store.Destination{Name: "off", Type: store.AlertWebhook, Enabled: false, URL: off.URL})

	NewDispatcher(st, nil, 0).Notify(store.EventSessionDown, "edge_v4", "went down")

	select {
	case got := <-hits:
		if got != "on" {
			t.Errorf("delivered to the disabled destination")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery")
	}
	// The disabled one must not fire.
	select {
	case got := <-hits:
		t.Errorf("unexpected second delivery: %s", got)
	case <-time.After(300 * time.Millisecond):
	}
}

// The email body is a valid MIME message with both parts and the alert content.
func TestBuildEmail(t *testing.T) {
	a := alert{Kind: store.EventSessionDown, Protocol: "edge_v4", Message: "edge_v4 went down", Router: "rtr1", Time: time.Unix(1_700_000_000, 0)}
	msg := buildMIME("birdy@example.com", []string{"noc@example.com"}, "birdy: "+a.title(), a.plainText(), emailHTML(a))
	for _, want := range []string{
		"From: birdy@example.com", "To: noc@example.com", "Subject:",
		"multipart/alternative", "text/plain", "text/html", "edge_v4 went down",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("email missing %q", want)
		}
	}
}

// deliverAllSync runs an event through the same throttle, filter and routing
// as Notify, but delivers inline, one destination after another, so a test can
// assert on the result as soon as it returns.
func (d *Dispatcher) deliverAllSync(kind, protocol, message string) {
	if d.throttle(notification{kind: kind, protocol: protocol, message: message}) {
		return
	}
	d.deliverInline(kind, protocol, message)
}

// deliverHeldSync is the routing worker's held-back check, delivered inline.
func (d *Dispatcher) deliverHeldSync() {
	for _, n := range d.takeHeld() {
		d.deliverInline(n.kind, n.protocol, n.message)
	}
}

func (d *Dispatcher) deliverInline(kind, protocol, message string) {
	for _, j := range d.route(kind, protocol, message) {
		d.deliverOne(j)
	}
}

func mustGet(t *testing.T, st *store.Store, name string) store.Destination {
	t.Helper()
	all, err := st.ListAlertDestinations()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("destination %q not found", name)
	return store.Destination{}
}

func TestThrottleSuppressesRepeatSessionEvents(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
	defer srv.Close()
	st := openStore(t)
	st.CreateAlertDestination(store.Destination{Name: "w", Type: store.AlertWebhook, Enabled: true, URL: srv.URL})

	d := NewDispatcher(st, nil, time.Minute)
	d.deliverAllSync(store.EventFlap, "edge_v4", "flapped")
	d.deliverAllSync(store.EventFlap, "edge_v4", "flapped again") // within cooldown -> suppressed
	if hits != 1 {
		t.Fatalf("repeat flap should be throttled: hits=%d", hits)
	}
	// A different session is independent.
	d.deliverAllSync(store.EventFlap, "edge_v6", "flapped")
	if hits != 2 {
		t.Fatalf("a different session should still alert: hits=%d", hits)
	}
	// Config events are never throttled.
	d.deliverAllSync(store.EventConfigRevert, "", "reverted")
	d.deliverAllSync(store.EventConfigRevert, "", "reverted again")
	if hits != 4 {
		t.Fatalf("config events must not be throttled: hits=%d", hits)
	}
}

func TestPerDestinationEventFilter(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = "hit"; w.WriteHeader(200) }))
	defer srv.Close()
	st := openStore(t)
	// This destination only wants session-down.
	st.CreateAlertDestination(store.Destination{Name: "w", Type: store.AlertWebhook, Enabled: true, URL: srv.URL, Events: store.EventSessionDown})

	d := NewDispatcher(st, nil, 0)
	d.deliverAllSync(store.EventFlap, "edge_v4", "flap")
	if got == "hit" {
		t.Error("a filtered-out kind should not be delivered")
	}
	d.deliverAllSync(store.EventSessionDown, "edge_v4", "down")
	if got != "hit" {
		t.Error("a wanted kind should be delivered")
	}
}

// One stalled destination must not hold up the others. Here the mail server
// (sorted first, so it is tried first) accepts and never answers; the webhook
// still has to get every alert promptly, not after the mail deadline per event.
func TestSlowDestinationDoesNotDelayOthers(t *testing.T) {
	host, port := fakeSMTP(t, "greeting")
	hits := make(chan string, 4)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		hits <- fmt.Sprint(body["protocol"])
		w.WriteHeader(200)
	}))
	defer hook.Close()

	st := openStore(t)
	if _, err := st.CreateAlertDestination(emailDest("a-mail", host, port)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAlertDestination(store.Destination{Name: "b-hook", Type: store.AlertWebhook, Enabled: true, URL: hook.URL}); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(st, nil, 0)
	d.smtpTimeout = time.Minute // the mail server stays wedged for the whole test

	d.Notify(store.EventSessionDown, "edge_v4", "down")
	d.Notify(store.EventSessionDown, "edge_v6", "down")
	for _, want := range []string{"edge_v4", "edge_v6"} {
		select {
		case got := <-hits:
			if got != want {
				t.Fatalf("webhook got %s, want %s (order must hold per destination)", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("webhook still waiting for %s after 3s: the stalled mail server is holding it up", want)
		}
	}
}

// A Slack or Discord webhook URL is the credential: whoever has it can post to
// the channel. A failed delivery is logged and flashed to the browser, so the
// error may name the host but never the path that carries the secret — not in
// the "returned HTTP 404" form, and not in the "Post \"<url>\": …" form Go's
// HTTP client puts on a transport error.
func TestDeliveryErrorsHideWebhookURL(t *testing.T) {
	const secretPath = "/services/TFAKE/BFAKE/not-a-real-secret"

	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	// The handler never sees the client give up (it leaves the body unread), so
	// it is released explicitly before Close, which waits for it.
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer slow.Close()
	defer close(release)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := "http://" + ln.Addr().String()
	ln.Close() // nothing listens there now

	for _, tc := range []struct{ name, base string }{
		{"http-404", notFound.URL},
		{"connection-refused", refused},
		{"timeout", slow.URL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			st := openStore(t)
			if _, err := st.CreateAlertDestination(store.Destination{Name: "ops-slack", Type: store.AlertSlack, Enabled: true, URL: tc.base + secretPath}); err != nil {
				t.Fatal(err)
			}
			d := NewDispatcher(st, slog.New(slog.NewTextHandler(&logs, nil)), 0)
			d.client = &http.Client{Timeout: 300 * time.Millisecond}

			err := d.SendTest(mustGet(t, st, "ops-slack"))
			if err == nil {
				t.Fatal("delivery should have failed")
			}
			if strings.Contains(err.Error(), "not-a-real-secret") || strings.Contains(err.Error(), "/services/") {
				t.Errorf("error leaks the webhook path: %v", err)
			}
			if !strings.Contains(err.Error(), "127.0.0.1") {
				t.Errorf("error should still name the host: %v", err)
			}

			d.deliverAllSync(store.EventSessionDown, "edge_v4", "down")
			if strings.Contains(logs.String(), "not-a-real-secret") {
				t.Errorf("log leaks the webhook path: %s", logs.String())
			}
			if !strings.Contains(logs.String(), "ops-slack") {
				t.Errorf("log should name the destination: %s", logs.String())
			}
		})
	}
}

// eventRecorder is a webhook that records the event kind of every alert it
// receives, in order.
type eventRecorder struct {
	*httptest.Server
	mu       sync.Mutex
	kinds    []string
	messages []string
	arrived  chan struct{}
}

func newEventRecorder(t *testing.T) *eventRecorder {
	t.Helper()
	rec := &eventRecorder{arrived: make(chan struct{}, 64)}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.mu.Lock()
		rec.kinds = append(rec.kinds, fmt.Sprint(body["event"]))
		rec.messages = append(rec.messages, fmt.Sprint(body["message"]))
		rec.mu.Unlock()
		rec.arrived <- struct{}{}
		w.WriteHeader(200)
	}))
	t.Cleanup(rec.Close)
	return rec
}

func (rec *eventRecorder) got() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.kinds...)
}

// The cooldown may delay news but must not lose it. down (sent), flap (sent),
// down again (inside the down window, so held back), then nothing: the
// operator's last alert said "flapping" while the session sat down, and no
// later transition would ever correct it. Once the window closes, the held-back
// state has to go out.
func TestCooldownDeliversHeldBackStateWhenWindowCloses(t *testing.T) {
	rec := newEventRecorder(t)
	st := openStore(t)
	if _, err := st.CreateAlertDestination(store.Destination{Name: "w", Type: store.AlertWebhook, Enabled: true, URL: rec.URL}); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(st, nil, 500*time.Millisecond)
	d.Notify(store.EventSessionDown, "edge_v4", "edge_v4 went down")
	d.Notify(store.EventFlap, "edge_v4", "edge_v4 is flapping")
	d.Notify(store.EventSessionDown, "edge_v4", "edge_v4 went down")

	want := []string{store.EventSessionDown, store.EventFlap, store.EventSessionDown}
	timeout := time.After(5 * time.Second)
	for range want {
		select {
		case <-rec.arrived:
		case <-timeout:
			t.Fatalf("operator was last told %v; the session is down", rec.got())
		}
	}
	if got := rec.got(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("alerts = %v, want %v", got, want)
	}
}

// What the cooldown holds back is delivered only once its window has closed,
// and only when it would change what the operator was last told. Driven on a
// fake clock through the same throttle and held-back check the worker runs.
func TestCooldownHeldBackRules(t *testing.T) {
	const (
		down = store.EventSessionDown
		up   = store.EventSessionUp
		flap = store.EventFlap
		// check runs the held-back check instead of raising an event.
		check = ""
	)
	type step struct {
		at   time.Duration // since the first event; the cooldown is 5 minutes
		kind string
		sent int // for a check: how many alerts the operator should have by now
	}
	for _, tc := range []struct {
		name  string
		steps []step
		want  []string
	}{
		{"a held state that differs goes out when its window closes", []step{
			{0, down, 0}, {time.Minute, flap, 0}, {2 * time.Minute, down, 0},
			{4 * time.Minute, check, 2}, // down's window is still open
			{5*time.Minute + time.Second, check, 3},
			{6 * time.Minute, check, 3}, // and only once
		}, []string{down, flap, down}},
		{"a held state the operator was last told is dropped", []step{
			{0, down, 0}, {time.Minute, up, 0}, {2 * time.Minute, down, 0}, {3 * time.Minute, up, 0},
			{5*time.Minute + time.Second, check, 2}, {7 * time.Minute, check, 2},
		}, []string{down, up}},
		{"a held state superseded by a delivered one is dropped", []step{
			{0, down, 0}, {time.Minute, up, 0}, {2 * time.Minute, down, 0}, {3 * time.Minute, flap, 0},
			{7 * time.Minute, check, 3},
		}, []string{down, up, flap}},
		{"a plain repeat stays suppressed", []step{
			{0, flap, 0}, {time.Minute, flap, 0},
			{7 * time.Minute, check, 1},
		}, []string{flap}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newEventRecorder(t)
			st := openStore(t)
			if _, err := st.CreateAlertDestination(store.Destination{Name: "w", Type: store.AlertWebhook, Enabled: true, URL: rec.URL}); err != nil {
				t.Fatal(err)
			}
			d := NewDispatcher(st, nil, 5*time.Minute)
			start := time.Unix(1_700_000_000, 0)
			var now time.Time
			d.now = func() time.Time { return now }

			for _, s := range tc.steps {
				now = start.Add(s.at)
				if s.kind != check {
					d.deliverAllSync(s.kind, "edge_v4", "edge_v4 "+s.kind)
					continue
				}
				d.deliverHeldSync()
				if got := rec.got(); len(got) != s.sent {
					t.Fatalf("at %v: alerts = %v, want %d of them", s.at, got, s.sent)
				}
			}
			if got := rec.got(); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("alerts = %v, want %v", got, tc.want)
			}
		})
	}
}

// An alert that arrives minutes after the event says why, so nobody reads it
// as a fresh transition.
func TestHeldBackAlertSaysItWasHeld(t *testing.T) {
	rec := newEventRecorder(t)
	st := openStore(t)
	if _, err := st.CreateAlertDestination(store.Destination{Name: "w", Type: store.AlertWebhook, Enabled: true, URL: rec.URL}); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(st, nil, 5*time.Minute)
	now := time.Unix(1_700_000_000, 0)
	d.now = func() time.Time { return now }
	d.deliverAllSync(store.EventSessionDown, "edge_v4", "edge_v4 went down")
	d.deliverAllSync(store.EventSessionUp, "edge_v4", "edge_v4 came back")
	d.deliverAllSync(store.EventSessionDown, "edge_v4", "edge_v4 went down")
	now = now.Add(6 * time.Minute)
	d.deliverHeldSync()

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.messages) != 3 || !strings.Contains(rec.messages[2], "held back by the alert cooldown") {
		t.Fatalf("messages = %q, want the third marked as held back", rec.messages)
	}
}
