package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// The label names the router in alerts, so it has to be editable after install —
// it used to be settable only via "birdy init --label", which meant renaming a
// router required re-initialising it.
func TestRouterLabelIsEditable(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{RouterLabel: "rt2", RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}

	env.do(t, "POST", "/settings/identity", url.Values{
		"routerLabel": {"rt2.kicked.ro"}, "routerId": {"192.0.2.1"}, "localAsn": {"64500"},
	})

	got, _, err := env.store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.RouterLabel != "rt2.kicked.ro" {
		t.Errorf("label should have been renamed, got %q", got.RouterLabel)
	}
	// The other identity fields must survive the save.
	if got.RouterID != "192.0.2.1" || !got.LocalASN.Valid || got.LocalASN.Int64 != 64500 {
		t.Errorf("identity fields clobbered: %+v", got)
	}
}

// A label ends up in a JSON alert payload and an email header; a line break there
// is a header injection, so it is refused rather than silently stripped.
func TestRouterLabelRejectsLineBreaks(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{RouterLabel: "rt2", RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}

	rec := env.do(t, "POST", "/settings/identity", url.Values{
		"routerLabel": {"rt2\r\nBcc: someone@example.net"}, "routerId": {"192.0.2.1"}, "localAsn": {"64500"},
	})
	if !strings.Contains(rec.Body.String(), "Line breaks are not allowed") {
		t.Error("a label with line breaks should be refused with an error on the form")
	}
	got, _, _ := env.store.GetSettings()
	if got.RouterLabel != "rt2" {
		t.Errorf("the rejected label must not be saved, got %q", got.RouterLabel)
	}
}
