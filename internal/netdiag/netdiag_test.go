package netdiag

import (
	"context"
	"testing"
)

func TestValidTarget(t *testing.T) {
	good := []string{"192.0.2.1", "2001:db8::1", "example.com", "host-1.example.com", "localhost", "a1.b2.c3"}
	for _, g := range good {
		if !ValidTarget(g) {
			t.Errorf("%q should be a valid target", g)
		}
	}
	// Anything that could start a flag, split a command, or carry a shell
	// metacharacter must be rejected.
	bad := []string{"", "-flag", "-", "a b", "a;b", "a|b", "a&b", "$(x)", "`x`", "a/b", "..", "1.2.3.4; rm -rf /", "host name.com", "a\nb"}
	for _, b := range bad {
		if ValidTarget(b) {
			t.Errorf("%q should be rejected", b)
		}
	}
}

// An invalid target is refused before anything is executed.
func TestRunRejectsInvalidTargetBeforeExec(t *testing.T) {
	res := Run(context.Background(), Ping, "-oops; rm -rf /")
	if res.Err == "" {
		t.Error("an invalid target must be rejected")
	}
	if res.Output != "" || res.Command != "" {
		t.Errorf("nothing should have run for an invalid target: %+v", res)
	}
}

// An unknown tool is refused.
func TestRunRejectsUnknownTool(t *testing.T) {
	if res := Run(context.Background(), Tool("nmap"), "192.0.2.1"); res.Err == "" {
		t.Error("an unknown tool must be refused")
	}
}
