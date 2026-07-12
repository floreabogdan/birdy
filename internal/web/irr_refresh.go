package web

import (
	"context"
	"fmt"
	"time"

	"github.com/floreabogdan/birdy/internal/irr"
	"github.com/floreabogdan/birdy/internal/store"
)

// Scheduled IRR refresh keeps prefix sets expanded from an IRR AS-SET current
// without hand-editing. It re-runs bgpq4 on a timer and writes the result into
// birdy's model, so a change appears as a normal pending diff on the Changes
// page. It NEVER applies: keeping a customer prefix filter fresh is worth
// automating; pushing it to the router without review is not.

// RunIRRRefresh refreshes every auto-refresh prefix set on a fixed interval
// until ctx is cancelled. It is a no-op unless bgpq4 is configured; interval <= 0
// disables it.
func (s *Server) RunIRRRefresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 || s.bgpq4Bin == "" {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refreshIRRSets(ctx)
		}
	}
}

func (s *Server) refreshIRRSets(ctx context.Context) {
	sets, err := s.store.ListAutoRefreshPrefixSets()
	if err != nil {
		s.log.Warn("irr refresh: list sets", "error", err)
		return
	}
	if len(sets) == 0 {
		return
	}
	client := irr.New(s.bgpq4Bin)
	for _, set := range sets {
		s.refreshOneSet(ctx, client, set)
	}
}

// refreshOneSet re-expands one set and records the outcome. On a real change it
// updates the model and alerts; on no change it just stamps the sync time; on
// failure — including an empty result that would wipe a populated set — it
// records the error and leaves the existing prefixes untouched.
func (s *Server) refreshOneSet(ctx context.Context, client *irr.Client, set store.PrefixSet) {
	prefixes, err := client.Prefixes(ctx, set.Source, set.Family == store.FamilyV6)
	if err != nil {
		s.log.Warn("irr refresh failed", "set", set.Name, "source", set.Source, "error", err)
		_ = s.store.MarkPrefixSetRefreshError(set.ID, err.Error())
		return
	}

	fresh := make([]store.PrefixEntry, 0, len(prefixes))
	for _, p := range prefixes {
		fresh = append(fresh, store.PrefixEntry{Prefix: p.Prefix, Modifier: p.Modifier})
	}

	// An empty expansion for a set that had prefixes is almost always a transient
	// IRR-mirror failure, not a real "announce nothing". Refuse to wipe the set.
	if len(fresh) == 0 && len(set.Entries) > 0 {
		s.log.Warn("irr refresh: empty result, keeping previous", "set", set.Name)
		_ = s.store.MarkPrefixSetRefreshError(set.ID, "IRR expansion returned no prefixes; kept the previous list")
		return
	}

	added, removed := diffEntries(set.Entries, fresh)
	if added == 0 && removed == 0 {
		if err := s.store.MarkPrefixSetRefreshed(set.ID); err != nil {
			s.log.Warn("irr refresh: stamp", "set", set.Name, "error", err)
		}
		return
	}
	if err := s.store.RefreshPrefixSetEntries(set.ID, fresh); err != nil {
		s.log.Warn("irr refresh: save", "set", set.Name, "error", err)
		return
	}
	s.emitEvent(store.EventIRRRefresh, "",
		fmt.Sprintf("Prefix set %s refreshed from %s (+%d/-%d) — review it under Changes and apply", set.Name, set.Source, added, removed))
}

// diffEntries counts prefixes added and removed between two entry lists,
// comparing on the full BIRD pattern (prefix plus length modifier).
func diffEntries(old, fresh []store.PrefixEntry) (added, removed int) {
	have := make(map[string]bool, len(old))
	for _, e := range old {
		have[e.Pattern()] = true
	}
	next := make(map[string]bool, len(fresh))
	for _, e := range fresh {
		next[e.Pattern()] = true
	}
	for k := range next {
		if !have[k] {
			added++
		}
	}
	for k := range have {
		if !next[k] {
			removed++
		}
	}
	return added, removed
}
