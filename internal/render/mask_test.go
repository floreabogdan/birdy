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

// The raw block is free text, and BIRD does not care where a statement starts.
// A password that is not first on its line — a one-line protocol, two options
// on a line, the keyword and its string on separate lines — must be masked all
// the same, or it reaches the Changes page and the config-backup email.
func TestMaskPasswordsAnywhereOnALine(t *testing.T) {
	cases := map[string]string{
		"one-line protocol":       `protocol ospf v2 o { area 0 { interface "eth0" { authentication simple; password "s3cretOSPF"; }; }; }`,
		"two on a line":           "\tneighbor 192.0.2.1 as 65001; password \"fake-one\"; password \"fake-two\";",
		"no space":                `password"fake-tight";`,
		"keyword on its own line": "\tpassword\n\t\t\"fake-split\";",
		"hash inside a string":    `description "#1 transit"; password "fake-after-hash";`,
		"block comment before":    `/* rotated */ password "fake-after-comment";`,
		"keyword inside a string": `description "password "; password "fake-after-quote";`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := MaskPasswords(in)
			for _, secret := range []string{"s3cretOSPF", "fake-one", "fake-two", "fake-tight", "fake-split", "fake-after-hash", "fake-after-comment", "fake-after-quote"} {
				if strings.Contains(got, secret) {
					t.Errorf("secret %q survived masking:\n%s", secret, got)
				}
			}
			if !strings.Contains(got, `"`+MaskedPassword+`"`) {
				t.Errorf("nothing was masked:\n%s", got)
			}
		})
	}
}

// Masking replaces the secret and nothing else: a string that only mentions
// the word, a commented-out line, and a word that merely ends in "password"
// are left as they are, byte for byte.
func TestMaskPasswordsTouchesOnlyTheSecret(t *testing.T) {
	in := "protocol bgp a { description \"rotate password \"; neighbor 192.0.2.1 as 65001; # password \"commented\"\n" +
		"\t/* password \"in a block comment\" */ mypassword \"not-the-keyword\"; password \"fake\"; }\n"
	want := "protocol bgp a { description \"rotate password \"; neighbor 192.0.2.1 as 65001; # password \"commented\"\n" +
		"\t/* password \"in a block comment\" */ mypassword \"not-the-keyword\"; password \"" + MaskedPassword + "\"; }\n"
	if got := MaskPasswords(in); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The masked render is what the Changes page and the config preview show.
func TestMaskedRenderHidesRawConfigPasswords(t *testing.T) {
	in := baseInput()
	in.RawConfig = `protocol ospf v2 o { area 0 { interface "eth0" { authentication simple; password "s3cretOSPF"; }; }; }`
	in.MaskSecrets = true
	if cfg := mustRender(t, in); strings.Contains(cfg, "s3cretOSPF") {
		t.Errorf("a raw-config password reached the masked render:\n%s", cfg)
	}
}
