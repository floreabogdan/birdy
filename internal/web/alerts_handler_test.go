package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/floreabogdan/birdy/internal/store"
)

// The bell counts only unread fault events, and clears once the viewer's "seen"
// marker reaches the latest event.
func TestAlertsSummaryUnread(t *testing.T) {
	env := newTestEnv(t, false)

	if err := env.store.InsertEvent(store.EventConfigApply, "", "applied"); err != nil {
		t.Fatal(err) // operator action — must not count
	}
	if err := env.store.InsertEvent(store.EventSessionDown, "edge_v4", "down"); err != nil {
		t.Fatal(err) // a fault — counts
	}

	rec := env.do(t, "GET", "/api/alerts/summary?since=0", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out struct {
		Unread        int   `json:"unread"`
		LatestEventID int64 `json:"latestEventId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Unread != 1 {
		t.Errorf("unread = %d, want 1 (the session_down; config_apply is not an alert)", out.Unread)
	}
	if out.LatestEventID == 0 {
		t.Error("latestEventId should be set")
	}

	// Having seen up to the latest event, the bell is clear.
	rec2 := env.do(t, "GET", "/api/alerts/summary?since="+strconv.FormatInt(out.LatestEventID, 10), nil)
	var out2 struct {
		Unread int `json:"unread"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil {
		t.Fatal(err)
	}
	if out2.Unread != 0 {
		t.Errorf("unread after seeing the latest event = %d, want 0", out2.Unread)
	}
}
