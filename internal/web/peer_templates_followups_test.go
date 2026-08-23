package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/store"
)

// A linked peer can keep its own import limit — the one knob that genuinely
// differs between thirty otherwise identical IX peers — and a template save
// leaves that limit alone while still rewriting everything else.
func TestImportLimitOverrideOnALinkedPeer(t *testing.T) {
	env := newTestEnv(t, false)
	tmpl := createTemplate(env, t)

	form := linkedPeerForm(tmpl)
	form.Set("overrideImportLimit", "on")
	form.Set("importLimit", "300000")
	form.Set("importLimitAction", "block")
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d body=%s", rec.Code, rec.Body)
	}
	p, _ := env.store.GetPeerByName("rs1_v4")
	if p.ImportLimit != 300000 || p.ImportLimitAction != "block" || p.TemplateOverrides != store.OverrideImportLimit {
		t.Fatalf("override should keep the posted limit: %+v", p)
	}
	if p.Role != store.RoleIXPeer || !p.GTSM {
		t.Errorf("everything else still comes from the template: %+v", p)
	}

	// The form shows the switch ticked and the limit controls live.
	body := env.do(t, "GET", "/peers/rs1_v4/edit", nil).Body.String()
	if !strings.Contains(body, `name="overrideImportLimit" data-override="importLimit" checked`) {
		t.Error("override switch should render ticked")
	}
	if strings.Contains(body, `value="300000" min="0" data-governed data-override-key="importLimit" disabled`) {
		t.Error("an overridden limit must not be locked")
	}
	if !strings.Contains(body, `name="importLimitAction" data-governed data-override-key="importLimit" >`) {
		t.Error("the limit action follows the same override")
	}

	// A template save rewrites the shape but not the overridden limit.
	tf := templateForm(env, t)
	tf.Set("importLimit", "80000")
	tf.Del("gtsm")
	if rec := env.do(t, "POST", "/peers/templates/IX_PEERS/edit", tf); rec.Code != http.StatusSeeOther {
		t.Fatalf("template save: %s", rec.Body)
	}
	p, _ = env.store.GetPeerByName("rs1_v4")
	if p.ImportLimit != 300000 || p.GTSM {
		t.Errorf("override should survive a template save while GTSM follows it: %+v", p)
	}

	// Unticking hands the limit back to the template.
	form.Del("overrideImportLimit")
	form.Set("importLimit", "1")
	if rec := env.do(t, "POST", "/peers/rs1_v4/edit", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("untick: %s", rec.Body)
	}
	p, _ = env.store.GetPeerByName("rs1_v4")
	if p.ImportLimit != 80000 || p.TemplateOverrides != "" {
		t.Errorf("without the switch the template's limit wins: %+v", p)
	}

	// Detaching keeps the values and drops the override with the link.
	form.Set("overrideImportLimit", "on")
	form.Set("importLimit", "42")
	form.Set("templateId", "")
	if rec := env.do(t, "POST", "/peers/rs1_v4/edit", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("detach: %s", rec.Body)
	}
	p, _ = env.store.GetPeerByName("rs1_v4")
	if p.TemplateID.Valid || p.TemplateOverrides != "" || p.ImportLimit != 42 {
		t.Errorf("an unlinked peer has no overrides, just values: %+v", p)
	}
}

// The seed page offers a template per row; an imported session linked to one
// arrives with the template's role, shape and — unlike any plain seed — chains.
func TestSeedAttachesTemplate(t *testing.T) {
	env := applyReady(t)
	tmpl := createTemplate(env, t)
	env.fc.details["edge_v4"] = seedDetail("edge_v4", "198.51.100.1", "64500", "65551", "external")
	env.fc.protocols = append(env.fc.protocols, birdc.ProtocolSummary{Name: "edge_v6", Proto: "BGP", Table: "---", State: "up", Since: "2026-07-08", Info: "Established"})
	env.fc.details["edge_v6"] = seedDetail("edge_v6", "2001:db8::1", "64500", "65551", "external")
	env.srv.poller.Run(cancelledCtx()) // re-poll so the new session is discoverable

	body := env.do(t, "GET", "/peers/seed", nil).Body.String()
	if !strings.Contains(body, `name="template_edge_v4"`) || !strings.Contains(body, `id="seed-template-all"`) {
		t.Fatal("seed page should offer a template per row and a set-all control")
	}

	f := url.Values{
		"include":      {"edge_v4", "edge_v6"},
		"role_edge_v4": {"upstream"}, "template_edge_v4": {strconv.FormatInt(tmpl.ID, 10)},
		"role_edge_v6": {"upstream"},
	}
	if rec := env.do(t, "POST", "/peers/seed", f); rec.Code != 303 {
		t.Fatalf("seed save: code=%d body=%s", rec.Code, rec.Body.String())
	}
	linked, err := env.store.GetPeerByName("edge_v4")
	if err != nil {
		t.Fatal(err)
	}
	if !linked.TemplateID.Valid || linked.TemplateName != "IX_PEERS" || linked.Role != store.RoleIXPeer || linked.ImportLimit != 50000 {
		t.Errorf("seeded peer should take the template's shape over the row's role: %+v", linked)
	}
	if linked.NeighborIP != "198.51.100.1" || linked.RemoteASN != 64500 {
		t.Errorf("identity still comes from BIRD: %+v", linked)
	}
	if imports, exports, _ := env.store.PeerPolicies(linked.ID); len(imports) != 1 || len(exports) != 1 {
		t.Errorf("seeded peer should carry the template's chains: %v %v", imports, exports)
	}
	plain, err := env.store.GetPeerByName("edge_v6")
	if err != nil {
		t.Fatal(err)
	}
	if plain.TemplateID.Valid || plain.Role != store.RoleUpstream {
		t.Errorf("a row without a template seeds as before: %+v", plain)
	}
	if imports, _, _ := env.store.PeerPolicies(plain.ID); len(imports) != 0 {
		t.Errorf("a plain seed has no chain: %v", imports)
	}
}

// The peers list attaches or detaches many peers at once — the migration path
// for a router whose IX peers were configured one by one before templates.
func TestBulkAttachAndDetach(t *testing.T) {
	env := newTestEnv(t, false)
	tmpl := createTemplate(env, t)
	for _, name := range []string{"rs1_v4", "rs2_v4", "rs3_v4"} {
		form := peerForm()
		form.Set("name", name)
		form.Set("importLimit", "7")
		if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
			t.Fatalf("create %s: %s", name, rec.Body)
		}
	}
	body := env.do(t, "GET", "/peers", nil).Body.String()
	for _, want := range []string{`id="bulk-attach"`, `name="peer" value="rs1_v4" form="bulk-attach"`, `Attach to IX_PEERS`, `data-check-all`} {
		if !strings.Contains(body, want) {
			t.Errorf("peers list should carry the bulk bar, missing %q", want)
		}
	}

	rec := env.do(t, "POST", "/peers/attach", url.Values{"peer": {"rs1_v4", "rs2_v4", "ghost"}, "templateId": {strconv.FormatInt(tmpl.ID, 10)}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("attach: %d", rec.Code)
	}
	if flash := flashOf(rec); !strings.Contains(flash, "Attached 2 peers to IX_PEERS") || !strings.Contains(flash, "Not found: ghost") {
		t.Errorf("flash: %q", flash)
	}
	for _, name := range []string{"rs1_v4", "rs2_v4"} {
		p, _ := env.store.GetPeerByName(name)
		if !p.TemplateID.Valid || p.ImportLimit != 50000 || p.Role != store.RoleIXPeer {
			t.Errorf("%s should be linked with the template's shape: %+v", name, p)
		}
		if imports, _, _ := env.store.PeerPolicies(p.ID); len(imports) != 1 || imports[0].Name != "IMPORT_SANITY" {
			t.Errorf("%s should carry the template's chain: %v", name, imports)
		}
	}
	if p, _ := env.store.GetPeerByName("rs3_v4"); p.TemplateID.Valid || p.ImportLimit != 7 {
		t.Errorf("unselected peer must be untouched: %+v", p)
	}

	rec = env.do(t, "POST", "/peers/attach", url.Values{"peer": {"rs1_v4"}, "templateId": {""}})
	if flash := flashOf(rec); !strings.Contains(flash, "Detached 1 peer") {
		t.Errorf("detach flash: %q", flash)
	}
	if p, _ := env.store.GetPeerByName("rs1_v4"); p.TemplateID.Valid || p.ImportLimit != 50000 {
		t.Errorf("detach keeps the values and drops the link: %+v", p)
	}
	if p, _ := env.store.GetPeerByName("rs2_v4"); !p.TemplateID.Valid {
		t.Error("rs2_v4 stays linked")
	}

	// Nothing selected, or a template that vanished, is a message — not a 500.
	if rec := env.do(t, "POST", "/peers/attach", url.Values{"templateId": {"1"}}); rec.Code != http.StatusSeeOther || !strings.Contains(flashOf(rec), "Select at least one") {
		t.Errorf("empty selection: code=%d flash=%q", rec.Code, flashOf(rec))
	}
	if rec := env.do(t, "POST", "/peers/attach", url.Values{"peer": {"rs3_v4"}, "templateId": {"999"}}); !strings.Contains(flashOf(rec), "no longer exists") {
		t.Errorf("missing template: flash=%q", flashOf(rec))
	}
}

func TestPeerNamedAttachIsRefused(t *testing.T) {
	env := newTestEnv(t, false)
	form := peerForm()
	form.Set("name", "attach")
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cannot be a peer") {
		t.Errorf("a peer named attach would shadow /peers/attach: code=%d", rec.Code)
	}
}
