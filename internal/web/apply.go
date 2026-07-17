package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// configDir is the directory bird.conf lives in; birdy.d/ sits beside it.
func (s *Server) configDir() string { return filepath.Dir(s.birdConfPath) }

// birdyIncludeDir is the directory birdy writes its per-section files into and
// owns exclusively.
func (s *Server) birdyIncludeDir() string { return filepath.Join(s.configDir(), birdconf.IncludeDir) }

var includeRe = regexp.MustCompile(`(?m)^\s*include\s+"([^"]+)"\s*;`)

// birdyIncludes returns, in order, the absolute include paths in a bird.conf
// that point into birdy's own include directory. An operator's own includes,
// pointing elsewhere, are ignored — so a hand-managed file still reads as one.
func (s *Server) birdyIncludes(conf string) []string {
	want := filepath.Clean(s.birdyIncludeDir())
	var out []string
	for _, m := range includeRe.FindAllStringSubmatch(conf, -1) {
		p := filepath.Clean(filepath.FromSlash(m[1]))
		if filepath.Dir(p) == want {
			out = append(out, p)
		}
	}
	return out
}

// readLogicalConfig returns the effective config text on disk: for a split
// layout, the concatenation of the birdy.d files bird.conf includes, in include
// order; for a single file, bird.conf itself. The bool is false when there is no
// bird.conf at all. This is the text birdy hashes and diffs, so how the config is
// split on disk is invisible to ownership detection and diffing — and an existing
// single-file install stays "owned" across the upgrade to the split layout.
func (s *Server) readLogicalConfig() (string, bool, error) {
	data, err := os.ReadFile(s.birdConfPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	includes := s.birdyIncludes(string(data))
	if len(includes) == 0 {
		return string(data), true, nil // single file: legacy birdy, or foreign
	}
	var b strings.Builder
	for _, inc := range includes {
		part, err := os.ReadFile(inc)
		if errors.Is(err, fs.ErrNotExist) {
			// A birdy include vanished out of band: the reconstruction is
			// incomplete, so its hash will not match — reported as edited.
			continue
		}
		if err != nil {
			return "", true, err
		}
		b.Write(part)
	}
	return b.String(), true, nil
}

// birdConfState reads the on-disk config and classifies birdy's ownership of it.
func (s *Server) birdConfState(storedHash string) (authorship, string, error) {
	text, exists, err := s.readLogicalConfig()
	if err != nil {
		return 0, "", err
	}
	if !exists {
		if storedHash == "" {
			return authAbsent, "", nil
		}
		return authEdited, "", nil // birdy wrote a config, something deleted it
	}
	onDisk := hashBytes([]byte(text))
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

// atomicWriteFile writes content to path via a temp file in the same directory
// and a rename, so a reader never sees a half-written file. 0640 keeps session
// passwords readable only by birdy and BIRD's group.
func atomicWriteFile(path, content string, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".birdy-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// writeFileSet writes bird.conf and its birdy.d/ includes. Includes are written
// first (each atomically) so bird.conf never references a file that is not yet
// there, then bird.conf, then any stale birdy.d/*.conf the set no longer includes
// is removed. birdy owns birdy.d exclusively, so pruning only ever touches its
// own files. A set with no includes is the single-file layout: bird.conf is
// written and birdy.d is cleared.
//
// The write is not atomic across the whole set, but nothing reads it until birdy
// itself asks BIRD to reconfigure, which happens only after this returns.
func (s *Server) writeFileSet(set birdconf.FileSet) error {
	keep := make(map[string]bool, len(set.Includes))
	for _, f := range set.Includes {
		abs := filepath.Join(s.configDir(), filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			return err
		}
		if err := atomicWriteFile(abs, f.Body, 0o640); err != nil {
			return err
		}
		keep[filepath.Base(abs)] = true
	}
	if err := atomicWriteFile(s.birdConfPath, set.Entry, 0o640); err != nil {
		return err
	}
	return s.pruneIncludeDir(keep)
}

// pruneIncludeDir removes every *.conf in birdy.d that is not in keep. A nil or
// empty keep clears the directory. A missing directory is fine.
func (s *Server) pruneIncludeDir(keep map[string]bool) error {
	entries, err := os.ReadDir(s.birdyIncludeDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		if keep[e.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(s.birdyIncludeDir(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// snapshotConfig copies the current config — bird.conf plus every birdy.d/*.conf
// — into a timestamped backup directory and returns its path, or "" when there is
// nothing to back up (no bird.conf yet).
func (s *Server) snapshotConfig(now time.Time) (string, error) {
	if _, err := os.Stat(s.birdConfPath); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	dest := filepath.Join(s.birdBackupDir, "bird."+now.UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return "", err
	}
	if err := copyFile(s.birdConfPath, filepath.Join(dest, "bird.conf"), 0o640); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(s.birdyIncludeDir())
	if errors.Is(err, fs.ErrNotExist) {
		return dest, nil
	}
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dest, birdconf.IncludeDir), 0o750); err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		if err := copyFile(filepath.Join(s.birdyIncludeDir(), e.Name()),
			filepath.Join(dest, birdconf.IncludeDir, e.Name()), 0o640); err != nil {
			return "", err
		}
	}
	return dest, nil
}

// restoreConfig makes the live config match a backup, undoing a write. It handles
// both new-style snapshot directories (bird.conf + birdy.d/) and older single
// .bak files. An empty path means the backed-up state was "no config", so
// bird.conf and birdy.d are removed.
func (s *Server) restoreConfig(backup string) error {
	if backup == "" {
		if err := os.Remove(s.birdConfPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return s.pruneIncludeDir(nil)
	}
	info, err := os.Stat(backup)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		// Legacy single-file backup: restore it and clear any split files.
		data, err := os.ReadFile(backup)
		if err != nil {
			return err
		}
		if err := atomicWriteFile(s.birdConfPath, string(data), 0o640); err != nil {
			return err
		}
		return s.pruneIncludeDir(nil)
	}
	data, err := os.ReadFile(filepath.Join(backup, "bird.conf"))
	if err != nil {
		return err
	}
	if err := atomicWriteFile(s.birdConfPath, string(data), 0o640); err != nil {
		return err
	}
	keep := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(backup, birdconf.IncludeDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil {
		if err := os.MkdirAll(s.birdyIncludeDir(), 0o750); err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(backup, birdconf.IncludeDir, e.Name()))
			if err != nil {
				return err
			}
			if err := atomicWriteFile(filepath.Join(s.birdyIncludeDir(), e.Name()), string(b), 0o640); err != nil {
				return err
			}
			keep[e.Name()] = true
		}
	}
	return s.pruneIncludeDir(keep)
}

func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return atomicWriteFile(dst, string(data), perm)
}

// writeGuard turns away every apply endpoint in read-only mode. Read-only is the
// hard boundary between viewing and touching the router.
func (s *Server) writeGuard(w http.ResponseWriter) bool {
	if s.readOnly {
		http.Error(w, "birdy is running in read-only mode; writes are disabled", http.StatusForbidden)
		return false
	}
	return true
}

// establishedSessions is the set of BGP sessions up right now, captured as the
// baseline before an apply so the pending panel can flag any that go down.
func (s *Server) establishedSessions() []string {
	var out []string
	for _, row := range s.liveStates() {
		if row.IsBGP() && row.Up {
			out = append(out, row.Name)
		}
	}
	return out
}

// emitEvent records an event and forwards it to the alert destinations, so an
// apply, a confirm, and especially an auto-revert reach whoever is on call —
// not just the timeline. Mirrors the poller's own emit.
func (s *Server) emitEvent(kind, protocol, message string) {
	if err := s.store.InsertEvent(kind, protocol, message); err != nil {
		s.log.Warn("failed to record event", "error", err)
	}
	if s.notifier != nil {
		s.notifier.Notify(kind, protocol, message)
	}
}

// emitAuditedEvent is emitEvent for an operator-initiated config action: it
// records the acting user on the timeline (the audit trail for who touched the
// router) and still alerts.
func (s *Server) emitAuditedEvent(r *http.Request, kind, message string) {
	if err := s.store.InsertAudit(s.actor(r), kind, message); err != nil {
		s.log.Warn("failed to record event", "error", err)
	}
	if s.notifier != nil {
		s.notifier.Notify(kind, "", message)
	}
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
	s.emitEvent(store.EventConfigRevert, "", "Config apply auto-reverted (not confirmed in time)")
	// The apply reloaded filters onto the table while it was armed; BIRD has since
	// reverted the config on its own, so re-run the restored filters to pull the
	// table back off the un-confirmed policy.
	s.reloadFilters("auto-revert")
	return nil
}

// reloadFilters re-runs import/export filters on the routes already in the table,
// so a soft reconfigure's filter changes actually take hold without restarting —
// and so without bouncing — any session. It is best-effort: a peer that does not
// support route-refresh cannot be re-imported without a restart, which BIRD
// reports per protocol while reloading the rest, so a non-OK result is a warning,
// not a failure — the config birdy applied stands either way. Returns BIRD's note
// when something was off, for the operator-facing flash.
func (s *Server) reloadFilters(reason string) (ok bool, note string) {
	res, err := s.client.Reload()
	if err != nil {
		s.log.Warn("reload failed", "when", reason, "error", err)
		return false, ""
	}
	if !res.OK {
		s.log.Warn("reload reported an issue", "when", reason, "message", res.Message)
	}
	return res.OK, res.Message
}

// canStartApply gathers the gates every apply shares: not read-only, no stale
// pending apply, and no live one. It writes its own response and returns false
// when the caller must stop.
func (s *Server) canStartApply(w http.ResponseWriter, r *http.Request) bool {
	if !s.writeGuard(w) {
		return false
	}
	if err := s.reconcilePending(); err != nil {
		s.serverError(w, "reconcile pending", err)
		return false
	}
	if _, ok, err := s.store.PendingConfigVersion(); err != nil {
		s.serverError(w, "pending check", err)
		return false
	} else if ok {
		s.redirectChanges(w, r, "There is already a pending apply — confirm or roll it back first.")
		return false
	}
	return true
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if !s.canStartApply(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
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
	warnings := birdconf.Lint(in)
	var dangers int
	for _, warning := range warnings {
		if warning.Severity == birdconf.SeverityDanger {
			dangers++
		}
	}
	wouldRemove := s.sessionsWouldRemove(in.Peers)
	if (dangers > 0 || len(wouldRemove) > 0) && r.FormValue("ackRisks") != "on" {
		s.redirectChanges(w, r, "Review the serious findings and live sessions at risk, then explicitly acknowledge them before applying.")
		return
	}
	set, err := birdconf.Files(in, s.configDir())
	if err != nil {
		s.redirectChanges(w, r, "The config cannot be rendered: "+err.Error())
		return
	}
	s.applyConfig(w, r, set.Logical(), set, r.FormValue("soft") == "on")
}

// applyConfig runs the write-and-arm pipeline for a config, shared by a fresh
// render and a re-apply of a stored version. cfg is the logical config text (for
// hashing, the bird -p pre-flight and the stored version); set is how it is laid
// out on disk, and set.Logical() must equal cfg. The caller must have passed
// canStartApply first. soft reloads filters without bouncing sessions.
func (s *Server) applyConfig(w http.ResponseWriter, r *http.Request, cfg string, set birdconf.FileSet, soft bool) {
	settings, _, err := s.store.GetSettings()
	if err != nil {
		s.serverError(w, "get settings", err)
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
		s.redirectChanges(w, r, "Already applied: the running config already matches this.")
		return
	}

	// Pre-flight with `bird -p` before touching anything on disk. A config that
	// fails here never becomes a backup-and-write churn.
	if res := birdconf.Check(r.Context(), s.birdBinary, cfg); res.Skipped == "" && !res.OK {
		s.redirectChanges(w, r, "bird -p rejected the config; nothing was written:\n"+res.Output)
		return
	}

	now := time.Now()
	backup, err := s.snapshotConfig(now)
	if err != nil {
		s.log.Error("back up config", "error", err)
		s.redirectChanges(w, r, "Could not back up the current config: "+err.Error())
		return
	}
	if err := s.writeFileSet(set); err != nil {
		// The usual cause is that birdy cannot write to bird.conf's directory —
		// grant it, or run read-only. Undo any partial write before bailing.
		s.log.Error("write config", "error", err)
		_ = s.restoreConfig(backup)
		s.redirectChanges(w, r, "Could not write to "+s.configDir()+": "+err.Error()+
			". birdy needs write access to that directory.")
		return
	}

	// The daemon's own check, against the exact files it will load.
	if res, err := s.client.ConfigureCheck(); err != nil {
		_ = s.restoreConfig(backup)
		s.serverError(w, "configure check", err)
		return
	} else if !res.OK {
		_ = s.restoreConfig(backup)
		s.redirectChanges(w, r, "BIRD rejected the config; it was rolled back:\n"+res.Message)
		return
	}

	// Apply with the safety timeout armed. If this operator loses reachability,
	// or the sessions never come up, BIRD reverts on its own.
	res, err := s.client.ConfigureTimeout(s.applyTimeout, soft)
	if err != nil {
		_ = s.restoreConfig(backup)
		s.serverError(w, "configure timeout", err)
		return
	}
	if !res.OK {
		_ = s.restoreConfig(backup)
		s.redirectChanges(w, r, "BIRD could not apply the config; it was rolled back:\n"+res.Message)
		return
	}

	// A soft reconfigure leaves the routes already in the table under the old
	// filters (see ConfigureTimeout) — it only applies the new ones to routes that
	// arrive later. So a policy or prefix change would not visibly take effect. Re-
	// run the filters against the current table now, while the config is armed, so
	// the operator sees the real result within the safety window and can roll it
	// back if it is wrong. A hard reconfigure restarted the affected protocols
	// already, so it needs no reload.
	var reloadNote string
	if soft {
		if ok, msg := s.reloadFilters("apply"); !ok && msg != "" {
			reloadNote = " Heads up — a peer could not be refreshed, so an import-policy" +
				" change to it may need a session restart to fully apply: " + msg
		}
	}

	// The trick that keeps disk and daemon consistent: BIRD now holds the new
	// config in memory (armed). Put the previous config back on disk, so if the
	// timeout fires — or anything restarts BIRD — disk still matches the config
	// BIRD reverts to. The new set lives in the version row until confirm.
	if err := s.restoreConfig(backup); err != nil {
		s.serverError(w, "restore pending backup", err)
		return
	}

	how := "hard"
	if soft {
		how = "soft"
	}
	filesJSON, err := json.Marshal(set)
	if err != nil {
		s.serverError(w, "encode file set", err)
		return
	}
	deadline := now.Add(time.Duration(s.applyTimeout) * time.Second)
	id, err := s.store.CreateConfigVersion(store.ConfigVersion{
		SHA256: newHash, Size: len(cfg), ConfigText: cfg, BackupPath: backup,
		Status: store.ConfigPending, Deadline: deadline,
		Message:          fmt.Sprintf("Applied (%s) with a %ds safety timeout.", how, s.applyTimeout),
		BaselineSessions: strings.Join(s.establishedSessions(), ","),
		ConfigFiles:      string(filesJSON),
	})
	if err != nil {
		s.serverError(w, "record config version", err)
		return
	}
	s.emitAuditedEvent(r, store.EventConfigApply,
		fmt.Sprintf("Config applied with a %ds safety timeout (version %d)", s.applyTimeout, id))

	s.redirectChanges(w, r, fmt.Sprintf("Applied. Confirm within %ds to keep it, or it reverts on its own.%s", s.applyTimeout, reloadNote))
}

func (s *Server) handleApplyConfirm(w http.ResponseWriter, r *http.Request) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
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
	// files back and leave nothing half-applied. On success this is what BIRD is
	// already running from memory.
	set, err := fileSetFor(pending)
	if err != nil {
		s.serverError(w, "decode pending file set", err)
		return
	}
	if err := s.writeFileSet(set); err != nil {
		s.serverError(w, "write config", err)
		return
	}
	res, err := s.client.ConfigureConfirm()
	if err != nil || !res.OK {
		_ = s.restoreConfig(pending.BackupPath)
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
	s.emitAuditedEvent(r, store.EventConfigApply, fmt.Sprintf("Config apply confirmed (version %d)", pending.ID))
	// Keep an off-box copy: mail the applied config, password-masked, to any
	// email destinations. A disk failure then does not lose what was running.
	if s.notifier != nil {
		s.notifier.MailConfig(birdconf.MaskPasswords(pending.ConfigText))
	}
	s.redirectChanges(w, r, "Confirmed. birdy now owns this config.")
}

func (s *Server) handleApplyRollback(w http.ResponseWriter, r *http.Request) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
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
	// The apply reloaded the new filters onto the table; now that the config is
	// back to the previous one, re-run its filters so the table follows.
	s.reloadFilters("rollback")
	if err := s.store.ResolveConfigVersion(pending.ID, store.ConfigReverted, msg); err != nil {
		s.serverError(w, "resolve version", err)
		return
	}
	s.emitAuditedEvent(r, store.EventConfigRevert, fmt.Sprintf("Config apply rolled back (version %d)", pending.ID))
	s.redirectChanges(w, r, msg)
}

// fileSetFor reconstructs the on-disk layout a stored version should write. A
// version saved with the split layout carries the exact set; an older
// single-file version falls back to writing its text as bird.conf alone.
func fileSetFor(v store.ConfigVersion) (birdconf.FileSet, error) {
	if v.ConfigFiles == "" {
		return birdconf.FileSet{Entry: v.ConfigText}, nil
	}
	var set birdconf.FileSet
	if err := json.Unmarshal([]byte(v.ConfigFiles), &set); err != nil {
		return birdconf.FileSet{}, err
	}
	return set, nil
}

// handleAdopt takes ownership of a config birdy did not write. It snapshots the
// current config and records its logical hash as the baseline birdy now manages,
// so the next apply diffs against it and replaces it cleanly. Nothing about the
// running daemon changes.
func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if !s.writeGuard(w) {
		return
	}
	backup, err := s.snapshotConfig(time.Now())
	if err != nil {
		s.serverError(w, "back up config", err)
		return
	}
	text, exists, err := s.readLogicalConfig()
	if err != nil {
		s.serverError(w, "read config", err)
		return
	}
	hash := ""
	if exists {
		hash = hashBytes([]byte(text))
	}
	if err := s.store.SetAppliedConfigHash(hash); err != nil {
		s.serverError(w, "set applied hash", err)
		return
	}
	_ = s.store.InsertAudit(s.actor(r), store.EventConfigApply, "Adopted the existing config")
	msg := "Adopted. birdy now manages this config; the previous config is backed up."
	if backup != "" {
		msg = "Adopted. The existing config is backed up to " + filepath.Base(backup) + "."
	}
	s.redirectChanges(w, r, msg)
}

func (s *Server) redirectChanges(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/changes?flash="+flash(msg), http.StatusSeeOther)
}
