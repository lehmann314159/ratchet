package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps sql.DB with Ratchet-specific helpers.
type DB struct {
	*sql.DB
}

// Open opens (or creates) the SQLite database at path and applies the schema.
// WAL mode and foreign-key enforcement are enabled on each connection via
// the DSN pragma parameters.
func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// Single writer avoids "database is locked" errors; the orchestrator is
	// single-process and doesn't need a connection pool.
	raw.SetMaxOpenConns(1)
	db := &DB{raw}
	if err := db.applySchema(); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}

// applySchema runs each DDL statement in schema.sql idempotently.
func (db *DB) applySchema() error {
	for _, stmt := range splitSQL(schemaSQL) {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%q: %w", truncate(stmt, 60), err)
		}
	}
	return nil
}

// splitSQL splits a SQL script on semicolons, returning non-empty statements.
// Safe for DDL-only scripts; does not handle semicolons inside string literals.
func splitSQL(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ";") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed+";")
		}
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
