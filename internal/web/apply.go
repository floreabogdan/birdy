package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	birdconf "github.com/floreabogdan/birdy/internal/render"
	"github.com/floreabogdan/birdy/internal/store"
)

// authorship is birdy's relationship to the bird.conf on disk. Apply is allowed
// only when birdy owns the file (or there is none); a config birdy did not write
// must be adopted first, so a hand-managed router is never silently clobbered.
type authorship int

const (
	authOwned   authorship = iota // on-disk hash matches what birdy last wrote
	authAbsent                    // no bird.conf, and birdy never wrote one
	authForeign                   // a file exists that birdy has never written
	authEdited                    // birdy wrote it, but it has changed since
)

func (a authorship) canApply() bool { return a == authOwned || a == authAbsent }

func (a authorship) String() string {
	switch a {
	case authOwned:
		return "owned"
	case authAbsent:
		return "absent"
	case authForeign:
		return "foreign"
	case authEdited:
		return "edited"
	default:
		return "unknown"
	}
}

// birdConfState reads bird.conf and classifies birdy's ownership of it.
func (s *Server) birdConfState(storedHash string) (authorship, string, error) {
	data, err := os.ReadFile(s.birdConfPath)
	if errors.Is(err, fs.ErrNotExist) {
		if storedHash == "" {
			return authAbsent, "", nil
		}
		return authEdited, "", nil // birdy wrote a config, something deleted it
	}
	if err != nil {
		return 0, "", err
	}
	onDisk := hashBytes(data)
	switch {
	case storedHash == "":
		return authForeign, onDisk, nil
	case onDisk == storedHash:
		return authOwned, onDisk, nil
	default:
		return authEdited, onDisk, nil
	}
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// writeBirdConf writes cfg to bird.conf atomically: a temp file in the same
// directory, then a rename, so BIRD never reads a half-written config. 0640
// keeps the session passwords readable only by birdy and BIRD's group.
func (s *Server) writeBirdConf(cfg string) error {
	dir := filepath.Dir(s.birdConfPath)
	tmp, err := os.CreateTemp(dir, ".birdy-conf-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.WriteString(cfg); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.birdConfPath)
}

// backupBirdConf copies the current bird.conf into the backup directory with a
// timestamped name and returns its path. It returns "" when there is nothing to
// back up (no file yet).
func (s *Server) backupBirdConf(now time.Time) (string, error) {
	data, err := os.ReadFile(s.birdConfPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.birdBackupDir, 0o750); err != nil {
		return "", err
	}
	name := fmt.Sprintf("bird.conf.%s.bak", now.UTC().Format("20060102T150405Z"))
	path := filepath.Join(s.birdBackupDir, name)
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return "", err
	}
	return path, nil
}

// restoreFrom copies a backup file back over bird.conf. An empty path means the
// backed-up state was "no file", so bird.conf is removed to match.
func (s *Server) restoreFrom(backupPath string) error {
	if backupPath == "" {
		err := os.Remove(s.birdConfPath)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return err
	}
	return s.writeBirdConf(string(data))
}

// writeGuard turns away every apply endpoint in read-only mode. Read-only is the
// hard boundary between viewing and touching the router.
func (s *Server) writeGuard(w http.ResponseWriter) bool {
	if s.readOnly {
		http.Error(w, "birdy is running in read-only mode; apply is disabled", http.StatusForbidden)
		return false
	}
	return true
}

// reconcilePending catches up with an auto-revert BIRD performed on its own. If a
// pending apply's deadline has passed, BIRD has already reverted to the previous
// config; birdy just records it. bird.conf on disk is already the previous
// config — the apply flow restores it the moment the timeout is armed — so there
// is no file to fix here.
func (s *Server) reconcilePending() error {
	pending, ok, err := s.store.PendingConfigVersion()
	if err != nil || !ok {
		return err
	}
	if !pending.Expired(time.Now()) {
		return nil
	}
	if err := s.store.ResolveConfigVersion(pending.ID, store.ConfigReverted,
		"Auto-reverted: the apply was not confirmed within the safety timeout."); err != nil {
		return err
	}
	return s.store.InsertEvent(store.EventConfigRevert, "",
		"Config apply auto-reverted (not confirmed in time)")
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if !s.writeGuard(w) {
		return
	}
	if err := s.reconcilePending(); err != nil {
		s.serverError(w, "reconcile pending", err)
		return
	}
	if _, ok, err := s.store.PendingConfigVersion(); err != nil {
		s.serverError(w, "pending check", err)
		return
	} else if ok {
		s.redirectChanges(w, r, "There is already a pending apply — confirm or roll it back first.")
		return
	}

	settings, _, err := s.store.GetSettings()
	if err != nil {
		s.serverError(w, "get settings", err)
		return
	}
	in, reason, err := s.renderInput(false) // real passwords: this is going to disk
	if err != nil {
		s.serverError(w, "build render input", err)
		return
	}
	if reason != "" {
		s.redirectChanges(w, r, reason)
		return
	}
	cfg, err := birdconf.Config(in)
	if err != nil {
		s.redirectChanges(w, r, "The config cannot be rendered: "+err.Error())
		return
	}
	newHash := hashBytes([]byte(cfg))

	auth, onDisk, err := s.birdConfState(settings.AppliedConfigHash)
	if err != nil {
		s.serverError(w, "read bird.conf", err)
		return
	}
	if !auth.canApply() {
		s.redirectChanges(w, r, "birdy has not adopted this router — bird.conf was not written by birdy. Adopt it first.")
		return
	}
	if onDisk == newHash {
		s.redirectChanges(w, r, "Already applied: the running config already matches what birdy would write.")
		return
	}

	// Pre-flight with `bird -p` before touching anything on disk. A config that
	// fails here never becomes a backup-and-write churn.
	if res := birdconf.Check(r.Context(), s.birdBinary, cfg); res.Skipped == "" && !res.OK {
		s.redirectChanges(w, r, "bird -p rejected the config; nothing was written:\n"+res.Output)
		return
	}

	now := time.Now()
	backup, err := s.backupBirdConf(now)
	if err != nil {
		s.log.Error("back up bird.conf", "error", err)
		s.redirectChanges(w, r, "Could not back up the current config: "+err.Error())
		return
	}
	if err := s.writeBirdConf(cfg); err != nil {
		// The usual cause is that birdy cannot write to bird.conf's directory —
		// grant it, or run read-only. Nothing has changed on the router yet.
		s.log.Error("write bird.conf", "error", err)
		s.redirectChanges(w, r, "Could not write "+s.birdConfPath+": "+err.Error()+
			". birdy needs write access to that file and its directory.")
		return
	}

	// The daemon's own check, against the exact file it will load.
	if res, err := s.client.ConfigureCheck(); err != nil {
		_ = s.restoreFrom(backup)
		s.serverError(w, "configure check", err)
		return
	} else if !res.OK {
		_ = s.restoreFrom(backup)
		s.redirectChanges(w, r, "BIRD rejected the config; it was rolled back:\n"+res.Message)
		return
	}

	// Apply with the safety timeout armed. If this operator loses reachability,
	// or the sessions never come up, BIRD reverts on its own.
	res, err := s.client.ConfigureTimeout(s.applyTimeout)
	if err != nil {
		_ = s.restoreFrom(backup)
		s.serverError(w, "configure timeout", err)
		return
	}
	if !res.OK {
		_ = s.restoreFrom(backup)
		s.redirectChanges(w, r, "BIRD could not apply the config; it was rolled back:\n"+res.Message)
		return
	}

	// The trick that keeps disk and daemon consistent: BIRD now holds the new
	// config in memory (armed). Put the previous file back on disk, so if the
	// timeout fires — or anything restarts BIRD — disk still matches the config
	// BIRD reverts to. The new bytes live in the version row until confirm.
	if err := s.restoreFrom(backup); err != nil {
		s.serverError(w, "restore pending backup", err)
		return
	}

	deadline := now.Add(time.Duration(s.applyTimeout) * time.Second)
	id, err := s.store.CreateConfigVersion(store.ConfigVersion{
		SHA256: newHash, Size: len(cfg), ConfigText: cfg, BackupPath: backup,
		Status: store.ConfigPending, Deadline: deadline,
		Message: fmt.Sprintf("Applied with a %ds safety timeout.", s.applyTimeout),
	})
	if err != nil {
		s.serverError(w, "record config version", err)
		return
	}
	_ = s.store.InsertEvent(store.EventConfigApply, "",
		fmt.Sprintf("Config applied with a %ds safety timeout (version %d)", s.applyTimeout, id))

	s.redirectChanges(w, r, fmt.Sprintf("Applied. Confirm within %ds to keep it, or it reverts on its own.", s.applyTimeout))
}

func (s *Server) handleApplyConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.writeGuard(w) {
		return
	}
	if err := s.reconcilePending(); err != nil {
		s.serverError(w, "reconcile pending", err)
		return
	}
	pending, ok, err := s.store.PendingConfigVersion()
	if err != nil {
		s.serverError(w, "pending check", err)
		return
	}
	if !ok {
		s.redirectChanges(w, r, "Nothing to confirm — the apply already resolved.")
		return
	}

	// Write the new config to disk first, so if confirm fails we can roll the
	// file back and leave nothing half-applied. On success this is the file
	// BIRD is already running from memory.
	if err := s.writeBirdConf(pending.ConfigText); err != nil {
		s.serverError(w, "write bird.conf", err)
		return
	}
	res, err := s.client.ConfigureConfirm()
	if err != nil || !res.OK {
		_ = s.restoreFrom(pending.BackupPath)
		msg := "BIRD would not confirm the apply"
		if err == nil {
			msg += ": " + res.Message
		}
		s.redirectChanges(w, r, msg+". The previous config is back on disk; roll back to clear this.")
		return
	}

	if err := s.store.SetAppliedConfigHash(pending.SHA256); err != nil {
		s.serverError(w, "set applied hash", err)
		return
	}
	if err := s.store.ResolveConfigVersion(pending.ID, store.ConfigConfirmed, "Confirmed."); err != nil {
		s.serverError(w, "resolve version", err)
		return
	}
	_ = s.store.InsertEvent(store.EventConfigApply, "", fmt.Sprintf("Config apply confirmed (version %d)", pending.ID))
	s.redirectChanges(w, r, "Confirmed. birdy now owns this config.")
}

func (s *Server) handleApplyRollback(w http.ResponseWriter, r *http.Request) {
	if !s.writeGuard(w) {
		return
	}
	pending, ok, err := s.store.PendingConfigVersion()
	if err != nil {
		s.serverError(w, "pending check", err)
		return
	}
	if !ok {
		s.redirectChanges(w, r, "Nothing to roll back.")
		return
	}

	// bird.conf on disk is already the previous config (restored when the
	// timeout was armed), so this only tells BIRD to drop the armed config and
	// records the outcome. Undo is best-effort: if the timeout already fired,
	// BIRD has nothing to undo and that is fine.
	res, err := s.client.ConfigureUndo()
	msg := "Rolled back."
	if err != nil {
		msg = "Rolled back (BIRD undo errored, but the previous config is on disk)."
		s.log.Warn("configure undo failed", "error", err)
	} else if !res.OK {
		msg = "Rolled back (" + res.Message + ")."
	}
	if err := s.store.ResolveConfigVersion(pending.ID, store.ConfigReverted, msg); err != nil {
		s.serverError(w, "resolve version", err)
		return
	}
	_ = s.store.InsertEvent(store.EventConfigRevert, "", fmt.Sprintf("Config apply rolled back (version %d)", pending.ID))
	s.redirectChanges(w, r, msg)
}

// handleAdopt takes ownership of a bird.conf birdy did not write. It backs the
// file up and records its hash as the baseline birdy now manages, so the next
// apply diffs against it and replaces it cleanly. Nothing about the running
// daemon changes.
func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	if !s.writeGuard(w) {
		return
	}
	backup, err := s.backupBirdConf(time.Now())
	if err != nil {
		s.serverError(w, "back up bird.conf", err)
		return
	}
	data, err := os.ReadFile(s.birdConfPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// No file to adopt — clear the hash so birdy is on a clean slate.
		if err := s.store.SetAppliedConfigHash(""); err != nil {
			s.serverError(w, "clear applied hash", err)
			return
		}
	case err != nil:
		s.serverError(w, "read bird.conf", err)
		return
	default:
		if err := s.store.SetAppliedConfigHash(hashBytes(data)); err != nil {
			s.serverError(w, "set applied hash", err)
			return
		}
	}
	_ = s.store.InsertEvent(store.EventConfigApply, "", "Adopted the existing bird.conf")
	msg := "Adopted. birdy now manages this config; the previous file is backed up."
	if backup != "" {
		msg = "Adopted. The existing config is backed up to " + filepath.Base(backup) + "."
	}
	s.redirectChanges(w, r, msg)
}

func (s *Server) redirectChanges(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/changes?flash="+flash(msg), http.StatusSeeOther)
}
