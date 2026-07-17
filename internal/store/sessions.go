package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Session is a logged-in browser session, keyed by an opaque cookie token and
// expiring at a fixed time.
type Session struct {
	Token     string
	UserID    int64
	ExpiresAt time.Time
}

// CreateSession stores a new login session token.
func (s *Store) CreateSession(token string, userID int64, expiresAt time.Time) error {
	ts := now()
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, ts); err != nil {
		return fmt.Errorf("store: prune sessions before create: %w", err)
	}
	_, err := s.db.Exec(`INSERT INTO sessions (token, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		token, userID, ts, expiresAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// GetSession returns (Session{}, false, nil) if the token doesn't exist or has expired.
func (s *Store) GetSession(token string) (Session, bool, error) {
	var sess Session
	var expiresAt string
	row := s.db.QueryRow(`SELECT token, user_id, expires_at FROM sessions WHERE token = ?`, token)
	err := row.Scan(&sess.Token, &sess.UserID, &expiresAt)
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

// DeleteSession removes a session (logout).
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
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
