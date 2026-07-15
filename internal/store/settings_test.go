package store

import "testing"

func TestValidateKernelPrefSrc(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		wantV4 string // normalized value after ValidateKernelPrefSrcV4
		errV4  bool
		wantV6 string // normalized value after ValidateKernelPrefSrcV6
		errV6  bool
	}{
		{name: "empty", in: "", wantV4: "", wantV6: ""},
		{name: "blank trims to empty", in: "   ", wantV4: "", wantV6: ""},
		{name: "v4 address", in: "203.0.113.1", wantV4: "203.0.113.1", errV6: true},
		{name: "v4 with spaces", in: "  203.0.113.1 ", wantV4: "203.0.113.1", errV6: true},
		{name: "v6 address", in: "2001:db8::1", errV4: true, wantV6: "2001:db8::1"},
		{name: "garbage", in: "nope", errV4: true, errV6: true},
		{name: "v4-mapped v6 is not a v6", in: "::ffff:203.0.113.1", errV4: true, errV6: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v4 := Settings{KernelPrefSrcV4: c.in}
			if msg := v4.ValidateKernelPrefSrcV4(); (msg != "") != c.errV4 {
				t.Errorf("v4 %q: errV4=%v, msg=%q", c.in, c.errV4, msg)
			} else if !c.errV4 && v4.KernelPrefSrcV4 != c.wantV4 {
				t.Errorf("v4 %q normalized to %q, want %q", c.in, v4.KernelPrefSrcV4, c.wantV4)
			}

			v6 := Settings{KernelPrefSrcV6: c.in}
			if msg := v6.ValidateKernelPrefSrcV6(); (msg != "") != c.errV6 {
				t.Errorf("v6 %q: errV6=%v, msg=%q", c.in, c.errV6, msg)
			} else if !c.errV6 && v6.KernelPrefSrcV6 != c.wantV6 {
				t.Errorf("v6 %q normalized to %q, want %q", c.in, v6.KernelPrefSrcV6, c.wantV6)
			}
		})
	}
}

func TestSettingsKernelPrefSrcRoundTrip(t *testing.T) {
	s := openTest(t)
	if err := s.SaveSettings(Settings{
		RouterID:        "192.0.2.1",
		KernelPrefSrcV4: "203.0.113.1",
		KernelPrefSrcV6: "2001:db8::1",
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetSettings()
	if err != nil || !ok {
		t.Fatalf("get settings: ok=%v err=%v", ok, err)
	}
	if got.KernelPrefSrcV4 != "203.0.113.1" || got.KernelPrefSrcV6 != "2001:db8::1" {
		t.Errorf("kernel preferred source not persisted: %+v", got)
	}
}
