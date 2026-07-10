package render

import (
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func rtr() store.RPKIServer {
	return store.RPKIServer{
		ID: 1, Name: "cloudflare", Description: "Public RTR",
		Host: "rtr.rpki.cloudflare.com", Port: 8282, Enabled: true,
		Refresh: 900, Retry: 90, Expire: 172800,
	}
}

func rovPolicy(mode string) store.Policy {
	p := sanityPolicy()
	p.ROV = mode
	return p
}

func TestRPKITablesAndProtocol(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()
	in.RPKIServers = []store.RPKIServer{rtr()}
	in.Policies = []store.Policy{rovPolicy(store.ROVReject)}
	out := mustRender(t, in)

	for _, want := range []string{
		"roa4 table rpki4;",
		"roa6 table rpki6;",
		"protocol rpki cloudflare {",
		`remote "rtr.rpki.cloudflare.com" port 8282;`,
		"refresh keep 900;",
		"retry keep 90;",
		"expire keep 172800;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}

	// The ROA tables must be declared before the functions that read them.
	if strings.Index(out, "roa4 table rpki4;") > strings.Index(out, "roa_check(rpki4") {
		t.Error("BIRD needs the ROA table declared before a filter reads it")
	}
}

// BIRD wants an address literal bare and a hostname quoted.
func TestRPKIRemoteQuoting(t *testing.T) {
	srv := rtr()
	srv.Host, srv.Port = "192.0.2.9", 3323
	in := baseInput()
	in.PrefixSets, in.RPKIServers = bogonSets(), []store.RPKIServer{srv}
	in.Policies = []store.Policy{rovPolicy(store.ROVReject)}

	if out := mustRender(t, in); !strings.Contains(out, "remote 192.0.2.9 port 3323;") {
		t.Error("an IP remote must not be quoted")
	}
}

func TestROVModes(t *testing.T) {
	in := baseInput()
	in.PrefixSets, in.RPKIServers = bogonSets(), []store.RPKIServer{rtr()}

	in.Policies = []store.Policy{rovPolicy(store.ROVReject)}
	fn := block(t, mustRender(t, in), "function imp_IMPORT_SANITY_v4()")
	if !strings.Contains(fn, `if roa_check(rpki4, net, bgp_path.last) = ROA_INVALID then reject "RPKI invalid";`) {
		t.Errorf("reject mode should drop invalids:\n%s", fn)
	}

	in.Policies = []store.Policy{rovPolicy(store.ROVLog)}
	out := mustRender(t, in)
	fn = block(t, out, "function imp_IMPORT_SANITY_v4()")
	if !strings.Contains(fn, "bgp_large_community.add(RPKI_INVALID);") {
		t.Errorf("log mode should tag invalids:\n%s", fn)
	}
	if strings.Contains(fn, `reject "RPKI invalid"`) {
		t.Error("log mode must not drop anything")
	}
	if !strings.Contains(out, "define RPKI_INVALID  = (65551, 2, 1);") {
		t.Error("the RPKI_INVALID community must be defined")
	}

	in.Policies = []store.Policy{rovPolicy(store.ROVOff)}
	if fn := block(t, mustRender(t, in), "function imp_IMPORT_SANITY_v4()"); strings.Contains(fn, "roa_check") {
		t.Error("off mode should emit no validation")
	}
}

// A v6 filter must consult the v6 ROA table.
func TestROVUsesTheRightTablePerFamily(t *testing.T) {
	in := baseInput()
	in.PrefixSets, in.RPKIServers = bogonSets(), []store.RPKIServer{rtr()}
	in.Policies = []store.Policy{rovPolicy(store.ROVReject)}
	out := mustRender(t, in)

	if v6 := block(t, out, "function imp_IMPORT_SANITY_v6()"); !strings.Contains(v6, "roa_check(rpki6,") || strings.Contains(v6, "rpki4") {
		t.Errorf("the v6 function must read rpki6:\n%s", v6)
	}
}

// Validating against a ROA table nothing fills parses fine and protects nothing:
// every lookup returns UNKNOWN. Refuse to render it.
func TestValidatingWithoutAnRTRServerIsAnError(t *testing.T) {
	in := baseInput()
	in.PrefixSets = bogonSets()
	in.Policies = []store.Policy{rovPolicy(store.ROVReject)}
	if _, err := Config(in); err == nil {
		t.Error("expected an error when a policy validates but no RTR server is enabled")
	}

	// A disabled server does not count.
	srv := rtr()
	srv.Enabled = false
	in.RPKIServers = []store.RPKIServer{srv}
	if _, err := Config(in); err == nil {
		t.Error("a disabled RTR server must not satisfy the check")
	}
	if out, err := Config(func() Input { in.Policies = []store.Policy{rovPolicy(store.ROVOff)}; return in }()); err != nil {
		t.Errorf("with validation off, a disabled server is fine: %v", err)
	} else if strings.Contains(out, "protocol rpki") {
		t.Error("a disabled server must not be rendered")
	}
}

func TestLintRPKIFetchedButIgnored(t *testing.T) {
	in := baseInput()
	in.RPKIServers = []store.RPKIServer{rtr()}
	in.Policies = []store.Policy{rovPolicy(store.ROVOff)}
	if ws := findings(t, in, ""); !hasMessage(ws, "fetched and ignored") {
		t.Errorf("expected a warning, got %+v", ws)
	}
}

func TestLintRPKILogOnlyEverywhere(t *testing.T) {
	in := baseInput()
	in.RPKIServers = []store.RPKIServer{rtr()}
	in.Policies = []store.Policy{rovPolicy(store.ROVLog)}
	if ws := findings(t, in, ""); !hasMessage(ws, "log-only mode") {
		t.Errorf("expected a log-only warning, got %+v", ws)
	}

	// Once one policy rejects, the reminder goes away.
	in.Policies = []store.Policy{rovPolicy(store.ROVLog), func() store.Policy { p := rovPolicy(store.ROVReject); p.Name = "STRICT"; return p }()}
	if ws := findings(t, in, ""); hasMessage(ws, "log-only mode") {
		t.Error("with a rejecting policy present, log-only should not be flagged")
	}
}
