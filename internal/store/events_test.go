package store

import "testing"

// The bell's unread count is alert kinds only, newer than a "seen" id. Operator
// actions and recoveries must not inflate it, and the since-id is a strict lower
// bound.
func TestCountAlertsAfterAndLatest(t *testing.T) {
	s := openTest(t)

	// Insertion order fixes the ids: apply(1), down(2), flap(3), up(4).
	for _, e := range []struct{ kind, msg string }{
		{EventConfigApply, "applied"}, // operator action — not an alert
		{EventSessionDown, "went down"},
		{EventFlap, "flapped"},
		{EventSessionUp, "back up"}, // recovery — not an alert
	} {
		if err := s.InsertEvent(e.kind, "edge_v4", e.msg); err != nil {
			t.Fatal(err)
		}
	}

	latest, err := s.LatestEventID()
	if err != nil {
		t.Fatal(err)
	}
	if latest == 0 {
		t.Fatal("latest event id should be > 0 after inserts")
	}

	// From the start, only the two fault events count — apply and up are excluded.
	if n, err := s.CountAlertsAfter(0); err != nil {
		t.Fatal(err)
	} else if n != 2 {
		t.Errorf("CountAlertsAfter(0) = %d, want 2 (down + flap)", n)
	}

	// events are newest-first: up, flap, down, apply — index 2 is the down event.
	events, err := s.ListEvents(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	downID := events[2].ID
	if events[2].Kind != EventSessionDown {
		t.Fatalf("expected down at index 2, got %q", events[2].Kind)
	}
	// After the down event only the flap is both newer and an alert.
	if n, err := s.CountAlertsAfter(downID); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Errorf("CountAlertsAfter(downID) = %d, want 1 (flap only)", n)
	}

	// Nothing is newer than the latest event.
	if n, err := s.CountAlertsAfter(latest); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Errorf("CountAlertsAfter(latest) = %d, want 0", n)
	}
}
