package web

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

// eventsVia reads /api/events with a session cookie or with a bearer token.
func eventsVia(t *testing.T, env *testEnv, query, bearer string) []store.Event {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/events"+query, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	} else {
		req.AddCookie(env.cookie)
	}
	rec := httptest.NewRecorder()
	env.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/events%s: status %d", query, rec.Code)
	}
	var payload struct {
		Events []store.Event `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Events
}

// An instance token is for the fleet view, which shows the last few events of
// each remote. It must not page through the whole audit trail or learn which
// operator did what.
func TestTokenReadOfEventsIsLimited(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveInstanceAPITokenHash(store.HashInstanceAPIToken("remote-secret")); err != nil {
		t.Fatal(err)
	}
	const total = 130
	for i := 1; i <= total; i++ {
		if err := env.store.InsertAudit("alice", store.EventModelChange, fmt.Sprintf("change %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	for _, query := range []string{"", "?limit=500", "?limit=500&offset=100", "?offset=120", "?limit=51"} {
		events := eventsVia(t, env, query, "remote-secret")
		if len(events) != 50 {
			t.Errorf("token read %q: %d events, want 50", query, len(events))
			continue
		}
		if events[0].Message != fmt.Sprintf("change %d", total) {
			t.Errorf("token read %q starts at %q, want the newest event (offset ignored)", query, events[0].Message)
		}
		for _, e := range events {
			if e.Actor != "" {
				t.Errorf("token read %q exposes the actor %q", query, e.Actor)
				break
			}
		}
	}
	// What the federation client really asks for is untouched.
	if events := eventsVia(t, env, "?limit=8", "remote-secret"); len(events) != 8 {
		t.Errorf("token read ?limit=8: %d events, want 8", len(events))
	}

	// A signed-in operator sees everything, as before.
	events := eventsVia(t, env, "?limit=500&offset=100", "")
	if len(events) != total-100 {
		t.Errorf("session read: %d events, want %d", len(events), total-100)
	}
	if len(events) > 0 && events[0].Actor != "alice" {
		t.Errorf("session read lost the actor: %+v", events[0])
	}
}

// Go's HTTP client wraps every failure in a *url.Error whose message quotes
// the whole request URL. The instance_down event is on the timeline, and the
// timeline is what token holders read.
func TestInstanceDownEventOmitsTheURL(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String() + "/hidden-path"
	ln.Close() // nothing listens there now

	env := newTestEnv(t, false)
	id, err := env.store.CreateInstance("remote-edge", base, strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	// It was healthy a moment ago, so this refresh is a transition to offline.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := env.store.UpdateInstanceHealth(id, now, now, 5, ""); err != nil {
		t.Fatal(err)
	}
	env.srv.refreshInstanceHealth(t.Context())

	events, err := env.store.ListEvents(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var down *store.Event
	for i := range events {
		if events[i].Kind == store.EventInstanceDown {
			down = &events[i]
		}
	}
	if down == nil {
		t.Fatalf("no instance_down event recorded: %+v", events)
	}
	if strings.Contains(down.Message, "hidden-path") || strings.Contains(down.Message, "http://") || strings.Contains(down.Message, "/api/dashboard") {
		t.Errorf("instance_down message quotes the URL: %q", down.Message)
	}
	if !strings.Contains(down.Message, "remote-edge") || !strings.Contains(down.Message, "refused") {
		t.Errorf("instance_down message should still name the instance and the cause: %q", down.Message)
	}
}

// Read-only mode is how birdy runs when it must not touch BIRD — which is also
// when a leaked observer token most needs to be revocable. Revoking narrows
// access to birdy itself, like the allow-list edits read-only mode already
// permits; minting a new token stays blocked.
func TestInstanceTokensCanBeRevokedInReadOnlyMode(t *testing.T) {
	env := newTestEnv(t, true)
	id, err := env.store.CreateInstanceToken("Dashboard observer", store.HashInstanceAPIToken("scoped-secret"), "dashboard timeline", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.SaveInstanceAPITokenHash(store.HashInstanceAPIToken("legacy-secret")); err != nil {
		t.Fatal(err)
	}
	valid := func(token string) bool {
		ok, err := env.store.VerifyInstanceAPIToken(token)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !valid("scoped-secret") || !valid("legacy-secret") {
		t.Fatal("setup: both tokens should be valid")
	}

	// The settings page offers the buttons...
	page := env.do(t, http.MethodGet, "/settings", nil).Body.String()
	if !strings.Contains(page, fmt.Sprintf("/settings/instance-token/%d/revoke", id)) || !strings.Contains(page, `data-post="/settings/instance-token/revoke"`) {
		t.Error("read-only settings page should offer to revoke the tokens")
	}
	if strings.Contains(page, `action="/settings/instance-token"`) {
		t.Error("read-only settings page must not offer to generate a token")
	}

	// ...and they work.
	if rec := env.do(t, http.MethodPost, fmt.Sprintf("/settings/instance-token/%d/revoke", id), url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("revoke one in read-only mode: code=%d", rec.Code)
	}
	if valid("scoped-secret") {
		t.Error("the scoped token is still valid after revoking it")
	}
	if rec := env.do(t, http.MethodPost, "/settings/instance-token/revoke", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("revoke all in read-only mode: code=%d", rec.Code)
	}
	if valid("legacy-secret") {
		t.Error("the legacy token is still valid after revoking it")
	}

	if rec := env.do(t, http.MethodPost, "/settings/instance-token", url.Values{"expiry": {"30"}}); rec.Code != http.StatusForbidden {
		t.Errorf("generating a token in read-only mode: code=%d, want 403", rec.Code)
	}
}
