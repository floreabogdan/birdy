package store

import (
	"database/sql"
	"fmt"
)

type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

// CreateUser inserts a new local user (there is normally exactly one: the admin).
func (s *Store) CreateUser(username, passwordHash string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		username, passwordHash, now())
	if err != nil {
		return 0, fmt.Errorf("store: create user: %w", err)
	}
	return res.LastInsertId()
}

// GetUserByUsername returns (User{}, false, nil) if no such user exists.
func (s *Store) GetUserByUsername(username string) (User, bool, error) {
	var u User
	row := s.db.QueryRow(`SELECT id, username, password_hash FROM users WHERE username = ?`, username)
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash)
	if err == sql.ErrNoRows {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("store: get user: %w", err)
	}
	return u, true, nil
}

// GetUserByID returns (User{}, false, nil) if no such user exists. It backs the
// profile page, which knows the logged-in user by the id carried in the session.
func (s *Store) GetUserByID(id int64) (User, bool, error) {
	var u User
	row := s.db.QueryRow(`SELECT id, username, password_hash FROM users WHERE id = ?`, id)
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash)
	if err == sql.ErrNoRows {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("store: get user by id: %w", err)
	}
	return u, true, nil
}

// SetUsername renames an existing user. The username column is UNIQUE, so a
// clash surfaces as the driver's unique-violation error for the caller to map.
func (s *Store) SetUsername(id int64, username string) error {
	_, err := s.db.Exec(`UPDATE users SET username = ? WHERE id = ?`, username, id)
	if err != nil {
		return fmt.Errorf("store: set username: %w", err)
	}
	return nil
}

// HasAnyUser reports whether at least one user account exists (used to decide
// whether `birdy init` still needs to run).
func (s *Store) HasAnyUser() (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false, fmt.Errorf("store: count users: %w", err)
	}
	return n > 0, nil
}

// SetPassword updates an existing user's password hash.
func (s *Store) SetPassword(userID int64, passwordHash string) error {
	_, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, userID)
	if err != nil {
		return fmt.Errorf("store: set password: %w", err)
	}
	return nil
}
