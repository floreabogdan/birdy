package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/updatecheck"
)

type fakeUpdateChecker struct {
	result  updatecheck.Result
	err     error
	channel string
}

func (f *fakeUpdateChecker) Check(_ context.Context, channel, _, _ string) (updatecheck.Result, error) {
	f.channel = channel
	return f.result, f.err
}

func TestUpdatesPageShowsAvailableDevelopmentCommit(t *testing.T) {
	checker := &fakeUpdateChecker{result: updatecheck.Result{
		Channel:      updatecheck.ChannelDevelopment,
		LatestCommit: "abcdef1234567890",
		URL:          "https://github.com/floreabogdan/birdy/commit/abcdef1234567890",
		Available:    true,
		CheckedAt:    time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC),
	}}
	env := newTestEnv(t, false, func(c *Config) { c.UpdateChecker = checker })
	if err := env.store.SaveUpdateChannel(updatecheck.ChannelDevelopment); err != nil {
		t.Fatal(err)
	}

	rec := env.do(t, http.MethodGet, "/updates", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /updates: status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Development branch", "update available", "abcdef1", "checked"} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Errorf("GET /updates body missing %q", want)
		}
	}
	if checker.channel != updatecheck.ChannelDevelopment {
		t.Fatalf("checked channel %q, want development", checker.channel)
	}
}

func TestUpdateChannelPost(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) {
		c.UpdateChecker = &fakeUpdateChecker{}
	})
	rec := env.do(t, http.MethodPost, "/updates/channel", url.Values{
		"channel": {updatecheck.ChannelDevelopment},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST channel: status %d", rec.Code)
	}
	settings, ok, err := env.store.GetSettings()
	if err != nil || !ok {
		t.Fatalf("GetSettings: ok=%v err=%v", ok, err)
	}
	if settings.UpdateChannel != updatecheck.ChannelDevelopment {
		t.Fatalf("saved channel %q", settings.UpdateChannel)
	}
}

func TestUpdateChannelRejectsInvalidAndReadOnly(t *testing.T) {
	env := newTestEnv(t, false)
	if rec := env.do(t, http.MethodPost, "/updates/channel", url.Values{
		"channel": {"feature/untrusted"},
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid channel: status %d", rec.Code)
	}

	readOnly := newTestEnv(t, true)
	if rec := readOnly.do(t, http.MethodPost, "/updates/channel", url.Values{
		"channel": {updatecheck.ChannelDevelopment},
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only channel change: status %d", rec.Code)
	}
}

func TestUpdatesPageHandlesCheckFailure(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) {
		c.UpdateChecker = &fakeUpdateChecker{err: errors.New("upstream unavailable")}
	})
	rec := env.do(t, http.MethodGet, "/updates", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "upstream unavailable") {
		t.Fatalf("check failure page: status=%d body=%q", rec.Code, rec.Body.String())
	}
}
