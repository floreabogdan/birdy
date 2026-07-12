package poller

import (
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
)

func upSessionWithCounts() *fakeClient {
	return &fakeClient{
		polls: [][]birdc.ProtocolSummary{{bgp("edge_v4", "up", "Established")}},
		detail: map[string]birdc.ProtocolDetail{
			"edge_v4": {Channels: []birdc.ChannelDetail{
				{AFI: "ipv4", RoutesImported: 900000, RoutesExported: 3},
			}},
		},
	}
}

// A poll with sampling due records one route-count point per up BGP session.
func TestPollerRecordsSamples(t *testing.T) {
	st := openTestStore(t)
	p := New(upSessionWithCounts(), st, time.Second, nil)
	p.SetSampling(time.Nanosecond, time.Hour) // due on every poll
	p.poll()

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
	p.poll()
	p.poll()

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
	p.poll()

	samples, _ := st.ListSamples("edge_v4", time.Now().Add(-time.Hour))
	if len(samples) != 0 {
		t.Errorf("disabled sampling must not write, got %d", len(samples))
	}
}
