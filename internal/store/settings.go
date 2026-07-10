package store

import (
	"database/sql"
	"fmt"
)

type Settings struct {
	RouterLabel string
	LocalASN    sql.NullInt64
	// RouterID is the BGP router ID written into the rendered config. It is
	// stored rather than read back from BIRD: the config is the source of
	// truth for what BIRD should be, not the other way round.
	RouterID       string
	BirdSocketPath string
	ListenAddr     string
	WebhookURL     string
}

// GetSettings returns the single settings row, or (Settings{}, false, nil) if
// birdy hasn't been initialized yet.
func (s *Store) GetSettings() (Settings, bool, error) {
	var st Settings
	row := s.db.QueryRow(`SELECT router_label, local_asn, router_id, bird_socket_path, listen_addr, webhook_url FROM settings WHERE id = 1`)
	err := row.Scan(&st.RouterLabel, &st.LocalASN, &st.RouterID, &st.BirdSocketPath, &st.ListenAddr, &st.WebhookURL)
	if err == sql.ErrNoRows {
		return Settings{}, false, nil
	}
	if err != nil {
		return Settings{}, false, fmt.Errorf("store: get settings: %w", err)
	}
	return st, true, nil
}

// SaveSettings upserts the single settings row.
func (s *Store) SaveSettings(st Settings) error {
	ts := now()
	_, err := s.db.Exec(`
		INSERT INTO settings (id, router_label, local_asn, router_id, bird_socket_path, listen_addr, webhook_url, created_at, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			router_label = excluded.router_label,
			local_asn = excluded.local_asn,
			router_id = excluded.router_id,
			bird_socket_path = excluded.bird_socket_path,
			listen_addr = excluded.listen_addr,
			webhook_url = excluded.webhook_url,
			updated_at = excluded.updated_at
	`, st.RouterLabel, st.LocalASN, st.RouterID, st.BirdSocketPath, st.ListenAddr, st.WebhookURL, ts, ts)
	if err != nil {
		return fmt.Errorf("store: save settings: %w", err)
	}
	return nil
}
