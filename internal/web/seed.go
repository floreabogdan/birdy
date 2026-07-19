package web

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/store"
)

// Seeding turns the BGP sessions BIRD is already running into editable model
// peers. birdy renders the whole config from its model, so adopting a router it
// did not build means re-describing every live session by hand — the trap the
// Changes impact panel warns about. Seeding is the warm start: read each session
// off the control socket, propose a peer row, and let the operator review and
// import the ones they want. It only writes birdy's own model (never BIRD), so
// it is safe in read-only mode, which is exactly when you adopt a router.

type seedView struct {
	Active   string
	ReadOnly bool
	Rows     []seedRow
	// Message explains an empty table: BIRD unreachable, or nothing left to seed.
	Message string
}

// Importable counts the rows the operator can actually bring in, so the page can
// disable the button and change its copy when there is nothing to do.
func (v seedView) Importable() int {
	n := 0
	for _, r := range v.Rows {
		if r.Invalid == "" {
			n++
		}
	}
	return n
}

type seedRow struct {
	Name       string
	NeighborIP string
	RemoteASN  int64
	LocalIP    string
	Role       string
	Multihop   int
	V6         bool
	Up         bool
	State      string
	// Note carries review hints for a row birdy can import but had to guess about
	// (an assumed role, a multihop hop count it cannot read).
	Note string
	// Invalid, when set, is why this session cannot be imported as-is; the row is
	// shown for context but has no checkbox.
	Invalid string
}

// seedPeerFromDetail maps one live BGP session to a proposed model peer, plus a
// human note about anything it had to assume. It is pure so the mapping can be
// tested without a socket. It never enables RFC 9234 role negotiation: turning
// that on for an already-established session can reset it, and adoption must not
// disturb what is running.
func seedPeerFromDetail(name string, d birdc.ProtocolDetail) (store.Peer, string) {
	remoteAS := parseASN(d.NeighborAS)
	localAS := parseASN(d.LocalAS)
	session := strings.ToLower(d.SessionType)
	internal := strings.Contains(session, "internal") || (localAS > 0 && remoteAS > 0 && localAS == remoteAS)

	role := store.RoleUpstream
	if internal {
		role = store.RoleIBGP
	}

	var notes []string
	multihop := 0
	if strings.Contains(session, "multihop") {
		// BIRD reports that a session is multihop but not the TTL it was configured
		// with, so seed a common value and flag it for review.
		multihop = 2
		notes = append(notes, "multihop session — confirm the hop count")
	}

	localIP := ""
	if ip, err := netip.ParseAddr(strings.TrimSpace(d.SourceAddress)); err == nil {
		localIP = ip.String()
	}

	p := store.Peer{
		Name:              name,
		NeighborIP:        strings.TrimSpace(d.NeighborAddress),
		RemoteASN:         remoteAS,
		LocalIP:           localIP,
		Role:              role,
		Enabled:           true,
		EnforceFirstAS:    !internal,
		ImportLimitAction: "restart",
		GracefulRestart:   store.GRAware,
		Multihop:          multihop,
		NextHopSelf:       internal,
	}
	if !internal {
		notes = append(notes, "review the role (assumed upstream)")
	}
	return p, strings.Join(notes, "; ")
}

// parseASN reads the leading integer out of BIRD's "Neighbor AS:" / "Local AS:"
// value. Returns 0 when it is missing or not a number.
func parseASN(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// discoverSeedRows lists the live BGP sessions birdy's model does not yet name,
// each mapped to a proposed peer. The second return is a message to show when
// the table is empty (BIRD unreachable, or nothing left to import).
func (s *Server) discoverSeedRows(ctx context.Context) ([]seedRow, string) {
	snap := s.poller.Snapshot()
	if snap.Err != nil {
		return nil, "birdy could not read BIRD: " + snap.Err.Error()
	}
	modelled := map[string]bool{}
	if peers, err := s.store.ListPeers(); err == nil {
		for _, p := range peers {
			modelled[p.Name] = true
		}
	}

	var rows []seedRow
	for _, proto := range snap.Protocols {
		if !strings.EqualFold(proto.Proto, "BGP") || modelled[proto.Name] {
			continue
		}
		state := strings.TrimSpace(proto.Info)
		if state == "" {
			state = strings.TrimSpace(proto.State)
		}
		row := seedRow{
			Name:  proto.Name,
			Up:    strings.EqualFold(strings.TrimSpace(proto.Info), "Established"),
			State: state,
		}
		detail, err := s.client.ProtocolDetail(ctx, proto.Name)
		if err != nil {
			row.Invalid = "could not read session detail: " + err.Error()
			rows = append(rows, row)
			continue
		}
		p, note := seedPeerFromDetail(proto.Name, detail)
		row.NeighborIP, row.RemoteASN, row.LocalIP = p.NeighborIP, p.RemoteASN, p.LocalIP
		row.Role, row.Multihop, row.Note = p.Role, p.Multihop, note
		row.V6 = p.IsV6()
		probe := p
		if errs := probe.Validate(); len(errs) > 0 {
			row.Invalid = firstFieldError(errs)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	msg := ""
	if len(rows) == 0 {
		msg = "birdy already models every BGP session BIRD is running. Nothing to import."
	}
	return rows, msg
}

func (s *Server) handleSeedPage(w http.ResponseWriter, r *http.Request) {
	rows, msg := s.discoverSeedRows(r.Context())
	render(w, s.log, "seed.html", seedView{
		Active: "peers", ReadOnly: s.readOnly, Rows: rows, Message: msg,
	})
}

// handleSeedSave imports the checked sessions. It re-reads each session from BIRD
// rather than trusting the posted values, so what lands in the model is what the
// router actually has; only the role is taken from the form. Sessions already
// modelled (or gone) since the page loaded are skipped, not duplicated.
func (s *Server) handleSeedSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	include := map[string]bool{}
	for _, name := range r.Form["include"] {
		include[name] = true
	}

	modelled := map[string]bool{}
	if peers, err := s.store.ListPeers(); err == nil {
		for _, p := range peers {
			modelled[p.Name] = true
		}
	}

	var created, skipped []string
	for _, proto := range s.poller.Snapshot().Protocols {
		if !strings.EqualFold(proto.Proto, "BGP") || !include[proto.Name] || modelled[proto.Name] {
			continue
		}
		detail, err := s.client.ProtocolDetail(r.Context(), proto.Name)
		if err != nil {
			skipped = append(skipped, proto.Name)
			continue
		}
		p, _ := seedPeerFromDetail(proto.Name, detail)
		if role := r.FormValue("role_" + proto.Name); peerRoleValid(role) {
			p.Role = role
			// Keep the role-dependent defaults consistent with the choice: iBGP
			// wants next-hop-self, eBGP wants the first-AS check.
			p.NextHopSelf = role == store.RoleIBGP
			p.EnforceFirstAS = role != store.RoleIBGP
		}
		if errs := p.Validate(); len(errs) > 0 {
			skipped = append(skipped, proto.Name)
			continue
		}
		if _, err := s.store.CreatePeer(p); err != nil {
			skipped = append(skipped, proto.Name)
			continue
		}
		created = append(created, proto.Name)
	}

	msg := fmt.Sprintf("Imported %d peer(s) from BIRD", len(created))
	if len(skipped) > 0 {
		msg += fmt.Sprintf("; skipped %d (%s)", len(skipped), strings.Join(skipped, ", "))
	}
	http.Redirect(w, r, "/peers?flash="+flash(msg), http.StatusSeeOther)
}

// peerRoleValid guards a posted role before it reaches a peer, so the form
// cannot smuggle in an unknown relationship.
func peerRoleValid(role string) bool {
	switch role {
	case store.RoleUpstream, store.RoleIXPeer, store.RoleCustomer, store.RoleIBGP:
		return true
	}
	return false
}

// firstFieldError returns any one message from a validation error map, for a
// compact "why this row can't be imported" note.
func firstFieldError(errs map[string]string) string {
	keys := make([]string, 0, len(errs))
	for k := range errs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return errs[keys[0]]
}
