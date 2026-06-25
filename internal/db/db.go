package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// columnMigration describes a column that must exist in a table.
// Used by applyMigrations to backfill columns added after a DB was created.
type columnMigration struct {
	table  string
	column string
	def    string // SQL type + constraints, e.g. "INTEGER NOT NULL DEFAULT 5"
}

// columnMigrations is the ordered list of columns added after the initial schema.
// Append here whenever a new column is added to an existing table in schema.sql.
var columnMigrations = []columnMigration{
	{"projects", "max_execution_attempts", "INTEGER NOT NULL DEFAULT 5"},
	{"handoff_attempts", "ended_at", "TIMESTAMP"},
}

//go:embed schema.sql
var schemaSQL string

// DB wraps sql.DB with Ratchet-specific helpers.
type DB struct {
	*sql.DB
	Path string // filesystem path passed to Open; needed by subprocesses
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
	db := &DB{DB: raw, Path: path}
	if err := db.applySchema(); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := db.applyMigrations(); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
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

// applyMigrations adds any columns in columnMigrations that are absent from
// the live table. Safe to run on both new and existing databases.
func (db *DB) applyMigrations() error {
	for _, m := range columnMigrations {
		rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", m.table))
		if err != nil {
			return fmt.Errorf("table_info %s: %w", m.table, err)
		}
		found := false
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return fmt.Errorf("scan table_info %s: %w", m.table, err)
			}
			if name == m.column {
				found = true
				break
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("table_info %s rows: %w", m.table, err)
		}
		if !found {
			stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", m.table, m.column, m.def)
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("migrate %s.%s: %w", m.table, m.column, err)
			}
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
