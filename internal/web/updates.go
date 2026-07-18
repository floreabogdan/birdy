package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/floreabogdan/birdy/internal/buildinfo"
	"github.com/floreabogdan/birdy/internal/updatecheck"
)

type updateChecker interface {
	Check(ctx context.Context, channel, currentVersion, currentCommit string) (updatecheck.Result, error)
}

type updatesView struct {
	Active       string
	ReadOnly     bool
	Channel      string
	BuildVersion string
	BuildCommit  string
	Result       updatecheck.Result
	CheckErr     string
	Flash        string
}

func (s *Server) handleUpdatesPage(w http.ResponseWriter, r *http.Request) {
	channel := updatecheck.ChannelStable
	if settings, ok, err := s.store.GetSettings(); err == nil && ok &&
		updatecheck.ValidChannel(settings.UpdateChannel) {
		channel = settings.UpdateChannel
	}
	view := updatesView{
		Active: "updates", ReadOnly: s.readOnly, Channel: channel,
		BuildVersion: buildinfo.Version, BuildCommit: buildinfo.Commit,
		Flash: r.URL.Query().Get("flash"),
	}
	ctx, cancel := context.WithTimeout(r.Context(), 9*time.Second)
	defer cancel()
	result, err := s.updates.Check(ctx, channel, buildinfo.Version, buildinfo.Commit)
	if err != nil {
		view.CheckErr = err.Error()
	} else {
		view.Result = result
	}
	render(w, s.log, "updates.html", view)
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, "birdy is read-only", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	channel := strings.TrimSpace(r.FormValue("channel"))
	if !updatecheck.ValidChannel(channel) {
		http.Error(w, "invalid update channel", http.StatusBadRequest)
		return
	}
	if err := s.store.SaveUpdateChannel(channel); err != nil {
		s.serverError(w, "save update channel", err)
		return
	}
	s.audit(r, "Update channel changed to "+channel)
	http.Redirect(w, r, "/updates?flash="+flash("Update channel saved"), http.StatusSeeOther)
}
