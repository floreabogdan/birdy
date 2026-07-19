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
	// BaselineSessions are the BGP sessions established when this was applied.
	BaselineSessions string
	// ConfigFiles is the exact on-disk file set this apply wrote, JSON-encoded
	// (a render.FileSet). Empty for older single-file versions.
	ConfigFiles string
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
		INSERT INTO config_versions (created_at, sha256, size, config_text, backup_path, status, timeout_deadline, message, baseline_sessions, config_files)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, v.SHA256, v.Size, v.ConfigText, v.BackupPath, v.Status, deadline, v.Message, v.BaselineSessions, v.ConfigFiles)
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
		SELECT id, created_at, sha256, size, config_text, backup_path, status, timeout_deadline, message, resolved_at, baseline_sessions, config_files
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
		SELECT id, created_at, sha256, size, config_text, backup_path, status, timeout_deadline, message, resolved_at, baseline_sessions, config_files
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

// ConfirmAppliedVersion records a confirmed apply atomically: it stamps the
// applied config hash and resolves the version in one transaction, so a failure
// between the two can never leave the hash advanced while the version stays
// pending — a state that would block the next apply on a change BIRD already ran.
func (s *Store) ConfirmAppliedVersion(id int64, hash, status, message string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: confirm applied version: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ts := now()
	if _, err := tx.Exec(`UPDATE settings SET applied_config_hash = ?, updated_at = ? WHERE id = 1`, hash, ts); err != nil {
		return fmt.Errorf("store: set applied config hash: %w", err)
	}
	res, err := tx.Exec(`UPDATE config_versions SET status = ?, message = ?, timeout_deadline = '', resolved_at = ? WHERE id = ?`, status, message, ts, id)
	if err != nil {
		return fmt.Errorf("store: resolve config version: %w", err)
	}
	if err := affectedOne(res); err != nil {
		return err
	}
	return tx.Commit()
}

// CountConfigVersions is the total the history pager needs. Counting rows is
// cheap; reading them is not — each carries a full rendered bird.conf.
func (s *Store) CountConfigVersions() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM config_versions`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count config versions: %w", err)
	}
	return n, nil
}

// ListConfigVersionsPage reads one page. Paging in SQL rather than in memory
// matters here: every row holds an entire config, so "read them all and slice" is
// megabytes of text to render fifty lines of table.
func (s *Store) ListConfigVersionsPage(limit, offset int) ([]ConfigVersion, error) {
	rows, err := s.db.Query(`
		SELECT id, created_at, sha256, size, config_text, backup_path, status, timeout_deadline, message, resolved_at, baseline_sessions, config_files
		FROM config_versions ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
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

func (s *Store) ListConfigVersions(limit int) ([]ConfigVersion, error) {
	rows, err := s.db.Query(`
		SELECT id, created_at, sha256, size, config_text, backup_path, status, timeout_deadline, message, resolved_at, baseline_sessions, config_files
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
		&v.Status, &deadline, &v.Message, &resolved, &v.BaselineSessions, &v.ConfigFiles); err != nil {
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
