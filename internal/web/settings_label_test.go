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
	if err := env.store.SaveSettings(store.Settings{RouterLabel: "rtr2", RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}

	env.do(t, "POST", "/settings/identity", url.Values{
		"routerLabel": {"rtr2.example.net"}, "routerId": {"192.0.2.1"}, "localAsn": {"64500"},
	})

	got, _, err := env.store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.RouterLabel != "rtr2.example.net" {
		t.Errorf("label should have been renamed, got %q", got.RouterLabel)
	}
	// The other identity fields must survive the save.
	if got.RouterID != "192.0.2.1" || !got.LocalASN.Valid || got.LocalASN.Int64 != 64500 {
		t.Errorf("identity fields clobbered: %+v", got)
	}
}

// The identity form is where the kernel preferred source is set. A valid pair is
// saved and normalized; the identity fields around it survive.
func TestKernelPrefSrcSavedFromForm(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{RouterLabel: "rtr2", RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}

	env.do(t, "POST", "/settings/identity", url.Values{
		"routerLabel": {"rtr2"}, "routerId": {"192.0.2.1"}, "localAsn": {"64500"},
		"kernelPrefsrcV4": {"203.0.113.1"}, "kernelPrefsrcV6": {"2001:db8::1"},
	})

	got, _, err := env.store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.KernelPrefSrcV4 != "203.0.113.1" || got.KernelPrefSrcV6 != "2001:db8::1" {
		t.Errorf("kernel preferred source not saved: %+v", got)
	}
}

// A v6 address in the v4 field is refused on the form, and nothing is saved — the
// v4 slot belongs to the kernel4 protocol, which only takes an IPv4 address.
func TestKernelPrefSrcRejectsWrongFamily(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{RouterLabel: "rtr2", RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}

	rec := env.do(t, "POST", "/settings/identity", url.Values{
		"routerLabel": {"rtr2"}, "routerId": {"192.0.2.1"}, "localAsn": {"64500"},
		"kernelPrefsrcV4": {"2001:db8::1"},
	})
	if !strings.Contains(rec.Body.String(), "Enter an IPv4 address") {
		t.Error("a v6 address in the v4 field should be refused on the form")
	}
	got, _, _ := env.store.GetSettings()
	if got.KernelPrefSrcV4 != "" {
		t.Errorf("the rejected value must not be saved, got %q", got.KernelPrefSrcV4)
	}
}

// A label ends up in a JSON alert payload and an email header; a line break there
// is a header injection, so it is refused rather than silently stripped.
func TestRouterLabelRejectsLineBreaks(t *testing.T) {
	env := newTestEnv(t, false)
	if err := env.store.SaveSettings(store.Settings{RouterLabel: "rtr2", RouterID: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}

	rec := env.do(t, "POST", "/settings/identity", url.Values{
		"routerLabel": {"rtr2\r\nBcc: someone@example.net"}, "routerId": {"192.0.2.1"}, "localAsn": {"64500"},
	})
	if !strings.Contains(rec.Body.String(), "Line breaks are not allowed") {
		t.Error("a label with line breaks should be refused with an error on the form")
	}
	got, _, _ := env.store.GetSettings()
	if got.RouterLabel != "rtr2" {
		t.Errorf("the rejected label must not be saved, got %q", got.RouterLabel)
	}
}
