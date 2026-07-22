package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStableAndDevelopmentChecks(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/releases/latest":
			w.Write([]byte(`{"tag_name":"v0.3.8","html_url":"https://example/release"}`))
		case "/commits/main":
			w.Write([]byte(`{"sha":"abcdef1234567890","html_url":"https://example/commit"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), TTL: time.Hour}

	stable, err := c.Check(context.Background(), ChannelStable, "0.3.7", "")
	if err != nil || !stable.Available || stable.LatestVersion != "0.3.8" {
		t.Fatalf("stable result = %+v, err %v", stable, err)
	}
	dev, err := c.Check(context.Background(), ChannelDevelopment, "0.3.9-dev", "abcdef1")
	if err != nil || dev.Available {
		t.Fatalf("development result = %+v, err %v", dev, err)
	}
	if _, err := c.Check(context.Background(), ChannelStable, "0.3.7", ""); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want one per channel due to cache", requests)
	}
}

// A failing check must be cached for the negative TTL, so a slow or rate-limited
// GitHub is not re-hit on every page render (the check runs inline in the
// request path).
func TestFailedCheckIsNegativelyCached(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), TTL: time.Hour}

	if _, err := c.Check(context.Background(), ChannelStable, "0.3.7", ""); err == nil {
		t.Fatal("expected an error from a failing endpoint")
	}
	if _, err := c.Check(context.Background(), ChannelStable, "0.3.7", ""); err == nil {
		t.Fatal("expected the cached error on the second call")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1 — a failed check must be negatively cached", requests)
	}
}

func TestStableAvailable(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"0.3.7", "0.3.8", true},
		{"0.3.8", "0.3.8", false},
		{"0.4.0", "0.3.8", false},
		// A -dev build ahead of the latest stable is NOT behind it: 0.4.2-dev is
		// a pre-release of 0.4.2, which is newer than 0.4.1 — offer nothing.
		{"0.3.9-dev", "0.3.8", false},
		{"0.4.2-dev", "0.4.1", false},
		// A -dev build whose stable tag has since shipped should offer it.
		{"0.4.2-dev", "0.4.2", true},
		{"0.4.2-dev", "0.4.3", true},
		{"unknown", "0.3.8", true},
	} {
		if got := stableAvailable(tc.current, tc.latest); got != tc.want {
			t.Errorf("stableAvailable(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}
