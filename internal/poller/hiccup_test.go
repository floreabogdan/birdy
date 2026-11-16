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
