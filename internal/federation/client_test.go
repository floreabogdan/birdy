package federation

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	body, err := (Client{BaseURL: srv.URL, Token: "secret"}).FetchDashboard(context.Background())
	if err != nil || !strings.Contains(string(body), "statusOK") {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

func TestFetchDashboardDoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := (Client{BaseURL: srv.URL, Token: "secret"}).FetchDashboard(context.Background()); err == nil {
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
	events, err := (Client{BaseURL: srv.URL, Token: "secret"}).FetchEvents(context.Background(), 8)
	if err != nil || len(events) != 1 || events[0].Message != "peer up" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}
