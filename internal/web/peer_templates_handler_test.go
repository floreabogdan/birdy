package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// templateForm is an IX route-server template: first-AS off, a 50k limit, the
// stock sanity import and EXPORT_OWN export, posted as the peer form posts it.
func templateForm(env *testEnv, t *testing.T) url.Values {
	t.Helper()
	sanity, err := env.store.GetPolicyByName("IMPORT_SANITY")
	if err != nil {
		t.Fatal(err)
	}
	own, err := env.store.GetPolicyByName("EXPORT_OWN")
	if err != nil {
		t.Fatal(err)
	}
	return url.Values{
		"name": {"IX_PEERS"}, "description": {"route servers"}, "role": {"ix_peer"},
		"importLimit": {"50000"}, "importLimitAction": {"restart"}, "gtsm": {"on"}, "gracefulRestart": {"aware"},
		"importPolicyIds": {strconv.FormatInt(sanity.ID, 10)}, "exportPolicyIds": {strconv.FormatInt(own.ID, 10)},
	}
}

func createTemplate(env *testEnv, t *testing.T) store.PeerTemplate {
	t.Helper()
	withIdentity(t, env)
	if rec := env.do(t, "POST", "/peers/templates/new", templateForm(env, t)); rec.Code != http.StatusSeeOther {
		t.Fatalf("create template: code=%d body=%s", rec.Code, rec.Body)
	}
	tmpl, err := env.store.GetPeerTemplateByName("IX_PEERS")
	if err != nil {
		t.Fatalf("template not stored: %v", err)
	}
	return tmpl
}

// linkedPeerForm posts a peer that names the template and — deliberately —
// carries governed values that contradict it, the way a stale or hand-built
// request could. The template must win.
func linkedPeerForm(tmpl store.PeerTemplate) url.Values {
	form := peerForm()
	form.Set("name", "rs1_v4")
	form.Set("templateId", strconv.FormatInt(tmpl.ID, 10))
	form.Set("role", "upstream")
	form.Set("importLimit", "7")
	form.Set("enforceFirstAs", "on")
	form.Del("importPolicyIds")
	return form
}

func TestPeerTemplateCreateEditDeleteRoundTrip(t *testing.T) {
	env := newTestEnv(t, false)
	tmpl := createTemplate(env, t)
	if tmpl.Role != store.RoleIXPeer || tmpl.ImportLimit != 50000 || tmpl.EnforceFirstAS || !tmpl.GTSM {
		t.Errorf("stored template wrong: %+v", tmpl)
	}
	if len(tmpl.ImportPolicies) != 1 || tmpl.ImportPolicies[0].Name != "IMPORT_SANITY" || len(tmpl.ExportPolicies) != 1 {
		t.Errorf("chains not stored: %+v", tmpl)
	}

	rec := env.do(t, "GET", "/peers/templates", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "IX_PEERS") || !strings.Contains(body, "IMPORT_SANITY") {
		t.Errorf("templates list should show the template and its chain: code=%d", rec.Code)
	}
	if strings.Contains(body, "Raw BIRD output") {
		t.Error("/peers/templates rendered the live session page: the literal route must outrank /peers/{name}")
	}

	rec = env.do(t, "GET", "/peers/templates/IX_PEERS/edit", nil)
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `value="IX_PEERS"`) || !strings.Contains(body, "No peers linked yet") {
		t.Errorf("template editor: code=%d", rec.Code)
	}
	for _, identity := range []string{`name="neighborIp"`, `name="password"`, `name="templateId"`, `name="drained"`, `name="enabled"`} {
		if strings.Contains(body, identity) {
			t.Errorf("template editor must not offer the per-peer field %s", identity)
		}
	}
	if !strings.Contains(body, "protocol bgp IX_PEERS") || !strings.Contains(body, "192.0.2.1") {
		t.Error("template editor should preview the shape on the sample neighbor")
	}

	form := templateForm(env, t)
	form.Set("name", "IX_RS")
	form.Set("importLimit", "60000")
	if rec := env.do(t, "POST", "/peers/templates/IX_PEERS/edit", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: code=%d body=%s", rec.Code, rec.Body)
	}
	if got, err := env.store.GetPeerTemplateByName("IX_RS"); err != nil || got.ImportLimit != 60000 {
		t.Errorf("edit not stored: %+v %v", got, err)
	}

	if rec := env.do(t, "POST", "/peers/templates/IX_RS/delete", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: code=%d", rec.Code)
	}
	if _, err := env.store.GetPeerTemplateByName("IX_RS"); err != store.ErrNotFound {
		t.Errorf("template should be gone, got %v", err)
	}
}

func TestPeerTemplateInvalidInputRedisplaysForm(t *testing.T) {
	env := newTestEnv(t, false)
	form := templateForm(env, t)
	form.Set("name", "bad name")
	form.Set("prependCount", "99")
	rec := env.do(t, "POST", "/peers/templates/new", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the editor to be redisplayed, got %d", rec.Code)
	}
	for _, want := range []string{"starting with a letter", "between 0 and 10"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("missing error %q", want)
		}
	}
	if list, _ := env.store.ListPeerTemplates(); len(list) != 0 {
		t.Error("an invalid template must not be stored")
	}
	// And a name clash is a form error, not a 500.
	createTemplate(env, t)
	if rec := env.do(t, "POST", "/peers/templates/new", templateForm(env, t)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "already exists") {
		t.Errorf("duplicate name: code=%d", rec.Code)
	}
}

func TestLinkedPeerTakesItsShapeFromTheTemplate(t *testing.T) {
	env := newTestEnv(t, false)
	tmpl := createTemplate(env, t)

	if rec := env.do(t, "POST", "/peers/new", linkedPeerForm(tmpl)); rec.Code != http.StatusSeeOther {
		t.Fatalf("create linked peer: code=%d body=%s", rec.Code, rec.Body)
	}
	p, err := env.store.GetPeerByName("rs1_v4")
	if err != nil {
		t.Fatal(err)
	}
	if !p.TemplateID.Valid || p.TemplateName != "IX_PEERS" {
		t.Fatalf("peer should be linked: %+v", p)
	}
	if p.Role != store.RoleIXPeer || p.ImportLimit != 50000 || p.EnforceFirstAS || !p.GTSM {
		t.Errorf("posted governed values must lose to the template: %+v", p)
	}
	if p.RemoteASN != 64497 || p.NeighborIP != "198.51.100.1" || !p.Enabled {
		t.Errorf("identity must come from the form: %+v", p)
	}
	imports, exports, _ := env.store.PeerPolicies(p.ID)
	if len(imports) != 1 || imports[0].Name != "IMPORT_SANITY" || len(exports) != 1 || exports[0].Name != "EXPORT_OWN" {
		t.Errorf("chain must come from the template: imports=%v exports=%v", imports, exports)
	}

	// The edit form renders the link: picker selected, governed controls locked,
	// and the inherited note naming the template.
	rec := env.do(t, "GET", "/peers/rs1_v4/edit", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("edit form: %d", rec.Code)
	}
	for _, want := range []string{
		`<option value="` + strconv.FormatInt(tmpl.ID, 10) + `" selected>IX_PEERS`,
		`data-locked`,
		`id="importLimit" name="importLimit" class="mono" value="50000" min="0" data-governed disabled`,
		`Inherited from template <a href="/peers/templates/IX_PEERS/edit"`,
		`Edit template IX_PEERS`,
		`Shape inherited from peer template IX_PEERS`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("linked peer's form should contain %q", want)
		}
	}
	// The password stays the peer's own: the field is live.
	if strings.Contains(body, `name="password" value="" autocomplete="new-password" placeholder="none" disabled`) {
		t.Error("the password field must not be locked by a template")
	}

	// The peers list carries the chip and the ?template= filter narrows to it.
	if rec := env.do(t, "POST", "/peers/new", peerForm()); rec.Code != http.StatusSeeOther {
		t.Fatal("second, unlinked peer")
	}
	body = env.do(t, "GET", "/peers?template=IX_PEERS", nil).Body.String()
	if !strings.Contains(body, "from IX_PEERS") || strings.Contains(body, "transit_v4") {
		t.Error("/peers?template= should list only the template's peers, with the chip")
	}
	body = env.do(t, "GET", "/peers", nil).Body.String()
	if !strings.Contains(body, "transit_v4") || !strings.Contains(body, "rs1_v4") {
		t.Error("/peers without a filter should list both")
	}
}

func TestTemplateSaveRewritesLinkedPeersAndSaysSo(t *testing.T) {
	env := newTestEnv(t, false)
	tmpl := createTemplate(env, t)
	if rec := env.do(t, "POST", "/peers/new", linkedPeerForm(tmpl)); rec.Code != http.StatusSeeOther {
		t.Fatalf("create linked peer: %s", rec.Body)
	}

	form := templateForm(env, t)
	form.Set("importLimit", "80000")
	form.Del("gtsm")
	form.Del("exportPolicyIds")
	rec := env.do(t, "POST", "/peers/templates/IX_PEERS/edit", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit template: code=%d body=%s", rec.Code, rec.Body)
	}
	if flash := flashOf(rec); !strings.Contains(flash, "1 linked peer") || !strings.Contains(flash, "Changes") {
		t.Errorf("flash should say how many peers changed and where to review: %q", flash)
	}
	p, _ := env.store.GetPeerByName("rs1_v4")
	if p.ImportLimit != 80000 || p.GTSM {
		t.Errorf("linked peer should follow the template: %+v", p)
	}
	if _, exports, _ := env.store.PeerPolicies(p.ID); len(exports) != 0 {
		t.Errorf("linked peer's export chain should follow the template: %v", exports)
	}
	// The editor now warns about the linked peer.
	if body := env.do(t, "GET", "/peers/templates/IX_PEERS/edit", nil).Body.String(); !strings.Contains(body, "1 peer linked") || !strings.Contains(body, "Save and update 1 peer") {
		t.Error("template editor should show how many peers a save rewrites")
	}
	// And the peer shows up in the rendered config with the inheritance comment.
	if body := env.do(t, "GET", "/export/config", nil).Body.String(); !strings.Contains(body, "# Shape inherited from peer template IX_PEERS") {
		t.Error("rendered config should mark the linked peer")
	}
}

func TestDetachKeepsTheShapeAndStopsFollowing(t *testing.T) {
	env := newTestEnv(t, false)
	tmpl := createTemplate(env, t)
	if rec := env.do(t, "POST", "/peers/new", linkedPeerForm(tmpl)); rec.Code != http.StatusSeeOther {
		t.Fatalf("create linked peer: %s", rec.Body)
	}
	sanity, _ := env.store.GetPolicyByName("IMPORT_SANITY")

	// Choosing "none" re-enables the controls, which then post the values the
	// template filled in — the browser does exactly this.
	form := peerForm()
	form.Set("name", "rs1_v4")
	form.Set("templateId", "")
	form.Set("role", "ix_peer")
	form.Set("importLimit", "50000")
	form.Del("enforceFirstAs")
	form.Set("gtsm", "on")
	form.Set("importPolicyIds", strconv.FormatInt(sanity.ID, 10))
	if rec := env.do(t, "POST", "/peers/rs1_v4/edit", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("detach: code=%d body=%s", rec.Code, rec.Body)
	}
	p, _ := env.store.GetPeerByName("rs1_v4")
	if p.TemplateID.Valid || p.TemplateName != "" || p.ImportLimit != 50000 || p.Role != store.RoleIXPeer {
		t.Errorf("detach should drop the link and keep the values: %+v", p)
	}
	// A template save no longer reaches it.
	tf := templateForm(env, t)
	tf.Set("importLimit", "1")
	rec := env.do(t, "POST", "/peers/templates/IX_PEERS/edit", tf)
	if flash := flashOf(rec); !strings.Contains(flash, "No peers are linked") {
		t.Errorf("flash after saving an unused template: %q", flash)
	}
	if p, _ = env.store.GetPeerByName("rs1_v4"); p.ImportLimit != 50000 {
		t.Errorf("detached peer must not follow the template: %+v", p)
	}
}

func TestPeerPreviewAppliesTheTemplate(t *testing.T) {
	env := newTestEnv(t, false)
	tmpl := createTemplate(env, t)
	rec := env.do(t, "POST", "/peers/preview", linkedPeerForm(tmpl))
	var resp previewResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("preview json: %v\n%s", err, rec.Body)
	}
	if resp.Err != "" {
		t.Fatalf("preview error: %s", resp.Err)
	}
	for _, want := range []string{"Shape inherited from peer template IX_PEERS", "imp_IMPORT_SANITY_v4();", "import limit 50000", "bgp_large_community.add(FROM_IX)"} {
		if !strings.Contains(resp.Preview, want) {
			t.Errorf("preview should reflect the template, missing %q:\n%s", want, resp.Preview)
		}
	}
	if strings.Contains(resp.Preview, "first AS is not the peer AS") {
		t.Error("the posted first-AS check must lose to the template's")
	}

	// A template that vanished between page load and preview is a message, not a 500.
	form := linkedPeerForm(tmpl)
	form.Set("templateId", "999")
	rec = env.do(t, "POST", "/peers/preview", form)
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || !strings.Contains(resp.Err, "no longer exists") {
		t.Errorf("missing template should be reported: %+v %v", resp, err)
	}
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "no longer exists") {
		t.Errorf("saving with a missing template should redisplay the form: code=%d", rec.Code)
	}
}

func TestTemplateEditorPreviewRendersSampleNeighbor(t *testing.T) {
	env := newTestEnv(t, false)
	withIdentity(t, env)
	rec := env.do(t, "POST", "/peers/templates/preview", templateForm(env, t))
	var resp previewResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("preview json: %v\n%s", err, rec.Body)
	}
	if resp.Err != "" {
		t.Fatalf("preview error: %s", resp.Err)
	}
	for _, want := range []string{"protocol bgp IX_PEERS {", "neighbor 192.0.2.1 as 64496;", "import limit 50000 action restart;", "ttl security on;"} {
		if !strings.Contains(resp.Preview, want) {
			t.Errorf("template preview missing %q:\n%s", want, resp.Preview)
		}
	}
}

func TestSaveAsTemplateCapturesAndLinksThePeer(t *testing.T) {
	env := newTestEnv(t, false)
	form := peerForm()
	form.Set("importLimit", "123456")
	form.Set("gtsm", "on")
	if rec := env.do(t, "POST", "/peers/new", form); rec.Code != http.StatusSeeOther {
		t.Fatal("create peer")
	}
	// The peer's edit page offers it.
	if body := env.do(t, "GET", "/peers/transit_v4/edit", nil).Body.String(); !strings.Contains(body, `href="/peers/templates/new?from=transit_v4"`) {
		t.Error("peer edit page should offer Save as template")
	}
	rec := env.do(t, "GET", "/peers/templates/new?from=transit_v4", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `value="123456"`) || !strings.Contains(body, `name="linkSource" checked`) || !strings.Contains(body, `name="from" value="transit_v4"`) {
		t.Errorf("save-as-template form should be prefilled from the peer and offer to link it: code=%d", rec.Code)
	}

	tf := url.Values{"name": {"TRANSIT"}, "role": {"upstream"}, "importLimit": {"123456"}, "importLimitAction": {"restart"},
		"enforceFirstAs": {"on"}, "gtsm": {"on"}, "gracefulRestart": {"aware"}, "from": {"transit_v4"}, "linkSource": {"on"}}
	rec = env.do(t, "POST", "/peers/templates/new", tf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create from peer: code=%d body=%s", rec.Code, rec.Body)
	}
	if flash := flashOf(rec); !strings.Contains(flash, "transit_v4 is now linked") {
		t.Errorf("flash should confirm the link: %q", flash)
	}
	p, _ := env.store.GetPeerByName("transit_v4")
	if !p.TemplateID.Valid || p.TemplateName != "TRANSIT" || p.ImportLimit != 123456 || !p.GTSM {
		t.Errorf("source peer should be linked with its shape intact: %+v", p)
	}

	// Without the tick, the template is created and the peer left alone.
	tf.Set("name", "TRANSIT2")
	tf.Del("linkSource")
	if rec := env.do(t, "POST", "/peers/templates/new", tf); rec.Code != http.StatusSeeOther {
		t.Fatal("create without link")
	}
	if p, _ = env.store.GetPeerByName("transit_v4"); p.TemplateName != "TRANSIT" {
		t.Errorf("unticked link must not relink the peer: %+v", p)
	}
}

func TestDeleteTemplateInUseIsRefusedWithNames(t *testing.T) {
	env := newTestEnv(t, false)
	tmpl := createTemplate(env, t)
	if rec := env.do(t, "POST", "/peers/new", linkedPeerForm(tmpl)); rec.Code != http.StatusSeeOther {
		t.Fatal("create linked peer")
	}
	rec := env.do(t, "POST", "/peers/templates/IX_PEERS/delete", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	if flash := flashOf(rec); !strings.Contains(flash, "used by 1 peer(s): rs1_v4") || !strings.Contains(flash, "Detach") {
		t.Errorf("refusal should name the peer and say what to do: %q", flash)
	}
	if _, err := env.store.GetPeerTemplateByName("IX_PEERS"); err != nil {
		t.Error("template must survive a refused delete")
	}
	// The policies page still counts the linked peer, and the policy cannot go.
	sanity, _ := env.store.GetPolicyByName("IMPORT_SANITY")
	rec = env.do(t, "POST", "/policies/IMPORT_SANITY/delete", nil)
	if flash := flashOf(rec); !strings.Contains(flash, "1 peer(s) and 1 peer template(s)") {
		t.Errorf("policy delete should count peers and templates: %q", flash)
	}
	if _, err := env.store.GetPolicyByName("IMPORT_SANITY"); err != nil {
		t.Errorf("policy must survive: %v", err)
	}
	_ = sanity
}

func TestNewPeerFromTemplateStartsLinked(t *testing.T) {
	env := newTestEnv(t, false)
	tmpl := createTemplate(env, t)
	rec := env.do(t, "GET", "/peers/new?template=IX_PEERS", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `<option value="`+strconv.FormatInt(tmpl.ID, 10)+`" selected>IX_PEERS`) || !strings.Contains(body, `value="50000"`) {
		t.Errorf("new peer from template should start linked with the template's values: code=%d", rec.Code)
	}
	// A clone of a linked peer stays linked.
	if rec := env.do(t, "POST", "/peers/new", linkedPeerForm(tmpl)); rec.Code != http.StatusSeeOther {
		t.Fatal("create linked peer")
	}
	if body := env.do(t, "GET", "/peers/new?from=rs1_v4", nil).Body.String(); !strings.Contains(body, `" selected>IX_PEERS`) {
		t.Error("cloning a linked peer should keep the link")
	}
}

func TestPeerNamedTemplatesIsRefused(t *testing.T) {
	env := newTestEnv(t, false)
	form := peerForm()
	form.Set("name", "templates")
	rec := env.do(t, "POST", "/peers/new", form)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cannot be a peer") {
		t.Errorf("a peer named templates would be unreachable behind /peers/templates: code=%d", rec.Code)
	}
}
