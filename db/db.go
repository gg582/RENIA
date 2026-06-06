package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps the underlying sql.DB with schema initialization.
type DB struct {
	sqlDB *sql.DB
}

// Open creates a new SQLite connection pool, applies the schema,
// and returns a ready-to-use DB handle.
func Open(dsn string) (*DB, error) {
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

// Close shuts down the connection pool.
func (d *DB) Close() error {
	return d.sqlDB.Close()
}

// Conn exposes the underlying sql.DB for repository constructors.
func (d *DB) Conn() *sql.DB {
	return d.sqlDB
}
