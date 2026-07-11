package web

import (
	"net/http"
	"strconv"
	"strings"

	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

type historyView struct {
	Active   string
	ReadOnly bool
	Flash    string
	Versions []store.ConfigVersion
}

type versionView struct {
	Active   string
	ReadOnly bool
	Version  store.ConfigVersion
	Lines    []string // masked config, split for the numbered gutter
	Hunks    []birdconf.Hunk
	Added    int
	Removed  int
	OnDisk   bool // whether this version's text matches the file on disk now
	Pending  bool // a pending apply blocks re-applying an old version
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	versions, err := s.store.ListConfigVersions(100)
	if err != nil {
		s.serverError(w, "list config versions", err)
		return
	}
	render(w, s.log, "history.html", historyView{
		Active: "changes", ReadOnly: s.readOnly, Flash: r.URL.Query().Get("flash"),
		Versions: versions,
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.versionFromPath(w, r)
	if err != nil {
		return
	}

	// Mask before anything reaches the browser: a stored version holds the real
	// session passwords that were written to disk.
	masked := birdconf.MaskPasswords(v.ConfigText)

	view := versionView{
		Active: "changes", ReadOnly: s.readOnly, Version: v,
		Lines: strings.Split(strings.TrimSuffix(masked, "\n"), "\n"),
	}
	if _, ok, err := s.store.PendingConfigVersion(); err == nil && ok {
		view.Pending = true
	}

	// Diff this version against what is on disk now, so "what would re-applying
	// change" is visible. Both sides masked, like the Changes diff. The on-disk
	// side is the logical config, reconstructed from the split files if needed.
	if live, exists, err := s.readLogicalConfig(); err == nil && exists {
		liveMasked := birdconf.MaskPasswords(live)
		view.OnDisk = liveMasked == masked
		view.Hunks = birdconf.Diff(liveMasked, masked, 3)
		view.Added, view.Removed = birdconf.Stat(view.Hunks)
	}

	render(w, s.log, "version.html", view)
}

// handleReapply feeds a stored version's config back through the apply pipeline.
// It is the emergency-rollback path: "the config from before worked, put it
// back", with the same timeout-armed safety as any apply.
func (s *Server) handleReapply(w http.ResponseWriter, r *http.Request) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if !s.canStartApply(w, r) {
		return
	}
	v, err := s.versionFromPath(w, r)
	if err != nil {
		return
	}
	set, err := fileSetFor(v)
	if err != nil {
		s.serverError(w, "decode version file set", err)
		return
	}
	// Emergency rollback defaults to soft: putting an old config back should not
	// bounce sessions that are currently fine.
	s.applyConfig(w, r, v.ConfigText, set, true)
}

func (s *Server) versionFromPath(w http.ResponseWriter, r *http.Request) (store.ConfigVersion, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return store.ConfigVersion{}, err
	}
	v, err := s.store.GetConfigVersion(id)
	if err == store.ErrNotFound {
		http.NotFound(w, r)
		return store.ConfigVersion{}, err
	}
	if err != nil {
		s.serverError(w, "get config version", err)
		return store.ConfigVersion{}, err
	}
	return v, nil
}
