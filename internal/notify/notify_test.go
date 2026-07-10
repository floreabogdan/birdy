package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

func TestDeliverPayload(t *testing.T) {
	var got payload
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	w := NewWebhook(nil, nil)
	if err := w.deliver(srv.URL, store.EventSessionDown, "edge_v4", "edge_v4 (BGP) went down", "rtr1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if gotType != "application/json" {
		t.Errorf("content-type = %q", gotType)
	}
	// Slack reads text, Discord reads content — both must be present and equal.
	if got.Text == "" || got.Text != got.Content {
		t.Errorf("text/content = %q / %q", got.Text, got.Content)
	}
	if got.Event != store.EventSessionDown || got.Protocol != "edge_v4" {
		t.Errorf("structured fields wrong: %+v", got)
	}
	if got.Router != "rtr1" {
		t.Errorf("router = %q", got.Router)
	}
}

func TestDeliverReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	w := NewWebhook(nil, nil)
	if err := w.deliver(srv.URL, "test", "", "hi", "", time.Now()); err == nil {
		t.Error("a 500 response should be an error")
	}
}

// A configured webhook receives session events; an empty one is a no-op.
func TestNotifyReadsStoredURL(t *testing.T) {
	received := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "birdy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(store.Settings{WebhookURL: srv.URL, BirdSocketPath: "/x", ListenAddr: "y"}); err != nil {
		t.Fatal(err)
	}

	w := NewWebhook(st, nil)
	w.Notify(store.EventSessionDown, "edge_v4", "down")
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("webhook was not called")
	}
}
