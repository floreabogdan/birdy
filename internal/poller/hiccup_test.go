package poller

import (
	"context"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
	"github.com/floreabogdan/birdy/internal/store"
)

// While BIRD reloads its ROA tables and re-validates full BGP tables, "show
// protocols" has been seen to come back empty or with rows short of words for a
// few seconds. These tests pin down that such a reply never pages anybody.

// The two empty shapes: the column header with no rows, and a bare terminal line.
var (
	headerOnlyReply = birdc.Reply{
		Blocks:   []birdc.Block{{Code: 2002, Lines: []string{"Name       Proto      Table      State  Since         Info"}}},
		Terminal: birdc.Block{Code: 0, Lines: []string{""}},
	}
	terminalOnlyReply = birdc.Reply{Terminal: birdc.Block{Code: 0, Lines: []string{""}}}
	// A rows block whose only line is blank: it parses, to no protocols at all.
	blankRowsReply = birdc.Reply{
		Blocks: []birdc.Block{
			{Code: 2002, Lines: []string{"Name       Proto      Table      State  Since         Info"}},
			{Code: 1002, Lines: []string{""}},
		},
		Terminal: birdc.Block{Code: 0, Lines: []string{""}},
	}
)

// testClock is a hand-wound clock for the poller, so a test can step past the
// hiccup grace without sleeping.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func withClock(p *Poller) *testClock {
	c := &testClock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	p.clock = c.now
	return c
}

func eventKinds(t *testing.T, st *store.Store) []string {
	t.Helper()
	events, err := st.ListEvents(100, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- { // oldest first reads like the poll sequence
		kinds = append(kinds, events[i].Kind)
	}
	return kinds
}

func TestEmptyProtocolsReplyIsAnUnknownPoll(t *testing.T) {
	for name, reply := range map[string]birdc.Reply{
		"header only":   headerOnlyReply,
		"terminal only": terminalOnlyReply,
		"no rows":       blankRowsReply,
	} {
		t.Run(name, func(t *testing.T) {
			st := openTestStore(t)
			fc := &fakeClient{
				polls:   [][]birdc.ProtocolSummary{{bgp("edge_v4", "up", "Established")}},
				replyAt: map[int]birdc.Reply{1: reply},
			}
			n := &capturingNotifier{}
			p := New(fc, st, time.Second, nil)
			p.SetNotifier(n)
			clk := withClock(p)

			p.poll(context.Background()) // baseline
			clk.advance(5 * time.Second)
			p.poll(context.Background()) // the empty reply
			snap := p.Snapshot()
			if snap.Err != nil {
				t.Errorf("an empty reply marked BIRD unreachable on the dashboard: %v", snap.Err)
			}
			if st, ok := snap.States["edge_v4"]; !ok || !st.Up || len(snap.Protocols) != 1 {
				t.Errorf("an empty reply must leave the last snapshot in place, got %+v", snap)
			}
			clk.advance(5 * time.Second)
			p.poll(context.Background()) // BIRD answers properly again

			if kinds := eventKinds(t, st); len(kinds) != 0 || len(n.kinds) != 0 {
				t.Fatalf("an empty reply paged: events %v, notified %v", kinds, n.kinds)
			}
		})
	}
}

// Unknown is for a hiccup. A BIRD that keeps answering with nothing readable has
// left birdy blind, and that must still reach somebody.
func TestUnreadableRepliesPastTheGraceAreReported(t *testing.T) {
	st := openTestStore(t)
	steps := map[int]birdc.Reply{}
	for i := 1; i <= 20; i++ {
		steps[i] = headerOnlyReply
	}
	fc := &fakeClient{
		polls:   [][]birdc.ProtocolSummary{{bgp("edge_v4", "up", "Established")}},
		replyAt: steps,
	}
	p := New(fc, st, time.Second, nil)
	clk := withClock(p)

	p.poll(context.Background()) // baseline
	for range 20 {
		clk.advance(5 * time.Second)
		p.poll(context.Background())
	}
	if p.Snapshot().Err == nil {
		t.Error("100s of unreadable replies should show on the dashboard as an error")
	}
	clk.advance(5 * time.Second)
	p.poll(context.Background()) // readable again

	want := []string{store.EventBirdUnreach, store.EventBirdReachable}
	if kinds := eventKinds(t, st); len(kinds) != 2 || kinds[0] != want[0] || kinds[1] != want[1] {
		t.Fatalf("events = %v, want %v", kinds, want)
	}
}

// A row missing from one reply is not a protocol that went away: nothing about
// it may be forgotten. Before, the up session came back as "established", the
// down one as "went down", and the limit already reported was reported again.
func TestProtocolMissingFromOneReplyKeepsItsState(t *testing.T) {
	st := openTestStore(t)
	upV4 := bgp("edge_v4", "up", "Established")
	downV6 := bgp("edge_v6", "start", "Connect")
	fc := &fakeClient{
		polls: [][]birdc.ProtocolSummary{
			{upV4, downV6}, // baseline
			{upV4, downV6}, // edge_v4 reaches its limit: one limit_hit
			{},             // placeholder, replaced below
			{upV4, downV6}, // both rows are back
		},
		detail: map[string]birdc.ProtocolDetail{
			"edge_v4": {Channels: []birdc.ChannelDetail{{AFI: "ipv4", ImportLimit: "10", RoutesImported: 5}}},
		},
	}
	// The third reply lists only the device protocol: both sessions missing.
	fc.polls[2] = []birdc.ProtocolSummary{{Name: "device1", Proto: "Device", Table: "---", State: "up", Since: "12:00:00.124"}}
	n := &capturingNotifier{}
	p := New(fc, st, time.Second, nil)
	p.SetNotifier(n)
	clk := withClock(p)

	p.poll(context.Background())
	fc.detail["edge_v4"] = birdc.ProtocolDetail{Channels: []birdc.ChannelDetail{{AFI: "ipv4", ImportLimit: "10", RoutesImported: 10}}}
	for range 3 {
		clk.advance(5 * time.Second)
		p.poll(context.Background())
	}

	if kinds := eventKinds(t, st); len(kinds) != 1 || kinds[0] != store.EventLimitHit {
		t.Fatalf("events = %v, want only the one limit_hit", kinds)
	}
}

// Carried forward is not kept forever: a peer removed by an apply, or renamed,
// leaves the poller's state once the grace has passed.
func TestProtocolGoneForLongerThanTheGraceIsForgotten(t *testing.T) {
	st := openTestStore(t)
	device := birdc.ProtocolSummary{Name: "device1", Proto: "Device", Table: "---", State: "up", Since: "12:00:00.124"}
	fc := &fakeClient{polls: [][]birdc.ProtocolSummary{
		{device, bgp("edge_v4", "up", "Established")},
		{device},
	}}
	p := New(fc, st, time.Second, nil)
	clk := withClock(p)

	p.poll(context.Background())
	clk.advance(5 * time.Second)
	p.poll(context.Background())
	if st, ok := p.Snapshot().States["edge_v4"]; !ok || !st.Up {
		t.Fatalf("a protocol missing from one reply must be carried forward, got %+v (present %v)", st, ok)
	}
	if got := len(p.Snapshot().Protocols); got != 1 {
		t.Errorf("the published rows are what BIRD reported (1), got %d", got)
	}
	for range 12 {
		clk.advance(5 * time.Second)
		p.poll(context.Background())
	}
	if _, ok := p.Snapshot().States["edge_v4"]; ok {
		t.Fatal("a protocol gone for longer than the grace must be forgotten")
	}
	if kinds := eventKinds(t, st); len(kinds) != 0 {
		t.Fatalf("a protocol leaving BIRD's list is not a session event, got %v", kinds)
	}
}

// rowsReply wraps "show protocols" rows in the reply framing, so the test row
// goes through the real parser exactly as BIRD's would.
func rowsReply(rows ...string) birdc.Reply {
	return birdc.Reply{
		Blocks: []birdc.Block{
			{Code: 2002, Lines: []string{"Name       Proto      Table      State  Since         Info"}},
			{Code: 1002, Lines: rows},
		},
		Terminal: birdc.Block{Code: 0, Lines: []string{""}},
	}
}

// BIRD names the BGP state in Info for every BGP protocol that is not down, so
// a row in state "up" without it lost words in transit. It says nothing about
// the session, and must not read as down: that paged "went down" and then
// "flapped (down for 0s)".
func TestTruncatedBGPRowKeepsThePriorState(t *testing.T) {
	for name, row := range map[string]string{
		"info missing":           "edge_v4    BGP        ---        up     12:00:00.124",
		"table and info missing": "edge_v4    BGP        up     12:00:00.124",
	} {
		t.Run(name, func(t *testing.T) {
			st := openTestStore(t)
			fc := &fakeClient{
				polls:   [][]birdc.ProtocolSummary{{bgp("edge_v4", "up", "Established")}},
				replyAt: map[int]birdc.Reply{1: rowsReply(row)},
			}
			p := New(fc, st, time.Second, nil)
			clk := withClock(p)

			p.poll(context.Background())
			clk.advance(5 * time.Second)
			p.poll(context.Background()) // the truncated row
			if !p.Snapshot().States["edge_v4"].Up {
				t.Error("a truncated row must not flip the session to down")
			}
			clk.advance(5 * time.Second)
			p.poll(context.Background())

			if kinds := eventKinds(t, st); len(kinds) != 0 {
				t.Fatalf("a truncated row paged: %v", kinds)
			}
		})
	}
}

// The other side of the line: a down or disabled BGP protocol really does have
// an empty Info (captured from BIRD 2.17.5), and going down must still page.
func TestDownBGPRowWithEmptyInfoStillAlerts(t *testing.T) {
	st := openTestStore(t)
	fc := &fakeClient{
		polls:   [][]birdc.ProtocolSummary{{bgp("edge_v4", "up", "Established")}},
		replyAt: map[int]birdc.Reply{1: rowsReply("edge_v4    BGP        ---        down   12:23:07.869  ")},
	}
	p := New(fc, st, time.Second, nil)
	p.poll(context.Background())
	p.poll(context.Background())

	if kinds := eventKinds(t, st); len(kinds) != 1 || kinds[0] != store.EventSessionDown {
		t.Fatalf("events = %v, want [session_down]", kinds)
	}
}

// A row that stays unreadable is no hiccup. After the grace it is read at face
// value, so a format BIRD really sends cannot hide a session going down.
func TestTruncatedRowPastTheGraceIsReadAtFaceValue(t *testing.T) {
	st := openTestStore(t)
	steps := map[int]birdc.Reply{}
	for i := 1; i <= 15; i++ {
		steps[i] = rowsReply("edge_v4    BGP        up     12:00:00.124")
	}
	fc := &fakeClient{
		polls:   [][]birdc.ProtocolSummary{{bgp("edge_v4", "up", "Established")}},
		replyAt: steps,
	}
	p := New(fc, st, time.Second, nil)
	clk := withClock(p)
	p.poll(context.Background())
	for range 15 {
		clk.advance(5 * time.Second)
		p.poll(context.Background())
	}
	if kinds := eventKinds(t, st); len(kinds) != 1 || kinds[0] != store.EventSessionDown {
		t.Fatalf("events = %v, want exactly one session_down once the grace ran out", kinds)
	}
}
