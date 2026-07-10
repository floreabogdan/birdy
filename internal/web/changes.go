package web

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/floreabogdan/birdy/internal/buildinfo"
	birdconf "github.com/floreabogdan/birdy/internal/render"
)

type changesView struct {
	Active   string
	ReadOnly bool
	Flash    string

	// Tab is which panel the page opens on: "config" or "diff".
	Tab string

	// Apply state. Ownership is birdy's relationship to the file on disk;
	// Pending is a live armed reconfigure awaiting confirm.
	Ownership      string // owned | absent | foreign | edited
	Pending        bool
	PendingSecs    int // seconds left before the auto-revert fires
	PendingMessage string
	InSync         bool // on-disk file already matches the candidate
	ApplyTimeout   int  // the configured safety-timeout window, for the ready panel
	// LiveSessions is the current BGP session states, shown on a pending apply so
	// the operator can see the effect before deciding to confirm.
	LiveSessions []protoRow

	// Candidate is the config birdy would write, secrets masked.
	Candidate string
	// CandidateLines is the same text split for the numbered gutter, so an
	// operator can find the line bird -p complained about.
	CandidateLines []string
	// RenderErr is set when the model cannot produce a valid config at all
	// (no router id, no local ASN, a peer pointing at a mismatched set).
	RenderErr string

	LivePath  string
	LiveErr   string // why the running config could not be read
	Hunks     []birdconf.Hunk
	Added     int
	Removed   int
	Identical bool

	Check    birdconf.CheckResult
	Warnings []birdconf.Warning

	PeerCount   int
	SetCount    int
	PolicyCount int
}

// Dangers counts the lint findings that describe a session which will not do
// what its author intended.
func (v changesView) Dangers() int {
	n := 0
	for _, w := range v.Warnings {
		if w.Severity == birdconf.SeverityDanger {
			n++
		}
	}
	return n
}

// renderInput assembles everything the renderer needs from the model. A
// non-empty reason means no config can be produced at all — the caller shows it
// instead of a config. Every page that renders the whole file goes through here,
// so /changes and the raw-config check can never disagree about what birdy
// would write.
func (s *Server) renderInput(mask bool) (birdconf.Input, string, error) {
	settings, ok, err := s.store.GetSettings()
	if err != nil {
		return birdconf.Input{}, "", err
	}
	peers, err := s.store.ListPeers()
	if err != nil {
		return birdconf.Input{}, "", err
	}
	for i := range peers {
		if err := s.loadPeerChains(&peers[i]); err != nil {
			return birdconf.Input{}, "", err
		}
	}
	sets, err := s.store.ListPrefixSets()
	if err != nil {
		return birdconf.Input{}, "", err
	}
	policies, err := s.store.ListPolicies()
	if err != nil {
		return birdconf.Input{}, "", err
	}
	asSets, err := s.store.ListASSets()
	if err != nil {
		return birdconf.Input{}, "", err
	}
	rpkiServers, err := s.store.ListRPKIServers()
	if err != nil {
		return birdconf.Input{}, "", err
	}
	bogonASNs, err := s.store.ListBogonASNs()
	if err != nil {
		return birdconf.Input{}, "", err
	}
	statics, err := s.store.ListStaticRoutes()
	if err != nil {
		return birdconf.Input{}, "", err
	}

	in := birdconf.Input{
		RouterID:     settings.RouterID,
		LocalASN:     settings.LocalASN.Int64,
		PrefixSets:   sets,
		ASSets:       asSets,
		Policies:     policies,
		RPKIServers:  rpkiServers,
		Peers:        peers,
		BogonASNs:    bogonASNs,
		StaticRoutes: statics,
		RRClusterID:  settings.RRClusterID,
		RawConfig:    settings.RawConfig,
		Version:      buildinfo.Version,
		Generated:    time.Now(),
		MaskSecrets:  mask,
	}
	switch {
	case !ok:
		return in, "birdy is not initialized.", nil
	case settings.RouterID == "" || !settings.LocalASN.Valid:
		return in, "Set the router ID and local ASN before a config can be rendered.", nil
	}
	return in, "", nil
}

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	v := changesView{
		Active:       "changes",
		ReadOnly:     s.readOnly,
		LivePath:     s.birdConfPath,
		Flash:        r.URL.Query().Get("flash"),
		Tab:          tabParam(r, "config", "diff"),
		ApplyTimeout: s.applyTimeout,
	}

	// An armed reconfigure that BIRD already auto-reverted is recorded here, so
	// the page never shows a pending apply that is really over.
	if err := s.reconcilePending(); err != nil {
		s.log.Error("reconcile pending", "error", err)
	}
	settings, _, err := s.store.GetSettings()
	if err != nil {
		s.serverError(w, "get settings", err)
		return
	}
	onDiskHash := s.fillApplyState(&v, settings.AppliedConfigHash)

	// Everything bound for a browser has its secrets masked. Applying renders
	// again, unmasked, straight to disk.
	in, reason, err := s.renderInput(true)
	if err != nil {
		s.serverError(w, "build render input", err)
		return
	}
	v.PeerCount, v.SetCount, v.PolicyCount = len(in.Peers), len(in.PrefixSets), len(in.Policies)
	if reason != "" {
		v.RenderErr = reason
		render(w, s.log, "changes.html", v)
		return
	}

	// Lint before rendering: a config that fails to render still has findings
	// worth showing, and bird -p will never catch a route leak.
	v.Warnings = birdconf.Lint(in)

	candidate, err := birdconf.Config(in)
	if err != nil {
		v.RenderErr = err.Error()
		render(w, s.log, "changes.html", v)
		return
	}
	v.Candidate = candidate
	v.CandidateLines = strings.Split(strings.TrimSuffix(candidate, "\n"), "\n")

	// bird -p on the masked candidate still proves the syntax: the password is
	// a string literal either way.
	v.Check = birdconf.Check(r.Context(), s.birdBinary, candidate)

	live, err := os.ReadFile(s.birdConfPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		v.LiveErr = "No config at this path yet — the whole candidate would be new."
	case errors.Is(err, fs.ErrPermission):
		v.LiveErr = "birdy cannot read this file (permission denied)."
	case err != nil:
		v.LiveErr = err.Error()
	}
	liveText := string(live)

	// The live file holds real passwords; the candidate holds masked ones. A
	// naive diff would report a change on every password line forever — and
	// would print the running secret into the browser. Mask the live side the
	// same way, and tell the user that password values are not compared.
	liveText = birdconf.MaskPasswords(liveText)

	v.Hunks = birdconf.Diff(liveText, candidate, 3)
	v.Added, v.Removed = birdconf.Stat(v.Hunks)
	v.Identical = err == nil && len(v.Hunks) == 0

	// The apply panel's "in sync" compares the REAL config to the file on disk,
	// not the masked diff, which would hide a changed password. Re-render the
	// same model unmasked (no extra database work) purely to hash it.
	unmasked := in
	unmasked.MaskSecrets = false
	if realCfg, rerr := birdconf.Config(unmasked); rerr == nil {
		v.InSync = v.Ownership == "owned" && onDiskHash == hashBytes([]byte(realCfg))
	}

	render(w, s.log, "changes.html", v)
}

// fillApplyState populates the apply panel: who owns the file on disk, and
// whether an armed reconfigure is waiting to be confirmed. It returns the hash
// of the on-disk file so the caller can decide whether it is already in sync.
func (s *Server) fillApplyState(v *changesView, storedHash string) string {
	auth, onDisk, err := s.birdConfState(storedHash)
	if err != nil {
		s.log.Error("read bird.conf state", "error", err)
		v.Ownership = "unknown"
		return ""
	}
	v.Ownership = auth.String()
	if pending, ok, err := s.store.PendingConfigVersion(); err == nil && ok {
		v.Pending = true
		v.PendingMessage = pending.Message
		if !pending.Deadline.IsZero() {
			left := int(time.Until(pending.Deadline).Seconds())
			if left < 0 {
				left = 0
			}
			v.PendingSecs = left
		}
		// Show the live session states so the operator can judge the apply's
		// effect — did the sessions stay up? — before confirming it.
		for _, row := range s.liveStates() {
			if row.IsBGP() {
				v.LiveSessions = append(v.LiveSessions, row)
			}
		}
		sort.Slice(v.LiveSessions, func(i, j int) bool { return v.LiveSessions[i].Name < v.LiveSessions[j].Name })
	}
	return onDisk
}
