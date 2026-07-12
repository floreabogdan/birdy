package store

import (
	"fmt"
	"time"
)

// A Sample is one point in a session's route-count history: how many routes it
// had imported and exported at a moment in time. Sampled on a slow cadence by
// the poller and used to draw the dashboard's history sparklines.
type Sample struct {
	Ts       time.Time
	Protocol string
	Imported int
	Exported int
}

// InsertSamples appends a batch of samples in one transaction. Ts is taken from
// each sample (the poller stamps them all with the same poll time).
func (s *Store) InsertSamples(samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, sm := range samples {
		ts := sm.Ts.UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`INSERT INTO route_samples (ts, protocol, imported, exported) VALUES (?, ?, ?, ?)`,
			ts, sm.Protocol, sm.Imported, sm.Exported); err != nil {
			return fmt.Errorf("store: insert sample: %w", err)
		}
	}
	return tx.Commit()
}

// ListSamples returns one protocol's samples at or after since, oldest first —
// the order a sparkline is drawn in.
func (s *Store) ListSamples(protocol string, since time.Time) ([]Sample, error) {
	rows, err := s.db.Query(`
		SELECT ts, protocol, imported, exported FROM route_samples
		WHERE protocol = ? AND ts >= ? ORDER BY ts`,
		protocol, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("store: list samples: %w", err)
	}
	defer rows.Close()
	return scanSamples(rows)
}

// RecentSamples returns every protocol's samples at or after since, oldest
// first, so the dashboard can draw a sparkline per session from one query.
func (s *Store) RecentSamples(since time.Time) ([]Sample, error) {
	rows, err := s.db.Query(`
		SELECT ts, protocol, imported, exported FROM route_samples
		WHERE ts >= ? ORDER BY ts`,
		since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("store: recent samples: %w", err)
	}
	defer rows.Close()
	return scanSamples(rows)
}

func scanSamples(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]Sample, error) {
	var out []Sample
	for rows.Next() {
		var sm Sample
		var ts string
		if err := rows.Scan(&ts, &sm.Protocol, &sm.Imported, &sm.Exported); err != nil {
			return nil, fmt.Errorf("store: scan sample: %w", err)
		}
		sm.Ts, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, sm)
	}
	return out, rows.Err()
}

// PruneSamples deletes samples older than before, keeping the table bounded to
// the retention window.
func (s *Store) PruneSamples(before time.Time) error {
	_, err := s.db.Exec(`DELETE FROM route_samples WHERE ts < ?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store: prune samples: %w", err)
	}
	return nil
}
