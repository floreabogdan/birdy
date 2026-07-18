package store

import (
	"crypto/subtle"
	"database/sql"
	"fmt"
	"time"
)

type InstanceToken struct {
	ID        int64
	Name      string
	Scope     string
	ExpiresAt string
	Revoked   bool
	CreatedAt string
	LastUsed  string
}

func (s *Store) CreateInstanceToken(name, hash, scope, expiresAt string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO instance_tokens (name, token_hash, scope, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`, name, hash, scope, expiresAt, now())
	if err != nil {
		return 0, fmt.Errorf("store: create instance token: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) ListInstanceTokens() ([]InstanceToken, error) {
	rows, err := s.db.Query(`SELECT id, name, scope, expires_at, revoked, created_at, last_used FROM instance_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list instance tokens: %w", err)
	}
	defer rows.Close()
	var out []InstanceToken
	for rows.Next() {
		var token InstanceToken
		if err := rows.Scan(&token.ID, &token.Name, &token.Scope, &token.ExpiresAt, &token.Revoked, &token.CreatedAt, &token.LastUsed); err != nil {
			return nil, fmt.Errorf("store: scan instance token: %w", err)
		}
		out = append(out, token)
	}
	return out, rows.Err()
}

func (s *Store) RevokeInstanceToken(id int64) error {
	res, err := s.db.Exec(`UPDATE instance_tokens SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: revoke instance token: %w", err)
	}
	return affectedOne(res)
}

func (s *Store) RevokeAllInstanceTokens() error {
	if _, err := s.db.Exec(`UPDATE instance_tokens SET revoked = 1 WHERE revoked = 0`); err != nil {
		return fmt.Errorf("store: revoke instance tokens: %w", err)
	}
	return nil
}

func (s *Store) VerifyScopedInstanceToken(raw string) (bool, error) {
	rows, err := s.db.Query(`SELECT id, token_hash, expires_at FROM instance_tokens WHERE revoked = 0`)
	if err != nil {
		return false, fmt.Errorf("store: get instance tokens: %w", err)
	}
	defer rows.Close()
	hash := HashInstanceAPIToken(raw)
	for rows.Next() {
		var id int64
		var stored, expiresAt string
		if err := rows.Scan(&id, &stored, &expiresAt); err != nil {
			return false, fmt.Errorf("store: scan instance token: %w", err)
		}
		if expiresAt != "" {
			expires, parseErr := time.Parse(time.RFC3339Nano, expiresAt)
			if parseErr != nil || !time.Now().Before(expires) {
				continue
			}
		}
		if len(stored) == len(hash) && subtle.ConstantTimeCompare([]byte(stored), []byte(hash)) == 1 {
			_, _ = s.db.Exec(`UPDATE instance_tokens SET last_used = ? WHERE id = ?`, now(), id)
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) CountInstanceTokens() int {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM instance_tokens WHERE revoked = 0`).Scan(&count); err != nil && err != sql.ErrNoRows {
		return 0
	}
	return count
}
