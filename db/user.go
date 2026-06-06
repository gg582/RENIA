package db

import (
	"context"
	"database/sql"
	"fmt"
)

// UserStore handles CRUD operations for the users table.
type UserStore struct {
	db *sql.DB
}

// NewUserStore returns a store bound to the provided database handle.
func NewUserStore(db *sql.DB) *UserStore {
	return &UserStore{db: db}
}

// User represents a tenant account in the system.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Salt         string
}

// CreateUser inserts a new user record with the pre-hashed credentials.
func (s *UserStore) CreateUser(ctx context.Context, username, salt, hash string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, salt, password_hash) VALUES (?, ?, ?)`,
		username, salt, hash)
	if err != nil {
		return 0, fmt.Errorf("insert user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last id: %w", err)
	}
	return id, nil
}

// GetUserByUsername looks up a single user by exact username match.
func (s *UserStore) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, salt FROM users WHERE username = ?`,
		username)
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Salt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("select user: %w", err)
	}
	return &u, nil
}
