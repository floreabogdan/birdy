package store

import (
	"database/sql"
	"fmt"
)

type Instance struct {
	ID            int64
	Name          string
	BaseURL       string
	Token         string
	GroupName     string
	Tags          string
	LastCheckAt   string
	LastSuccessAt string
	LastLatencyMS int
	LastError     string
	CreatedAt     string
	UpdatedAt     string
}

func (s *Store) ListInstances() ([]Instance, error) {
	rows, err := s.db.Query(`SELECT id, name, base_url, token, group_name, tags, last_check_at, last_success_at, last_latency_ms, last_error, created_at, updated_at FROM instances ORDER BY group_name, name`)
	if err != nil {
		return nil, fmt.Errorf("store: list instances: %w", err)
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		var i Instance
		if err := rows.Scan(&i.ID, &i.Name, &i.BaseURL, &i.Token, &i.GroupName, &i.Tags, &i.LastCheckAt, &i.LastSuccessAt, &i.LastLatencyMS, &i.LastError, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan instance: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) CreateInstance(name, baseURL, token string) (int64, error) {
	return s.CreateInstanceWithMetadata(name, baseURL, token, "", "")
}

func (s *Store) CreateInstanceWithMetadata(name, baseURL, token, groupName, tags string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO instances (name, base_url, token, group_name, tags, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, name, baseURL, token, groupName, tags, now(), now())
	if err != nil {
		return 0, fmt.Errorf("store: create instance: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create instance id: %w", err)
	}
	return id, nil
}

func (s *Store) UpdateInstanceMetadata(id int64, groupName, tags string) error {
	res, err := s.db.Exec(`UPDATE instances SET group_name = ?, tags = ?, updated_at = ? WHERE id = ?`, groupName, tags, now(), id)
	if err != nil {
		return fmt.Errorf("store: update instance metadata: %w", err)
	}
	return affectedOne(res)
}

func (s *Store) UpdateInstanceHealth(id int64, checkedAt, successAt string, latencyMS int, lastError string) error {
	res, err := s.db.Exec(`UPDATE instances SET last_check_at = ?, last_success_at = ?, last_latency_ms = ?, last_error = ?, updated_at = ? WHERE id = ?`, checkedAt, successAt, latencyMS, lastError, now(), id)
	if err != nil {
		return fmt.Errorf("store: update instance health: %w", err)
	}
	return affectedOne(res)
}

func (s *Store) DeleteInstance(id int64) error {
	res, err := s.db.Exec(`DELETE FROM instances WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete instance: %w", err)
	}
	return affectedOne(res)
}

// RenameInstance changes only the friendly display name. The URL and bearer
// token stay untouched so renaming never interrupts an active target.
func (s *Store) RenameInstance(id int64, name string) error {
	res, err := s.db.Exec(`UPDATE instances SET name = ?, updated_at = ? WHERE id = ?`, name, now(), id)
	if err != nil {
		return fmt.Errorf("store: rename instance: %w", err)
	}
	return affectedOne(res)
}

func (s *Store) GetInstance(id int64) (Instance, bool, error) {
	var i Instance
	err := s.db.QueryRow(`SELECT id, name, base_url, token, group_name, tags, last_check_at, last_success_at, last_latency_ms, last_error, created_at, updated_at FROM instances WHERE id = ?`, id).
		Scan(&i.ID, &i.Name, &i.BaseURL, &i.Token, &i.GroupName, &i.Tags, &i.LastCheckAt, &i.LastSuccessAt, &i.LastLatencyMS, &i.LastError, &i.CreatedAt, &i.UpdatedAt)
	if err == sql.ErrNoRows {
		return Instance{}, false, nil
	}
	if err != nil {
		return Instance{}, false, fmt.Errorf("store: get instance: %w", err)
	}
	return i, true, nil
}
