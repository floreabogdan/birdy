package render

import (
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func TestBMPRendersEnabledStations(t *testing.T) {
	in := baseInput()
	in.BMPStations = []store.BMPStation{
		{Name: "collector", Description: "route collector", Address: "198.51.100.10", Port: 1790,
			Enabled: true, PrePolicy: true, PostPolicy: true, TxBufferLimit: 64},
		{Name: "disabled_one", Address: "203.0.113.9", Port: 1790, Enabled: false, PrePolicy: true},
	}
	cfg := mustRender(t, in)

	want := []string{
		"# route collector\n",
		"protocol bmp collector {\n",
		"\tstation address ip 198.51.100.10 port 1790;\n",
		"\tmonitoring rib in pre_policy;\n",
		"\tmonitoring rib in post_policy;\n",
		"\ttx buffer limit 64;\n",
	}
	for _, w := range want {
		if !strings.Contains(cfg, w) {
			t.Errorf("rendered config missing %q\n%s", w, cfg)
		}
	}
	// A disabled station is not rendered at all.
	if strings.Contains(cfg, "disabled_one") || strings.Contains(cfg, "203.0.113.9") {
		t.Error("a disabled BMP station must not be rendered")
	}
}

// With neither RIB view selected and no buffer override, only the station line
// is emitted — BIRD still reports session state.
func TestBMPStateOnlyStation(t *testing.T) {
	in := baseInput()
	in.BMPStations = []store.BMPStation{
		{Name: "state_only", Address: "2001:db8::1", Port: 6543, Enabled: true},
	}
	cfg := mustRender(t, in)
	if !strings.Contains(cfg, "protocol bmp state_only {\n\tstation address ip 2001:db8::1 port 6543;\n}\n") {
		t.Errorf("state-only station rendered wrong:\n%s", cfg)
	}
	if strings.Contains(cfg, "monitoring rib") || strings.Contains(cfg, "tx buffer") {
		t.Error("state-only station should emit no monitoring or buffer lines")
	}
}

// BMP is its own addressable section, and a station appears there.
func TestBMPHasOwnSection(t *testing.T) {
	in := baseInput()
	in.BMPStations = []store.BMPStation{{Name: "collector", Address: "198.51.100.10", Port: 1790, Enabled: true, PrePolicy: true}}
	secs, err := Sections(in)
	if err != nil {
		t.Fatalf("Sections: %v", err)
	}
	var found *Section
	for i := range secs {
		if secs[i].Path == "protocols/bmp" {
			found = &secs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no protocols/bmp section")
	}
	if !strings.Contains(found.Body, "protocol bmp collector") {
		t.Errorf("bmp section missing the station:\n%s", found.Body)
	}
}
