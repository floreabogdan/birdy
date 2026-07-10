package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Event kinds recorded on the timeline.
const (
	EventSessionUp    = "session_up"
	EventSessionDown  = "session_down"
	EventFlap         = "flap"
	EventLimitHit     = "limit_hit"
	EventConfigApply  = "config_apply"
	EventConfigRevert = "config_revert"
)

type Event struct {
	ID       int64
	Ts       time.Time
	Kind     string
	Protocol string
	Message  string
}

// InsertEvent appends one row to the timeline.
func (s *Store) InsertEvent(kind, protocol, message string) error {
	ts := now()
	_, err := s.db.Exec(`INSERT INTO events (ts, kind, protocol, message, created_at) VALUES (?, ?, ?, ?, ?)`,
		ts, kind, protocol, message, ts)
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
		rows, err = s.db.Query(`SELECT id, ts, kind, protocol, message FROM events ORDER BY id DESC LIMIT ?`, limit)
	} else {
		rows, err = s.db.Query(`SELECT id, ts, kind, protocol, message FROM events WHERE id < ? ORDER BY id DESC LIMIT ?`,
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
		if err := rows.Scan(&e.ID, &ts, &e.Kind, &e.Protocol, &e.Message); err != nil {
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
