package web

import (
	"context"
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

	"github.com/floreabogdan/birdy/internal/birdc"
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

// includeLineRe matches a line that is nothing but an include statement — the
// only kind birdy writes into bird.conf.
var includeLineRe = regexp.MustCompile(`^include\s+"([^"]+)"\s*;$`)

// readLogicalConfig returns the effective config text on disk: for a split
// layout, bird.conf with each birdy.d include spliced in where it is included;
// for a single file, bird.conf itself. The bool is false when there is no
// bird.conf at all. This is the text birdy hashes and diffs, so how the config is
// split on disk is invisible to ownership detection and diffing — and an existing
// single-file install stays "owned" across the upgrade to the split layout.
//
// Comments and blank lines in a split bird.conf drop out: they are all birdy
// writes there besides the includes, and its banner names the birdy version,
// which an upgrade changes. Every other line is kept where it stands, because
// BIRD parses it: a protocol an operator added to bird.conf is part of the
// running config, so it must read as an edit (and show in the diff as what the
// next apply would remove), not stay invisible behind an unchanged include list.
func (s *Server) readLogicalConfig() (string, bool, error) {
	data, err := os.ReadFile(s.birdConfPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(s.birdyIncludes(string(data))) == 0 {
		return string(data), true, nil // single file: legacy birdy, or foreign
	}
	want := filepath.Clean(s.birdyIncludeDir())
	var b strings.Builder
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		m := includeLineRe.FindStringSubmatch(trimmed)
		if m == nil || filepath.Dir(filepath.Clean(filepath.FromSlash(m[1]))) != want {
			b.WriteString(line)
			continue
		}
		part, err := os.ReadFile(filepath.Clean(filepath.FromSlash(m[1])))
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

// rollbackConfig undoes a partial config write. If the undo itself fails, the
// files on disk may no longer match what BIRD is running, so it logs loudly and
// returns a suffix the caller appends to the operator-facing message. An empty
// suffix means the rollback succeeded.
func (s *Server) rollbackConfig(backup string) string {
	if err := s.restoreConfig(backup); err != nil {
		s.log.Error("config rollback failed; bird.conf on disk may not match the running config", "error", err)
		return " WARNING: rolling the config files back also failed, so bird.conf on disk may not match what BIRD is running — check it before retrying."
	}
	return ""
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

// revertGrace is how long past an apply's deadline birdy waits before it
// records BIRD's auto-revert. The deadline is counted from BIRD's answer to
// `configure timeout`, so BIRD's timer started no later than that; the grace
// covers BIRD running its timer late on a busy main loop and the revert itself.
const revertGrace = 5 * time.Second

// reconcilePending catches up with an auto-revert BIRD performed on its own. Once
// a pending apply's deadline and revertGrace have passed, BIRD has reverted to
// the previous config; birdy just records it. bird.conf on disk is normally the
// previous config already — the apply flow restores it the moment the timeout is
// armed — but it is put back again in case that restore failed.
//
// Acting early is worse than acting late: the reload below would run under the
// still-armed config, whose routes then survive BIRD's soft revert, and the
// apply this unblocks would stack on the armed config and make it BIRD's undo
// target. Late only means the panel shows 0s for a few seconds; a confirm in
// that window gets BIRD's "nothing to do" and is recorded as reverted.
func (s *Server) reconcilePending() error {
	pending, ok, err := s.store.PendingConfigVersion()
	if err != nil || !ok {
		return err
	}
	if !pending.Expired(time.Now().Add(-revertGrace)) {
		return nil
	}
	// The auto-revert reconcile is self-healing, not tied to one request, so it
	// runs on a background context bounded by the client's own command timeout.
	if _, err := s.recordRevert(context.Background(), pending, "auto-revert",
		"Auto-reverted: the apply was not confirmed within the safety timeout."); err != nil {
		return err
	}
	s.emitEvent(store.EventConfigRevert, "", "Config apply auto-reverted (not confirmed in time)")
	return nil
}

// recordRevert records a pending apply as reverted once BIRD is back on the
// previous config. It first puts that config back on disk: normally a no-op, as
// the apply restored it when it armed, but that restore can fail, and a BIRD
// restart must never load the unconfirmed files. The apply may also have
// reloaded the new filters onto the table, which BIRD's revert does not undo, so
// the restored filters are re-run. The returned suffix warns the operator when
// the files could not be put back.
func (s *Server) recordRevert(ctx context.Context, pending store.ConfigVersion, reason, message string) (string, error) {
	suffix := s.rollbackConfig(pending.BackupPath)
	if err := s.store.ResolveConfigVersion(pending.ID, store.ConfigReverted, message); err != nil {
		return suffix, err
	}
	s.reloadFilters(ctx, reason)
	return suffix, nil
}

// alwaysUnreloadable is the protocol birdy renders into every config that BIRD
// can never reload: a direct protocol has no import to re-run, so `reload all`
// lists it as failed every time (verified on BIRD 2.17.5).
const alwaysUnreloadable = "direct1"

// reloadFilters re-runs import/export filters on the routes already in the table,
// so a soft reconfigure's filter changes actually take hold without restarting —
// and so without bouncing — any session. It is best-effort: a peer that does not
// support route-refresh cannot be re-imported without a restart, which BIRD
// reports per protocol while reloading the rest, so a non-OK result is a warning,
// not a failure — the config birdy applied stands either way. Returns a note
// when something was off, for the operator-facing flash.
func (s *Server) reloadFilters(ctx context.Context, reason string) (ok bool, note string) {
	res, err := s.client.Reload(ctx)
	if err != nil {
		s.log.Warn("reload failed", "when", reason, "error", err)
		return false, ""
	}
	if !res.OK {
		s.log.Warn("reload reported an issue", "when", reason, "message", res.Message)
		return false, res.Message
	}
	var failed []string
	for _, p := range res.Failed {
		if p != alwaysUnreloadable {
			failed = append(failed, p)
		}
	}
	if len(failed) > 0 {
		s.log.Warn("BIRD could not reload some protocols", "when", reason, "protocols", failed)
		return false, "BIRD could not reload " + strings.Join(failed, ", ")
	}
	return true, ""
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

	// Encoded before anything is touched, so it cannot fail once BIRD is armed.
	filesJSON, err := json.Marshal(set)
	if err != nil {
		s.serverError(w, "encode file set", err)
		return
	}

	backup, err := s.snapshotConfig(time.Now())
	if err != nil {
		s.log.Error("back up config", "error", err)
		s.redirectChanges(w, r, "Could not back up the current config: "+err.Error())
		return
	}
	if err := s.writeFileSet(set); err != nil {
		// The usual cause is that birdy cannot write to bird.conf's directory —
		// grant it, or run read-only. Undo any partial write before bailing.
		s.log.Error("write config", "error", err)
		suffix := s.rollbackConfig(backup)
		s.redirectChanges(w, r, "Could not write to "+s.configDir()+": "+err.Error()+
			". birdy needs write access to that directory."+suffix)
		return
	}

	// From here birdy is mutating BIRD's running config: check, arm with the
	// auto-revert timeout, then reload filters. A dropped browser connection must
	// not abort this mid-flight — BIRD's armed timeout is the safety net, and
	// leaving a version unrecorded while BIRD holds a freshly-armed config is the
	// worst outcome. Detach from the request; birdc still bounds each call with its
	// own socket deadline, so this cannot hang.
	applyCtx := context.WithoutCancel(r.Context())

	// The daemon's own check, against the exact files it will load.
	if res, err := s.client.ConfigureCheck(applyCtx); err != nil {
		s.rollbackConfig(backup)
		s.serverError(w, "configure check", err)
		return
	} else if !res.OK {
		suffix := s.rollbackConfig(backup)
		s.redirectChanges(w, r, "BIRD rejected the config; it was rolled back:\n"+res.Message+suffix)
		return
	}

	// Apply with the safety timeout armed. If this operator loses reachability,
	// or the sessions never come up, BIRD reverts on its own.
	res, err := s.client.ConfigureTimeout(applyCtx, s.applyTimeout, soft)
	// BIRD arms its timer while handling the command, so it is running by the time
	// BIRD answers: counting from the answer puts birdy's deadline at or after
	// BIRD's revert, never before it. Counting from before the snapshot, the writes
	// and `configure check` (a full parse of its own) put it early by however long
	// those took.
	deadline := time.Now().Add(time.Duration(s.applyTimeout) * time.Second)
	// A reply lost after the command went out is not a refusal: BIRD may be
	// running the new config with the revert armed. Rolling back and recording
	// nothing would let the next apply stack on it and make it BIRD's undo target.
	// So it is recorded like an armed apply: a confirm then gets BIRD's verdict
	// (confirmed, or nothing to do), and the timeout path reconciles either way.
	unanswered := errors.Is(err, birdc.ErrNoReply)
	if unanswered {
		s.log.Error("configure timeout: no reply from BIRD; treating the apply as armed", "error", err)
	} else if err != nil {
		s.rollbackConfig(backup)
		s.serverError(w, "configure timeout", err)
		return
	}
	if !unanswered && !res.OK {
		suffix := s.rollbackConfig(backup)
		s.redirectChanges(w, r, "BIRD could not apply the config; it was rolled back:\n"+res.Message+suffix)
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
		// Detached like the rest of the armed window (ConfigureCheck/ConfigureTimeout
		// above, and the rollback path): a dropped browser must not cancel the reload
		// that makes the filter change visible while the config is still revertible.
		if ok, msg := s.reloadFilters(applyCtx, "apply"); !ok && msg != "" {
			reloadNote = " Heads up — a peer could not be refreshed, so an import-policy" +
				" change to it may need a session restart to fully apply: " + msg
		}
	}

	// The trick that keeps disk and daemon consistent: BIRD now holds the new
	// config in memory (armed). Put the previous config back on disk, so if the
	// timeout fires — or anything restarts BIRD — disk still matches the config
	// BIRD reverts to. The new set lives in the version row until confirm.
	//
	// If that fails, BIRD is armed all the same: carry on and record the version,
	// so the apply can still be confirmed or rolled back (both rewrite the files),
	// and nothing stacks on it.
	var restoreWarn string
	if err := s.restoreConfig(backup); err != nil {
		s.log.Error("restore the previous config while armed", "error", err)
		restoreWarn = " WARNING: birdy could not put the previous config back on disk (" + err.Error() +
			"), so a BIRD restart would load the unconfirmed files."
	}

	how := "hard"
	if soft {
		how = "soft"
	}
	message := fmt.Sprintf("Applied (%s) with a %ds safety timeout.", how, s.applyTimeout)
	if unanswered {
		message += " BIRD's answer was lost, so it may not have taken effect."
	}
	id, err := s.store.CreateConfigVersion(store.ConfigVersion{
		SHA256: newHash, Size: len(cfg), ConfigText: cfg, BackupPath: backup,
		Status: store.ConfigPending, Deadline: deadline,
		Message:          message,
		BaselineSessions: strings.Join(s.establishedSessions(), ","),
		ConfigFiles:      string(filesJSON),
	})
	if err != nil {
		s.unrecordedApply(w, r, backup, unanswered, restoreWarn, err)
		return
	}
	s.emitAuditedEvent(r, store.EventConfigApply,
		fmt.Sprintf("Config applied with a %ds safety timeout (version %d)", s.applyTimeout, id))

	if restoreWarn != "" {
		restoreWarn += " Confirm or roll back now; either rewrites them."
	}
	if unanswered {
		s.redirectChanges(w, r, fmt.Sprintf("birdy sent the apply, but BIRD's answer never arrived, so BIRD may be running"+
			" the new config with its %ds safety timeout armed. birdy is treating it as pending: check the sessions,"+
			" then confirm it or let it revert on its own. If BIRD never took it, confirming says so and records it"+
			" as reverted.%s%s", s.applyTimeout, reloadNote, restoreWarn))
		return
	}
	s.redirectChanges(w, r, fmt.Sprintf("Applied. Confirm within %ds to keep it, or it reverts on its own.%s%s", s.applyTimeout, reloadNote, restoreWarn))
}

// unrecordedApply handles an armed apply whose version could not be recorded.
// Unrecorded, nothing would ever resolve it, and the next apply would stack on
// it. birdy armed it a moment ago, so BIRD's state is known: undo it now. When
// BIRD's answer to the apply was lost that is not known — an undo with nothing
// armed reverts BIRD's previous reconfigure instead — so the timeout is left to
// revert it.
func (s *Server) unrecordedApply(w http.ResponseWriter, r *http.Request, backup string, unanswered bool, restoreWarn string, cause error) {
	s.log.Error("record config version", "error", cause)
	ctx := context.WithoutCancel(r.Context()) // still inside the armed window
	if restoreWarn != "" {
		restoreWarn = s.rollbackConfig(backup) // one more try; "" once it worked
	}
	if unanswered {
		s.redirectChanges(w, r, fmt.Sprintf("birdy could not record the apply in its database, and BIRD's answer to it"+
			" was lost, so BIRD may be running it armed: it reverts on its own within %ds. Do not apply again before"+
			" then.%s", s.applyTimeout, restoreWarn))
		return
	}
	res, err := s.client.ConfigureUndo(ctx)
	if err != nil || !res.OK {
		s.log.Error("undo the unrecorded apply", "error", err, "message", res.Message)
		s.redirectChanges(w, r, fmt.Sprintf("birdy could not record the apply in its database, and undoing it failed"+
			" too: BIRD reverts it on its own within %ds. Do not apply again before then.%s", s.applyTimeout, restoreWarn))
		return
	}
	s.reloadFilters(ctx, "undo unrecorded apply")
	s.emitAuditedEvent(r, store.EventConfigRevert, "Config apply undone: it could not be recorded")
	s.redirectChanges(w, r, "birdy could not record the apply in its database, so it was undone: BIRD is back on the"+
		" previous config."+restoreWarn)
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
		// Some files may already be the new ones; a BIRD restart must not load
		// that mix. BIRD is untouched and still armed, so the apply stays pending.
		s.log.Error("write config for confirm", "error", err)
		suffix := s.rollbackConfig(pending.BackupPath)
		s.redirectChanges(w, r, "Could not write the config to "+s.configDir()+": "+err.Error()+
			". Nothing was confirmed and the previous config is back on disk; BIRD is still running the new one"+
			" armed, so fix the cause and confirm again, or roll back."+suffix)
		return
	}
	// A dropped connection here must not leave BIRD confirmed while birdy still
	// records the version as pending; detach so confirm-and-record completes.
	applyCtx := context.WithoutCancel(r.Context())
	res, err := s.client.ConfigureConfirm(applyCtx)
	if err == nil && res.Code == birdc.CodeNothingToDo {
		// BIRD had no armed reconfigure to keep, so it is not running this apply:
		// its timeout fired (birdy's countdown is only an estimate of BIRD's), it
		// restarted, or something reconfigured it outside birdy. Keeping the new
		// files and stamping their hash would make disk and record agree with each
		// other while BIRD runs the previous config — a split no drift check sees.
		suffix, err := s.recordRevert(applyCtx, pending, "confirm",
			"Reverted before it was confirmed: BIRD had nothing to confirm (its timeout had fired, or it was restarted or reconfigured outside birdy).")
		if err != nil {
			s.serverError(w, "resolve version", err)
			return
		}
		s.emitAuditedEvent(r, store.EventConfigRevert,
			fmt.Sprintf("Config apply found already reverted by BIRD at confirm (version %d)", pending.ID))
		s.redirectChanges(w, r, "BIRD had nothing to confirm, so it is no longer running this apply: its safety timeout"+
			" already reverted it, or BIRD was restarted or reconfigured outside birdy. The previous config is back on"+
			" disk and the apply is recorded as reverted; apply again if you still want it."+suffix)
		return
	}
	if err != nil || !res.OK {
		if err != nil {
			s.log.Error("configure confirm", "error", err)
		}
		suffix := s.rollbackConfig(pending.BackupPath)
		msg := "BIRD would not confirm the apply"
		if err == nil {
			msg += ": " + res.Message
		}
		s.redirectChanges(w, r, msg+". The previous config is back on disk; roll back to clear this."+suffix)
		return
	}

	// One transaction: stamping the applied hash and resolving the version must
	// not tear, or a crash between them wedges the next apply on a phantom
	// pending change that BIRD already confirmed.
	if err := s.store.ConfirmAppliedVersion(pending.ID, pending.SHA256, store.ConfigConfirmed, "Confirmed."); err != nil {
		s.serverError(w, "confirm applied version", err)
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

	// bird.conf on disk is normally the previous config already (restored when
	// the timeout was armed), so this mainly tells BIRD to drop the armed config
	// and records the outcome. Undo is best-effort: if the timeout already fired,
	// BIRD has nothing to undo and that is fine. Detach from the request so a
	// dropped connection cannot leave BIRD reverted while birdy records nothing.
	applyCtx := context.WithoutCancel(r.Context())
	res, err := s.client.ConfigureUndo(applyCtx)
	msg := "Rolled back."
	if err != nil {
		msg = "Rolled back (BIRD undo errored, but the previous config is on disk)."
		s.log.Warn("configure undo failed", "error", err)
	} else if !res.OK {
		msg = "Rolled back (" + res.Message + ")."
	}
	suffix, err := s.recordRevert(applyCtx, pending, "rollback", msg)
	if err != nil {
		s.serverError(w, "resolve version", err)
		return
	}
	s.emitAuditedEvent(r, store.EventConfigRevert, fmt.Sprintf("Config apply rolled back (version %d)", pending.ID))
	s.redirectChanges(w, r, msg+suffix)
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
	s.flashRedirect(w, r, "/changes", msg, false)
}
