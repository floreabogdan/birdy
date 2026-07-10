package web

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func decodePreview(t *testing.T, body string) previewResp {
	t.Helper()
	var pr previewResp
	if err := json.Unmarshal([]byte(body), &pr); err != nil {
		t.Fatalf("preview response not JSON: %v (%s)", err, body)
	}
	return pr
}

func TestPeerPreviewLive(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	form := peerForm() // a valid eBGP peer
	rec := env.do(t, "POST", "/peers/preview", form)
	pr := decodePreview(t, rec.Body.String())
	if pr.Err != "" {
		t.Fatalf("valid peer should preview: err=%q", pr.Err)
	}
	if !strings.Contains(pr.Preview, "protocol bgp transit_v4") {
		t.Errorf("preview should contain the peer protocol block:\n%s", pr.Preview)
	}
}

// The preview reflects unsaved edits — change a field and the code changes.
func TestPeerPreviewReflectsEdits(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	form := peerForm()
	form.Set("neighborIp", "203.0.113.77")
	pr := decodePreview(t, env.do(t, "POST", "/peers/preview", form).Body.String())
	if !strings.Contains(pr.Preview, "neighbor 203.0.113.77") {
		t.Errorf("preview should reflect the unsaved neighbor address:\n%s", pr.Preview)
	}
}

// A live preview returns lint findings so the operator sees them before saving.
func TestPeerPreviewReturnsWarnings(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	form := peerForm()
	form.Set("drained", "on") // a drained peer is surfaced by lint
	pr := decodePreview(t, env.do(t, "POST", "/peers/preview", form).Body.String())
	var found bool
	for _, w := range pr.Warnings {
		if strings.Contains(w.Message, "draining") {
			found = true
		}
	}
	if !found {
		t.Errorf("live preview should carry lint warnings: %+v", pr.Warnings)
	}
}

func TestOtherPreviewEndpoints(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)

	cases := []struct {
		path string
		form url.Values
		want string
	}{
		{"/library/static-routes/preview", url.Values{"prefix": {"198.51.100.0/26"}, "action": {"blackhole"}, "enabled": {"on"}}, "blackhole"},
		{"/library/prefix-sets/preview", url.Values{"name": {"MY_SET"}, "family": {"ipv4"}, "entries": {"192.0.2.0/24"}}, "MY_SET"},
		{"/library/as-sets/preview", url.Values{"name": {"AS_CUST"}, "entries": {"64512"}}, "AS_CUST"},
		{"/policies/preview", url.Values{"name": {"IMP"}, "direction": {"import"}, "defaultRoute": {"reject"}, "bogonAsns": {"off"}, "rov": {"off"}}, "function imp_IMP"},
	}
	for _, c := range cases {
		pr := decodePreview(t, env.do(t, "POST", c.path, c.form).Body.String())
		if pr.Err != "" {
			t.Errorf("%s: unexpected err %q", c.path, pr.Err)
			continue
		}
		if !strings.Contains(pr.Preview, c.want) {
			t.Errorf("%s: preview missing %q:\n%s", c.path, c.want, pr.Preview)
		}
	}
}

// Preview is a viewer action, allowed even in read-only mode.
func TestPreviewAllowedInReadOnly(t *testing.T) {
	env := newTestEnv(t, true)
	withIdentity(t, env)
	rec := env.do(t, "POST", "/peers/preview", peerForm())
	if rec.Code != 200 {
		t.Fatalf("preview in read-only should work, got %d", rec.Code)
	}
}
