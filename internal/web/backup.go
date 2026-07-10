package web

import (
	"archive/zip"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/floreabogdan/birdy/internal/buildinfo"
	birdconf "github.com/floreabogdan/birdy/internal/render"
)

// handleBackupDownload streams a single zip holding everything needed to rebuild
// birdy elsewhere: a fresh SQLite snapshot (the source of truth) and the current
// rendered bird.conf for reference, password-masked. One click to get a backup
// off the router's disk.
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	if s.snap == nil {
		http.Error(w, "snapshots not configured", http.StatusServiceUnavailable)
		return
	}
	snapPath, err := s.snap.CreateSnapshot(s.store)
	if err != nil {
		s.log.Error("backup snapshot failed", "error", err)
		http.Error(w, "failed to create snapshot", http.StatusInternalServerError)
		return
	}
	dbBytes, err := os.ReadFile(snapPath)
	if err != nil {
		s.log.Error("backup read snapshot failed", "error", err)
		http.Error(w, "failed to read snapshot", http.StatusInternalServerError)
		return
	}

	// The rendered config is a convenience — it is regenerable from the DB — so a
	// render failure just omits it rather than failing the whole backup.
	var confBytes []byte
	if in, reason, ierr := s.renderInput(true); ierr == nil && reason == "" {
		if cfg, cerr := birdconf.Config(in); cerr == nil {
			confBytes = []byte(cfg)
		}
	}

	stamp := time.Now().UTC().Format("20060102T150405Z")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "birdy-backup-"+stamp+".zip"))

	zw := zip.NewWriter(w)
	defer zw.Close()

	add := func(name string, data []byte) {
		f, werr := zw.Create(name)
		if werr != nil {
			s.log.Error("backup zip entry failed", "name", name, "error", werr)
			return
		}
		_, _ = f.Write(data)
	}

	manifest := fmt.Sprintf("birdy backup\nversion: %s\ncreated: %s\n\n"+
		"birdy.db      — the complete model; restore it under Settings, or point a\n"+
		"                fresh birdy at it. This is the real backup.\n"+
		"bird.conf     — the config birdy would write right now, for reference.\n"+
		"                Passwords are masked; the real ones live in birdy.db.\n",
		buildinfo.Version, stamp)

	add("MANIFEST.txt", []byte(manifest))
	add("birdy.db", dbBytes)
	if confBytes != nil {
		add("bird.conf", confBytes)
	}
}
