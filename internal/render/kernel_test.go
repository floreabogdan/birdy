package render

import (
	"strings"
	"testing"
)

// With no preferred source configured, kernel route installation is disabled.
func TestKernelExportDefaultDisabled(t *testing.T) {
	cfg := mustRender(t, baseInput())
	const want = `protocol kernel kernel4 {
	ipv4 {
		import none;
		export none;
	};
}

protocol kernel kernel6 {
	ipv6 {
		import none;
		export none;
	};
}`
	if !strings.Contains(cfg, want) {
		t.Errorf("kernel protocols changed with no preferred source set:\n%s", cfg)
	}
}

// A preferred source permits only Birdy-originated static routes and stamps
// krt_prefsrc on them, per family.
func TestKernelExportPrefSrc(t *testing.T) {
	in := baseInput()
	in.KernelPrefSrcV4 = "203.0.113.1"
	in.KernelPrefSrcV6 = "2001:db8::1"
	cfg := mustRender(t, in)

	const wantV4 = `protocol kernel kernel4 {
	ipv4 {
		import none;
		export filter {
			if source = RTS_STATIC then {
				krt_prefsrc = 203.0.113.1;
				accept;
			}
			reject;
		};
	};
}`
	if !strings.Contains(cfg, wantV4) {
		t.Errorf("kernel4 missing its krt_prefsrc export filter:\n%s", cfg)
	}
	const wantV6 = `protocol kernel kernel6 {
	ipv6 {
		import none;
		export filter {
			if source = RTS_STATIC then {
				krt_prefsrc = 2001:db8::1;
				accept;
			}
			reject;
		};
	};
}`
	if !strings.Contains(cfg, wantV6) {
		t.Errorf("kernel6 missing its krt_prefsrc export filter:\n%s", cfg)
	}
	if !strings.Contains(cfg, "if source = RTS_STATIC then") {
		t.Errorf("kernel filters must allow only static routes:\n%s", cfg)
	}
}

// The two families are independent: pinning v4 must not touch kernel6.
func TestKernelExportPerFamily(t *testing.T) {
	in := baseInput()
	in.KernelPrefSrcV4 = "203.0.113.1"
	cfg := mustRender(t, in)

	if !strings.Contains(cfg, "krt_prefsrc = 203.0.113.1;") {
		t.Error("kernel4 should carry the v4 preferred source")
	}
	k6 := block(t, cfg, "protocol kernel kernel6 {")
	if !strings.Contains(k6, "export none;") || strings.Contains(k6, "krt_prefsrc") {
		t.Errorf("kernel6 was left unset and must export nothing:\n%s", k6)
	}
}

func TestKernelExportSelectedBGPPerFamily(t *testing.T) {
	in := baseInput()
	in.KernelExportBGPV4 = true
	cfg := mustRender(t, in)

	k4 := block(t, cfg, "protocol kernel kernel4 {")
	if !strings.Contains(k4, "if source = RTS_BGP then {") {
		t.Errorf("kernel4 should export selected BGP routes:\n%s", k4)
	}
	if strings.Contains(k4, "RTS_STATIC") || strings.Contains(k4, "krt_prefsrc") {
		t.Errorf("BGP-only kernel export should not admit or modify static routes:\n%s", k4)
	}
	k6 := block(t, cfg, "protocol kernel kernel6 {")
	if !strings.Contains(k6, "export none;") {
		t.Errorf("kernel6 must remain disabled:\n%s", k6)
	}
}

func TestKernelExportSelectedBGPWithPreferredSource(t *testing.T) {
	in := baseInput()
	in.KernelExportBGPV6 = true
	in.KernelPrefSrcV6 = "2001:db8::1"
	cfg := mustRender(t, in)

	k6 := block(t, cfg, "protocol kernel kernel6 {")
	if strings.Count(k6, "krt_prefsrc = 2001:db8::1;") != 2 {
		t.Errorf("preferred source should stamp both selected BGP and static routes:\n%s", k6)
	}
	for _, source := range []string{"RTS_BGP", "RTS_STATIC"} {
		if !strings.Contains(k6, "if source = "+source+" then {") {
			t.Errorf("kernel6 missing %s export:\n%s", source, k6)
		}
	}
}

func TestKernelExportAllowsDefaultRoute(t *testing.T) {
	in := baseInput()
	in.KernelExportBGPV4 = true
	in.KernelExportBGPV6 = true
	v4 := ebgpPeer()
	v4.LocalIP = "192.0.2.1"
	in.Peers = append(in.Peers, v4)
	cfg := mustRender(t, in)

	k4 := block(t, cfg, "protocol kernel kernel4 {")
	if !strings.Contains(k4, "if net.len > 0 then") {
		t.Errorf("control-plane guards must skip the default route:\n%s", k4)
	}
}

func TestKernelExportProtectsControlPlaneAddresses(t *testing.T) {
	in := baseInput()
	in.KernelExportBGPV4 = true
	in.KernelExportBGPV6 = true
	in.KernelPrefSrcV6 = "2001:db8::100"
	v4 := ebgpPeer()
	v4.LocalIP = "192.0.2.1"
	v6 := ebgpPeer()
	v6.Name = "edge_v6"
	v6.NeighborIP = "2001:db8::10"
	v6.LocalIP = "2001:db8::1"
	in.Peers = append(in.Peers, v4, v6)

	cfg := mustRender(t, in)
	k4 := block(t, cfg, "protocol kernel kernel4 {")
	for _, addr := range []string{in.RouterID, v4.NeighborIP, v4.LocalIP} {
		if !strings.Contains(k4, "if "+addr+" ~ net then reject;") {
			t.Errorf("kernel4 does not protect %s:\n%s", addr, k4)
		}
	}
	k6 := block(t, cfg, "protocol kernel kernel6 {")
	for _, addr := range []string{"2001:db8::1", "2001:db8::10", "2001:db8::100"} {
		if !strings.Contains(k6, "if "+addr+" ~ net then reject;") {
			t.Errorf("kernel6 does not protect %s:\n%s", addr, k6)
		}
	}
}
