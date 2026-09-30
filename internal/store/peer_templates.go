package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// PeerTemplate is a peer without an identity: the shape a session has —
// role, policy chains, limits, transport safeguards, export transforms —
// captured once and linked from many peers. It is the "peer group" of other
// routers' configs, and the persistent form of cloning a peer: the fields it
// governs are exactly the ones a clone copies, and the identity it lacks is
// exactly what a clone drops.
//
// A linked peer carries a full copy of every governed field. The template is
// not consulted when the config renders; it is consulted when it is saved,
// and the save rewrites every linked peer. That is what keeps the renderer,
// the linter and every "in use" count reading plain peer rows.
type PeerTemplate struct {
	ID          int64
	Name        string
	Description string

	// The governed fields carry the same names as on Peer, so ApplyTo and
	// TemplateFromPeer read as a list and TestEveryPeerFieldIsClassified can
	// hold the two in step.
	Role              string
	Multihop          int
	Passive           bool
	ImportLimit       int
	ImportLimitAction string
	ImportCommunities string
	ExportCommunities string
	PrependCount      int
	EnforceFirstAS    bool
	OriginPeerOnly    bool
	BGPRole           bool
	GTSM              bool
	BFD               bool
	GracefulRestart   string
	NextHopSelf       bool
	RRClient          bool
	IBGPExportDefault string

	// Ordered chains, in application order, filled by the store.
	ImportPolicies []Policy
	ExportPolicies []Policy
}

// IsIBGP reports whether peers of this template are internal sessions.
func (t PeerTemplate) IsIBGP() bool { return t.Role == RoleIBGP }

// ApplyTo overwrites p's governed fields and chains with the template's,
// leaving identity and operational state alone: name, description, enabled,
// addresses, ASN, password and the drain flag are the peer's own. A field the
// peer has overridden keeps its value. The link is recorded on the peer too,
// so a peer that went through ApplyTo is linked by construction.
func (t PeerTemplate) ApplyTo(p *Peer) {
	keep := p.Overrides()
	p.Role = t.Role
	p.Multihop = t.Multihop
	p.Passive = t.Passive
	if !keep[OverrideImportLimit] {
		p.ImportLimit = t.ImportLimit
		p.ImportLimitAction = t.ImportLimitAction
	}
	p.ImportCommunities = t.ImportCommunities
	p.ExportCommunities = t.ExportCommunities
	p.PrependCount = t.PrependCount
	p.EnforceFirstAS = t.EnforceFirstAS
	p.OriginPeerOnly = t.OriginPeerOnly
	p.BGPRole = t.BGPRole
	p.GTSM = t.GTSM
	p.BFD = t.BFD
	p.GracefulRestart = t.GracefulRestart
	p.NextHopSelf = t.NextHopSelf
	p.RRClient = t.RRClient
	p.IBGPExportDefault = t.IBGPExportDefault
	p.ImportPolicies = t.ImportPolicies
	p.ExportPolicies = t.ExportPolicies
	p.TemplateID = sql.NullInt64{Int64: t.ID, Valid: t.ID != 0}
	p.TemplateName = t.Name
}

// TemplateFromPeer captures a peer's shape as a template — the other half of
// the clone split, for "save this peer as a template". The result has no name:
// a template is named for the kind of session, not for the one it came from.
func TemplateFromPeer(p Peer) PeerTemplate {
	return PeerTemplate{
		Role:              p.Role,
		Multihop:          p.Multihop,
		Passive:           p.Passive,
		ImportLimit:       p.ImportLimit,
		ImportLimitAction: p.ImportLimitAction,
		ImportCommunities: p.ImportCommunities,
		ExportCommunities: p.ExportCommunities,
		PrependCount:      p.PrependCount,
		EnforceFirstAS:    p.EnforceFirstAS,
		OriginPeerOnly:    p.OriginPeerOnly,
		BGPRole:           p.BGPRole,
		GTSM:              p.GTSM,
		BFD:               p.BFD,
		GracefulRestart:   p.GracefulRestart,
		NextHopSelf:       p.NextHopSelf,
		RRClient:          p.RRClient,
		IBGPExportDefault: p.IBGPExportDefault,
		ImportPolicies:    p.ImportPolicies,
		ExportPolicies:    p.ExportPolicies,
	}
}

// Validate checks the name and description, then runs the shape through the
// same checks and normalisation a peer gets, so a template can never hold a
// combination a peer could not — an iBGP template loses its eBGP transforms
// here, exactly as an iBGP peer would.
func (t *PeerTemplate) Validate() map[string]string {
	var errs map[string]string
	t.Name, errs = validateNameDesc(t.Name, t.Description)

	var probe Peer
	t.ApplyTo(&probe)
	probe.validateShape(errs)
	shape := TemplateFromPeer(probe)
	shape.ID, shape.Name, shape.Description = t.ID, t.Name, t.Description
	*t = shape
	return errs
}

const templateCols = `id, name, description, role, multihop, passive, import_limit, import_limit_action,
	import_communities, export_communities, prepend_count, enforce_first_as, origin_peer_only,
	bgp_role, gtsm, bfd, graceful_restart, next_hop_self, rr_client, ibgp_export_default`

func scanTemplate(sc scanner) (PeerTemplate, error) {
	var t PeerTemplate
	err := sc.Scan(&t.ID, &t.Name, &t.Description, &t.Role, &t.Multihop, &t.Passive, &t.ImportLimit, &t.ImportLimitAction,
		&t.ImportCommunities, &t.ExportCommunities, &t.PrependCount, &t.EnforceFirstAS, &t.OriginPeerOnly,
		&t.BGPRole, &t.GTSM, &t.BFD, &t.GracefulRestart, &t.NextHopSelf, &t.RRClient, &t.IBGPExportDefault)
	return t, err
}

// ListPeerTemplates returns every template with its chains loaded, by name.
func (s *Store) ListPeerTemplates() ([]PeerTemplate, error) {
	rows, err := s.db.Query(`SELECT ` + templateCols + ` FROM peer_templates ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list peer templates: %w", err)
	}
	defer rows.Close()
	var out []PeerTemplate
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].ImportPolicies, out[i].ExportPolicies, err = s.chainFor(s.db, "template_policies", "template_id", out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) GetPeerTemplate(id int64) (PeerTemplate, error) {
	return s.getTemplate(`SELECT `+templateCols+` FROM peer_templates WHERE id = ?`, id)
}

func (s *Store) GetPeerTemplateByName(name string) (PeerTemplate, error) {
	return s.getTemplate(`SELECT `+templateCols+` FROM peer_templates WHERE name = ?`, name)
}

func (s *Store) getTemplate(query string, arg any) (PeerTemplate, error) {
	t, err := scanTemplate(s.db.QueryRow(query, arg))
	if err == sql.ErrNoRows {
		return PeerTemplate{}, ErrNotFound
	}
	if err != nil {
		return PeerTemplate{}, fmt.Errorf("store: get peer template: %w", err)
	}
	if t.ImportPolicies, t.ExportPolicies, err = s.chainFor(s.db, "template_policies", "template_id", t.ID); err != nil {
		return PeerTemplate{}, err
	}
	return t, nil
}

// CreatePeerTemplate stores a template and its chains. Nothing links to a new
// template yet, so there is nothing to fan out.
func (s *Store) CreatePeerTemplate(t PeerTemplate, importIDs, exportIDs []int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.Exec(`
		INSERT INTO peer_templates (name, description, role, multihop, passive, import_limit, import_limit_action,
			import_communities, export_communities, prepend_count, enforce_first_as, origin_peer_only,
			bgp_role, gtsm, bfd, graceful_restart, next_hop_self, rr_client, ibgp_export_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.Description, t.Role, t.Multihop, t.Passive, t.ImportLimit, t.ImportLimitAction,
		t.ImportCommunities, t.ExportCommunities, t.PrependCount, t.EnforceFirstAS, t.OriginPeerOnly,
		t.BGPRole, t.GTSM, t.BFD, t.GracefulRestart, t.NextHopSelf, t.RRClient, t.IBGPExportDefault, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("store: create peer template: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := replaceChain(tx, "template_policies", "template_id", id, importIDs, exportIDs); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdatePeerTemplate saves the template and then rewrites every peer linked
// to it — governed columns and chains — in the same transaction, so thirty IX
// peers change together or not at all. It returns how many peers changed, for
// the flash message: an operator who edits a template should be told, in
// numbers, what they just did.
//
// This is the one place that must never forget about linked peers. Every
// other path that writes a linked peer's shape (the peer form, a link) goes
// through ApplyTo, so a linked peer cannot drift from its template.
func (s *Store) UpdatePeerTemplate(t PeerTemplate, importIDs, exportIDs []int64) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`
		UPDATE peer_templates SET name = ?, description = ?, role = ?, multihop = ?, passive = ?,
			import_limit = ?, import_limit_action = ?, import_communities = ?, export_communities = ?,
			prepend_count = ?, enforce_first_as = ?, origin_peer_only = ?, bgp_role = ?, gtsm = ?, bfd = ?,
			graceful_restart = ?, next_hop_self = ?, rr_client = ?, ibgp_export_default = ?, updated_at = ?
		WHERE id = ?`,
		t.Name, t.Description, t.Role, t.Multihop, t.Passive,
		t.ImportLimit, t.ImportLimitAction, t.ImportCommunities, t.ExportCommunities,
		t.PrependCount, t.EnforceFirstAS, t.OriginPeerOnly, t.BGPRole, t.GTSM, t.BFD,
		t.GracefulRestart, t.NextHopSelf, t.RRClient, t.IBGPExportDefault, now(), t.ID)
	if err != nil {
		return 0, fmt.Errorf("store: update peer template: %w", err)
	}
	if err := affectedOne(res); err != nil {
		return 0, err
	}
	if err := replaceChain(tx, "template_policies", "template_id", t.ID, importIDs, exportIDs); err != nil {
		return 0, err
	}

	// The linked peers are read inside the transaction, so a peer linked between
	// our read and our write cannot be missed.
	rows, err := tx.Query(`SELECT `+peerCols+` FROM peers WHERE template_id = ? ORDER BY id`, t.ID)
	if err != nil {
		return 0, fmt.Errorf("store: peers using template: %w", err)
	}
	var linked []Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		linked = append(linked, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, p := range linked {
		t.ApplyTo(&p)
		if err := updatePeerShape(tx, p); err != nil {
			return 0, err
		}
		if err := replaceChain(tx, "peer_policies", "peer_id", p.ID, importIDs, exportIDs); err != nil {
			return 0, err
		}
	}
	return len(linked), tx.Commit()
}

// LinkPeerToTemplate attaches one existing peer to a template, copying the
// template's shape over the peer's own. Overrides are cleared: a peer that
// changes template takes the new template's word for everything.
func (s *Store) LinkPeerToTemplate(peerID, templateID int64) error {
	t, err := s.GetPeerTemplate(templateID)
	if err != nil {
		return err
	}
	p, err := s.GetPeer(peerID)
	if err != nil {
		return err
	}
	p.TemplateOverrides = ""
	t.ApplyTo(&p)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := updatePeerShape(tx, p); err != nil {
		return err
	}
	if err := replaceChain(tx, "peer_policies", "peer_id", p.ID, PolicyIDs(t.ImportPolicies), PolicyIDs(t.ExportPolicies)); err != nil {
		return err
	}
	return tx.Commit()
}

// DetachPeer clears a peer's link and its overrides, leaving every value it
// inherited in place: the peer now owns the shape it had. The bulk "detach"
// on the peers list and nothing else goes through here; the form detaches by
// saving the peer with no template.
func (s *Store) DetachPeer(peerID int64) error {
	res, err := s.db.Exec(`UPDATE peers SET template_id = NULL, template_overrides = '', updated_at = ? WHERE id = ?`, now(), peerID)
	if err != nil {
		return fmt.Errorf("store: detach peer: %w", err)
	}
	return affectedOne(res)
}

// PolicyIDs lists a chain's ids in order, for the chain writers.
func PolicyIDs(chain []Policy) []int64 {
	out := make([]int64, 0, len(chain))
	for _, p := range chain {
		out = append(out, p.ID)
	}
	return out
}

// PeersUsingTemplate names the peers linked to a template, for a delete
// refusal that says which sessions stand in the way.
func (s *Store) PeersUsingTemplate(id int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM peers WHERE template_id = ? ORDER BY name`, id)
	if err != nil {
		return nil, fmt.Errorf("store: peers using template: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// TemplateUsage counts linked peers per template, for the list page.
func (s *Store) TemplateUsage() (map[int64]int, error) {
	rows, err := s.db.Query(`SELECT template_id, COUNT(*) FROM peers WHERE template_id IS NOT NULL GROUP BY template_id`)
	if err != nil {
		return nil, fmt.Errorf("store: template usage: %w", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// DeletePeerTemplate refuses while any peer still links to the template:
// detaching them silently would leave thirty sessions owning a shape nobody
// chose for them on purpose. The foreign key is the backstop; this is the
// message.
func (s *Store) DeletePeerTemplate(id int64) error {
	users, err := s.PeersUsingTemplate(id)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		shown := users
		if len(shown) > 5 {
			shown = append(shown[:5:5], "…")
		}
		return fmt.Errorf("store: template is used by %d peer(s): %s", len(users), strings.Join(shown, ", "))
	}
	res, err := s.db.Exec(`DELETE FROM peer_templates WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete peer template: %w", err)
	}
	return affectedOne(res)
}
