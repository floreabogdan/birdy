package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Config version lifecycle.
const (
	ConfigPending   = "pending"   // applied with an armed auto-revert, awaiting confirm
	ConfigConfirmed = "confirmed" // kept
	ConfigReverted  = "reverted"  // rolled back, or the auto-revert fired
	ConfigFailed    = "failed"    // never took effect
)

// ConfigVersion is one apply: the rendered config, what it replaced, and how it
// turned out.
type ConfigVersion struct {
	ID         int64
	CreatedAt  time.Time
	SHA256     string
	Size       int
	ConfigText string
	BackupPath string
	Status     string
	Deadline   time.Time // zero once resolved
	Message    string
	ResolvedAt time.Time // zero while pending
}

// Expired reports whether a pending version's auto-revert deadline has passed,
// meaning BIRD has already reverted on its own and birdy just needs to catch up.
func (v ConfigVersion) Expired(now time.Time) bool {
	return v.Status == ConfigPending && !v.Deadline.IsZero() && now.After(v.Deadline)
}

func (s *Store) CreateConfigVersion(v ConfigVersion) (int64, error) {
	ts := now()
	deadline := ""
	if !v.Deadline.IsZero() {
		deadline = v.Deadline.UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.Exec(`
		INSERT INTO config_versions (created_at, sha256, size, config_text, backup_path, status, timeout_deadline, message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, v.SHA256, v.Size, v.ConfigText, v.BackupPath, v.Status, deadline, v.Message)
	if err != nil {
		return 0, fmt.Errorf("store: create config version: %w", err)
	}
	return res.LastInsertId()
}

// PendingConfigVersion returns the single pending apply, or (_, false) if none.
// A pending apply blocks another: birdy allows only one armed reconfigure at a
// time, because BIRD itself holds only one previous config to revert to.
func (s *Store) PendingConfigVersion() (ConfigVersion, bool, error) {
	row := s.db.QueryRow(`
		SELECT id, created_at, sha256, size, config_text, backup_path, status, timeout_deadline, message, resolved_at
		FROM config_versions WHERE status = ? ORDER BY id DESC LIMIT 1`, ConfigPending)
	v, err := scanConfigVersion(row)
	if err == sql.ErrNoRows {
		return ConfigVersion{}, false, nil
	}
	if err != nil {
		return ConfigVersion{}, false, err
	}
	return v, true, nil
}

func (s *Store) GetConfigVersion(id int64) (ConfigVersion, error) {
	row := s.db.QueryRow(`
		SELECT id, created_at, sha256, size, config_text, backup_path, status, timeout_deadline, message, resolved_at
		FROM config_versions WHERE id = ?`, id)
	v, err := scanConfigVersion(row)
	if err == sql.ErrNoRows {
		return ConfigVersion{}, ErrNotFound
	}
	return v, err
}

// ResolveConfigVersion records a terminal outcome for a version and clears its
// deadline, so a resolved apply never looks pending again.
func (s *Store) ResolveConfigVersion(id int64, status, message string) error {
	res, err := s.db.Exec(`
		UPDATE config_versions SET status = ?, message = ?, timeout_deadline = '', resolved_at = ?
		WHERE id = ?`, status, message, now(), id)
	if err != nil {
		return fmt.Errorf("store: resolve config version: %w", err)
	}
	return affectedOne(res)
}

func (s *Store) ListConfigVersions(limit int) ([]ConfigVersion, error) {
	rows, err := s.db.Query(`
		SELECT id, created_at, sha256, size, config_text, backup_path, status, timeout_deadline, message, resolved_at
		FROM config_versions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list config versions: %w", err)
	}
	defer rows.Close()
	var out []ConfigVersion
	for rows.Next() {
		v, err := scanConfigVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanConfigVersion(sc scanner) (ConfigVersion, error) {
	var v ConfigVersion
	var created, deadline, resolved string
	if err := sc.Scan(&v.ID, &created, &v.SHA256, &v.Size, &v.ConfigText, &v.BackupPath,
		&v.Status, &deadline, &v.Message, &resolved); err != nil {
		return ConfigVersion{}, err
	}
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if deadline != "" {
		v.Deadline, _ = time.Parse(time.RFC3339Nano, deadline)
	}
	if resolved != "" {
		v.ResolvedAt, _ = time.Parse(time.RFC3339Nano, resolved)
	}
	return v, nil
}

// SetAppliedConfigHash records the sha256 of the bytes birdy has written to
// bird.conf and that BIRD is running. The authorship guard reads it back to
// decide whether birdy owns the file on disk.
func (s *Store) SetAppliedConfigHash(hash string) error {
	res, err := s.db.Exec(`UPDATE settings SET applied_config_hash = ?, updated_at = ? WHERE id = 1`, hash, now())
	if err != nil {
		return fmt.Errorf("store: set applied config hash: %w", err)
	}
	return affectedOne(res)
}
