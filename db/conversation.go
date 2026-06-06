package db

import (
	"context"
	"database/sql"
	"fmt"
)

// ConversationStore handles persistence of per-user chat history.
type ConversationStore struct {
	db *sql.DB
}

// NewConversationStore returns a store bound to the provided database handle.
func NewConversationStore(db *sql.DB) *ConversationStore {
	return &ConversationStore{db: db}
}

// Message represents a single turn in a conversation.
type Message struct {
	Role    string
	Content string
}

// AppendMessage records a new message strictly bound to user_id.
func (s *ConversationStore) AppendMessage(ctx context.Context, userID int64, role, content string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO conversations (user_id, role, content) VALUES (?, ?, ?)`,
		userID, role, content)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	return nil
}

// RecentMessages retrieves the last N messages for a specific user,
// ordered by creation time descending, then reversed to chronological order.
func (s *ConversationStore) RecentMessages(ctx context.Context, userID int64, limit int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT role, content FROM conversations WHERE user_id = ? ORDER BY created_at DESC LIMIT ?`,
		userID, limit)
	if err != nil {
		return nil, fmt.Errorf("select messages: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}
