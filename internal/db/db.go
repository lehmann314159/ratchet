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
	{"projects", "language", "TEXT NOT NULL DEFAULT 'go'"},
	{"projects", "pause_after_reconcile", "INTEGER NOT NULL DEFAULT 0"},
	{"executions", "infra_failure", "INTEGER NOT NULL DEFAULT 0"},
	{"beads", "execution_attempts_override", "INTEGER"},
	{"executions", "test_first_attempt", "INTEGER NOT NULL DEFAULT 0"},
	{"handoff_jobs", "refinement_cycle_id", "INTEGER"},
	{"test_refinements", "cycle_id", "INTEGER NOT NULL DEFAULT 1"},
	{"test_refinements", "decision", "TEXT NOT NULL DEFAULT ''"},
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
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
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
	if err := db.applyTableMigrations(); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("apply table migrations: %w", err)
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
			if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migrate %s.%s: %w", m.table, m.column, err)
			}
		}
	}
	return nil
}

// applyTableMigrations applies structural migrations that columnMigrations cannot
// handle (e.g. CHECK constraint changes, new data seeding). Safe to call on both
// new and existing databases.
func (db *DB) applyTableMigrations() error {
	if err := db.migrateBeadRevisionVerbs(); err != nil {
		return err
	}
	if err := db.seedRevisePendingAssignments(); err != nil {
		return err
	}
	if err := db.seedRefineTestsAssignments(); err != nil {
		return err
	}
	if err := db.migrateProjectsStatus(); err != nil {
		return err
	}
	if err := db.migrateTestRefinementsVerbs(); err != nil {
		return err
	}
	return nil
}

// migrateBeadRevisionVerbs updates bead_revisions' created_by_verb CHECK constraint
// to include 'REVISE_PENDING'. SQLite does not support ALTER TABLE … ALTER COLUMN,
// so we reconstruct the table with FK enforcement temporarily disabled.
func (db *DB) migrateBeadRevisionVerbs() error {
	var createSQL string
	if err := db.QueryRow(
		`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type='table' AND name='bead_revisions'`,
	).Scan(&createSQL); err != nil {
		return fmt.Errorf("query bead_revisions schema: %w", err)
	}
	if strings.Contains(createSQL, "REVISE_PENDING") {
		return nil
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}
	stmts := []string{
		// legacy_alter_table prevents SQLite from rewriting FK references in
		// other tables (e.g. beads.current_revision_id) to point at the renamed
		// _bead_revisions_old, which would leave a dangling FK after the DROP.
		`PRAGMA legacy_alter_table = ON`,
		`ALTER TABLE bead_revisions RENAME TO _bead_revisions_old`,
		`PRAGMA legacy_alter_table = OFF`,
		`CREATE TABLE bead_revisions (
		  id               INTEGER PRIMARY KEY,
		  project_id       INTEGER NOT NULL REFERENCES projects(id),
		  bead_id          INTEGER NOT NULL REFERENCES beads(id),
		  revision_number  INTEGER NOT NULL,
		  full_text        TEXT    NOT NULL,
		  execution_budget INTEGER NOT NULL,
		  monitor_override TEXT    NOT NULL CHECK (monitor_override IN ('honor', 'ignore')),
		  created_by_verb  TEXT    NOT NULL CHECK (created_by_verb IN ('DECOMPOSE_SPEC', 'RECONCILE_DECOMPOSITION', 'ADJUDICATE_NEXT_EXECUTION', 'REVISE_PENDING')),
		  created_at       TIMESTAMP NOT NULL
		)`,
		`INSERT INTO bead_revisions SELECT * FROM _bead_revisions_old`,
		`DROP TABLE _bead_revisions_old`,
		`CREATE INDEX IF NOT EXISTS idx_bead_revisions_bead ON bead_revisions (bead_id, revision_number)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			_, _ = db.Exec(`PRAGMA foreign_keys = ON`)
			return fmt.Errorf("migrate bead_revisions (%s): %w", truncate(stmt, 40), err)
		}
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("re-enable foreign_keys: %w", err)
	}
	return nil
}

// seedRevisePendingAssignments inserts a REVISE_PENDING verb_model_assignments row
// for any active project that has an AUDIT assignment but no REVISE_PENDING assignment.
// This backfills projects created before REVISE_PENDING was added.
func (db *DB) seedRevisePendingAssignments() error {
	_, err := db.Exec(`
		INSERT INTO verb_model_assignments (project_id, verb, model)
		SELECT audit.project_id, 'REVISE_PENDING', audit.model
		FROM verb_model_assignments audit
		WHERE audit.verb = 'AUDIT_DECOMPOSITION'
		  AND NOT EXISTS (
		    SELECT 1 FROM verb_model_assignments rp
		    WHERE rp.project_id = audit.project_id AND rp.verb = 'REVISE_PENDING'
		  )`)
	if err != nil {
		return fmt.Errorf("seed REVISE_PENDING assignments: %w", err)
	}
	return nil
}

// seedRefineTestsAssignments inserts REFINE_TESTS_WRITE, REFINE_TESTS_CRITIQUE,
// and REFINE_TESTS_JUDGE verb_model_assignments for any project that is missing them.
// Write/Judge: seeded from EXECUTE_BEAD (gemma4:31b — proven code writer).
// Critique: seeded from AUDIT_DECOMPOSITION (qwen3:32b — independent reviewer).
// This backfills projects created before the three-verb REFINE_TESTS was added.
func (db *DB) seedRefineTestsAssignments() error {
	for _, pair := range []struct{ verb, srcVerb string }{
		{"REFINE_TESTS_WRITE", "EXECUTE_BEAD"},
		{"REFINE_TESTS_CRITIQUE", "AUDIT_DECOMPOSITION"},
		{"REFINE_TESTS_JUDGE", "EXECUTE_BEAD"},
	} {
		_, err := db.Exec(`
			INSERT INTO verb_model_assignments (project_id, verb, model)
			SELECT project_id, ?, model
			FROM verb_model_assignments
			WHERE verb = ?
			  AND NOT EXISTS (
			    SELECT 1 FROM verb_model_assignments x
			    WHERE x.project_id = verb_model_assignments.project_id
			      AND x.verb = ?
			  )`, pair.verb, pair.srcVerb, pair.verb)
		if err != nil {
			return fmt.Errorf("seed %s assignments: %w", pair.verb, err)
		}
	}
	return nil
}

// migrateTestRefinementsVerbs updates the test_refinements table's verb CHECK
// constraint from ('REFINE_TESTS_A', 'REFINE_TESTS_B') to the three-verb
// ('REFINE_TESTS_WRITE', 'REFINE_TESTS_CRITIQUE', 'REFINE_TESTS_JUDGE') design.
// Also bakes in the decision column (previously added via columnMigrations).
func (db *DB) migrateTestRefinementsVerbs() error {
	var createSQL string
	if err := db.QueryRow(
		`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type='table' AND name='test_refinements'`,
	).Scan(&createSQL); err != nil {
		return fmt.Errorf("query test_refinements schema: %w", err)
	}
	if strings.Contains(createSQL, "REFINE_TESTS_WRITE") {
		return nil
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}
	stmts := []string{
		`PRAGMA legacy_alter_table = ON`,
		`ALTER TABLE test_refinements RENAME TO _test_refinements_old`,
		`PRAGMA legacy_alter_table = OFF`,
		`CREATE TABLE test_refinements (
		  id          INTEGER PRIMARY KEY,
		  project_id  INTEGER NOT NULL REFERENCES projects(id),
		  bead_id     INTEGER NOT NULL REFERENCES beads(id),
		  cycle_id    INTEGER NOT NULL DEFAULT 1,
		  turn        INTEGER NOT NULL,
		  verb        TEXT    NOT NULL CHECK (verb IN ('REFINE_TESTS_WRITE', 'REFINE_TESTS_CRITIQUE', 'REFINE_TESTS_JUDGE')),
		  changed     INTEGER NOT NULL,
		  summary     TEXT,
		  decision    TEXT    NOT NULL DEFAULT '',
		  created_at  TIMESTAMP NOT NULL
		)`,
		// Only copy rows with new verb names; old REFINE_TESTS_A/B rows from
		// completed projects are discarded rather than violating the new CHECK.
		`INSERT INTO test_refinements
		  (id, project_id, bead_id, cycle_id, turn, verb, changed, summary, decision, created_at)
		SELECT id, project_id, bead_id, cycle_id, turn, verb, changed, summary, decision, created_at
		FROM _test_refinements_old
		WHERE verb IN ('REFINE_TESTS_WRITE', 'REFINE_TESTS_CRITIQUE', 'REFINE_TESTS_JUDGE')`,
		`DROP TABLE _test_refinements_old`,
		`CREATE INDEX IF NOT EXISTS idx_test_refinements_bead ON test_refinements (bead_id, turn)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			_, _ = db.Exec(`PRAGMA foreign_keys = ON`)
			return fmt.Errorf("migrate test_refinements (%s): %w", truncate(stmt, 40), err)
		}
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("re-enable foreign_keys: %w", err)
	}
	return nil
}

// migrateProjectsStatus updates the projects table's status CHECK constraint to
// include 'paused'. columnMigrations has already added pause_after_reconcile by
// the time this runs, so the INSERT from the old table can name that column.
func (db *DB) migrateProjectsStatus() error {
	var createSQL string
	if err := db.QueryRow(
		`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type='table' AND name='projects'`,
	).Scan(&createSQL); err != nil {
		return fmt.Errorf("query projects schema: %w", err)
	}
	if strings.Contains(createSQL, "'paused'") {
		return nil
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}
	stmts := []string{
		`PRAGMA legacy_alter_table = ON`,
		`ALTER TABLE projects RENAME TO _projects_old`,
		`PRAGMA legacy_alter_table = OFF`,
		`CREATE TABLE projects (
		  id                          INTEGER PRIMARY KEY,
		  label                       TEXT    NOT NULL,
		  folder_path                 TEXT    NOT NULL,
		  design_doc_path             TEXT    NOT NULL,
		  status                      TEXT    NOT NULL CHECK (status IN ('active', 'full_stopped', 'complete', 'paused')),
		  recovered_from_project_id   INTEGER REFERENCES projects(id),
		  monitor_override_default    TEXT    NOT NULL CHECK (monitor_override_default IN ('honor', 'ignore')),
		  execution_budget_default    INTEGER NOT NULL,
		  audit_reconcile_round_cap   INTEGER NOT NULL DEFAULT 2,
		  max_execution_attempts      INTEGER NOT NULL DEFAULT 5,
		  language                    TEXT    NOT NULL DEFAULT 'go',
		  pause_after_reconcile       INTEGER NOT NULL DEFAULT 0,
		  created_at                  TIMESTAMP NOT NULL,
		  updated_at                  TIMESTAMP NOT NULL
		)`,
		`INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status, recovered_from_project_id,
		   monitor_override_default, execution_budget_default, audit_reconcile_round_cap,
		   max_execution_attempts, language, pause_after_reconcile, created_at, updated_at)
		SELECT
		  id, label, folder_path, design_doc_path, status, recovered_from_project_id,
		  monitor_override_default, execution_budget_default, audit_reconcile_round_cap,
		  max_execution_attempts, language, pause_after_reconcile, created_at, updated_at
		FROM _projects_old`,
		`DROP TABLE _projects_old`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			_, _ = db.Exec(`PRAGMA foreign_keys = ON`)
			return fmt.Errorf("migrate projects status (%s): %w", truncate(stmt, 40), err)
		}
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("re-enable foreign_keys: %w", err)
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
