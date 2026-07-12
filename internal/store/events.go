package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Event kinds recorded on the timeline.
const (
	EventSessionUp     = "session_up"
	EventSessionDown   = "session_down"
	EventFlap          = "flap"
	EventLimitHit      = "limit_hit"
	EventConfigApply   = "config_apply"
	EventConfigRevert  = "config_revert"
	EventBirdUnreach   = "bird_unreachable" // the control socket / daemon went away
	EventBirdReachable = "bird_reachable"   // ... and came back
	EventPrefixDrop    = "prefix_drop"      // a session's imported count fell sharply
	EventConfigDrift   = "config_drift"     // bird.conf on disk changed outside birdy
	EventIRRRefresh    = "irr_refresh"      // a prefix set was re-expanded from IRR
	EventModelChange   = "model_change"     // an operator created/edited/deleted a model object
)

// Event is one entry on the timeline: a session transition, flap, import-limit
// hit, prefix drop, config apply/revert, drift, BIRD reachability change, or an
// operator action (model change / config apply, which also carry an actor).
type Event struct {
	ID       int64     `json:"id"`
	Ts       time.Time `json:"ts"`
	Kind     string    `json:"kind"`
	Protocol string    `json:"protocol"`
	// Actor is the operator who performed an audited action; empty for the
	// system/BIRD events (a session flap has no actor).
	Actor   string `json:"actor,omitempty"`
	Message string `json:"message"`
}

// InsertEvent appends one system/BIRD event to the timeline (no actor).
func (s *Store) InsertEvent(kind, protocol, message string) error {
	return s.insertEvent(kind, protocol, "", message)
}

// InsertAudit appends one operator action to the timeline, attributed to actor.
// It is the audit trail: who applied a config, who changed which peer.
func (s *Store) InsertAudit(actor, kind, message string) error {
	return s.insertEvent(kind, "", actor, message)
}

func (s *Store) insertEvent(kind, protocol, actor, message string) error {
	ts := now()
	_, err := s.db.Exec(`INSERT INTO events (ts, kind, protocol, actor, message, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ts, kind, protocol, actor, message, ts)
	if err != nil {
		return fmt.Errorf("store: insert event: %w", err)
	}
	return nil
}

// ListEvents returns up to limit most recent events, optionally only those
// with id strictly less than beforeID (for pagination — ids are monotonic
// and unique, unlike timestamps, which can collide within the same insert
// burst). Pass 0 for the first page.
func (s *Store) ListEvents(limit int, beforeID int64) ([]Event, error) {
	var rows *sql.Rows
	var err error
	if beforeID == 0 {
		rows, err = s.db.Query(`SELECT id, ts, kind, protocol, actor, message FROM events ORDER BY id DESC LIMIT ?`, limit)
	} else {
		rows, err = s.db.Query(`SELECT id, ts, kind, protocol, actor, message FROM events WHERE id < ? ORDER BY id DESC LIMIT ?`,
			beforeID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.Kind, &e.Protocol, &e.Actor, &e.Message); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		e.Ts, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, fmt.Errorf("store: parse event ts: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
