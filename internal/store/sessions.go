package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// Session is a logged-in browser session. It is looked up by the opaque cookie
// token, but deliberately does not carry it: the database only ever holds the
// token's hash (see hashSessionToken), so there is no raw value to read back.
type Session struct {
	UserID    int64
	ExpiresAt time.Time
}

// CreateSession stores a new login session, keyed on the hash of token.
func (s *Store) CreateSession(token string, userID int64, expiresAt time.Time) error {
	ts := now()
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, ts); err != nil {
		return fmt.Errorf("store: prune sessions before create: %w", err)
	}
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hashSessionToken(token), userID, ts, expiresAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// GetSession returns (Session{}, false, nil) if the token doesn't exist or has expired.
func (s *Store) GetSession(token string) (Session, bool, error) {
	var sess Session
	var expiresAt string
	row := s.db.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE token_hash = ?`, hashSessionToken(token))
	err := row.Scan(&sess.UserID, &expiresAt)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("store: get session: %w", err)
	}
	sess.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return Session{}, false, fmt.Errorf("store: parse session expiry: %w", err)
	}
	if time.Now().After(sess.ExpiresAt) {
		if err := s.DeleteSession(token); err != nil {
			return Session{}, false, err
		}
		return Session{}, false, nil
	}
	return sess, true, nil
}

// DeleteSession removes the session belonging to a cookie token (logout).
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashSessionToken(token))
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// PruneExpiredSessions deletes all sessions past their expiry.
func (s *Store) PruneExpiredSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now())
	if err != nil {
		return fmt.Errorf("store: prune sessions: %w", err)
	}
	return nil
}

// hashSessionToken is what the sessions table is keyed on. The cookie value is
// a bearer credential, so — like instance API tokens (HashInstanceAPIToken) —
// only a SHA-256 digest of it is stored: a leaked database, snapshot or backup
// then holds nothing that can be replayed as a login. The token is 32 random
// bytes, so a fast unsalted digest is enough and keeps the per-request lookup
// cheap.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
