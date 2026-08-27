package project

import (
	"context"
	"testing"

	"ratchet/internal/db"
)

// seedReanalyzeBead inserts a bead + revision 1 + one completed execution
// for it, mirroring what a real EXECUTE_BEAD success leaves behind (enough
// for ANALYZE_EXECUTION.Run's own "most recent completed execution" query
// to find it — see analyze_execution.go).
func seedReanalyzeBead(t *testing.T, d *db.DB, projectID int64, status string) (beadID int64) {
	t.Helper()
	ctx := context.Background()
	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (?, ?, NULL)`, projectID, status)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	beadID, _ = res.LastInsertId()

	revRes, err := d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (?, ?, 1, '{"title":"B"}', 300, 'honor', 'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		projectID, beadID)
	if err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	revID, _ := revRes.LastInsertId()
	if _, err := d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
		t.Fatalf("set current_revision_id: %v", err)
	}

	if _, err := d.ExecContext(ctx, `
		INSERT INTO executions
		  (project_id, bead_id, bead_revision_id, trace_path, termination_cause,
		   monitor_fired, monitor_honored, started_at, ended_at)
		VALUES (?, ?, ?, '/tmp/trace.log', 'success', 0, 0, '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z')`,
		projectID, beadID, revID); err != nil {
		t.Fatalf("seed execution: %v", err)
	}

	return beadID
}

func TestReanalyzeBead_EnqueuesFreshAnalysisAndCancelsStuckJob(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	folder := t.TempDir()
	seedRewindProject(t, d, 1, folder)
	beadID := seedReanalyzeBead(t, d, 1, "executing")

	// A stuck escalated ADJUDICATE_NEXT_EXECUTION job from the corrupted
	// analysis, plus a stale compressed-history summary built from it.
	if _, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (1, ?, ?, 'escalated', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		db.VerbAdjudicateNextExecution, beadID); err != nil {
		t.Fatalf("seed escalated job: %v", err)
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO compressed_history (bead_id, project_id, compressed_text, updated_at)
		VALUES (?, 1, 'stale summary built from the corrupted analysis', '2026-01-01T00:00:00Z')`,
		beadID); err != nil {
		t.Fatalf("seed compressed_history: %v", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE projects SET status = 'full_stopped' WHERE id = 1`); err != nil {
		t.Fatalf("full-stop project: %v", err)
	}

	result, err := reanalyzeBead(ctx, d, beadID)
	if err != nil {
		t.Fatalf("reanalyzeBead: %v", err)
	}
	if result.ProjectID != 1 {
		t.Errorf("ProjectID = %d, want 1", result.ProjectID)
	}
	if result.JobsCancelled != 1 {
		t.Errorf("JobsCancelled = %d, want 1", result.JobsCancelled)
	}

	var jobCount int
	var verb, status string
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*), verb, status FROM handoff_jobs WHERE bead_id = ? AND status = 'pending'`, beadID,
	).Scan(&jobCount, &verb, &status); err != nil {
		t.Fatalf("query pending jobs: %v", err)
	}
	if jobCount != 1 || verb != db.VerbAnalyzeExecution || status != "pending" {
		t.Errorf("pending job = (%d, %s, %s), want (1, ANALYZE_EXECUTION, pending)", jobCount, verb, status)
	}

	var escalatedStatus string
	if err := d.QueryRowContext(ctx,
		`SELECT status FROM handoff_jobs WHERE bead_id = ? AND verb = ?`, beadID, db.VerbAdjudicateNextExecution,
	).Scan(&escalatedStatus); err != nil {
		t.Fatalf("query escalated job: %v", err)
	}
	if escalatedStatus != "complete" {
		t.Errorf("escalated job status = %q, want complete (cancelled)", escalatedStatus)
	}

	var historyCount int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM compressed_history WHERE bead_id = ?`, beadID,
	).Scan(&historyCount); err != nil {
		t.Fatalf("query compressed_history: %v", err)
	}
	if historyCount != 0 {
		t.Errorf("compressed_history count = %d, want 0 (cleared)", historyCount)
	}

	var projectStatus string
	if err := d.QueryRowContext(ctx,
		`SELECT status FROM projects WHERE id = 1`,
	).Scan(&projectStatus); err != nil {
		t.Fatalf("query project status: %v", err)
	}
	if projectStatus != "active" {
		t.Errorf("project status = %q, want active (reactivated)", projectStatus)
	}

	// Bead status itself is untouched by reanalysis — no reset to 'pending'
	// the way a rewind would; ANALYZE_EXECUTION.Commit is what eventually
	// moves it forward via the normal chain.
	var beadStatus string
	if err := d.QueryRowContext(ctx,
		`SELECT status FROM beads WHERE id = ?`, beadID,
	).Scan(&beadStatus); err != nil {
		t.Fatalf("query bead status: %v", err)
	}
	if beadStatus != "executing" {
		t.Errorf("bead status = %q, want executing (untouched)", beadStatus)
	}
}

func TestReanalyzeBead_RefusesAlreadySucceededBead(t *testing.T) {
	d := openTestDB(t)
	folder := t.TempDir()
	seedRewindProject(t, d, 2, folder)
	beadID := seedReanalyzeBead(t, d, 2, "succeeded")

	if _, err := reanalyzeBead(context.Background(), d, beadID); err == nil {
		t.Fatal("expected an error for an already-succeeded bead")
	}
}

func TestReanalyzeBead_RefusesBeadWithNoCompletedExecution(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	folder := t.TempDir()
	seedRewindProject(t, d, 3, folder)

	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (3, 'pending', NULL)`)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	beadID, _ := res.LastInsertId()

	if _, err := reanalyzeBead(ctx, d, beadID); err == nil {
		t.Fatal("expected an error for a bead with no completed execution")
	}
}

func TestReanalyzeBead_UnknownBeadErrors(t *testing.T) {
	d := openTestDB(t)
	if _, err := reanalyzeBead(context.Background(), d, 999999); err == nil {
		t.Fatal("expected an error for an unknown bead id")
	}
}
