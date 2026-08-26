package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"ratchet/internal/db"
)

// seedCascadeProject inserts a minimal projects row like seedProject, but
// with a real temp-dir folder_path instead of the shared "/tmp" placeholder
// — enqueueCascadeReview's bead reset does real file I/O (SnapshotBeadFiles,
// WriteScaffoldStubsTx, os.Remove), which must not touch the host's actual
// /tmp. cascadeBaselineProjectID may be 0 (NULL, not a cascade project).
func seedCascadeProject(t *testing.T, d *db.DB, id int64, cascadeBaselineProjectID int64) string {
	t.Helper()
	folder := t.TempDir()
	var baseline sql.NullInt64
	if cascadeBaselineProjectID != 0 {
		baseline = sql.NullInt64{Int64: cascadeBaselineProjectID, Valid: true}
	}
	_, err := d.ExecContext(context.Background(), `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, cascade_baseline_project_id, created_at, updated_at)
		VALUES (?, ?, ?, 'design.md', 'active', 'honor', 300, 2, ?,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, "cascade test project", folder, baseline)
	if err != nil {
		t.Fatalf("seedCascadeProject %d: %v", id, err)
	}
	return folder
}

// seedCascadeBead inserts a bead + revision 1 with an explicit output_files
// list (seedBead in commit_test.go omits it) and status, needed to exercise
// enqueueCascadeReview's diff and file-reset logic meaningfully.
func seedCascadeBead(t *testing.T, d *db.DB, projectID int64, title, fullTextProse string, outputFiles []string, budget int, status string) (beadID int64) {
	t.Helper()
	ctx := context.Background()
	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (?, ?, NULL)`, projectID, status)
	if err != nil {
		t.Fatalf("seedCascadeBead insert: %v", err)
	}
	beadID, _ = res.LastInsertId()

	fullText, _ := json.Marshal(map[string]any{
		"title": title, "full_text": fullTextProse,
		"output_files": outputFiles, "exit_criteria": []string{"true"},
		"execution_budget": budget, "monitor_override": "honor",
	})
	res, err = d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text,
		   execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (?, ?, 1, ?, ?, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		projectID, beadID, string(fullText), budget)
	if err != nil {
		t.Fatalf("seedCascadeBead revision: %v", err)
	}
	revID, _ := res.LastInsertId()

	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
		t.Fatalf("seedCascadeBead set current_revision_id: %v", err)
	}
	return beadID
}

func mustExec(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("mustExec(%q): %v", query, err)
	}
}

func beadStatusOf(t *testing.T, d *db.DB, beadID int64) string {
	t.Helper()
	var status string
	if err := d.QueryRowContext(context.Background(),
		`SELECT status FROM beads WHERE id = ?`, beadID).Scan(&status); err != nil {
		t.Fatalf("query bead status: %v", err)
	}
	return status
}

func TestEnqueueCascadeReview_AllUnchangedMarksProjectComplete(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	seedCascadeProject(t, d, -30, 0)
	seedCascadeBead(t, d, -30, "B1", "prose", nil, 300, "succeeded")

	cascadeFolder := seedCascadeProject(t, d, -31, -30)
	cascadeBeadID := seedCascadeBead(t, d, -31, "B1", "prose", nil, 300, "succeeded")

	var dispatched bool
	var err error
	inTx(t, d, func(tx *sql.Tx) error {
		dispatched, err = enqueueCascadeReview(ctx, tx, -31, -30, "2026-01-02T00:00:00Z")
		return err
	})
	if dispatched {
		t.Errorf("dispatched = true, want false (no bead changed)")
	}
	if status := beadStatusOf(t, d, cascadeBeadID); status != "succeeded" {
		t.Errorf("unchanged bead status = %q, want succeeded (untouched)", status)
	}
	if status := projectStatus(t, d, -31); status != "complete" {
		t.Errorf("project status = %q, want complete", status)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -31`); n != 0 {
		t.Errorf("handoff_jobs count = %d, want 0 (nothing to dispatch)", n)
	}
	// The project-complete report writes its own traces/project-report.md,
	// but the unchanged bead itself must not have been snapshotted — that
	// only happens for a bead resetBeadForRerun actually touches.
	snapshotDir := filepath.Join(cascadeFolder, "traces", fmt.Sprintf("bead-%d-cascade-1", cascadeBeadID))
	if _, statErr := os.Stat(snapshotDir); !os.IsNotExist(statErr) {
		t.Errorf("expected no cascade snapshot dir for the unchanged bead at %s", snapshotDir)
	}
}

func TestEnqueueCascadeReview_ChangedBeadResetUnchangedLeftAlone(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	seedCascadeProject(t, d, -32, 0)
	seedCascadeBead(t, d, -32, "B1", "unchanged prose", nil, 300, "succeeded")
	seedCascadeBead(t, d, -32, "B2", "old prose", []string{"b2_test.go"}, 300, "succeeded")

	cascadeFolder := seedCascadeProject(t, d, -33, -32)
	b1ID := seedCascadeBead(t, d, -33, "B1", "unchanged prose", nil, 300, "succeeded")
	b2ID := seedCascadeBead(t, d, -33, "B2", "new prose — this is what changed", []string{"b2_test.go"}, 300, "succeeded")

	// Simulate stale state a clone would carry forward: leftover budget
	// override and working-state rows that must be cleared on reset.
	mustExec(t, d, `UPDATE beads SET execution_attempts_override = 9 WHERE id = ?`, b2ID)
	mustExec(t, d, `INSERT INTO test_refinements (project_id, bead_id, cycle_id, turn, verb, changed, summary, decision, created_at)
		VALUES (-33, ?, 1, 1, 'REFINE_TESTS_WRITE', 1, 'summary', 'approved', '2026-01-01T00:00:00Z')`, b2ID)
	mustExec(t, d, `INSERT INTO compressed_history (bead_id, project_id, compressed_text, updated_at)
		VALUES (?, -33, 'history', '2026-01-01T00:00:00Z')`, b2ID)
	// A leftover escalated job from the baseline's clone — must be cancelled.
	mustExec(t, d, `INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (-33, 'EXECUTE_BEAD', ?, 'escalated', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, b2ID)

	b2TestFile := filepath.Join(cascadeFolder, "b2_test.go")
	if err := os.WriteFile(b2TestFile, []byte("package p\n"), 0o644); err != nil {
		t.Fatalf("write b2 test file: %v", err)
	}

	var dispatched bool
	var err error
	inTx(t, d, func(tx *sql.Tx) error {
		dispatched, err = enqueueCascadeReview(ctx, tx, -33, -32, "2026-01-02T00:00:00Z")
		return err
	})
	if !dispatched {
		t.Fatalf("dispatched = false, want true (B2 changed)")
	}

	if status := beadStatusOf(t, d, b1ID); status != "succeeded" {
		t.Errorf("unchanged B1 status = %q, want succeeded (untouched)", status)
	}
	if status := beadStatusOf(t, d, b2ID); status != "pending" {
		t.Errorf("changed B2 status = %q, want pending", status)
	}

	var override sql.NullInt64
	if err := d.QueryRowContext(ctx, `SELECT execution_attempts_override FROM beads WHERE id = ?`, b2ID).Scan(&override); err != nil {
		t.Fatalf("query override: %v", err)
	}
	if override.Valid {
		t.Errorf("execution_attempts_override = %v, want NULL (fresh budget)", override)
	}

	if n := countRows(t, d, `SELECT COUNT(*) FROM test_refinements WHERE bead_id = ?`, b2ID); n != 0 {
		t.Errorf("test_refinements for B2 = %d, want 0 (cleared)", n)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM compressed_history WHERE bead_id = ?`, b2ID); n != 0 {
		t.Errorf("compressed_history for B2 = %d, want 0 (cleared)", n)
	}

	if _, statErr := os.Stat(b2TestFile); !os.IsNotExist(statErr) {
		t.Errorf("expected b2_test.go to be deleted")
	}

	var escalatedStatus string
	if err := d.QueryRowContext(ctx,
		`SELECT status FROM handoff_jobs WHERE project_id = -33 AND verb = 'EXECUTE_BEAD' AND bead_id = ?`, b2ID,
	).Scan(&escalatedStatus); err != nil {
		t.Fatalf("query leftover job: %v", err)
	}
	if escalatedStatus != "complete" {
		t.Errorf("leftover escalated job status = %q, want complete (cancelled)", escalatedStatus)
	}

	// The lowest-id changed bead (B2, the only changed one here) must be
	// dispatched — REFINE_TESTS_WRITE since its output_files has a _test.go.
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -33 AND verb = 'REFINE_TESTS_WRITE' AND bead_id = ? AND status = 'pending'`, b2ID); n != 1 {
		t.Errorf("REFINE_TESTS_WRITE jobs for B2 = %d, want 1", n)
	}

	snapshotDir := filepath.Join(cascadeFolder, "traces", fmt.Sprintf("bead-%d-cascade-1", b2ID))
	if _, statErr := os.Stat(filepath.Join(snapshotDir, "b2_test.go")); statErr != nil {
		t.Errorf("expected pre-reset snapshot of b2_test.go at %s: %v", snapshotDir, statErr)
	}
	if _, statErr := os.Stat(filepath.Join(snapshotDir, "README.md")); statErr != nil {
		t.Errorf("expected cascade reset manifest README.md at %s: %v", snapshotDir, statErr)
	}
}

// TestAuditDecompositionCommitNoIssuesRoutesCascadeProjectsToCascadeReview
// verifies enqueueDecompositionApproved (called from
// AuditDecomposition.Commit's no_issues branch) checks
// cascade_baseline_project_id and takes the cascade path instead of
// enqueueFirstBeadForExecution's "always dispatch bead 1" behavior, which
// would be wrong here — bead 1 already succeeded under the clone and is
// unchanged.
func TestAuditDecompositionCommitNoIssuesRoutesCascadeProjectsToCascadeReview(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	seedCascadeProject(t, d, -34, 0)
	seedCascadeBead(t, d, -34, "B1", "same prose", nil, 300, "succeeded")

	seedCascadeProject(t, d, -35, -34)
	b1ID := seedCascadeBead(t, d, -35, "B1", "same prose", nil, 300, "succeeded")

	job := seedJob(t, d, -35, db.VerbAuditDecomposition, sql.NullInt64{})
	out := AuditDecompositionOutput{OverallVerdict: "no_issues"}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AuditDecomposition{}).Commit(ctx, tx, job, out)
	})

	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -35 AND verb = ?`, db.VerbExecuteBead); n != 0 {
		t.Errorf("EXECUTE_BEAD jobs = %d, want 0 — B1 is unchanged and already succeeded, must not be re-dispatched", n)
	}
	if status := beadStatusOf(t, d, b1ID); status != "succeeded" {
		t.Errorf("B1 status = %q, want succeeded (untouched)", status)
	}
	if status := projectStatus(t, d, -35); status != "complete" {
		t.Errorf("project status = %q, want complete (no bead changed)", status)
	}
}
