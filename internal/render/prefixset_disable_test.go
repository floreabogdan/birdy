package render

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

func originatedSet(disabled bool) store.PrefixSet {
	return store.PrefixSet{
		ID: 1, Name: "MY_ANYCAST", Family: store.FamilyV4,
		Originate: true, OriginateAction: store.OriginateBlackhole,
		Disabled: disabled,
		Entries:  []store.PrefixEntry{{Prefix: "203.0.113.0/24"}},
	}
}

// A disabled set renders neither its define nor its originator — only a breadcrumb
// comment. A filter that still names it is left to fail the pre-apply bird -p.
func TestDisabledPrefixSetNotRendered(t *testing.T) {
	in := baseInput()
	in.PrefixSets = []store.PrefixSet{originatedSet(true)}
	cfg := mustRender(t, in)

	if strings.Contains(cfg, "define MY_ANYCAST") {
		t.Error("a disabled set must not render its define")
	}
	if strings.Contains(cfg, "originate_MY_ANYCAST") {
		t.Error("a disabled set must not render its originator")
	}
	if !strings.Contains(cfg, "MY_ANYCAST is disabled and was not rendered") {
		t.Errorf("a disabled set should leave a breadcrumb comment:\n%s", cfg)
	}
}

// The zero value is enabled: an ordinary set still renders its define and its
// originator, exactly as before the flag existed.
func TestEnabledPrefixSetStillRenders(t *testing.T) {
	in := baseInput()
	in.PrefixSets = []store.PrefixSet{originatedSet(false)}
	cfg := mustRender(t, in)

	if !strings.Contains(cfg, "define MY_ANYCAST = [") {
		t.Error("an enabled set must render its define")
	}
	if !strings.Contains(cfg, "protocol static originate_MY_ANYCAST") {
		t.Error("an enabled originated set must render its originator")
	}
}

// Disabling a set cascades: a policy that announces it drops the reference rather
// than emitting a name whose define was withheld (which would fail bird -p).
func TestDisabledExportSetDroppedFromPolicy(t *testing.T) {
	in := baseInput()
	in.PrefixSets = []store.PrefixSet{{
		ID: 5, Name: "ANNOUNCE_X", Family: store.FamilyV4, Disabled: true,
		Entries: []store.PrefixEntry{{Prefix: "203.0.113.0/24"}},
	}}
	in.Policies = []store.Policy{{
		ID: 2, Name: "EXPORT_X", Direction: store.DirExport, SetIDs: []int64{5},
	}}
	cfg := mustRender(t, in)

	fn := block(t, cfg, "function exp_EXPORT_X_v4()")
	if strings.Contains(fn, "net ~ ANNOUNCE_X") {
		t.Errorf("a disabled set must not be announced:\n%s", fn)
	}
	if !strings.Contains(fn, "ANNOUNCE_X is disabled") {
		t.Errorf("the dropped announce should leave a breadcrumb:\n%s", fn)
	}
	if strings.Contains(cfg, "define ANNOUNCE_X") {
		t.Error("the disabled set's define must stay withheld")
	}
}

// An import allow-list built on a disabled set must fail closed — permit nothing —
// not drop the membership check and accept everything.
func TestDisabledImportAllowListFailsClosed(t *testing.T) {
	in := baseInput()
	in.PrefixSets = []store.PrefixSet{{
		ID: 6, Name: "CUST_IN", Family: store.FamilyV4, Disabled: true,
		Entries: []store.PrefixEntry{{Prefix: "198.51.100.0/24"}},
	}}
	in.Policies = []store.Policy{{
		ID: 3, Name: "IMPORT_X", Direction: store.DirImport,
		AcceptOnlySetID: sql.NullInt64{Int64: 6, Valid: true},
	}}
	cfg := mustRender(t, in)

	fn := block(t, cfg, "function imp_IMPORT_X_v4()")
	if strings.Contains(fn, "! (net ~ CUST_IN)") {
		t.Errorf("a disabled allow-list must not render its membership check:\n%s", fn)
	}
	if !strings.Contains(fn, `reject "CUST_IN is disabled`) {
		t.Errorf("a disabled allow-list must fail closed (reject all):\n%s", fn)
	}
}

// The silent cascade is surfaced by lint: a danger for the fail-closed import,
// a warning for the dropped announce.
func TestDisabledSetReferencesLinted(t *testing.T) {
	in := baseInput()
	in.PrefixSets = []store.PrefixSet{
		{ID: 5, Name: "ANNOUNCE_X", Family: store.FamilyV4, Disabled: true, Entries: []store.PrefixEntry{{Prefix: "203.0.113.0/24"}}},
		{ID: 6, Name: "CUST_IN", Family: store.FamilyV4, Disabled: true, Entries: []store.PrefixEntry{{Prefix: "198.51.100.0/24"}}},
	}
	in.Policies = []store.Policy{
		{ID: 2, Name: "EXPORT_X", Direction: store.DirExport, SetIDs: []int64{5}},
		{ID: 3, Name: "IMPORT_X", Direction: store.DirImport, AcceptOnlySetID: sql.NullInt64{Int64: 6, Valid: true}},
	}

	var gotImport, gotExport bool
	for _, w := range Lint(in) {
		if strings.Contains(w.Message, "CUST_IN") && w.Severity == SeverityDanger {
			gotImport = true
		}
		if strings.Contains(w.Message, "ANNOUNCE_X") && w.Severity == SeverityWarn {
			gotExport = true
		}
	}
	if !gotImport {
		t.Error("lint should flag a disabled import allow-list as a danger")
	}
	if !gotExport {
		t.Error("lint should warn about a disabled announced set")
	}
}
