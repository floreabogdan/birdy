package poller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/store"
)

// fakeClient lets tests script a sequence of "show protocols" results without
// a live BIRD socket.
type fakeClient struct {
	polls  [][]birdc.ProtocolSummary
	detail map[string]birdc.ProtocolDetail
	counts []birdc.RouteCountEntry // optional; RouteCount returns a default when nil
	i      int
	step   int
	errAt  map[int]error // poll (call) number -> error, to script BIRD-unreachable
}

func (f *fakeClient) Status() (birdc.Status, error) { return birdc.Status{}, nil }

func (f *fakeClient) Protocols() ([]birdc.ProtocolSummary, error) {
	step := f.step
	f.step++
	if err, ok := f.errAt[step]; ok {
		return nil, err // an errored poll does not consume a scripted result
	}
	if f.i >= len(f.polls) {
		return f.polls[len(f.polls)-1], nil
	}
	p := f.polls[f.i]
	f.i++
	return p, nil
}

func (f *fakeClient) ProtocolDetail(name string) (birdc.ProtocolDetail, error) {
	if d, ok := f.detail[name]; ok {
		return d, nil
	}
	return birdc.ProtocolDetail{}, nil
}

func (f *fakeClient) RouteCount() ([]birdc.RouteCountEntry, error) {
	if f.counts != nil {
		return f.counts, nil
	}
	return []birdc.RouteCountEntry{{Table: "master4", Routes: 5, Networks: 4}}, nil
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "birdy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func bgp(name, state, info string) birdc.ProtocolSummary {
	return birdc.ProtocolSummary{Name: name, Proto: "BGP", Table: "---", State: state, Since: "2026-07-10", Info: info}
}

func TestPollerNoEventsOnFirstPoll(t *testing.T) {
	st := openTestStore(t)
	fc := &fakeClient{polls: [][]birdc.ProtocolSummary{{bgp("edge_v4", "up", "Established")}}}
	p := New(fc, st, time.Second, nil)

	p.poll()

	events, err := st.ListEvents(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expected no events on first poll (baseline), got %+v", events)
	}
	snap := p.Snapshot()
	if !snap.States["edge_v4"].Up {
		t.Fatalf("expected edge_v4 up in snapshot: %+v", snap.States["edge_v4"])
	}
}

func TestPollerDetectsDownThenFlap(t *testing.T) {
	st := openTestStore(t)
	fc := &fakeClient{polls: [][]birdc.ProtocolSummary{
		{bgp("edge_v4", "up", "Established")}, // baseline, no event
		{bgp("edge_v4", "start", "Connect")},  // down
		{bgp("edge_v4", "up", "Established")}, // back up quickly -> flap
	}}
	p := New(fc, st, time.Second, nil)

	p.poll()
	p.poll()
	p.poll()

	events, err := st.ListEvents(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (down, flap): %+v", len(events), events)
	}
	// newest first
	if events[0].Kind != store.EventFlap {
		t.Fatalf("events[0].Kind = %q, want flap: %+v", events[0].Kind, events[0])
	}
	if events[1].Kind != store.EventSessionDown {
		t.Fatalf("events[1].Kind = %q, want session_down: %+v", events[1].Kind, events[1])
	}
	if events[0].Protocol != "edge_v4" || events[1].Protocol != "edge_v4" {
		t.Fatalf("unexpected protocol on events: %+v", events)
	}
}

func TestPollerImportLimitHitOnce(t *testing.T) {
	st := openTestStore(t)
	detail := birdc.ProtocolDetail{
		Channels: []birdc.ChannelDetail{
			{AFI: "ipv4", ImportLimit: "10", RoutesImported: 10},
		},
	}
	fc := &fakeClient{
		polls: [][]birdc.ProtocolSummary{
			{bgp("edge_v4", "up", "Established")},
			{bgp("edge_v4", "up", "Established")},
			{bgp("edge_v4", "up", "Established")},
		},
		detail: map[string]birdc.ProtocolDetail{"edge_v4": detail},
	}
	p := New(fc, st, time.Second, nil)

	p.poll() // baseline
	p.poll() // limit already at cap -> should log once
	p.poll() // still at cap -> should NOT log again

	events, err := st.ListEvents(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var hits int
	for _, e := range events {
		if e.Kind == store.EventLimitHit {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("got %d limit_hit events, want exactly 1: %+v", hits, events)
	}
}

func TestPollerRunStopsOnContextCancel(t *testing.T) {
	st := openTestStore(t)
	fc := &fakeClient{polls: [][]birdc.ProtocolSummary{{bgp("edge_v4", "up", "Established")}}}
	p := New(fc, st, 10*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// The notifier receives the same events the poller records.
type capturingNotifier struct{ kinds []string }

func (c *capturingNotifier) Notify(kind, protocol, message string) {
	c.kinds = append(c.kinds, kind)
}

func TestPollerNotifiesOnTransition(t *testing.T) {
	st := openTestStore(t)
	fc := &fakeClient{polls: [][]birdc.ProtocolSummary{
		{bgp("edge_v4", "up", "Established")}, // baseline
		{bgp("edge_v4", "start", "Connect")},  // down -> event + notify
	}}
	n := &capturingNotifier{}
	p := New(fc, st, time.Second, nil)
	p.SetNotifier(n)

	p.poll()
	p.poll()

	if len(n.kinds) != 1 || n.kinds[0] != store.EventSessionDown {
		t.Fatalf("notifier kinds = %v, want [session_down]", n.kinds)
	}
}

func TestPollerAlertsWhenBirdUnreachable(t *testing.T) {
	st := openTestStore(t)
	fc := &fakeClient{
		polls: [][]birdc.ProtocolSummary{
			{bgp("edge_v4", "up", "Established")}, // step 0: baseline (reachable)
			{bgp("edge_v4", "up", "Established")}, // step 2: recovery
		},
		errAt: map[int]error{1: errBirdGone}, // step 1: unreachable
	}
	n := &capturingNotifier{}
	p := New(fc, st, time.Second, nil)
	p.SetNotifier(n)

	p.poll() // baseline reachable, no event
	p.poll() // unreachable -> bird_unreachable
	p.poll() // reachable again -> bird_reachable

	events, err := st.ListEvents(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	// newest first: reachable, then unreachable
	if len(events) != 2 || events[0].Kind != store.EventBirdReachable || events[1].Kind != store.EventBirdUnreach {
		t.Fatalf("want [reachable, unreachable], got %v", kinds)
	}
	if len(n.kinds) != 2 {
		t.Fatalf("both reachability transitions should notify, got %v", n.kinds)
	}
}

var errBirdGone = fmtError("dial /run/bird/bird.ctl: connection refused")

type fmtError string

func (e fmtError) Error() string { return string(e) }

func TestPollerAlertsOnPrefixDrop(t *testing.T) {
	st := openTestStore(t)
	full := birdc.ProtocolDetail{Channels: []birdc.ChannelDetail{{AFI: "ipv4", RoutesImported: 900000}}}
	broken := birdc.ProtocolDetail{Channels: []birdc.ChannelDetail{{AFI: "ipv4", RoutesImported: 50}}}
	fc := &fakeClient{
		polls: [][]birdc.ProtocolSummary{
			{bgp("edge_v4", "up", "Established")}, // baseline: 900k
			{bgp("edge_v4", "up", "Established")}, // 900k again (no drop)
			{bgp("edge_v4", "up", "Established")}, // 50 -> sharp drop
		},
		detail: map[string]birdc.ProtocolDetail{"edge_v4": full},
	}
	p := New(fc, st, time.Second, nil)

	p.poll() // baseline
	p.poll() // still 900k, no alert
	fc.detail["edge_v4"] = broken
	p.poll() // 50 -> prefix_drop

	events, _ := st.ListEvents(10, 0)
	var drops int
	for _, e := range events {
		if e.Kind == store.EventPrefixDrop {
			drops++
		}
	}
	if drops != 1 {
		t.Fatalf("want exactly 1 prefix_drop event, got %d: %+v", drops, events)
	}
}

// A small session dropping is noise, not an alert.
func TestPollerIgnoresSmallDrops(t *testing.T) {
	st := openTestStore(t)
	fc := &fakeClient{
		polls: [][]birdc.ProtocolSummary{
			{bgp("edge_v4", "up", "Established")},
			{bgp("edge_v4", "up", "Established")},
		},
		detail: map[string]birdc.ProtocolDetail{"edge_v4": {Channels: []birdc.ChannelDetail{{AFI: "ipv4", RoutesImported: 100}}}},
	}
	p := New(fc, st, time.Second, nil)
	p.poll()
	fc.detail["edge_v4"] = birdc.ProtocolDetail{Channels: []birdc.ChannelDetail{{AFI: "ipv4", RoutesImported: 1}}}
	p.poll()
	events, _ := st.ListEvents(10, 0)
	for _, e := range events {
		if e.Kind == store.EventPrefixDrop {
			t.Errorf("a drop below the baseline should not alert: %+v", e)
		}
	}
}

func TestIsROATable(t *testing.T) {
	roa := []string{"rpki4", "rpki6", "RPKI4", "rpki_cache", " rpki4 "}
	for _, n := range roa {
		if !isROATable(n) {
			t.Errorf("isROATable(%q) = false, want true", n)
		}
	}
	notRoa := []string{"master4", "master6", "", "rp", "ki4", "backbone"}
	for _, n := range notRoa {
		if isROATable(n) {
			t.Errorf("isROATable(%q) = true, want false", n)
		}
	}
}

func TestTotalRoutesExcludesROATables(t *testing.T) {
	fc := &fakeClient{
		polls:  [][]birdc.ProtocolSummary{{bgp("edge_v4", "up", "Established")}},
		counts: []birdc.RouteCountEntry{{Table: "master4", Routes: 5}, {Table: "master6", Routes: 5}, {Table: "rpki4", Routes: 745832}, {Table: "rpki6", Routes: 228482}},
	}
	st := openTestStore(t)
	p := New(fc, st, time.Second, nil)
	p.poll()
	if got := p.Snapshot().TotalRoutes; got != 10 {
		t.Fatalf("TotalRoutes = %d, want 10 (ROA tables excluded)", got)
	}
}
