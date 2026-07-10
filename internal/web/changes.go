package web

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/floreabogdan/birdy/internal/buildinfo"
	birdconf "github.com/floreabogdan/birdy/internal/render"
)

type changesView struct {
	Active   string
	ReadOnly bool

	// Tab is which panel the page opens on: "config" or "diff".
	Tab string

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

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	v := changesView{
		Active:   "changes",
		ReadOnly: s.readOnly,
		LivePath: s.birdConfPath,
		Tab:      tabParam(r, "config", "diff"),
	}

	settings, ok, err := s.store.GetSettings()
	if err != nil {
		s.serverError(w, "get settings", err)
		return
	}
	peers, err := s.store.ListPeers()
	if err != nil {
		s.serverError(w, "list peers", err)
		return
	}
	for i := range peers {
		if err := s.loadPeerChains(&peers[i]); err != nil {
			s.serverError(w, "peer policies", err)
			return
		}
	}
	sets, err := s.store.ListPrefixSets()
	if err != nil {
		s.serverError(w, "list prefix sets", err)
		return
	}
	policies, err := s.store.ListPolicies()
	if err != nil {
		s.serverError(w, "list policies", err)
		return
	}
	asSets, err := s.store.ListASSets()
	if err != nil {
		s.serverError(w, "list AS sets", err)
		return
	}
	rpkiServers, err := s.store.ListRPKIServers()
	if err != nil {
		s.serverError(w, "list RPKI servers", err)
		return
	}
	bogonASNs, err := s.store.ListBogonASNs()
	if err != nil {
		s.serverError(w, "list bogon ASNs", err)
		return
	}
	v.PeerCount, v.SetCount, v.PolicyCount = len(peers), len(sets), len(policies)

	if !ok {
		v.RenderErr = "birdy is not initialized."
		render(w, s.log, "changes.html", v)
		return
	}
	if settings.RouterID == "" || !settings.LocalASN.Valid {
		v.RenderErr = "Set the router ID and local ASN before a config can be rendered."
		render(w, s.log, "changes.html", v)
		return
	}

	in := birdconf.Input{
		RouterID:    settings.RouterID,
		LocalASN:    settings.LocalASN.Int64,
		PrefixSets:  sets,
		ASSets:      asSets,
		Policies:    policies,
		RPKIServers: rpkiServers,
		Peers:       peers,
		BogonASNs:   bogonASNs,
		Version:     buildinfo.Version,
		Generated:   time.Now(),
		// Everything below is bound for a browser, so secrets stay masked.
		// The apply pipeline (M2b) will render again, unmasked, straight to disk.
		MaskSecrets: true,
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

	render(w, s.log, "changes.html", v)
}
