package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Every field of Peer is either identity — it stays the peer's own — or shape,
// owned by the template. A new peer knob that lands in neither list fails here,
// so nobody can add one without deciding which side it lives on, and then
// PeerTemplate, ApplyTo and TemplateFromPeer are checked to carry every shape
// field under the same name and type.
func TestEveryPeerFieldIsClassified(t *testing.T) {
	identity := []string{
		"ID", "Name", "Description", "Enabled", "NeighborIP", "RemoteASN", "LocalIP",
		"Interface", "TransportEndpoint", "Password", "Drained",
		"TemplateID", "TemplateName", "TemplateOverrides",
	}
	shape := []string{
		"Role", "Multihop", "Passive", "ImportLimit", "ImportLimitAction",
		"ImportCommunities", "ExportCommunities", "PrependCount",
		"EnforceFirstAS", "OriginPeerOnly", "BGPRole", "GTSM", "BFD", "GracefulRestart",
		"NextHopSelf", "RRClient", "IBGPExportDefault", "ImportPolicies", "ExportPolicies",
	}
	peerT := reflect.TypeOf(Peer{})
	tmplT := reflect.TypeOf(PeerTemplate{})
	for i := 0; i < peerT.NumField(); i++ {
		f := peerT.Field(i)
		isID, isShape := slices.Contains(identity, f.Name), slices.Contains(shape, f.Name)
		if isID == isShape {
			t.Errorf("Peer.%s must be listed as exactly one of identity or shape (is it something a template should own?)", f.Name)
		}
		if isShape {
			tf, ok := tmplT.FieldByName(f.Name)
			if !ok {
				t.Errorf("PeerTemplate lacks %s; add the column, the struct field, and the ApplyTo/TemplateFromPeer lines", f.Name)
			} else if tf.Type != f.Type {
				t.Errorf("PeerTemplate.%s is %s, Peer.%s is %s", f.Name, tf.Type, f.Name, f.Type)
			}
		}
	}
	for _, name := range append(identity, shape...) {
		if _, ok := peerT.FieldByName(name); !ok {
			t.Errorf("%s is listed here but Peer has no such field", name)
		}
	}

	// Now the behavioural half: ApplyTo must change every shape field and no
	// identity field. Use a template whose every field differs from the peer's.
	p := Peer{
		ID: 7, Name: "rs1_v4", Description: "LONAP RS1", Enabled: true, NeighborIP: "198.51.100.10",
		RemoteASN: 64500, LocalIP: "198.51.100.1", Interface: "eth1", TransportEndpoint: "192.0.2.9",
		Password: "s3cret", Drained: true,
		Role: RoleUpstream, Multihop: 2, Passive: true, ImportLimit: 10, ImportLimitAction: "warn",
		ImportCommunities: "65000:1", ExportCommunities: "65000:2", PrependCount: 1,
		EnforceFirstAS: true, OriginPeerOnly: true, BGPRole: true, GTSM: true, BFD: true, GracefulRestart: GROn,
		NextHopSelf: true, RRClient: true, IBGPExportDefault: IBGPExportNone,
		ImportPolicies: []Policy{{ID: 1}}, ExportPolicies: []Policy{{ID: 2}},
	}
	tmpl := PeerTemplate{
		ID: 3, Name: "IX_PEERS",
		Role: RoleIXPeer, Multihop: 0, Passive: false, ImportLimit: 50000, ImportLimitAction: "restart",
		ImportCommunities: "65000:3", ExportCommunities: "65000:4", PrependCount: 0,
		EnforceFirstAS: false, OriginPeerOnly: false, BGPRole: false, GTSM: false, BFD: false, GracefulRestart: GRAware,
		NextHopSelf: false, RRClient: false, IBGPExportDefault: IBGPExportAll,
		ImportPolicies: []Policy{{ID: 5}}, ExportPolicies: []Policy{{ID: 6}},
	}
	before := p
	tmpl.ApplyTo(&p)
	pv, bv, tv := reflect.ValueOf(p), reflect.ValueOf(before), reflect.ValueOf(tmpl)
	for _, name := range identity {
		if strings.HasPrefix(name, "Template") {
			continue // the link itself is what ApplyTo writes
		}
		if !reflect.DeepEqual(pv.FieldByName(name).Interface(), bv.FieldByName(name).Interface()) {
			t.Errorf("ApplyTo changed identity field %s: %v -> %v", name, bv.FieldByName(name), pv.FieldByName(name))
		}
	}
	for _, name := range shape {
		if !reflect.DeepEqual(pv.FieldByName(name).Interface(), tv.FieldByName(name).Interface()) {
			t.Errorf("ApplyTo did not copy shape field %s: peer has %v, template has %v", name, pv.FieldByName(name), tv.FieldByName(name))
		}
	}
	if !p.TemplateID.Valid || p.TemplateID.Int64 != 3 || p.TemplateName != "IX_PEERS" {
		t.Errorf("ApplyTo should record the link, got id=%v name=%q", p.TemplateID, p.TemplateName)
	}

	// And the round trip: capturing a peer's shape and applying it back is the identity.
	again := TemplateFromPeer(p)
	again.ID, again.Name = tmpl.ID, tmpl.Name
	if !reflect.DeepEqual(again, tmpl) {
		t.Errorf("TemplateFromPeer(ApplyTo(p)) != template:\n got %+v\nwant %+v", again, tmpl)
	}
}

func TestApplyToHonoursImportLimitOverride(t *testing.T) {
	p := Peer{ImportLimit: 300000, ImportLimitAction: "block", TemplateOverrides: OverrideImportLimit}
	tmpl := PeerTemplate{ID: 1, ImportLimit: 50000, ImportLimitAction: "restart", Role: RoleIXPeer}
	tmpl.ApplyTo(&p)
	if p.ImportLimit != 300000 || p.ImportLimitAction != "block" {
		t.Errorf("overridden limit should survive ApplyTo, got %d/%s", p.ImportLimit, p.ImportLimitAction)
	}
	if p.Role != RoleIXPeer {
		t.Error("fields that are not overridden must still come from the template")
	}
}

func TestPeerTemplateValidateNormalisesLikeAPeer(t *testing.T) {
	tmpl := PeerTemplate{Name: "RR_CLIENTS", Role: RoleIBGP, EnforceFirstAS: true, PrependCount: 3,
		ExportCommunities: "65000:1", BGPRole: true, GTSM: true, NextHopSelf: true, ImportLimitAction: "restart"}
	if errs := tmpl.Validate(); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// The same normalisation Peer.Validate applies to an iBGP session.
	if tmpl.EnforceFirstAS || tmpl.PrependCount != 0 || tmpl.ExportCommunities != "" || tmpl.BGPRole || tmpl.GTSM {
		t.Errorf("iBGP template should lose its eBGP-only knobs, got %+v", tmpl)
	}
	if tmpl.GracefulRestart != GRAware || tmpl.IBGPExportDefault != IBGPExportAll {
		t.Errorf("blank modes should take their defaults, got gr=%q ibgp=%q", tmpl.GracefulRestart, tmpl.IBGPExportDefault)
	}
	if !tmpl.NextHopSelf || tmpl.Name != "RR_CLIENTS" {
		t.Errorf("identity and iBGP fields must survive, got %+v", tmpl)
	}

	bad := PeerTemplate{Name: "bad name", Role: "nonsense", ImportLimitAction: "restart"}
	errs := bad.Validate()
	if errs["name"] == "" || errs["role"] == "" {
		t.Errorf("expected name and role errors, got %v", errs)
	}
}

func TestPeerRejectsReservedNames(t *testing.T) {
	for _, name := range []string{"new", "seed", "preview", "templates"} {
		p := validPeer()
		p.Name = name
		if errs := p.Validate(); errs["name"] == "" {
			t.Errorf("peer name %q shadows a /peers/ page and must be rejected", name)
		}
	}
}

// seedTemplateFixture makes two import and one export policy and a template
// chaining them, returning the template (with chains loaded) and the policies.
func seedTemplateFixture(t *testing.T, s *Store) (PeerTemplate, Policy, Policy, Policy) {
	t.Helper()
	sanity, err := s.GetPolicyByName("IMPORT_SANITY")
	if err != nil {
		t.Fatal(err)
	}
	own, err := s.GetPolicyByName("EXPORT_OWN")
	if err != nil {
		t.Fatal(err)
	}
	noDefault := Policy{Name: "NO_DEFAULT", Direction: DirImport, DefaultRoute: DefaultReject, BogonASNs: BogonASNsOff, ROV: ROVOff}
	if errs := noDefault.Validate(); len(errs) != 0 {
		t.Fatalf("fixture policy: %v", errs)
	}
	if noDefault.ID, err = s.CreatePolicy(noDefault); err != nil {
		t.Fatal(err)
	}
	tmpl := PeerTemplate{Name: "IX_PEERS", Description: "route servers", Role: RoleIXPeer,
		ImportLimit: 50000, ImportLimitAction: "restart", EnforceFirstAS: false, GTSM: true, GracefulRestart: GRAware}
	if errs := tmpl.Validate(); len(errs) != 0 {
		t.Fatalf("fixture template: %v", errs)
	}
	id, err := s.CreatePeerTemplate(tmpl, []int64{sanity.ID, noDefault.ID}, []int64{own.ID})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err = s.GetPeerTemplate(id)
	if err != nil {
		t.Fatal(err)
	}
	return tmpl, sanity, noDefault, own
}

func TestPeerTemplateCRUDAndChains(t *testing.T) {
	s := openTest(t)
	tmpl, sanity, noDefault, own := seedTemplateFixture(t, s)

	if len(tmpl.ImportPolicies) != 2 || tmpl.ImportPolicies[0].ID != sanity.ID || tmpl.ImportPolicies[1].ID != noDefault.ID {
		t.Fatalf("import chain wrong or out of order: %+v", tmpl.ImportPolicies)
	}
	if len(tmpl.ExportPolicies) != 1 || tmpl.ExportPolicies[0].ID != own.ID {
		t.Fatalf("export chain wrong: %+v", tmpl.ExportPolicies)
	}
	if tmpl.ImportLimit != 50000 || tmpl.Role != RoleIXPeer || !tmpl.GTSM || tmpl.EnforceFirstAS {
		t.Errorf("stored template wrong: %+v", tmpl)
	}
	byName, err := s.GetPeerTemplateByName("IX_PEERS")
	if err != nil || byName.ID != tmpl.ID {
		t.Fatalf("GetPeerTemplateByName: %v %+v", err, byName)
	}
	list, err := s.ListPeerTemplates()
	if err != nil || len(list) != 1 || len(list[0].ImportPolicies) != 2 {
		t.Fatalf("ListPeerTemplates should load chains: %v %+v", err, list)
	}
	if _, err := s.GetPeerTemplateByName("nope"); err != ErrNotFound {
		t.Errorf("missing template should be ErrNotFound, got %v", err)
	}

	// Nothing linked: update changes nobody, delete works.
	tmpl.ImportLimit = 60000
	if n, err := s.UpdatePeerTemplate(tmpl, []int64{sanity.ID}, nil); err != nil || n != 0 {
		t.Fatalf("update with no linked peers: n=%d err=%v", n, err)
	}
	if got, _ := s.GetPeerTemplate(tmpl.ID); got.ImportLimit != 60000 || len(got.ImportPolicies) != 1 || len(got.ExportPolicies) != 0 {
		t.Errorf("update not stored: %+v", got)
	}
	if err := s.DeletePeerTemplate(tmpl.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetPeerTemplate(tmpl.ID); err != ErrNotFound {
		t.Errorf("template should be gone, got %v", err)
	}
}

func TestTemplateSaveRewritesEveryLinkedPeer(t *testing.T) {
	s := openTest(t)
	tmpl, sanity, noDefault, own := seedTemplateFixture(t, s)

	// Three peers: two linked to the template, one with the same shape but
	// owning it. The big one overrides its import limit.
	mk := func(name, ip string, link bool, overrides string) Peer {
		p := Peer{Name: name, Role: RoleUpstream, Enabled: true, NeighborIP: ip, RemoteASN: 64500,
			Password: "pw-" + name, Drained: name == "rs2_v4", Description: "desc " + name,
			ImportLimit: 1, ImportLimitAction: "warn", EnforceFirstAS: true, TemplateOverrides: overrides}
		if link {
			tmpl.ApplyTo(&p)
		}
		if errs := p.Validate(); len(errs) != 0 {
			t.Fatalf("%s: %v", name, errs)
		}
		id, err := s.CreatePeer(p)
		if err != nil {
			t.Fatal(err)
		}
		p.ID = id
		if err := s.SetPeerPolicies(id, PolicyIDs(p.ImportPolicies), PolicyIDs(p.ExportPolicies)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rs1 := mk("rs1_v4", "198.51.100.10", true, "")
	rs2 := mk("rs2_v4", "198.51.100.11", true, OverrideImportLimit)
	own1 := mk("own_v4", "198.51.100.12", false, "")

	// The override kept rs2's own limit through the link.
	if got, _ := s.GetPeer(rs2.ID); got.ImportLimit != 1 || got.ImportLimitAction != "warn" {
		t.Fatalf("override should keep the peer's own limit, got %d/%s", got.ImportLimit, got.ImportLimitAction)
	}
	if got, _ := s.GetPeer(rs1.ID); got.ImportLimit != 50000 || got.Role != RoleIXPeer || got.TemplateName != "IX_PEERS" || !got.TemplateID.Valid {
		t.Fatalf("linked peer should carry the template's shape and name, got %+v", got)
	}

	// Edit the template: role stays, limit and GTSM change, chain loses NO_DEFAULT.
	tmpl.ImportLimit = 75000
	tmpl.GTSM = false
	tmpl.BFD = true
	n, err := s.UpdatePeerTemplate(tmpl, []int64{sanity.ID}, []int64{own.ID})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 linked peers rewritten, got %d", n)
	}

	for _, id := range []int64{rs1.ID, rs2.ID} {
		got, err := s.GetPeer(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.GTSM || !got.BFD || got.Role != RoleIXPeer {
			t.Errorf("%s: governed fields not rewritten: %+v", got.Name, got)
		}
		imports, exports, err := s.PeerPolicies(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(imports) != 1 || imports[0].ID != sanity.ID || len(exports) != 1 || exports[0].ID != own.ID {
			t.Errorf("%s: chains not rewritten: imports=%v exports=%v", got.Name, imports, exports)
		}
		// Identity and operational state are the peer's own.
		if got.Password != "pw-"+got.Name || got.Description != "desc "+got.Name || !got.Enabled || got.RemoteASN != 64500 {
			t.Errorf("%s: identity must not change on a template save: %+v", got.Name, got)
		}
	}
	if got, _ := s.GetPeer(rs1.ID); got.ImportLimit != 75000 {
		t.Errorf("rs1 should take the new limit, got %d", got.ImportLimit)
	}
	if got, _ := s.GetPeer(rs2.ID); got.ImportLimit != 1 || !got.Drained {
		t.Errorf("rs2 should keep its overridden limit and drain flag, got limit=%d drained=%v", got.ImportLimit, got.Drained)
	}
	// The unlinked peer is untouched.
	if got, _ := s.GetPeer(own1.ID); got.ImportLimit != 1 || got.Role != RoleUpstream || got.TemplateID.Valid {
		t.Errorf("unlinked peer must not be touched: %+v", got)
	}
	if imports, _, _ := s.PeerPolicies(own1.ID); len(imports) != 0 {
		t.Errorf("unlinked peer's chain must not be touched: %v", imports)
	}
	_ = noDefault

	usage, err := s.TemplateUsage()
	if err != nil || usage[tmpl.ID] != 2 {
		t.Errorf("usage = %v (%v), want 2 for the template", usage, err)
	}
}

func TestLinkPeerToTemplateCopiesShapeAndDetachKeepsIt(t *testing.T) {
	s := openTest(t)
	tmpl, sanity, _, _ := seedTemplateFixture(t, s)
	p := validPeer()
	p.ImportLimit, p.ImportLimitAction = 123, "warn"
	p.TemplateOverrides = OverrideImportLimit
	id, err := s.CreatePeer(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkPeerToTemplate(id, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPeer(id)
	if err != nil {
		t.Fatal(err)
	}
	// Linking clears overrides: the peer takes the template's word for everything.
	if got.ImportLimit != 50000 || got.Role != RoleIXPeer || got.TemplateOverrides != "" || !got.TemplateID.Valid {
		t.Errorf("link should copy the shape and clear overrides: %+v", got)
	}
	if imports, _, _ := s.PeerPolicies(id); len(imports) != 2 || imports[0].ID != sanity.ID {
		t.Errorf("link should copy the chain, got %v", imports)
	}

	// Detach: the shape stays, only the link goes.
	got.TemplateID = sql.NullInt64{}
	if err := s.UpdatePeer(got); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetPeer(id)
	if got.TemplateID.Valid || got.TemplateName != "" || got.ImportLimit != 50000 || got.Role != RoleIXPeer {
		t.Errorf("detach should keep the values and drop the link: %+v", got)
	}
	// A template save no longer reaches it.
	tmpl.ImportLimit = 1
	if n, err := s.UpdatePeerTemplate(tmpl, []int64{sanity.ID}, nil); err != nil || n != 0 {
		t.Fatalf("detached peer must not be rewritten: n=%d err=%v", n, err)
	}
	if got, _ = s.GetPeer(id); got.ImportLimit != 50000 {
		t.Errorf("detached peer changed on template save: %+v", got)
	}
	if err := s.LinkPeerToTemplate(id, 999); err != ErrNotFound {
		t.Errorf("linking to a missing template should be ErrNotFound, got %v", err)
	}
}

func TestDeleteGuardsNameWhatStandsInTheWay(t *testing.T) {
	s := openTest(t)
	tmpl, sanity, noDefault, _ := seedTemplateFixture(t, s)
	p := validPeer()
	tmpl.ApplyTo(&p)
	id, err := s.CreatePeer(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPeerPolicies(id, PolicyIDs(tmpl.ImportPolicies), PolicyIDs(tmpl.ExportPolicies)); err != nil {
		t.Fatal(err)
	}

	err = s.DeletePeerTemplate(tmpl.ID)
	if err == nil || !strings.Contains(err.Error(), "1 peer(s): edge_v4") {
		t.Errorf("deleting a linked template should refuse and name the peer, got %v", err)
	}
	// NO_DEFAULT is in the template's chain and, through it, the peer's.
	err = s.DeletePolicy(noDefault.ID)
	if err == nil || !strings.Contains(err.Error(), "1 peer(s) and 1 peer template(s)") {
		t.Errorf("deleting a policy a template uses should count both, got %v", err)
	}
	// Drop the peer and its chain; the template alone still holds the policy.
	if err := s.DeletePeer(id); err != nil {
		t.Fatal(err)
	}
	err = s.DeletePolicy(noDefault.ID)
	if err == nil || !strings.Contains(err.Error(), "1 peer template(s)") || strings.Contains(err.Error(), "peer(s) and") {
		t.Errorf("policy used only by a template: got %v", err)
	}
	if err := s.DeletePeerTemplate(tmpl.ID); err != nil {
		t.Fatalf("template with no linked peers should delete: %v", err)
	}
	// template_policies cascaded away with the template.
	if err := s.DeletePolicy(noDefault.ID); err != nil {
		t.Errorf("policy should be free once the template is gone: %v", err)
	}
	_ = sanity
}

// A v37 database — peers without the template columns, no template tables —
// must open cleanly, keep its peers unlinked, and be able to link one.
func TestMigratePeerTemplatesFromV37(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v37.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE peers (
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, description TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT 'upstream', enabled INTEGER NOT NULL DEFAULT 1, neighbor_ip TEXT NOT NULL,
			remote_asn INTEGER NOT NULL, local_ip TEXT NOT NULL DEFAULT '', interface TEXT NOT NULL DEFAULT '',
			transport_endpoint TEXT NOT NULL DEFAULT '', multihop INTEGER NOT NULL DEFAULT 0, passive INTEGER NOT NULL DEFAULT 0,
			password TEXT NOT NULL DEFAULT '', import_limit INTEGER NOT NULL DEFAULT 0, import_limit_action TEXT NOT NULL DEFAULT 'restart',
			import_communities TEXT NOT NULL DEFAULT '', enforce_first_as INTEGER NOT NULL DEFAULT 1,
			ibgp_export_default TEXT NOT NULL DEFAULT 'all', origin_peer_only INTEGER NOT NULL DEFAULT 0,
			next_hop_self INTEGER NOT NULL DEFAULT 1, rr_client INTEGER NOT NULL DEFAULT 0, prepend_count INTEGER NOT NULL DEFAULT 0,
			export_communities TEXT NOT NULL DEFAULT '', drained INTEGER NOT NULL DEFAULT 0, bfd INTEGER NOT NULL DEFAULT 0,
			bgp_role INTEGER NOT NULL DEFAULT 0, gtsm INTEGER NOT NULL DEFAULT 0, graceful_restart TEXT NOT NULL DEFAULT 'aware',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`INSERT INTO peers (name, neighbor_ip, remote_asn, import_limit, created_at, updated_at)
			VALUES ('edge_v4', '198.51.100.1', 64497, 1000, '2026-08-01T00:00:00Z', '2026-08-01T00:00:00Z')`,
		// policies as of v37: schema.go's base columns plus those the v4/v5/v12/v15
		// migrations added, none of which run when starting from 37.
		`CREATE TABLE policies (
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, description TEXT NOT NULL DEFAULT '',
			direction TEXT NOT NULL, builtin INTEGER NOT NULL DEFAULT 0, default_route TEXT NOT NULL DEFAULT 'reject',
			min_len_v4 INTEGER NOT NULL DEFAULT 0, max_len_v4 INTEGER NOT NULL DEFAULT 0, min_len_v6 INTEGER NOT NULL DEFAULT 0,
			max_len_v6 INTEGER NOT NULL DEFAULT 0, reject_own_asn INTEGER NOT NULL DEFAULT 1, max_as_path_len INTEGER NOT NULL DEFAULT 0,
			bogon_asns TEXT NOT NULL DEFAULT 'all', accept_only_set_id INTEGER, set_local_pref INTEGER NOT NULL DEFAULT 0,
			announce_everything INTEGER NOT NULL DEFAULT 0, announce_default INTEGER NOT NULL DEFAULT 0,
			announce_from_upstream INTEGER NOT NULL DEFAULT 0, announce_from_ix INTEGER NOT NULL DEFAULT 0,
			announce_from_customer INTEGER NOT NULL DEFAULT 0, reject_bogon_prefixes INTEGER NOT NULL DEFAULT 1,
			origin_as_set_id INTEGER, rov TEXT NOT NULL DEFAULT 'off', match_community TEXT NOT NULL DEFAULT '',
			accept_blackhole INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`PRAGMA user_version = 37`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	raw.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open/migrate: %v", err)
	}
	defer st.Close()

	peers, err := st.ListPeers()
	if err != nil {
		t.Fatalf("list peers after migrate (columns missing?): %v", err)
	}
	if len(peers) != 1 || peers[0].TemplateID.Valid || peers[0].TemplateName != "" || peers[0].ImportLimit != 1000 {
		t.Errorf("the old peer should survive, unlinked: %+v", peers)
	}
	tmpl := PeerTemplate{Name: "T", Role: RoleIXPeer, ImportLimitAction: "restart"}
	if errs := tmpl.Validate(); len(errs) != 0 {
		t.Fatal(errs)
	}
	id, err := st.CreatePeerTemplate(tmpl, nil, nil)
	if err != nil {
		t.Fatalf("template tables missing after migrate: %v", err)
	}
	if err := st.LinkPeerToTemplate(peers[0].ID, id); err != nil {
		t.Fatalf("link on a migrated database: %v", err)
	}
	if got, _ := st.GetPeer(peers[0].ID); !got.TemplateID.Valid || got.TemplateName != "T" {
		t.Errorf("link not recorded: %+v", got)
	}
	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Errorf("user_version = %d, want %d", version, schemaVersion)
	}
}
