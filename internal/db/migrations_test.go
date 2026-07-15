package db

import (
	"context"
	"database/sql"
	"testing"
)

// Construct legacy-shaped tables (predating each rename+recreate+copy+drop
// migration in db.go) and confirm the migration preserves existing data and
// accepts the new CHECK constraint values. Before this file, none of these
// migrations had any test coverage — TestSchemaIdempotent only exercises a
// fresh DB where every migration's guard short-circuits as a no-op.

func openRawTestDB(t *testing.T) *DB {
	t.Helper()
	raw, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { raw.Close() })
	return &DB{DB: raw}
}

func mustExec(t *testing.T, d *DB, q string, args ...interface{}) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestProbeMigrateBeadRevisionVerbs(t *testing.T) {
	ctx := context.Background()
	d := openRawTestDB(t)
	mustExec(t, d, `CREATE TABLE projects (id INTEGER PRIMARY KEY, label TEXT)`)
	mustExec(t, d, `INSERT INTO projects (id, label) VALUES (1, 'p')`)
	mustExec(t, d, `CREATE TABLE beads (id INTEGER PRIMARY KEY, project_id INTEGER, status TEXT, current_revision_id INTEGER)`)
	mustExec(t, d, `INSERT INTO beads (id, project_id, status) VALUES (1, 1, 'pending')`)
	mustExec(t, d, `CREATE TABLE bead_revisions (
	  id INTEGER PRIMARY KEY,
	  project_id INTEGER NOT NULL,
	  bead_id INTEGER NOT NULL,
	  revision_number INTEGER NOT NULL,
	  full_text TEXT NOT NULL,
	  execution_budget INTEGER NOT NULL,
	  monitor_override TEXT NOT NULL CHECK (monitor_override IN ('honor','ignore')),
	  created_by_verb TEXT NOT NULL CHECK (created_by_verb IN ('DECOMPOSE_SPEC','RECONCILE_DECOMPOSITION','ADJUDICATE_NEXT_EXECUTION')),
	  created_at TIMESTAMP NOT NULL
	)`)
	mustExec(t, d, `INSERT INTO bead_revisions (id,project_id,bead_id,revision_number,full_text,execution_budget,monitor_override,created_by_verb,created_at)
	  VALUES (1,1,1,1,'text-1',300,'honor','DECOMPOSE_SPEC','2026-01-01T00:00:00Z')`)
	mustExec(t, d, `UPDATE beads SET current_revision_id = 1 WHERE id = 1`)

	if err := d.migrateBeadRevisionVerbs(); err != nil {
		t.Fatalf("migrateBeadRevisionVerbs: %v", err)
	}

	var fullText, verb string
	if err := d.QueryRowContext(ctx, `SELECT full_text, created_by_verb FROM bead_revisions WHERE id = 1`).Scan(&fullText, &verb); err != nil {
		t.Fatalf("row lost after migration: %v", err)
	}
	if fullText != "text-1" || verb != "DECOMPOSE_SPEC" {
		t.Errorf("data corrupted: full_text=%q verb=%q", fullText, verb)
	}
	var curRev int64
	if err := d.QueryRowContext(ctx, `SELECT current_revision_id FROM beads WHERE id = 1`).Scan(&curRev); err != nil {
		t.Fatalf("beads.current_revision_id FK broke: %v", err)
	}
	if curRev != 1 {
		t.Errorf("beads.current_revision_id = %d, want 1", curRev)
	}

	mustExec(t, d, `INSERT INTO bead_revisions (id,project_id,bead_id,revision_number,full_text,execution_budget,monitor_override,created_by_verb,created_at)
	  VALUES (2,1,1,2,'text-2',300,'honor','REVISE_PENDING','2026-01-01T00:00:00Z')`)
}

func TestMigrateTestRefinementsVerbsDropsOldRows(t *testing.T) {
	ctx := context.Background()
	d := openRawTestDB(t)
	mustExec(t, d, `CREATE TABLE projects (id INTEGER PRIMARY KEY, label TEXT)`)
	mustExec(t, d, `INSERT INTO projects (id, label) VALUES (1, 'p')`)
	mustExec(t, d, `CREATE TABLE beads (id INTEGER PRIMARY KEY, project_id INTEGER, status TEXT)`)
	mustExec(t, d, `INSERT INTO beads (id, project_id, status) VALUES (1, 1, 'pending')`)
	mustExec(t, d, `CREATE TABLE test_refinements (
	  id INTEGER PRIMARY KEY,
	  project_id INTEGER NOT NULL,
	  bead_id INTEGER NOT NULL,
	  turn INTEGER NOT NULL,
	  verb TEXT NOT NULL CHECK (verb IN ('REFINE_TESTS_A','REFINE_TESTS_B')),
	  changed INTEGER NOT NULL,
	  summary TEXT,
	  created_at TIMESTAMP NOT NULL
	)`)
	mustExec(t, d, `INSERT INTO test_refinements (id,project_id,bead_id,turn,verb,changed,summary,created_at)
	  VALUES (1,1,1,1,'REFINE_TESTS_A',1,'old row summary','2026-01-01T00:00:00Z')`)

	// Simulate columnMigrations having already added cycle_id/decision (they
	// run before applyTableMigrations in the real Open() sequence).
	mustExec(t, d, `ALTER TABLE test_refinements ADD COLUMN cycle_id INTEGER NOT NULL DEFAULT 1`)
	mustExec(t, d, `ALTER TABLE test_refinements ADD COLUMN decision TEXT NOT NULL DEFAULT ''`)

	var before int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM test_refinements`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 1 {
		t.Fatalf("setup: want 1 row before migration, got %d", before)
	}

	if err := d.migrateTestRefinementsVerbs(); err != nil {
		t.Fatalf("migrateTestRefinementsVerbs: %v", err)
	}

	var after int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM test_refinements`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	t.Logf("rows before=%d after=%d (old REFINE_TESTS_A/B row expected to be DISCARDED by design)", before, after)
	if after != 0 {
		t.Errorf("expected old-verb row to be discarded, got %d rows surviving", after)
	}
}

func TestMigrateProjectsStatusPreservesData(t *testing.T) {
	ctx := context.Background()
	d := openRawTestDB(t)
	mustExec(t, d, `CREATE TABLE projects (
	  id INTEGER PRIMARY KEY,
	  label TEXT NOT NULL,
	  folder_path TEXT NOT NULL,
	  design_doc_path TEXT NOT NULL,
	  status TEXT NOT NULL CHECK (status IN ('active','full_stopped','complete')),
	  recovered_from_project_id INTEGER REFERENCES projects(id),
	  monitor_override_default TEXT NOT NULL CHECK (monitor_override_default IN ('honor','ignore')),
	  execution_budget_default INTEGER NOT NULL,
	  audit_reconcile_round_cap INTEGER NOT NULL DEFAULT 2,
	  created_at TIMESTAMP NOT NULL,
	  updated_at TIMESTAMP NOT NULL
	)`)
	mustExec(t, d, `INSERT INTO projects (id,label,folder_path,design_doc_path,status,monitor_override_default,execution_budget_default,created_at,updated_at)
	  VALUES (1,'proj-A','/tmp/a','a.md','active','honor',300,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	mustExec(t, d, `INSERT INTO projects (id,label,folder_path,design_doc_path,status,recovered_from_project_id,monitor_override_default,execution_budget_default,created_at,updated_at)
	  VALUES (2,'proj-B','/tmp/b','b.md','active',1,'ignore',400,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	// Simulate the three columnMigrations that precede this in real Open().
	mustExec(t, d, `ALTER TABLE projects ADD COLUMN max_execution_attempts INTEGER NOT NULL DEFAULT 5`)
	mustExec(t, d, `ALTER TABLE projects ADD COLUMN language TEXT NOT NULL DEFAULT 'go'`)
	mustExec(t, d, `ALTER TABLE projects ADD COLUMN pause_after_reconcile INTEGER NOT NULL DEFAULT 0`)
	mustExec(t, d, `UPDATE projects SET language = 'python' WHERE id = 2`)

	if err := d.migrateProjectsStatus(); err != nil {
		t.Fatalf("migrateProjectsStatus: %v", err)
	}

	var label, lang string
	var recov sql.NullInt64
	if err := d.QueryRowContext(ctx, `SELECT label, language, recovered_from_project_id FROM projects WHERE id = 2`).
		Scan(&label, &lang, &recov); err != nil {
		t.Fatalf("row lost after migration: %v", err)
	}
	if label != "proj-B" || lang != "python" || !recov.Valid || recov.Int64 != 1 {
		t.Errorf("data corrupted: label=%q language=%q recovered_from=%v", label, lang, recov)
	}
	mustExec(t, d, `INSERT INTO projects (id,label,folder_path,design_doc_path,status,monitor_override_default,execution_budget_default,created_at,updated_at)
	  VALUES (3,'proj-C','/tmp/c','c.md','paused','honor',300,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
}

func TestMigrateAuditReconcileRoundsOutcomePreservesData(t *testing.T) {
	ctx := context.Background()
	d := openRawTestDB(t)
	mustExec(t, d, `CREATE TABLE projects (id INTEGER PRIMARY KEY)`)
	mustExec(t, d, `INSERT INTO projects (id) VALUES (1)`)
	mustExec(t, d, `CREATE TABLE audit_reconcile_rounds (
	  id INTEGER PRIMARY KEY,
	  project_id INTEGER NOT NULL REFERENCES projects(id),
	  round_number INTEGER NOT NULL,
	  critique_text TEXT NOT NULL,
	  reconciliation TEXT NOT NULL,
	  outcome TEXT NOT NULL CHECK (outcome IN ('converged','disagreed_continuing','escalated')),
	  created_at TIMESTAMP NOT NULL
	)`)
	mustExec(t, d, `INSERT INTO audit_reconcile_rounds (id,project_id,round_number,critique_text,reconciliation,outcome,created_at)
	  VALUES (1,1,1,'crit','recon','escalated','2026-01-01T00:00:00Z')`)

	if err := d.migrateAuditReconcileRoundsOutcome(); err != nil {
		t.Fatalf("migrateAuditReconcileRoundsOutcome: %v", err)
	}
	var outcome string
	if err := d.QueryRowContext(ctx, `SELECT outcome FROM audit_reconcile_rounds WHERE id = 1`).Scan(&outcome); err != nil {
		t.Fatalf("row lost: %v", err)
	}
	if outcome != "escalated" {
		t.Errorf("data corrupted: outcome=%q", outcome)
	}
	mustExec(t, d, `INSERT INTO audit_reconcile_rounds (id,project_id,round_number,critique_text,reconciliation,outcome,created_at)
	  VALUES (2,1,2,'crit','','redecompose','2026-01-01T00:00:00Z')`)
}

func TestMigrateAdjudicationsDecisionPreservesData(t *testing.T) {
	ctx := context.Background()
	d := openRawTestDB(t)
	mustExec(t, d, `CREATE TABLE projects (id INTEGER PRIMARY KEY)`)
	mustExec(t, d, `CREATE TABLE beads (id INTEGER PRIMARY KEY)`)
	mustExec(t, d, `CREATE TABLE executions (id INTEGER PRIMARY KEY)`)
	mustExec(t, d, `INSERT INTO projects (id) VALUES (1)`)
	mustExec(t, d, `INSERT INTO beads (id) VALUES (1)`)
	mustExec(t, d, `INSERT INTO executions (id) VALUES (1)`)
	mustExec(t, d, `CREATE TABLE adjudications (
	  id INTEGER PRIMARY KEY,
	  project_id INTEGER NOT NULL REFERENCES projects(id),
	  bead_id INTEGER NOT NULL REFERENCES beads(id),
	  execution_id INTEGER NOT NULL REFERENCES executions(id),
	  trend TEXT NOT NULL CHECK (trend IN ('same','narrower','unrelated','not_applicable')),
	  bead_spec_fit TEXT NOT NULL CHECK (bead_spec_fit IN ('bead_problem','execution_capability_problem','not_applicable')),
	  reasoning_text TEXT NOT NULL,
	  attempt_budget_cost REAL NOT NULL,
	  monitor_escalation_status INTEGER NOT NULL,
	  decision TEXT NOT NULL CHECK (decision IN ('execute_as_is','execute_revised','full_stop')),
	  created_at TIMESTAMP NOT NULL
	)`)
	mustExec(t, d, `INSERT INTO adjudications (id,project_id,bead_id,execution_id,trend,bead_spec_fit,reasoning_text,attempt_budget_cost,monitor_escalation_status,decision,created_at)
	  VALUES (1,1,1,1,'same','not_applicable','reasoning here',1.5,0,'full_stop','2026-01-01T00:00:00Z')`)

	if err := d.migrateAdjudicationsDecision(); err != nil {
		t.Fatalf("migrateAdjudicationsDecision: %v", err)
	}
	var reasoning string
	if err := d.QueryRowContext(ctx, `SELECT reasoning_text FROM adjudications WHERE id = 1`).Scan(&reasoning); err != nil {
		t.Fatalf("row lost: %v", err)
	}
	if reasoning != "reasoning here" {
		t.Errorf("data corrupted: reasoning_text=%q", reasoning)
	}
	mustExec(t, d, `INSERT INTO adjudications (id,project_id,bead_id,execution_id,trend,bead_spec_fit,reasoning_text,attempt_budget_cost,monitor_escalation_status,decision,created_at)
	  VALUES (2,1,1,1,'same','not_applicable','r',1,0,'re_refine','2026-01-01T00:00:00Z')`)
}

func TestMigrateBeadRevisionVerbsRewindPreservesData(t *testing.T) {
	ctx := context.Background()
	d := openRawTestDB(t)
	mustExec(t, d, `CREATE TABLE projects (id INTEGER PRIMARY KEY)`)
	mustExec(t, d, `INSERT INTO projects (id) VALUES (1)`)
	mustExec(t, d, `CREATE TABLE beads (id INTEGER PRIMARY KEY, current_revision_id INTEGER)`)
	mustExec(t, d, `INSERT INTO beads (id) VALUES (1)`)
	mustExec(t, d, `CREATE TABLE bead_revisions (
	  id INTEGER PRIMARY KEY,
	  project_id INTEGER NOT NULL,
	  bead_id INTEGER NOT NULL,
	  revision_number INTEGER NOT NULL,
	  full_text TEXT NOT NULL,
	  execution_budget INTEGER NOT NULL,
	  monitor_override TEXT NOT NULL CHECK (monitor_override IN ('honor','ignore')),
	  created_by_verb TEXT NOT NULL CHECK (created_by_verb IN ('DECOMPOSE_SPEC','RECONCILE_DECOMPOSITION','ADJUDICATE_NEXT_EXECUTION','REVISE_PENDING')),
	  created_at TIMESTAMP NOT NULL
	)`)
	mustExec(t, d, `INSERT INTO bead_revisions (id,project_id,bead_id,revision_number,full_text,execution_budget,monitor_override,created_by_verb,created_at)
	  VALUES (1,1,1,1,'text-1',300,'honor','DECOMPOSE_SPEC','2026-01-01T00:00:00Z')`)
	mustExec(t, d, `UPDATE beads SET current_revision_id = 1 WHERE id = 1`)

	if err := d.migrateBeadRevisionVerbsRewind(); err != nil {
		t.Fatalf("migrateBeadRevisionVerbsRewind: %v", err)
	}
	var fullText string
	if err := d.QueryRowContext(ctx, `SELECT full_text FROM bead_revisions WHERE id = 1`).Scan(&fullText); err != nil {
		t.Fatalf("row lost: %v", err)
	}
	if fullText != "text-1" {
		t.Errorf("data corrupted: full_text=%q", fullText)
	}
	var curRev int64
	if err := d.QueryRowContext(ctx, `SELECT current_revision_id FROM beads WHERE id = 1`).Scan(&curRev); err != nil || curRev != 1 {
		t.Fatalf("beads.current_revision_id FK broke: err=%v curRev=%d", err, curRev)
	}
	mustExec(t, d, `INSERT INTO bead_revisions (id,project_id,bead_id,revision_number,full_text,execution_budget,monitor_override,created_by_verb,created_at)
	  VALUES (2,1,1,2,'text-2',300,'honor','REWIND_BEAD','2026-01-01T00:00:00Z')`)
}

func TestMigrateExecutionsTerminationCausePreservesData(t *testing.T) {
	ctx := context.Background()
	d := openRawTestDB(t)
	mustExec(t, d, `CREATE TABLE projects (id INTEGER PRIMARY KEY)`)
	mustExec(t, d, `INSERT INTO projects (id) VALUES (1)`)
	mustExec(t, d, `CREATE TABLE beads (id INTEGER PRIMARY KEY)`)
	mustExec(t, d, `INSERT INTO beads (id) VALUES (1)`)
	mustExec(t, d, `CREATE TABLE bead_revisions (id INTEGER PRIMARY KEY)`)
	mustExec(t, d, `INSERT INTO bead_revisions (id) VALUES (1)`)
	mustExec(t, d, `CREATE TABLE executions (
	  id INTEGER PRIMARY KEY,
	  project_id INTEGER NOT NULL,
	  bead_id INTEGER NOT NULL,
	  bead_revision_id INTEGER NOT NULL,
	  trace_path TEXT NOT NULL,
	  termination_cause TEXT CHECK (termination_cause IN ('success','timeout','monitor_terminated','monitor_force_killed')),
	  monitor_fired INTEGER,
	  monitor_honored INTEGER,
	  started_at TIMESTAMP NOT NULL,
	  ended_at TIMESTAMP
	)`)
	mustExec(t, d, `INSERT INTO executions (id,project_id,bead_id,bead_revision_id,trace_path,termination_cause,monitor_fired,monitor_honored,started_at,ended_at)
	  VALUES (1,1,1,1,'/tmp/trace-1.log','success',0,1,'2026-01-01T00:00:00Z','2026-01-01T00:05:00Z')`)
	// Simulate the two columnMigrations that precede this in real Open().
	mustExec(t, d, `ALTER TABLE executions ADD COLUMN infra_failure INTEGER NOT NULL DEFAULT 0`)
	mustExec(t, d, `ALTER TABLE executions ADD COLUMN test_first_attempt INTEGER NOT NULL DEFAULT 0`)
	mustExec(t, d, `UPDATE executions SET infra_failure = 1 WHERE id = 1`)
	// A table with a FK into executions, to confirm it survives the rename dance.
	mustExec(t, d, `CREATE TABLE analyses (
	  id INTEGER PRIMARY KEY,
	  execution_id INTEGER NOT NULL REFERENCES executions(id)
	)`)
	mustExec(t, d, `INSERT INTO analyses (id, execution_id) VALUES (1, 1)`)

	if err := d.migrateExecutionsTerminationCause(); err != nil {
		t.Fatalf("migrateExecutionsTerminationCause: %v", err)
	}

	var tracePath string
	var infraFailure int
	if err := d.QueryRowContext(ctx, `SELECT trace_path, infra_failure FROM executions WHERE id = 1`).
		Scan(&tracePath, &infraFailure); err != nil {
		t.Fatalf("row lost after migration: %v", err)
	}
	if tracePath != "/tmp/trace-1.log" || infraFailure != 1 {
		t.Errorf("data corrupted: trace_path=%q infra_failure=%d", tracePath, infraFailure)
	}
	var execID int64
	if err := d.QueryRowContext(ctx, `SELECT execution_id FROM analyses WHERE id = 1`).Scan(&execID); err != nil || execID != 1 {
		t.Fatalf("analyses.execution_id FK broke: err=%v execID=%d", err, execID)
	}
	mustExec(t, d, `INSERT INTO executions (id,project_id,bead_id,bead_revision_id,trace_path,termination_cause,monitor_fired,monitor_honored,started_at,ended_at)
	  VALUES (2,1,1,1,'/tmp/trace-2.log','no_write',0,1,'2026-01-01T00:00:00Z','2026-01-01T00:05:00Z')`)
}
