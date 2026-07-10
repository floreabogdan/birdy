// Package poller periodically polls the BIRD control socket, keeps an
// in-memory snapshot for the dashboard, and records session transitions,
// flaps and import-limit hits to the event timeline.
package poller

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/store"
)

// flapWindow: an up transition that follows a down transition within this
// window is logged as a flap rather than a plain session-up.
const flapWindow = 3 * time.Minute

// ProtoState is the poller's last-known view of one protocol.
type ProtoState struct {
	Summary    birdc.ProtocolSummary
	Up         bool
	LastChange time.Time
	LimitHit   map[string]bool // by channel AFI
	// Channels holds the per-AFI route counts and limits from the most
	// recent detail fetch; nil for non-BGP protocols and for sessions
	// that are down (no detail is fetched then).
	Channels []birdc.ChannelDetail
}

// Snapshot is the latest poll result, safe to read concurrently via Poller.Snapshot.
type Snapshot struct {
	Status      birdc.Status
	Protocols   []birdc.ProtocolSummary
	States      map[string]ProtoState
	TotalRoutes int // sum of RouteCountEntry.Routes across all tables
	UpdatedAt   time.Time
	Err         error
}

// birdClient is the subset of *birdc.Client the poller needs; it exists so
// tests can drive the transition/flap/limit-hit logic with a fake instead of
// a live BIRD socket.
type birdClient interface {
	Status() (birdc.Status, error)
	Protocols() ([]birdc.ProtocolSummary, error)
	ProtocolDetail(name string) (birdc.ProtocolDetail, error)
	RouteCount() ([]birdc.RouteCountEntry, error)
}

// Notifier receives every event the poller records, so an out-of-band channel
// (a webhook) can alert on session changes. Optional: nil means no alerts.
type Notifier interface {
	Notify(kind, protocol, message string)
}

type Poller struct {
	client   birdClient
	store    *store.Store
	interval time.Duration
	log      *slog.Logger
	notifier Notifier

	mu             sync.RWMutex
	snap           Snapshot
	initialized    bool // false until the first poll completes, so we don't log spurious transitions at startup
	birdReachable  bool // last-known reachability of the control socket, for edge-triggered alerts
	reachableKnown bool // whether birdReachable has been set at least once
}

// SetNotifier attaches an alert sink. Call before Run.
func (p *Poller) SetNotifier(n Notifier) { p.notifier = n }

// emit records an event and forwards it to the notifier. Every event birdy logs
// is a candidate alert; the notifier decides whether a webhook is configured.
func (p *Poller) emit(kind, protocol, message string) {
	if err := p.store.InsertEvent(kind, protocol, message); err != nil {
		p.log.Warn("failed to record event", "error", err)
	}
	if p.notifier != nil {
		p.notifier.Notify(kind, protocol, message)
	}
}

func New(client birdClient, st *store.Store, interval time.Duration, log *slog.Logger) *Poller {
	if log == nil {
		log = slog.Default()
	}
	return &Poller{
		client:   client,
		store:    st,
		interval: interval,
		log:      log,
		snap:     Snapshot{States: map[string]ProtoState{}},
	}
}

// Snapshot returns a copy of the latest poll result.
func (p *Poller) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	states := make(map[string]ProtoState, len(p.snap.States))
	maps.Copy(states, p.snap.States)
	return Snapshot{
		Status:      p.snap.Status,
		Protocols:   append([]birdc.ProtocolSummary(nil), p.snap.Protocols...),
		States:      states,
		TotalRoutes: p.snap.TotalRoutes,
		UpdatedAt:   p.snap.UpdatedAt,
		Err:         p.snap.Err,
	}
}

// Run polls on a fixed interval until ctx is cancelled. It polls once
// immediately before entering the loop so a dashboard load right after
// startup already has data.
func (p *Poller) Run(ctx context.Context) {
	p.poll()
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.poll()
		}
	}
}

// poll does all BIRD I/O and event-log writes first, building the next
// snapshot entirely from local state, then publishes it under a single lock.
// This keeps the lock held only for the in-memory copy, never across socket
// round trips, and avoids any unsynchronized access to the shared map.
func (p *Poller) poll() {
	status, statusErr := p.client.Status()
	if statusErr != nil {
		p.log.Warn("show status failed", "error", statusErr)
	}

	protocols, err := p.client.Protocols()
	if err != nil {
		// BIRD is unreachable. Alert on the transition — this is the one failure
		// the session-transition alerts can never catch, because detecting a
		// session change needs a working poll. Edge-triggered, so a persistent
		// outage pages once, not every interval.
		if p.reachableKnown && p.birdReachable {
			p.emit(store.EventBirdUnreach, "", "BIRD is unreachable: "+err.Error())
		}
		p.birdReachable, p.reachableKnown = false, true
		p.mu.Lock()
		p.snap.Err = err
		p.snap.UpdatedAt = time.Now()
		p.mu.Unlock()
		p.log.Warn("poll failed", "error", err)
		return
	}
	if p.reachableKnown && !p.birdReachable {
		p.emit(store.EventBirdReachable, "", "BIRD is reachable again")
	}
	p.birdReachable, p.reachableKnown = true, true

	prevStates := p.Snapshot().States
	first := !p.initialized
	next := make(map[string]ProtoState, len(protocols))

	for _, proto := range protocols {
		up := isUp(proto)
		prior, seen := prevStates[proto.Name]

		state := ProtoState{Summary: proto, Up: up, LimitHit: map[string]bool{}}
		switch {
		case seen:
			state.LastChange = prior.LastChange
			maps.Copy(state.LimitHit, prior.LimitHit)
		default:
			state.LastChange = time.Now()
		}

		if !first && (!seen || prior.Up != up) {
			state.LastChange = time.Now()
			p.recordTransition(proto, prior, seen, up)
		}

		if proto.Proto == "BGP" && up {
			p.updateImportLimits(proto, &state)
		}

		next[proto.Name] = state
	}

	totalRoutes := 0
	if counts, err := p.client.RouteCount(); err != nil {
		p.log.Warn("show route count failed", "error", err)
	} else {
		for _, c := range counts {
			totalRoutes += c.Routes
		}
	}

	p.mu.Lock()
	p.initialized = true
	p.snap = Snapshot{Status: status, Protocols: protocols, States: next, TotalRoutes: totalRoutes, UpdatedAt: time.Now()}
	p.mu.Unlock()
}

func isUp(p birdc.ProtocolSummary) bool {
	if p.Proto == "BGP" {
		return strings.TrimSpace(p.Info) == "Established"
	}
	return p.State == "up"
}

func (p *Poller) recordTransition(proto birdc.ProtocolSummary, prior ProtoState, seen bool, up bool) {
	kind := store.EventSessionDown
	msg := fmt.Sprintf("%s (%s) went down", proto.Name, proto.Proto)
	if up {
		kind = store.EventSessionUp
		msg = fmt.Sprintf("%s (%s) established", proto.Name, proto.Proto)
		if seen && !prior.LastChange.IsZero() && time.Since(prior.LastChange) < flapWindow {
			kind = store.EventFlap
			msg = fmt.Sprintf("%s (%s) flapped (down for %s)", proto.Name, proto.Proto, time.Since(prior.LastChange).Round(time.Second))
		}
	}
	p.emit(kind, proto.Name, msg)
}

// updateImportLimits fetches protocol detail for one BGP session, keeps the
// per-channel route counts for the dashboard, and logs an event the first
// time a channel's imported route count reaches its import limit, mutating
// state in place.
func (p *Poller) updateImportLimits(proto birdc.ProtocolSummary, state *ProtoState) {
	detail, err := p.client.ProtocolDetail(proto.Name)
	if err != nil {
		return
	}
	state.Channels = detail.Channels
	for _, ch := range detail.Channels {
		limit, err := strconv.Atoi(strings.TrimSpace(ch.ImportLimit))
		if err != nil || limit <= 0 {
			continue
		}
		hit := ch.RoutesImported >= limit
		if hit && !state.LimitHit[ch.AFI] {
			msg := fmt.Sprintf("%s (%s): import limit reached (%d/%d)", proto.Name, ch.AFI, ch.RoutesImported, limit)
			p.emit(store.EventLimitHit, proto.Name, msg)
		}
		state.LimitHit[ch.AFI] = hit
	}
}
