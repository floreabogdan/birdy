package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// Creating and deleting a peer records an audit event attributed to the
// logged-in operator; applying a config does too.
func TestAuditTrailRecordsOperatorActions(t *testing.T) {
	env := applyReady(t) // logged in as "admin"

	f := peerForm()
	f.Set("name", "cust_a")
	f.Set("neighborIp", "198.51.100.9")
	if rec := env.do(t, "POST", "/peers/new", f); rec.Code != 303 {
		t.Fatalf("peer create: %d", rec.Code)
	}
	env.do(t, "POST", "/apply", nil)

	events, err := env.store.ListEvents(50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var created, applied *store.Event
	for i := range events {
		switch {
		case events[i].Kind == store.EventModelChange && strings.Contains(events[i].Message, "created peer cust_a"):
			created = &events[i]
		case events[i].Kind == store.EventConfigApply:
			applied = &events[i]
		}
	}
	if created == nil {
		t.Fatal("creating a peer should record a model_change audit event")
	}
	if created.Actor != "admin" {
		t.Errorf("audit event actor = %q, want admin", created.Actor)
	}
	if applied == nil || applied.Actor != "admin" {
		t.Errorf("a config apply should be attributed to the operator, got %+v", applied)
	}
}

// The timeline surfaces the actor.
func TestTimelineShowsActor(t *testing.T) {
	env := applyReady(t)
	env.do(t, "POST", "/peers/new", func() url.Values {
		f := peerForm()
		f.Set("name", "cust_b")
		f.Set("neighborIp", "198.51.100.10")
		return f
	}())

	body := env.do(t, "GET", "/timeline", nil).Body.String()
	if !strings.Contains(body, "created peer cust_b") || !strings.Contains(body, "by admin") {
		t.Error("the timeline should show the audited action and its actor")
	}
}

// System/BIRD events have no actor and don't claim one.
func TestSystemEventsHaveNoActor(t *testing.T) {
	env := applyReady(t)
	if err := env.store.InsertEvent(store.EventSessionUp, "edge_v4", "established"); err != nil {
		t.Fatal(err)
	}
	events, _ := env.store.ListEvents(10, 0)
	for _, e := range events {
		if e.Kind == store.EventSessionUp && e.Actor != "" {
			t.Errorf("a system event should have no actor, got %q", e.Actor)
		}
	}
}
