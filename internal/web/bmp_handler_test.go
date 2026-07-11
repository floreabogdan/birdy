package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func TestBMPPageEmptyState(t *testing.T) {
	env := newTestEnv(t, false)
	body := env.do(t, "GET", "/bmp", nil).Body.String()
	if !strings.Contains(body, "No BMP station") {
		t.Error("the empty state should invite adding a station")
	}
}

func TestBMPCreateAndRender(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{
		RouterID: "192.0.2.1", LocalASN: nullInt(65551),
		BirdSocketPath: "/run/bird/bird.ctl", ListenAddr: "127.0.0.1:8080",
	}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"name": {"collector"}, "description": {"route collector"},
		"address": {"198.51.100.10"}, "port": {"1790"},
		"monitorPre": {"on"}, "monitorPost": {"on"}, "enabled": {"on"},
	}
	if rec := env.do(t, "POST", "/bmp/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create should redirect, got %d: %s", rec.Code, rec.Body.String())
	}

	body := env.do(t, "GET", "/bmp", nil).Body.String()
	if !strings.Contains(body, "collector") || !strings.Contains(body, "198.51.100.10") {
		t.Error("the new station should be listed")
	}

	changes := env.do(t, "GET", "/changes", nil).Body.String()
	if !strings.Contains(changes, "protocol bmp collector") {
		t.Errorf("the station should be rendered into the candidate config:\n%s", changes)
	}
	if !strings.Contains(changes, "station address ip 198.51.100.10 port 1790") {
		t.Error("the station line should render with address and port")
	}
}

func TestBMPValidation(t *testing.T) {
	env := newTestEnv(t, false)
	form := url.Values{
		"name": {"1bad"}, "address": {"collector.example.com"}, "port": {"0"},
		"enabled": {"on"},
	}
	body := env.do(t, "POST", "/bmp/new", form).Body.String()
	for _, want := range []string{"letters, digits and underscore", "not a hostname", "port between 1 and 65535"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing validation message %q", want)
		}
	}
	// Nothing should have been created.
	if stations, _ := env.store.ListBMPStations(); len(stations) != 0 {
		t.Errorf("an invalid station must not be saved, have %d", len(stations))
	}
}

func TestBMPDelete(t *testing.T) {
	env := newTestEnv(t, false)
	form := url.Values{
		"name": {"gone"}, "address": {"203.0.113.5"}, "port": {"1790"}, "enabled": {"on"},
	}
	if rec := env.do(t, "POST", "/bmp/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", rec.Code)
	}
	if rec := env.do(t, "POST", "/bmp/gone/delete", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete should redirect, got %d", rec.Code)
	}
	if _, err := env.store.GetBMPStationByName("gone"); err != store.ErrNotFound {
		t.Errorf("station should be gone, got %v", err)
	}
}
