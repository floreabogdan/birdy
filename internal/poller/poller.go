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

// Counting every route in every table is materially slower than reading
// protocol/session state on full-table routers. Keep session visibility fast
// and refresh the aggregate on a slower cadence.
const routeCountInterval = time.Minute

// Status contains mostly static identity/version data. Do not ask the control
// socket for it on every session poll; a short cache preserves quick recovery
// while removing one command from the hot path.
const statusInterval = 30 * time.Second

const disabledPeerInterval = 10 * time.Second

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
	// Imported is the total accepted routes across the session's channels, kept
	// so a poll can spot a sharp drop against the previous one.
	Imported int
	// Disabled reports that birdy's model has this peer switched off. BIRD reports
	// such a protocol as simply "down", which is indistinguishable from a session
	// that failed — the model is the only place that knows the difference.
	Disabled bool
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

// Poller periodically reads BIRD's control socket into an in-memory snapshot for
// the dashboard, records session transitions/flaps/limit hits to the timeline,
// and samples route counts for the history sparklines.
type Poller struct {
	client    birdClient
	store     *store.Store
	interval  time.Duration
	log       *slog.Logger
	notifier  Notifier
	dropRatio float64 // alert if imported falls to <= this fraction of the prior poll

	// Route-count history sampling. Cheaper than a poll: written on a slow
	// cadence to back the dashboard sparklines, then pruned to a window. Both
	// touched only from the single poll goroutine, so they need no lock.
	sampleInterval time.Duration
	sampleRetain   time.Duration
	lastSample     time.Time
	lastRouteCount time.Time

	mu             sync.RWMutex
	snap           Snapshot
	initialized    bool // false until the first poll completes, so we don't log spurious transitions at startup
	birdReachable  bool // last-known reachability of the control socket, for edge-triggered alerts
	reachableKnown bool // whether birdReachable has been set at least once
	lastStatus     birdc.Status
	lastStatusAt   time.Time
	disabledPeers  map[string]bool
	disabledAt     time.Time
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
		client:    client,
		store:     st,
		interval:  interval,
		log:       log,
		dropRatio: 0.5,
		snap:      Snapshot{States: map[string]ProtoState{}},
	}
}

// SetDropRatio configures the prefix-drop alert threshold: a session's imported
// count falling to this fraction (or less) of the previous poll fires an alert.
// 0 disables the check. Call before Run.
func (p *Poller) SetDropRatio(r float64) { p.dropRatio = r }

// SetSampling enables periodic route-count history sampling: one point per up
// BGP session every interval, retained for retain. interval 0 disables it. Call
// before Run.
func (p *Poller) SetSampling(interval, retain time.Duration) {
	p.sampleInterval = interval
	p.sampleRetain = retain
}

// maybeSample records a route-count point per up BGP session, at most once per
// sampleInterval, then prunes past the retention window. Called at the end of
// each poll; a no-op until enough time has passed. Runs only on the poll
// goroutine, so lastSample needs no lock.
func (p *Poller) maybeSample(states map[string]ProtoState, now time.Time) {
	if p.sampleInterval <= 0 {
		return
	}
	if !p.lastSample.IsZero() && now.Sub(p.lastSample) < p.sampleInterval {
		return
	}
	p.lastSample = now

	samples := make([]store.Sample, 0, len(states))
	for name, st := range states {
		if st.Summary.Proto != "BGP" || !st.Up || len(st.Channels) == 0 {
			continue // down sessions leave a gap in the line, which is the point
		}
		imported, exported := 0, 0
		for _, ch := range st.Channels {
			imported += ch.RoutesImported
			exported += ch.RoutesExported
		}
		samples = append(samples, store.Sample{Ts: now, Protocol: name, Imported: imported, Exported: exported})
	}
	if err := p.store.InsertSamples(samples); err != nil {
		p.log.Warn("record route samples failed", "error", err)
	}
	if p.sampleRetain > 0 {
		if err := p.store.PruneSamples(now.Add(-p.sampleRetain)); err != nil {
			p.log.Warn("prune route samples failed", "error", err)
		}
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
	now := time.Now()
	status := p.lastStatus
	if p.lastStatusAt.IsZero() || now.Sub(p.lastStatusAt) >= statusInterval {
		var statusErr error
		status, statusErr = p.client.Status()
		p.lastStatusAt = now
		if statusErr != nil {
			p.log.Warn("show status failed", "error", statusErr)
		} else {
			p.lastStatus = status
		}
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

	// A peer switched off in the model is *meant* to be down. BIRD still reports
	// its protocol (rendered with "disabled"), so without this the act of applying
	// a disable would fire a session-down alert and wake somebody up over a change
	// they made on purpose.
	disabled := p.disabledPeers
	if disabled == nil || p.disabledAt.IsZero() || now.Sub(p.disabledAt) >= disabledPeerInterval {
		var disabledErr error
		disabled, disabledErr = p.store.DisabledPeerNames()
		p.disabledAt = now
		if disabledErr != nil {
			p.log.Warn("could not read disabled peers; treating all as enabled", "error", disabledErr)
			disabled = map[string]bool{}
		}
		p.disabledPeers = disabled
	}

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

		state.Disabled = disabled[proto.Name]

		if !first && (!seen || prior.Up != up) {
			state.LastChange = time.Now()
			// Only BGP sessions get down/up/flap events. Infrastructure protocols
			// (RPKI, device, kernel, static, direct) are not sessions, and treating
			// them as such alerts on things like an RPKI RTR cache reconnecting —
			// harmless, but noisy. Their state is still on the dashboard's infra card.
			// A peer disabled in the model is excluded for the same reason: its
			// session going down is the change working, not a fault.
			if proto.Proto == "BGP" && !state.Disabled {
				p.recordTransition(proto, prior, seen, up)
			}
		}

		if proto.Proto == "BGP" && up {
			p.updateImportLimits(proto, &state)
			p.checkPrefixDrop(proto, prior, seen, first, &state)
		}

		next[proto.Name] = state
	}

	// Publish session state before the expensive whole-RIB count. On a router
	// carrying full tables this is the difference between seeing the BGP rows
	// immediately and staring at an empty table until the count times out.
	previousTotal := p.Snapshot().TotalRoutes
	p.mu.Lock()
	p.initialized = true
	p.snap = Snapshot{Status: status, Protocols: protocols, States: next, TotalRoutes: previousTotal, UpdatedAt: time.Now()}
	p.mu.Unlock()

	now = time.Now()
	if p.lastRouteCount.IsZero() || now.Sub(p.lastRouteCount) >= routeCountInterval {
		p.lastRouteCount = now
		totalRoutes := 0
		if counts, err := p.client.RouteCount(); err != nil {
			p.log.Warn("show route count failed; keeping previous total", "error", err)
		} else {
			for _, c := range counts {
				if isROATable(c.Table) {
					continue // RPKI ROA tables hold ROAs, not routes
				}
				totalRoutes += c.Routes
			}
			p.mu.Lock()
			p.snap.TotalRoutes = totalRoutes
			p.mu.Unlock()
		}
	}

	p.maybeSample(next, time.Now())
}

func isUp(p birdc.ProtocolSummary) bool {
	if p.Proto == "BGP" {
		return strings.TrimSpace(p.Info) == "Established"
	}
	return p.State == "up"
}

// isROATable reports whether a routing-table name is an RPKI ROA table. birdy
// renders these as rpki4/rpki6 (see internal/render); their entries are ROAs,
// not routes, and pollute the RIB count — often dwarfing it by a million.
func isROATable(name string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "rpki")
}

// dropBaseline is the smallest prior count worth alerting on — below it, a
// "sharp drop" is just noise on a session that carries little.
const dropBaseline = 1000

// checkPrefixDrop alerts when a session's imported count falls sharply from the
// previous poll — the classic sign of a broken filter or a withdrawn table,
// which the session-state alerts (the session is still up) would miss entirely.
func (p *Poller) checkPrefixDrop(proto birdc.ProtocolSummary, prior ProtoState, seen, first bool, state *ProtoState) {
	if first || !seen || p.dropRatio <= 0 {
		return
	}
	if !prior.Up || prior.Imported < dropBaseline {
		return
	}
	if float64(state.Imported) <= float64(prior.Imported)*p.dropRatio {
		p.emit(store.EventPrefixDrop, proto.Name,
			fmt.Sprintf("%s: imported routes dropped from %d to %d", proto.Name, prior.Imported, state.Imported))
	}
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
		state.Imported += ch.RoutesImported
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
