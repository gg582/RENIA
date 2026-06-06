package db

import (
	"context"
	"database/sql"
	"fmt"
)

// SessionStore defines the contract for session persistence.
type SessionStore interface {
	CreateSession(ctx context.Context, token string, userID int64) error
	ResolveToken(ctx context.Context, token string) (int64, error)
}

type sessionStore struct {
	db *sql.DB
}

// NewSessionStore returns a store bound to the provided database handle.
func NewSessionStore(db *sql.DB) SessionStore {
	return &sessionStore{db: db}
}

// CreateSession persists a new session token with a fixed 24-hour expiration.
func (s *sessionStore) CreateSession(ctx context.Context, token string, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, datetime('now', '+24 hours'))`,
		token, userID)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// ResolveToken validates a token and returns the associated user_id.
func (s *sessionStore) ResolveToken(ctx context.Context, token string) (int64, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT user_id FROM sessions WHERE token = ? AND expires_at > datetime('now')`,
		token)
	var uid int64
	if err := row.Scan(&uid); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("resolve token: %w", err)
	}
	return uid, nil
}

// DeleteUserSessions removes all sessions for a given user.
func (s *sessionStore) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ?`,
		userID)
	if err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	return nil
}

// GC purges expired sessions to prevent table bloat.
func (s *sessionStore) GC(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= datetime('now')`)
	if err != nil {
		return fmt.Errorf("gc sessions: %w", err)
	}
	return nil
}
