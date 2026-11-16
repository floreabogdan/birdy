package poller

import (
	"context"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
)

func upSessionWithCounts() *fakeClient {
	return &fakeClient{
		polls: [][]birdc.ProtocolSummary{{bgp("edge_v4", "up", "Established")}},
		detail: map[string]birdc.ProtocolDetail{
			"edge_v4": established(birdc.ChannelDetail{AFI: "ipv4", RoutesImported: 900000, RoutesExported: 3}),
		},
	}
}

// A poll with sampling due records one route-count point per up BGP session.
func TestPollerRecordsSamples(t *testing.T) {
	st := openTestStore(t)
	p := New(upSessionWithCounts(), st, time.Second, nil)
	p.SetSampling(time.Nanosecond, time.Hour) // due on every poll
	p.poll(context.Background())

	samples, err := st.ListSamples("edge_v4", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("want 1 sample, got %d", len(samples))
	}
	if samples[0].Imported != 900000 || samples[0].Exported != 3 {
		t.Errorf("sample carried the wrong counts: %+v", samples[0])
	}
}

// A second poll inside the sampling interval must not record again.
func TestPollerSamplingRespectsInterval(t *testing.T) {
	st := openTestStore(t)
	p := New(upSessionWithCounts(), st, time.Second, nil)
	p.SetSampling(time.Hour, time.Hour)
	p.poll(context.Background())
	p.poll(context.Background())

	samples, _ := st.ListSamples("edge_v4", time.Now().Add(-2*time.Hour))
	if len(samples) != 1 {
		t.Errorf("sampling should honour its interval, got %d samples", len(samples))
	}
}

// Sampling disabled (interval 0) never writes.
func TestPollerSamplingDisabled(t *testing.T) {
	st := openTestStore(t)
	p := New(upSessionWithCounts(), st, time.Second, nil)
	// SetSampling not called: interval is zero.
	p.poll(context.Background())

	samples, _ := st.ListSamples("edge_v4", time.Now().Add(-time.Hour))
	if len(samples) != 0 {
		t.Errorf("disabled sampling must not write, got %d", len(samples))
	}
}

// A session carried forward because one reply lacked its row has no fresh
// counts, so it must not add a point to its history line.
func TestPollerDoesNotSampleACarriedSession(t *testing.T) {
	st := openTestStore(t)
	fc := upSessionWithCounts()
	device := birdc.ProtocolSummary{Name: "device1", Proto: "Device", Table: "---", State: "up", Since: "12:00:00.124"}
	fc.polls = [][]birdc.ProtocolSummary{{device, bgp("edge_v4", "up", "Established")}, {device}}
	p := New(fc, st, time.Second, nil)
	p.SetSampling(time.Nanosecond, time.Hour)
	p.poll(context.Background())
	p.poll(context.Background()) // edge_v4 missing from the reply

	samples, _ := st.ListSamples("edge_v4", time.Now().Add(-time.Hour))
	if len(samples) != 1 {
		t.Errorf("want only the sample from the poll that saw the session, got %d", len(samples))
	}
}

// The poller prunes the event timeline to its retention window, on a slow
// cadence; retention 0 keeps everything.
func TestPollerPrunesEventsPastRetention(t *testing.T) {
	for name, tc := range map[string]struct {
		retain time.Duration
		want   int
	}{
		"pruned":   {retain: 90 * 24 * time.Hour, want: 0},
		"disabled": {retain: 0, want: 1},
	} {
		t.Run(name, func(t *testing.T) {
			st := openTestStore(t)
			if err := st.InsertEvent("session_down", "edge_v4", "edge_v4 (BGP) went down"); err != nil {
				t.Fatal(err)
			}
			p := New(upSessionWithCounts(), st, time.Second, nil)
			p.SetEventRetention(tc.retain)
			later := time.Now().Add(100 * 24 * time.Hour) // the event is now 100 days old
			p.clock = func() time.Time { return later }
			p.poll(context.Background())

			events, _ := st.ListEvents(10, 0)
			if len(events) != tc.want {
				t.Errorf("got %d events after the poll, want %d", len(events), tc.want)
			}
		})
	}
}
