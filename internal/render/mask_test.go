package render

import (
	"strings"
	"testing"
)

func TestMaskPasswords(t *testing.T) {
	in := `protocol bgp a {
	password "s3cr3t";
	neighbor 192.0.2.1 as 65001;
}
protocol bgp b {
        password    "another one with spaces";
}
`
	got := MaskPasswords(in)
	for _, secret := range []string{"s3cr3t", "another one with spaces"} {
		if strings.Contains(got, secret) {
			t.Errorf("secret %q survived masking:\n%s", secret, got)
		}
	}
	if n := strings.Count(got, `"`+MaskedPassword+`"`); n != 2 {
		t.Errorf("want 2 masked passwords, got %d:\n%s", n, got)
	}
	// Indentation and the trailing semicolon must survive, or every password
	// line shows up as a diff hunk against the freshly rendered candidate.
	if !strings.Contains(got, "\tpassword \""+MaskedPassword+"\";") {
		t.Errorf("surrounding syntax not preserved:\n%s", got)
	}
}

func TestMaskPasswordsLeavesOtherLinesAlone(t *testing.T) {
	in := `# password "not-a-directive"
	description "password of the realm";
	neighbor 192.0.2.1 as 65001;
`
	if got := MaskPasswords(in); got != in {
		t.Errorf("non-password lines were rewritten:\n%s", got)
	}
}

// A masked render and a masked live file must agree on an unchanged password,
// otherwise the Changes screen shows a permanent phantom diff.
func TestMaskedRenderMatchesMaskedLiveFile(t *testing.T) {
	p := ebgpPeer()
	p.Password = "hunter2"

	// Rendered unmasked (what would land on disk), then masked afterwards.
	unmasked := baseInput()
	unmasked.Peers = append(unmasked.Peers, p)
	onDisk, err := Config(unmasked)
	if err != nil {
		t.Fatal(err)
	}
	shown := baseInput()
	shown.Peers = append(shown.Peers, p)
	shown.MaskSecrets = true
	inBrowser, err := Config(shown)
	if err != nil {
		t.Fatal(err)
	}
	if MaskPasswords(onDisk) != inBrowser {
		t.Error("masking the on-disk config must reproduce the masked render exactly")
	}
}
