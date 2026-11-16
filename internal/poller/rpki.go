package poller

import (
	"context"
	"maps"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
)

// rpkiInterval is how often the poller reads each RPKI protocol's detail. ROA
// data moves on a scale of minutes to hours; a socket round trip per feed on
// every session poll would buy nothing.
const rpkiInterval = time.Minute

// rpkiForget is how long a feed missing from "show protocols" is remembered.
// BIRD can answer partially while it reloads ROA tables, and that must not
// reset how long a serial has stood still; a feed removed by an apply ages out.
const rpkiForget = 10 * time.Minute

// RPKIFeed is the poller's view of one RPKI protocol's session with its RTR
// cache.
type RPKIFeed struct {
	Cache birdc.RPKICache
	// ROAs4 and ROAs6 are the ROAs this feed holds in the rpki4/rpki6 tables. They
	// are the number to compare across routers fed by the same cache: a feed that
	// is connected and refreshing but stuck on an old serial falls behind here.
	ROAs4, ROAs6 int
	// SerialSince is when birdy first saw the current session and serial. BIRD's
	// "Last update" resets on every refresh even when the cache has nothing new,
	// so this is the only clock on how long the data has stood still. It starts
	// when birdy starts, so it understates the age after a restart of birdy.
	SerialSince time.Time
	// CheckedAt is the last successful read of the feed.
	CheckedAt time.Time
}

// SerialAge is how long the feed's serial has stood still, as far as birdy has
// seen. How long is too long depends on the cache: a healthy public cache has
// been seen to go hours without a new serial, so this is a number to show and to
// compare, not a verdict on its own.
func (f RPKIFeed) SerialAge(now time.Time) time.Duration {
	if f.SerialSince.IsZero() {
		return 0
	}
	return now.Sub(f.SerialSince)
}

// RPKIFeeds returns a copy of the latest view of every RPKI protocol, by name.
func (p *Poller) RPKIFeeds() map[string]RPKIFeed {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return maps.Clone(p.rpki)
}

// RefreshRPKISoon makes the next poll re-read the RPKI feeds instead of waiting
// out rpkiInterval — after an operator restarted one, so the page shows the new
// session within seconds. Safe to call from any goroutine.
func (p *Poller) RefreshRPKISoon() { p.rpkiRefresh.Store(true) }

// refreshRPKI re-reads every RPKI protocol in this poll's reply, when due, and
// publishes the result. Runs only on the poll goroutine: p.rpki is replaced
// wholesale under the lock and read without it here, its only writer.
func (p *Poller) refreshRPKI(ctx context.Context, protocols []birdc.ProtocolSummary, now time.Time) {
	var names []string
	due := p.lastRPKI.IsZero() || now.Sub(p.lastRPKI) >= rpkiInterval || p.rpkiRefresh.Swap(false)
	for _, proto := range protocols {
		if proto.Proto != "RPKI" {
			continue
		}
		names = append(names, proto.Name)
		if _, known := p.rpki[proto.Name]; !known {
			due = true // a feed an apply just added: show it now, not in a minute
		}
	}
	if !due {
		return
	}
	p.lastRPKI = now

	next := make(map[string]RPKIFeed, len(names))
	for _, name := range names {
		prior, seen := p.rpki[name]
		d, err := p.client.ProtocolDetail(ctx, name)
		if err != nil || d.RPKI == nil {
			// A transient read failure says nothing about the feed; keep what we knew.
			if seen {
				next[name] = prior
			}
			continue
		}
		f := RPKIFeed{Cache: *d.RPKI, CheckedAt: now, SerialSince: now}
		f.ROAs4, f.ROAs6 = d.RPKI.ROAs(d.Channels)
		// Serials are per session: the same number under a new session is new data.
		if seen && prior.Cache.SessionID == f.Cache.SessionID && prior.Cache.Serial == f.Cache.Serial {
			f.SerialSince = prior.SerialSince
		}
		next[name] = f
	}
	for name, f := range p.rpki {
		if _, ok := next[name]; !ok && now.Sub(f.CheckedAt) < rpkiForget {
			next[name] = f
		}
	}

	p.mu.Lock()
	p.rpki = next
	p.mu.Unlock()
}
