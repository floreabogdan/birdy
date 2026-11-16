package poller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/floreabogdan/birdy/internal/birdc"
)

func rpkiProto(name string) birdc.ProtocolSummary {
	return birdc.ProtocolSummary{Name: name, Proto: "RPKI", Table: "---", State: "up", Since: "12:00:00", Info: "Established"}
}

func rpkiDetail(session, serial int64, roa4, roa6 int) birdc.ProtocolDetail {
	return birdc.ProtocolDetail{
		RPKI: &birdc.RPKICache{Status: "Established", Version: 1, SessionID: session, Serial: serial, LastUpdate: "before 3.000 s"},
		Channels: []birdc.ChannelDetail{
			{AFI: "roa4", State: "UP", RoutesImported: roa4},
			{AFI: "roa6", State: "UP", RoutesImported: roa6},
		},
	}
}

// The serial is the only clock on how fresh a feed's data is: BIRD's own "Last
// update" resets on every refresh even when the cache has nothing new. The
// poller must remember when it first saw the current serial, and only move
// that mark when the serial (or the session) actually changes.
func TestRPKIFeedTracksHowLongTheSerialStoodStill(t *testing.T) {
	fc := &fakeClient{detail: map[string]birdc.ProtocolDetail{"cf": rpkiDetail(25012, 1, 795569, 237784)}}
	p := New(fc, openTestStore(t), time.Second, nil)
	protos := []birdc.ProtocolSummary{rpkiProto("cf")}
	t0 := time.Date(2026, 11, 12, 23, 10, 0, 0, time.UTC)

	p.refreshRPKI(context.Background(), protos, t0)
	f := p.RPKIFeeds()["cf"]
	if f.Cache.Serial != 1 || f.ROAs4 != 795569 || f.ROAs6 != 237784 || !f.SerialSince.Equal(t0) {
		t.Fatalf("first sight = %+v, want serial 1, the ROA counts, and SerialSince = t0", f)
	}

	// Hours of refreshes that bring nothing new: the mark must not move.
	for _, after := range []time.Duration{time.Hour, 4 * time.Hour, 96 * time.Hour} {
		p.refreshRPKI(context.Background(), protos, t0.Add(after))
	}
	f = p.RPKIFeeds()["cf"]
	if !f.SerialSince.Equal(t0) {
		t.Errorf("SerialSince moved to %v with the serial unchanged, want %v", f.SerialSince, t0)
	}
	if age := f.SerialAge(t0.Add(96 * time.Hour)); age != 96*time.Hour {
		t.Errorf("SerialAge = %v, want 96h", age)
	}

	// A new serial moves it.
	fc.detail["cf"] = rpkiDetail(25012, 2, 796982, 238005)
	t1 := t0.Add(97 * time.Hour)
	p.refreshRPKI(context.Background(), protos, t1)
	if f = p.RPKIFeeds()["cf"]; !f.SerialSince.Equal(t1) || f.ROAs4 != 796982 {
		t.Errorf("after a new serial: %+v, want SerialSince = %v and the new count", f, t1)
	}

	// So does a new session with the same serial number: serials are per session,
	// so serial 2 of one session says nothing about serial 2 of another.
	fc.detail["cf"] = rpkiDetail(14447, 2, 796982, 238005)
	t2 := t1.Add(time.Hour)
	p.refreshRPKI(context.Background(), protos, t2)
	if f = p.RPKIFeeds()["cf"]; !f.SerialSince.Equal(t2) {
		t.Errorf("a new session kept SerialSince %v, want %v", f.SerialSince, t2)
	}
}

// Reading a feed costs a socket round trip; the data moves on a scale of
// minutes to hours, so the poller reads it once a rpkiInterval, not every poll.
func TestRPKIFeedIsReadOncePerInterval(t *testing.T) {
	fc := &fakeClient{detail: map[string]birdc.ProtocolDetail{"cf": rpkiDetail(1, 1, 10, 5)}}
	p := New(fc, openTestStore(t), time.Second, nil)
	protos := []birdc.ProtocolSummary{rpkiProto("cf")}
	t0 := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

	p.refreshRPKI(context.Background(), protos, t0)
	fc.detail["cf"] = rpkiDetail(1, 2, 11, 5)
	p.refreshRPKI(context.Background(), protos, t0.Add(rpkiInterval/2))
	if got := p.RPKIFeeds()["cf"].Cache.Serial; got != 1 {
		t.Errorf("serial = %d half an interval later, want the feed not re-read (1)", got)
	}
	p.refreshRPKI(context.Background(), protos, t0.Add(rpkiInterval))
	if got := p.RPKIFeeds()["cf"].Cache.Serial; got != 2 {
		t.Errorf("serial = %d a full interval later, want it re-read (2)", got)
	}
}

// After an operator restarts a feed, or an apply adds one, the page should not
// wait up to a minute to show it.
func TestRPKIFeedIsReadAtOnceWhenAskedOrNew(t *testing.T) {
	fc := &fakeClient{detail: map[string]birdc.ProtocolDetail{
		"cf":    rpkiDetail(1, 1, 10, 5),
		"local": rpkiDetail(7, 40, 12, 6),
	}}
	p := New(fc, openTestStore(t), time.Second, nil)
	t0 := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	p.refreshRPKI(context.Background(), []birdc.ProtocolSummary{rpkiProto("cf")}, t0)

	// A feed that was not there before is read on the next poll.
	p.refreshRPKI(context.Background(), []birdc.ProtocolSummary{rpkiProto("cf"), rpkiProto("local")}, t0.Add(time.Second))
	if _, ok := p.RPKIFeeds()["local"]; !ok {
		t.Error("a newly appeared feed was not read until the interval passed")
	}

	// A restart asks for a re-read on the next poll.
	fc.detail["cf"] = rpkiDetail(2, 1, 10, 5)
	p.RefreshRPKISoon()
	p.refreshRPKI(context.Background(), []birdc.ProtocolSummary{rpkiProto("cf"), rpkiProto("local")}, t0.Add(2*time.Second))
	if got := p.RPKIFeeds()["cf"].Cache.SessionID; got != 2 {
		t.Errorf("session = %d after RefreshRPKISoon, want the new session (2)", got)
	}
}

// A hiccup must not reset the clock. A failed detail read, or a "show protocols"
// reply that came back without the feed (BIRD answers partially while it reloads
// ROA tables), keeps the last known state; only a feed gone for longer than
// rpkiForget — removed by an apply — is dropped.
func TestRPKIFeedSurvivesHiccups(t *testing.T) {
	fc := &fakeClient{detail: map[string]birdc.ProtocolDetail{"cf": rpkiDetail(25012, 1, 100, 50)}}
	p := New(fc, openTestStore(t), time.Second, nil)
	protos := []birdc.ProtocolSummary{rpkiProto("cf")}
	t0 := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	p.refreshRPKI(context.Background(), protos, t0)

	fc.detailErr = map[string]error{"cf": errors.New("birdc: read: i/o timeout")}
	p.refreshRPKI(context.Background(), protos, t0.Add(rpkiInterval))
	if f, ok := p.RPKIFeeds()["cf"]; !ok || !f.SerialSince.Equal(t0) || f.ROAs4 != 100 {
		t.Errorf("after a failed read: %+v (present=%v), want the last known state", f, ok)
	}
	fc.detailErr = nil

	p.refreshRPKI(context.Background(), nil, t0.Add(2*rpkiInterval))
	if f, ok := p.RPKIFeeds()["cf"]; !ok || !f.SerialSince.Equal(t0) {
		t.Errorf("after a reply without the feed: %+v (present=%v), want it remembered", f, ok)
	}

	p.refreshRPKI(context.Background(), nil, t0.Add(rpkiForget+rpkiInterval))
	if _, ok := p.RPKIFeeds()["cf"]; ok {
		t.Error("a feed missing for longer than rpkiForget is still listed")
	}
}

// The full poll reads RPKI feeds, and only RPKI protocols.
func TestPollReadsRPKIFeeds(t *testing.T) {
	fc := &fakeClient{
		polls: [][]birdc.ProtocolSummary{{bgp("edge_v4", "up", "Established"), rpkiProto("cf")}},
		detail: map[string]birdc.ProtocolDetail{
			"edge_v4": {Channels: []birdc.ChannelDetail{{AFI: "ipv4", State: "UP", RoutesImported: 10}}},
			"cf":      rpkiDetail(25012, 1, 795569, 237784),
		},
	}
	p := New(fc, openTestStore(t), time.Second, nil)
	p.poll(context.Background())
	feeds := p.RPKIFeeds()
	if len(feeds) != 1 || feeds["cf"].ROAs4 != 795569 {
		t.Errorf("RPKIFeeds = %+v, want exactly the cf feed", feeds)
	}
}
