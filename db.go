package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the underlying sql.DB with schema initialization.
type DB struct {
	sqlDB *sql.DB
}

// Open creates a new SQLite connection pool, applies the schema, and returns a handle.
func openDB(dsn string) (*DB, error) {
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sdb.SetMaxOpenConns(1)
	sdb.SetMaxIdleConns(1)

	d := &DB{sqlDB: sdb}
	if err := d.migrate(); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func (d *DB) Close() error { return d.sqlDB.Close() }

func (d *DB) migrate() error {
	schema := `
	PRAGMA journal_mode = WAL;
	PRAGMA synchronous = NORMAL;
	PRAGMA foreign_keys = ON;

	CREATE TABLE IF NOT EXISTS users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		username      TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		salt          TEXT NOT NULL,
		mode          TEXT DEFAULT 'approval' CHECK(mode IN ('approval','yolo')),
		created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS sessions (
		token      TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS workspaces (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name       TEXT NOT NULL,
		path       TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(user_id, path)
	);

	CREATE TABLE IF NOT EXISTS chat_sessions (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		workspace_id  INTEGER REFERENCES workspaces(id) ON DELETE SET NULL,
		title         TEXT NOT NULL DEFAULT 'New Chat',
		created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS conversations (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		session_id INTEGER REFERENCES chat_sessions(id) ON DELETE CASCADE,
		role       TEXT NOT NULL CHECK(role IN ('user','assistant','system','tool')),
		content    TEXT NOT NULL,
		tool_call  TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS file_snapshots (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id INTEGER NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
		file_path  TEXT NOT NULL,
		content    TEXT NOT NULL,
		action     TEXT NOT NULL CHECK(action IN ('read','write','replace','delete')),
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS memory_tags (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		tag        TEXT NOT NULL,
		value      TEXT NOT NULL,
		importance REAL DEFAULT 1.0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS memory_entries (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		entry_type TEXT NOT NULL CHECK(entry_type IN ('fact','summary','intent','context','code_summary')),
		content    TEXT NOT NULL,
		source_conversation_id INTEGER REFERENCES conversations(id) ON DELETE SET NULL,
		confidence REAL DEFAULT 1.0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_conversations_user_session_created ON conversations(user_id, session_id, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_file_snapshots_session ON file_snapshots(session_id);
	CREATE INDEX IF NOT EXISTS idx_memory_tags_user_tag ON memory_tags(user_id, tag);
	CREATE INDEX IF NOT EXISTS idx_memory_entries_user_type ON memory_entries(user_id, entry_type);
	`
	_, err := d.sqlDB.Exec(schema)
	if err != nil {
		return err
	}
	return d.upgradeMemoryEntriesPreference()
}

func (d *DB) upgradeMemoryEntriesPreference() error {
	var createSQL string
	row := d.sqlDB.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='memory_entries'`)
	if err := row.Scan(&createSQL); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if strings.Contains(createSQL, "'preference'") {
		return nil
	}
	_, err := d.sqlDB.Exec(`
		PRAGMA foreign_keys=OFF;
		BEGIN TRANSACTION;
		CREATE TABLE memory_entries_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			entry_type TEXT NOT NULL CHECK(entry_type IN ('fact','summary','intent','context','code_summary','preference')),
			content TEXT NOT NULL,
			source_conversation_id INTEGER REFERENCES conversations(id) ON DELETE SET NULL,
			confidence REAL DEFAULT 1.0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO memory_entries_new SELECT * FROM memory_entries;
		DROP TABLE memory_entries;
		ALTER TABLE memory_entries_new RENAME TO memory_entries;
		CREATE INDEX idx_memory_entries_user_type ON memory_entries(user_id, entry_type);
		COMMIT;
		PRAGMA foreign_keys=ON;
	`)
	return err
}

type user struct {
	ID           int64
	Username     string
	PasswordHash string
	Salt         string
	Mode         string
}

func (d *DB) createUser(ctx context.Context, username, salt, hash string) (int64, error) {
	res, err := d.sqlDB.ExecContext(ctx,
		`INSERT INTO users (username, salt, password_hash) VALUES (?, ?, ?)`,
		username, salt, hash)
	if err != nil {
		return 0, fmt.Errorf("insert user: %w", err)
	}
	return res.LastInsertId()
}

func (d *DB) getUserByUsername(ctx context.Context, username string) (*user, error) {
	row := d.sqlDB.QueryRowContext(ctx,
		`SELECT id, username, password_hash, salt, mode FROM users WHERE username = ?`, username)
	var u user
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Salt, &u.Mode); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("select user: %w", err)
	}
	return &u, nil
}

func (d *DB) getUserByID(ctx context.Context, id int64) (*user, error) {
	row := d.sqlDB.QueryRowContext(ctx,
		`SELECT id, username, password_hash, salt, mode FROM users WHERE id = ?`, id)
	var u user
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Salt, &u.Mode); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("select user: %w", err)
	}
	return &u, nil
}

func (d *DB) setUserMode(ctx context.Context, userID int64, mode string) error {
	_, err := d.sqlDB.ExecContext(ctx,
		`UPDATE users SET mode = ? WHERE id = ?`, mode, userID)
	return err
}

func (d *DB) createSession(ctx context.Context, token string, userID int64) error {
	_, err := d.sqlDB.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, datetime('now', '+24 hours'))`,
		token, userID)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (d *DB) resolveToken(ctx context.Context, token string) (int64, error) {
	row := d.sqlDB.QueryRowContext(ctx,
		`SELECT user_id FROM sessions WHERE token = ? AND expires_at > datetime('now')`, token)
	var uid int64
	if err := row.Scan(&uid); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("resolve token: %w", err)
	}
	return uid, nil
}

func (d *DB) gcSessions(ctx context.Context) error {
	_, err := d.sqlDB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= datetime('now')`)
	return err
}

// Workspace represents a user workspace directory.
type Workspace struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
}

func (d *DB) createWorkspace(ctx context.Context, userID int64, name, path string) (int64, error) {
	res, err := d.sqlDB.ExecContext(ctx,
		`INSERT INTO workspaces (user_id, name, path) VALUES (?, ?, ?)`,
		userID, name, path)
	if err != nil {
		return 0, fmt.Errorf("insert workspace: %w", err)
	}
	return res.LastInsertId()
}

func (d *DB) getWorkspaces(ctx context.Context, userID int64) ([]Workspace, error) {
	rows, err := d.sqlDB.QueryContext(ctx,
		`SELECT id, name, path, created_at FROM workspaces WHERE user_id = ? ORDER BY created_at DESC`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("select workspaces: %w", err)
	}
	defer rows.Close()

	var ws []Workspace
	for rows.Next() {
		var w Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.Path, &w.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan workspace: %w", err)
		}
		ws = append(ws, w)
	}
	return ws, rows.Err()
}

func (d *DB) getWorkspaceByID(ctx context.Context, userID, workspaceID int64) (*Workspace, error) {
	row := d.sqlDB.QueryRowContext(ctx,
		`SELECT id, name, path, created_at FROM workspaces WHERE id = ? AND user_id = ?`,
		workspaceID, userID)
	var w Workspace
	if err := row.Scan(&w.ID, &w.Name, &w.Path, &w.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &w, nil
}

func (d *DB) deleteWorkspace(ctx context.Context, userID, workspaceID int64) error {
	_, err := d.sqlDB.ExecContext(ctx,
		`DELETE FROM workspaces WHERE id = ? AND user_id = ?`,
		workspaceID, userID)
	return err
}

// ChatSession represents a user conversation group.
type ChatSession struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	WorkspaceID *int64 `json:"workspace_id,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func (d *DB) createChatSession(ctx context.Context, userID int64, workspaceID *int64, title string) (int64, error) {
	if title == "" {
		title = "New Chat"
	}
	res, err := d.sqlDB.ExecContext(ctx,
		`INSERT INTO chat_sessions (user_id, workspace_id, title) VALUES (?, ?, ?)`,
		userID, workspaceID, title)
	if err != nil {
		return 0, fmt.Errorf("insert chat_session: %w", err)
	}
	return res.LastInsertId()
}

func (d *DB) getChatSessions(ctx context.Context, userID int64) ([]ChatSession, error) {
	rows, err := d.sqlDB.QueryContext(ctx,
		`SELECT id, title, workspace_id, created_at FROM chat_sessions WHERE user_id = ? ORDER BY created_at DESC`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("select chat_sessions: %w", err)
	}
	defer rows.Close()

	var sessions []ChatSession
	for rows.Next() {
		var s ChatSession
		if err := rows.Scan(&s.ID, &s.Title, &s.WorkspaceID, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan chat_session: %w", err)
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

func (d *DB) getChatSession(ctx context.Context, userID, sessionID int64) (*ChatSession, error) {
	row := d.sqlDB.QueryRowContext(ctx,
		`SELECT id, title, workspace_id, created_at FROM chat_sessions WHERE id = ? AND user_id = ?`,
		sessionID, userID)
	var s ChatSession
	if err := row.Scan(&s.ID, &s.Title, &s.WorkspaceID, &s.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func (d *DB) updateChatSessionTitle(ctx context.Context, userID, sessionID int64, title string) error {
	_, err := d.sqlDB.ExecContext(ctx,
		`UPDATE chat_sessions SET title = ? WHERE id = ? AND user_id = ?`,
		title, sessionID, userID)
	return err
}

func (d *DB) deleteChatSession(ctx context.Context, userID, sessionID int64) error {
	_, err := d.sqlDB.ExecContext(ctx,
		`DELETE FROM chat_sessions WHERE id = ? AND user_id = ?`,
		sessionID, userID)
	return err
}

// Message represents a single turn in a conversation.
type message struct {
	Role     string `json:"role"`
	Content  string `json:"content"`
	ToolCall string `json:"tool_call,omitempty"`
}

func (d *DB) appendConversation(ctx context.Context, userID, sessionID int64, role, content, toolCall string) error {
	_, err := d.sqlDB.ExecContext(ctx,
		`INSERT INTO conversations (user_id, session_id, role, content, tool_call) VALUES (?, ?, ?, ?, ?)`,
		userID, sessionID, role, content, toolCall)
	if err != nil {
		return fmt.Errorf("insert conversation: %w", err)
	}
	return nil
}

func (d *DB) getChatSessionMessages(ctx context.Context, userID, sessionID int64, limit int) ([]message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.sqlDB.QueryContext(ctx,
		`SELECT role, content, tool_call FROM conversations WHERE user_id = ? AND session_id = ? ORDER BY created_at DESC LIMIT ?`,
		userID, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("select messages: %w", err)
	}
	defer rows.Close()

	var msgs []message
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.Role, &m.Content, &m.ToolCall); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (d *DB) searchConversations(ctx context.Context, userID int64, sessionID int64, keyword string, limit int) ([]message, error) {
	pattern := "%" + strings.ToLower(keyword) + "%"
	var rows *sql.Rows
	var err error
	if sessionID > 0 {
		rows, err = d.sqlDB.QueryContext(ctx,
			`SELECT role, content, tool_call FROM conversations WHERE user_id = ? AND session_id = ? AND lower(content) LIKE ? ORDER BY created_at DESC LIMIT ?`,
			userID, sessionID, pattern, limit)
	} else {
		rows, err = d.sqlDB.QueryContext(ctx,
			`SELECT role, content, tool_call FROM conversations WHERE user_id = ? AND lower(content) LIKE ? ORDER BY created_at DESC LIMIT ?`,
			userID, pattern, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("search conversations: %w", err)
	}
	defer rows.Close()

	var msgs []message
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.Role, &m.Content, &m.ToolCall); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// FileSnapshot records a file state before/after modification.
type FileSnapshot struct {
	ID       int64  `json:"id"`
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
	Action   string `json:"action"`
}

func (d *DB) appendFileSnapshot(ctx context.Context, sessionID int64, filePath, content, action string) error {
	_, err := d.sqlDB.ExecContext(ctx,
		`INSERT INTO file_snapshots (session_id, file_path, content, action) VALUES (?, ?, ?, ?)`,
		sessionID, filePath, content, action)
	if err != nil {
		return fmt.Errorf("insert file_snapshot: %w", err)
	}
	return nil
}

func (d *DB) getFileSnapshots(ctx context.Context, sessionID int64) ([]FileSnapshot, error) {
	rows, err := d.sqlDB.QueryContext(ctx,
		`SELECT id, file_path, content, action FROM file_snapshots WHERE session_id = ? ORDER BY created_at ASC`,
		sessionID)
	if err != nil {
		return nil, fmt.Errorf("select file_snapshots: %w", err)
	}
	defer rows.Close()

	var snaps []FileSnapshot
	for rows.Next() {
		var s FileSnapshot
		if err := rows.Scan(&s.ID, &s.FilePath, &s.Content, &s.Action); err != nil {
			return nil, fmt.Errorf("scan file_snapshot: %w", err)
		}
		snaps = append(snaps, s)
	}
	return snaps, rows.Err()
}

// MemoryTag represents a labeled key-value pair.
type memoryTag struct {
	ID         int64   `json:"id"`
	Tag        string  `json:"tag"`
	Value      string  `json:"value"`
	Importance float64 `json:"importance"`
}

// MemoryEntry represents an analytical memory record.
type memoryEntry struct {
	ID         int64   `json:"id"`
	EntryType  string  `json:"entry_type"`
	Content    string  `json:"content"`
	Confidence float64 `json:"confidence"`
}

func (d *DB) writeMemoryTag(ctx context.Context, userID int64, tag, value string, importance float64) error {
	_, err := d.sqlDB.ExecContext(ctx,
		`INSERT INTO memory_tags (user_id, tag, value, importance) VALUES (?, ?, ?, ?)`,
		userID, tag, value, importance)
	if err != nil {
		return fmt.Errorf("insert memory_tag: %w", err)
	}
	return nil
}

func (d *DB) replaceMemoryTag(ctx context.Context, userID int64, tag, value string, importance float64) error {
	_, _ = d.sqlDB.ExecContext(ctx,
		`DELETE FROM memory_tags WHERE user_id = ? AND tag = ?`,
		userID, tag)
	_, err := d.sqlDB.ExecContext(ctx,
		`INSERT INTO memory_tags (user_id, tag, value, importance) VALUES (?, ?, ?, ?)`,
		userID, tag, value, importance)
	if err != nil {
		return fmt.Errorf("replace memory_tag: %w", err)
	}
	return nil
}

func (d *DB) searchMemoryTags(ctx context.Context, userID int64, tagPattern string) ([]memoryTag, error) {
	pattern := "%" + strings.ToLower(tagPattern) + "%"
	rows, err := d.sqlDB.QueryContext(ctx,
		`SELECT id, tag, value, importance FROM memory_tags WHERE user_id = ? AND lower(tag) LIKE ? ORDER BY importance DESC`,
		userID, pattern)
	if err != nil {
		return nil, fmt.Errorf("search memory_tags: %w", err)
	}
	defer rows.Close()

	var tags []memoryTag
	for rows.Next() {
		var t memoryTag
		if err := rows.Scan(&t.ID, &t.Tag, &t.Value, &t.Importance); err != nil {
			return nil, fmt.Errorf("scan memory_tag: %w", err)
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func (d *DB) writeMemoryEntry(ctx context.Context, userID int64, entryType, content string, confidence float64) error {
	_, err := d.sqlDB.ExecContext(ctx,
		`INSERT INTO memory_entries (user_id, entry_type, content, confidence) VALUES (?, ?, ?, ?)`,
		userID, entryType, content, confidence)
	if err != nil {
		return fmt.Errorf("insert memory_entry: %w", err)
	}
	return nil
}

func (d *DB) searchMemoryEntries(ctx context.Context, userID int64, entryType, keyword string, limit int) ([]memoryEntry, error) {
	pattern := "%" + strings.ToLower(keyword) + "%"
	var rows *sql.Rows
	var err error
	if entryType == "" {
		rows, err = d.sqlDB.QueryContext(ctx,
			`SELECT id, entry_type, content, confidence FROM memory_entries WHERE user_id = ? AND lower(content) LIKE ? ORDER BY confidence DESC LIMIT ?`,
			userID, pattern, limit)
	} else {
		rows, err = d.sqlDB.QueryContext(ctx,
			`SELECT id, entry_type, content, confidence FROM memory_entries WHERE user_id = ? AND entry_type = ? AND lower(content) LIKE ? ORDER BY confidence DESC LIMIT ?`,
			userID, entryType, pattern, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("search memory_entries: %w", err)
	}
	defer rows.Close()

	var entries []memoryEntry
	for rows.Next() {
		var e memoryEntry
		if err := rows.Scan(&e.ID, &e.EntryType, &e.Content, &e.Confidence); err != nil {
			return nil, fmt.Errorf("scan memory_entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ToolCall represents a structured request from RWKV.
type toolCall struct {
	Tool   string                 `json:"tool"`
	Params map[string]interface{} `json:"params"`
}

// ExecuteTool validates and executes a tool call from RWKV.
func (d *DB) executeTool(ctx context.Context, userID int64, call toolCall) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	switch call.Tool {
	case "search_conversations":
		keyword, _ := call.Params["query"].(string)
		limitF, _ := call.Params["limit"].(float64)
		limit := int(limitF)
		if limit <= 0 || limit > 50 {
			limit = 10
		}
		msgs, err := d.searchConversations(ctx, userID, 0, keyword, limit)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(msgs)
		return string(b), nil

	case "search_memory_tags":
		tag, _ := call.Params["tag"].(string)
		tags, err := d.searchMemoryTags(ctx, userID, tag)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(tags)
		return string(b), nil

	case "write_memory_tag":
		tag, _ := call.Params["tag"].(string)
		value, _ := call.Params["value"].(string)
		importance, _ := call.Params["importance"].(float64)
		if importance == 0 {
			importance = 1.0
		}
		if err := d.writeMemoryTag(ctx, userID, tag, value, importance); err != nil {
			return "", err
		}
		return `{"status":"ok"}`, nil

	case "search_memory_entries":
		entryType, _ := call.Params["entry_type"].(string)
		keyword, _ := call.Params["keyword"].(string)
		limitF, _ := call.Params["limit"].(float64)
		limit := int(limitF)
		if limit <= 0 || limit > 50 {
			limit = 10
		}
		entries, err := d.searchMemoryEntries(ctx, userID, entryType, keyword, limit)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(entries)
		return string(b), nil

	case "write_memory_entry":
		entryType, _ := call.Params["entry_type"].(string)
		content, _ := call.Params["content"].(string)
		confidence, _ := call.Params["confidence"].(float64)
		if confidence == 0 {
			confidence = 1.0
		}
		if err := d.writeMemoryEntry(ctx, userID, entryType, content, confidence); err != nil {
			return "", err
		}
		return `{"status":"ok"}`, nil

	default:
		return "", fmt.Errorf("unknown tool: %s", call.Tool)
	}
}

// ParseToolCall extracts a ToolCall from an assistant response if present.
func parseToolCall(text string) (toolCall, bool) {
	const prefix = "TOOL_CALL:"
	idx := strings.Index(text, prefix)
	if idx == -1 {
		return toolCall{}, false
	}
	jsonPart := strings.TrimSpace(text[idx+len(prefix):])
	var call toolCall
	if err := json.Unmarshal([]byte(jsonPart), &call); err != nil {
		return toolCall{}, false
	}
	return call, true
}
