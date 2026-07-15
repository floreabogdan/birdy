package render

import (
	"strings"
	"testing"
)

// With no preferred source configured, the kernel protocols must render exactly
// what birdy has always written — a plain `export all` on both families. This is
// the byte-for-byte guarantee an existing router relies on across an upgrade.
func TestKernelExportDefaultUnchanged(t *testing.T) {
	cfg := mustRender(t, baseInput())
	const want = `protocol kernel kernel4 {
	ipv4 {
		import none;
		export all;
	};
}

protocol kernel kernel6 {
	ipv6 {
		import none;
		export all;
	};
}`
	if !strings.Contains(cfg, want) {
		t.Errorf("kernel protocols changed with no preferred source set:\n%s", cfg)
	}
}

// A preferred source turns the plain export into an export filter that stamps
// krt_prefsrc on every route, per family.
func TestKernelExportPrefSrc(t *testing.T) {
	in := baseInput()
	in.KernelPrefSrcV4 = "203.0.113.1"
	in.KernelPrefSrcV6 = "2001:db8::1"
	cfg := mustRender(t, in)

	const wantV4 = `protocol kernel kernel4 {
	ipv4 {
		import none;
		export filter {
			krt_prefsrc = 203.0.113.1;
			accept;
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
			krt_prefsrc = 2001:db8::1;
			accept;
		};
	};
}`
	if !strings.Contains(cfg, wantV6) {
		t.Errorf("kernel6 missing its krt_prefsrc export filter:\n%s", cfg)
	}
	// With no peers, the only exports are the two kernel channels; both are now
	// filters, so nothing should still say "export all".
	if strings.Contains(cfg, "export all;") {
		t.Errorf("both families pinned, yet a bare export all survives:\n%s", cfg)
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
	if !strings.Contains(k6, "export all;") || strings.Contains(k6, "krt_prefsrc") {
		t.Errorf("kernel6 was left unset and must stay export all:\n%s", k6)
	}
}
