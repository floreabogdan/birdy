package web

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/floreabogdan/birdy/internal/irr"
	"github.com/floreabogdan/birdy/internal/store"
)

// Scheduled IRR refresh keeps the sets expanded from an IRR AS-SET current
// without hand-editing — prefix sets (what a peer may announce) and AS sets
// (which origins it may announce them from). It re-runs bgpq4 on a timer and
// writes the result into birdy's model, so a change appears as a normal pending
// diff on the Changes page. It NEVER applies: keeping a customer filter fresh is
// worth automating; pushing it to the router without review is not.

// RunIRRRefresh refreshes every auto-refresh set on a fixed interval until ctx is
// cancelled. It is a no-op unless bgpq4 is configured; interval <= 0 disables it.
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
	client := irr.New(s.bgpq4Bin)

	sets, err := s.store.ListAutoRefreshPrefixSets()
	if err != nil {
		s.log.Warn("irr refresh: list prefix sets", "error", err)
	}
	for _, set := range sets {
		s.refreshOneSet(ctx, client, set)
	}

	asSets, err := s.store.ListAutoRefreshASSets()
	if err != nil {
		s.log.Warn("irr refresh: list AS sets", "error", err)
	}
	for _, as := range asSets {
		s.refreshOneASSet(ctx, client, as)
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

// refreshOneASSet re-expands one AS set's members from its IRR AS-SET and
// records the outcome, with the same guardrails as a prefix set: an empty
// expansion never wipes a populated set, and a change lands in the model as a
// pending diff rather than on the router. It returns a message describing what
// happened, for the "Refresh now" button's flash; the timer ignores it.
func (s *Server) refreshOneASSet(ctx context.Context, client *irr.Client, set store.ASSet) string {
	asns, err := client.ASNs(ctx, set.Source)
	if err != nil {
		s.log.Warn("irr refresh failed", "as_set", set.Name, "source", set.Source, "error", err)
		_ = s.store.MarkASSetRefreshError(set.ID, err.Error())
		return "Could not expand " + set.Source + ": " + err.Error()
	}

	// bgpq4 answers an unknown AS-SET, or a mirror that is having a bad day, with
	// an empty list and exit 0. Emptying the set would turn "only these origins"
	// into a filter that rejects every route, so keep what we have and say why.
	if len(asns) == 0 {
		s.log.Warn("irr refresh: empty result, keeping previous", "as_set", set.Name)
		msg := "IRR expansion returned no AS numbers; kept the previous members"
		_ = s.store.MarkASSetRefreshError(set.ID, msg)
		return set.Source + " expanded to nothing — kept " + set.Name + " as it was."
	}

	// Members carry hand-written notes ("the customer", "their downstream"); the
	// IRR does not, so carry them across the refresh rather than losing them.
	notes := make(map[int64]string, len(set.Entries))
	for _, e := range set.Entries {
		if e.Note != "" && e.Low == e.High {
			notes[e.Low] = e.Note
		}
	}
	fresh := make([]store.ASNRange, 0, len(asns))
	for _, asn := range asns {
		fresh = append(fresh, store.ASNRange{Low: asn, High: asn, Note: notes[asn]})
	}

	added, removed := diffASNs(set.Entries, fresh)
	if added == 0 && removed == 0 {
		if err := s.store.MarkASSetRefreshed(set.ID); err != nil {
			s.log.Warn("irr refresh: stamp", "as_set", set.Name, "error", err)
		}
		return set.Name + " is already up to date with " + set.Source + " (" + strconv.Itoa(len(fresh)) + " members)."
	}
	if err := s.store.RefreshASSetEntries(set.ID, fresh); err != nil {
		s.log.Warn("irr refresh: save", "as_set", set.Name, "error", err)
		return "Could not save the refreshed " + set.Name + ": " + err.Error()
	}
	s.emitEvent(store.EventIRRRefresh, "",
		fmt.Sprintf("AS set %s refreshed from %s (+%d/-%d) — review it under Changes and apply", set.Name, set.Source, added, removed))
	return fmt.Sprintf("Refreshed %s from %s: +%d/-%d, now %d members. Review it under Changes and apply.",
		set.Name, set.Source, added, removed, len(fresh))
}

// diffASNs counts members added and removed between two AS lists.
func diffASNs(old, fresh []store.ASNRange) (added, removed int) {
	have := make(map[store.ASNRange]bool, len(old))
	for _, e := range old {
		have[store.ASNRange{Low: e.Low, High: e.High}] = true
	}
	next := make(map[store.ASNRange]bool, len(fresh))
	for _, e := range fresh {
		next[store.ASNRange{Low: e.Low, High: e.High}] = true
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
