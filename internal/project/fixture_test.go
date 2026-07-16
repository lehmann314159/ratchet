package project

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"ratchet/internal/db"
)

func seedFixtureBaseProject(t *testing.T, d *db.DB, id int64, status string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   max_execution_attempts, created_at, updated_at)
		VALUES (?, 'fixture-source', '/tmp/x', 'design.md', ?, 'honor', 300, 5,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, status); err != nil {
		t.Fatalf("seed project: %v", err)
	}
}

// seedFixtureFullProject seeds one row in every table in fixtureScopedTables
// (plus a second project whose recovered_from_project_id points at it), so a
// renumber test can confirm every table actually moves — not just the ones
// easy to remember.
func seedFixtureFullProject(t *testing.T, d *db.DB, projectID int64) (beadID, execID, jobID int64) {
	t.Helper()
	ctx := context.Background()
	seedFixtureBaseProject(t, d, projectID, "active")

	mustExecFixture(t, d, `INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (?, 'DECOMPOSE_SPEC', 'm1')`, projectID)

	res, err := d.ExecContext(ctx, `INSERT INTO beads (project_id, status, current_revision_id) VALUES (?, 'pending', NULL)`, projectID)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	beadID, _ = res.LastInsertId()

	revRes, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (?, ?, 1, '{"title":"B01"}', 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		projectID, beadID)
	if err != nil {
		t.Fatalf("seed bead_revisions: %v", err)
	}
	revID, _ := revRes.LastInsertId()
	mustExecFixture(t, d, `UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID)

	mustExecFixture(t, d, `
		INSERT INTO audit_reconcile_rounds (project_id, round_number, critique_text, reconciliation, outcome, created_at)
		VALUES (?, 1, 'critique', 'reconciliation', 'converged', '2026-01-01T00:00:00Z')`, projectID)

	execRes, err := d.ExecContext(ctx, `
		INSERT INTO executions (project_id, bead_id, bead_revision_id, trace_path, termination_cause, started_at, ended_at)
		VALUES (?, ?, ?, '/tmp/trace.log', 'success', '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z')`,
		projectID, beadID, revID)
	if err != nil {
		t.Fatalf("seed executions: %v", err)
	}
	execID, _ = execRes.LastInsertId()

	mustExecFixture(t, d, `
		INSERT INTO analyses (project_id, execution_id, mechanical_findings, created_at)
		VALUES (?, ?, 'findings', '2026-01-01T00:00:00Z')`, projectID, execID)

	mustExecFixture(t, d, `
		INSERT INTO compressed_history (bead_id, project_id, compressed_text, updated_at)
		VALUES (?, ?, 'history', '2026-01-01T00:00:00Z')`, beadID, projectID)

	mustExecFixture(t, d, `
		INSERT INTO adjudications
		  (project_id, bead_id, execution_id, trend, bead_spec_fit, reasoning_text,
		   attempt_budget_cost, monitor_escalation_status, decision, created_at)
		VALUES (?, ?, ?, 'same', 'not_applicable', 'reasoning', 1.0, 0, 'execute_as_is', '2026-01-01T00:00:00Z')`,
		projectID, beadID, execID)

	res, err = d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, 'EXECUTE_BEAD', ?, 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		projectID, beadID)
	if err != nil {
		t.Fatalf("seed handoff_jobs: %v", err)
	}
	jobID, _ = res.LastInsertId()

	mustExecFixture(t, d, `
		INSERT INTO verify_attempts
		  (project_id, job_id, attempt_number, file_presence_pass, no_behavioral_tests_pass,
		   compile_pass, api_check_pass, stub_purity_pass, created_at)
		VALUES (?, ?, 1, 1, 1, 1, 1, 1, '2026-01-01T00:00:00Z')`, projectID, jobID)

	var verifyAttemptID int64
	if err := d.QueryRowContext(ctx, `SELECT id FROM verify_attempts WHERE project_id = ?`, projectID).Scan(&verifyAttemptID); err != nil {
		t.Fatalf("read verify_attempt id: %v", err)
	}
	mustExecFixture(t, d, `
		INSERT INTO certifications (project_id, verify_attempt_id, preliminary_decision, final_decision, created_at)
		VALUES (?, ?, 'approve', 'approve', '2026-01-01T00:00:00Z')`, projectID, verifyAttemptID)

	mustExecFixture(t, d, `
		INSERT INTO test_refinements (project_id, bead_id, cycle_id, turn, verb, changed, created_at)
		VALUES (?, ?, 1, 1, 'REFINE_TESTS_WRITE', 1, '2026-01-01T00:00:00Z')`, projectID, beadID)

	mustExecFixture(t, d, `
		INSERT INTO spec_revisions (project_id, trigger_bead_id, revised_bead_id, old_revision_id, new_revision_id, created_at)
		VALUES (?, ?, ?, ?, ?, '2026-01-01T00:00:00Z')`, projectID, beadID, beadID, revID, revID)

	return beadID, execID, jobID
}

func mustExecFixture(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func countProjectScoped(t *testing.T, d *db.DB, table string, projectID int64) int {
	t.Helper()
	var n int
	if err := d.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM "+table+" WHERE project_id = ?", projectID,
	).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestSaveFixture_RenumbersEveryScopedTable is the comprehensive regression
// test: seeds one row in every table fixtureScopedTables lists, then confirms
// every single one actually moved to the new negative project_id and none are
// left orphaned under the old positive ID.
func TestSaveFixture_RenumbersEveryScopedTable(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	beadID, execID, jobID := seedFixtureFullProject(t, d, 98)
	_, _, _ = beadID, execID, jobID

	newID, label, err := saveFixture(ctx, d, 98, "checkers post-RECONCILE")
	if err != nil {
		t.Fatalf("saveFixture: %v", err)
	}
	if newID != -98 {
		t.Errorf("newID = %d, want -98", newID)
	}
	if label != "fixture: checkers post-RECONCILE" {
		t.Errorf("label = %q, want %q", label, "fixture: checkers post-RECONCILE")
	}

	var status, gotLabel string
	if err := d.QueryRowContext(ctx, `SELECT status, label FROM projects WHERE id = -98`).Scan(&status, &gotLabel); err != nil {
		t.Fatalf("query renumbered project: %v", err)
	}
	if status != "fixture" {
		t.Errorf("status = %q, want fixture", status)
	}
	if gotLabel != "fixture: checkers post-RECONCILE" {
		t.Errorf("label = %q, want %q", gotLabel, "fixture: checkers post-RECONCILE")
	}

	var origExists int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = 98`).Scan(&origExists); err != nil {
		t.Fatalf("query original project: %v", err)
	}
	if origExists != 0 {
		t.Errorf("original project row (id=98) still present after renumber")
	}

	for _, table := range fixtureScopedTables {
		if n := countProjectScoped(t, d, table, -98); n != 1 {
			t.Errorf("%s: rows under new id -98 = %d, want 1", table, n)
		}
		if n := countProjectScoped(t, d, table, 98); n != 0 {
			t.Errorf("%s: rows still under old id 98 = %d, want 0 (orphaned)", table, n)
		}
	}

	// beads.current_revision_id, executions.bead_id/bead_revision_id — row IDs
	// themselves must be untouched by the renumber (only project_id changes).
	var currentRevID sql.NullInt64
	if err := d.QueryRowContext(ctx, `SELECT current_revision_id FROM beads WHERE id = ?`, beadID).Scan(&currentRevID); err != nil {
		t.Fatalf("query bead: %v", err)
	}
	if !currentRevID.Valid {
		t.Errorf("bead's current_revision_id lost during renumber")
	}
}

// TestSaveFixture_RepointsRecoveredFromProjectID confirms another project's
// restart-lineage backreference is repointed at the new negative ID, not left
// dangling at the now-nonexistent positive one.
func TestSaveFixture_RepointsRecoveredFromProjectID(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedFixtureBaseProject(t, d, 98, "active")
	if _, err := d.ExecContext(ctx, `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status, recovered_from_project_id,
		   monitor_override_default, execution_budget_default, max_execution_attempts,
		   created_at, updated_at)
		VALUES (99, 'restarted-from-98', '/tmp/y', 'design.md', 'active', 98, 'honor', 300, 5,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed restarted project: %v", err)
	}

	newID, _, err := saveFixture(ctx, d, 98, "")
	if err != nil {
		t.Fatalf("saveFixture: %v", err)
	}

	var recov int64
	if err := d.QueryRowContext(ctx, `SELECT recovered_from_project_id FROM projects WHERE id = 99`).Scan(&recov); err != nil {
		t.Fatalf("query recovered_from_project_id: %v", err)
	}
	if recov != newID {
		t.Errorf("recovered_from_project_id = %d, want %d (repointed to the fixture)", recov, newID)
	}
}

// TestSaveFixture_DefaultLabelUsesOriginal confirms an empty --label falls
// back to the project's own label rather than producing "fixture: ".
func TestSaveFixture_DefaultLabelUsesOriginal(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedFixtureBaseProject(t, d, 98, "active")

	_, label, err := saveFixture(ctx, d, 98, "")
	if err != nil {
		t.Fatalf("saveFixture: %v", err)
	}
	if label != "fixture: fixture-source" {
		t.Errorf("label = %q, want %q", label, "fixture: fixture-source")
	}
}

func TestSaveFixture_RejectsNonPositiveProjectID(t *testing.T) {
	d := openTestDB(t)
	if _, _, err := saveFixture(context.Background(), d, -1, ""); err == nil {
		t.Error("expected error for a non-positive project ID, got nil")
	}
	if _, _, err := saveFixture(context.Background(), d, 0, ""); err == nil {
		t.Error("expected error for project ID 0, got nil")
	}
}

func TestSaveFixture_NotFound(t *testing.T) {
	d := openTestDB(t)
	if _, _, err := saveFixture(context.Background(), d, 999, ""); err == nil {
		t.Error("expected error for unknown project ID, got nil")
	}
}

// TestSaveFixture_RejectsIDReuseCollision reproduces the live scenario found
// 2026-07-16 (checkers-v9 -> -99, then chess-v4 reused freed id 99): saving a
// second project as a fixture when -projectID is already taken by an earlier
// fixture must fail with a clear, actionable error instead of a raw SQLite
// UNIQUE constraint violation, and must leave both the earlier fixture and
// the second project completely untouched.
func TestSaveFixture_RejectsIDReuseCollision(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	seedFixtureBaseProject(t, d, 99, "active")
	firstFixtureID, firstLabel, err := saveFixture(ctx, d, 99, "first fixture")
	if err != nil {
		t.Fatalf("first saveFixture: %v", err)
	}
	if firstFixtureID != -99 {
		t.Fatalf("firstFixtureID = %d, want -99", firstFixtureID)
	}

	// Reuse the freed id 99, exactly as SQLite's own INTEGER PRIMARY KEY
	// (no AUTOINCREMENT) does in practice once the lowest id is freed.
	seedFixtureBaseProject(t, d, 99, "active")

	_, _, err = saveFixture(ctx, d, 99, "second fixture")
	if err == nil {
		t.Fatal("expected error for a fixture id collision, got nil")
	}
	if !strings.Contains(err.Error(), "already taken") {
		t.Errorf("error = %q, want a clear message mentioning the id is already taken", err.Error())
	}

	// The earlier fixture must be untouched.
	var status, label string
	if err := d.QueryRowContext(ctx, `SELECT status, label FROM projects WHERE id = -99`).Scan(&status, &label); err != nil {
		t.Fatalf("query first fixture: %v", err)
	}
	if status != "fixture" || label != firstLabel {
		t.Errorf("first fixture disturbed: status=%q label=%q, want status=fixture label=%q", status, label, firstLabel)
	}

	// The second project must be untouched too (still active, still id 99).
	if err := d.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = 99`).Scan(&status); err != nil {
		t.Fatalf("query second project: %v", err)
	}
	if status != "active" {
		t.Errorf("second project status = %q, want active (untouched)", status)
	}
}

// TestSaveFixture_RefusesWithRunningJob confirms the precondition guard and
// that it leaves the project completely untouched on failure.
func TestSaveFixture_RefusesWithRunningJob(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedFixtureBaseProject(t, d, 98, "active")
	mustExecFixture(t, d, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (98, 'EXECUTE_BEAD', NULL, 'running', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)

	if _, _, err := saveFixture(ctx, d, 98, ""); err == nil {
		t.Fatal("expected error for a project with a running job, got nil")
	}

	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = 98`).Scan(&status); err != nil {
		t.Fatalf("project row missing after failed saveFixture: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q, want active (untouched)", status)
	}
}

// TestSaveFixture_AllowsPendingAndEscalatedJobs confirms the guard is scoped
// to 'running' specifically — a paused project's inert pending job (per the
// enqueue-then-gate pause pattern) or an escalated job must not block saving
// a fixture.
func TestSaveFixture_AllowsPendingAndEscalatedJobs(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedFixtureBaseProject(t, d, 98, "paused")
	mustExecFixture(t, d, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (98, 'EXECUTE_BEAD', NULL, 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	mustExecFixture(t, d, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (98, 'ADJUDICATE_NEXT_EXECUTION', NULL, 'escalated', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)

	if _, _, err := saveFixture(ctx, d, 98, ""); err != nil {
		t.Fatalf("saveFixture: %v", err)
	}
}
