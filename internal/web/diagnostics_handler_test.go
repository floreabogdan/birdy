package web

import (
	"strings"
	"testing"
)

// Diagnostics are opt-in: off by default, the page explains how to turn them on
// and never runs anything even if a target is passed.
func TestDiagnosticsDisabledByDefault(t *testing.T) {
	env := newTestEnv(t, false)

	body := env.do(t, "GET", "/diagnostics", nil).Body.String()
	if !strings.Contains(body, "Diagnostics are disabled") {
		t.Error("the page should show the disabled state by default")
	}
	// A target must not trigger a run while disabled.
	body = env.do(t, "GET", "/diagnostics?tool=ping&target=192.0.2.1", nil).Body.String()
	if !strings.Contains(body, "Diagnostics are disabled") {
		t.Error("a target must not run anything while disabled")
	}
}

// Enabled, the page shows the form; a malformed target is rejected before any
// command runs.
func TestDiagnosticsEnabledValidatesTarget(t *testing.T) {
	env := newTestEnv(t, false, func(c *Config) { c.NetDiag = true })

	body := env.do(t, "GET", "/diagnostics", nil).Body.String()
	if !strings.Contains(body, "Run a check") || strings.Contains(body, "Diagnostics are disabled") {
		t.Error("the enabled page should show the run form")
	}

	// A target that could be read as a flag (leading dash) is refused (no exec).
	body = env.do(t, "GET", "/diagnostics?tool=ping&target=-x", nil).Body.String()
	if !strings.Contains(body, "valid IP address or hostname") {
		t.Error("a flag-like target should be rejected with a validation message")
	}
}
