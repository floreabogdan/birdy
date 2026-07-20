package store

import (
	"crypto/subtle"
	"fmt"
	"strings"
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
	// Revocation is idempotent: revoking a token that is already gone or was
	// never issued is a no-op success, not a 500 for the operator.
	if _, err := s.db.Exec(`UPDATE instance_tokens SET revoked = 1 WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: revoke instance token: %w", err)
	}
	return nil
}

func (s *Store) RevokeAllInstanceTokens() error {
	if _, err := s.db.Exec(`UPDATE instance_tokens SET revoked = 1 WHERE revoked = 0`); err != nil {
		return fmt.Errorf("store: revoke instance tokens: %w", err)
	}
	return nil
}

func (s *Store) VerifyScopedInstanceToken(raw string) (bool, error) {
	rows, err := s.db.Query(`SELECT id, token_hash, expires_at, scope FROM instance_tokens WHERE revoked = 0`)
	if err != nil {
		return false, fmt.Errorf("store: get instance tokens: %w", err)
	}
	hash := HashInstanceAPIToken(raw)
	var matchedID int64
	found := false
	for rows.Next() {
		var id int64
		var stored, expiresAt, scope string
		if err := rows.Scan(&id, &stored, &expiresAt, &scope); err != nil {
			rows.Close()
			return false, fmt.Errorf("store: scan instance token: %w", err)
		}
		if expiresAt != "" {
			expires, parseErr := time.Parse(time.RFC3339Nano, expiresAt)
			if parseErr != nil || !time.Now().Before(expires) {
				continue
			}
		}
		// Enforce the token's scope: only a dashboard-scoped token authorizes the
		// read-only dashboard/timeline API, so a scope added later cannot silently
		// ride these endpoints.
		if len(stored) == len(hash) && subtle.ConstantTimeCompare([]byte(stored), []byte(hash)) == 1 && scopeGrantsDashboard(scope) {
			matchedID = id
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	if !found {
		return false, nil
	}
	// Stamp last_used only after the SELECT cursor is closed, so the write does
	// not check out a second pooled connection while the read is still open.
	_, _ = s.db.Exec(`UPDATE instance_tokens SET last_used = ? WHERE id = ?`, now(), matchedID)
	return true, nil
}

// scopeGrantsDashboard reports whether a token scope authorizes the read-only
// dashboard and timeline API. Tokens are minted with scope "dashboard timeline";
// requiring the capability here means a scope added later does not silently gain
// access to these endpoints.
func scopeGrantsDashboard(scope string) bool {
	for _, f := range strings.Fields(scope) {
		if f == "dashboard" {
			return true
		}
	}
	return false
}
