package web

import (
	"context"
	"time"

	"github.com/floreabogdan/birdy/internal/store"
)

// Drift detection watches for the BIRD config on disk diverging from what birdy
// last applied. The authorship guard already refuses to overwrite a file birdy
// did not write, but it only fires when someone clicks Apply. This is the
// proactive half: a background check that alerts the moment the config changes
// out from under birdy — a hand edit, a `birdc configure`, a revert birdy did
// not perform — instead of the operator discovering it the next time they open
// the Changes page.
//
// It is inert until birdy owns a config (a stored applied hash), so a read-only
// viewer of a hand-written router never sees a false alarm.

// WatchDrift checks for config drift on a fixed interval until ctx is cancelled.
// interval <= 0 disables it. It owns lastAlerted, so no lock is needed: this is
// the only goroutine that touches it.
func (s *Server) WatchDrift(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastAlerted string // the on-disk hash we last alerted about; "" = in sync
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.checkDrift(&lastAlerted)
		}
	}
}

// checkDrift compares the logical config on disk to the hash birdy recorded when
// it last applied, and alerts once per distinct drift. lastAlerted dedupes a
// standing drift to a single alert and re-arms when the config returns to sync.
// It holds applyMu so it can never read a half-written config mid-apply.
func (s *Server) checkDrift(lastAlerted *string) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	settings, ok, err := s.store.GetSettings()
	if err != nil || !ok || settings.AppliedConfigHash == "" {
		// birdy owns nothing yet (read-only viewer, or never applied). Re-arm so a
		// later adopt/apply followed by a drift alerts cleanly.
		*lastAlerted = ""
		return
	}
	// During an armed apply the previous config is intentionally on disk while the
	// new one lives in memory; that is not drift.
	if _, pending, perr := s.store.PendingConfigVersion(); perr == nil && pending {
		return
	}

	logical, exists, err := s.readLogicalConfig()
	if err != nil {
		// Unreadable (e.g. permission denied) is surfaced on the Changes page as a
		// distinct condition, not as drift.
		return
	}
	onDisk := "<missing>"
	if exists {
		onDisk = hashBytes([]byte(logical))
	}
	if exists && onDisk == settings.AppliedConfigHash {
		*lastAlerted = "" // in sync — re-arm for a future drift
		return
	}
	if onDisk == *lastAlerted {
		return // already alerted for this exact state
	}
	*lastAlerted = onDisk

	msg := "The BIRD config on disk no longer matches what birdy applied — it was " +
		"edited, reconfigured with birdc, or reverted outside birdy. Review it under Changes."
	if !exists {
		msg = "The BIRD config birdy applied is gone from disk. Review it under Changes."
	}
	s.emitEvent(store.EventConfigDrift, "", msg)
}
