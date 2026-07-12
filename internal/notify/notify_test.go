package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
