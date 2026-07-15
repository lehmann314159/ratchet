package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"ratchet/internal/db"
)

var errAssertCommitFailure = errors.New("bead lookup: sql: no rows in result set")

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedRunningJob inserts a minimal project + handoff_jobs row in 'running'
// status, as claimNextJob would have left it mid-dispatch.
func seedRunningJob(t *testing.T, d *db.DB) *db.HandoffJob {
	t.Helper()
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (1, 'p', '/tmp', 'design.md', 'active', 'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (1, ?, NULL, 'running', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		db.VerbReconcileDecomposition)
	if err != nil {
		t.Fatalf("seed handoff_jobs: %v", err)
	}
	id, _ := res.LastInsertId()
	return &db.HandoffJob{ID: id, ProjectID: 1, Verb: db.VerbReconcileDecomposition}
}

// TestRecordCommitFailure_UnderTolerance reproduces the scenario the Stage 2
// audit found: handler.Commit fails (e.g. RECONCILE's applyFixes hitting a
// bead lookup that doesn't exist) after the surrounding transaction already
// rolled back. Before this fix, the job was simply left in 'running' forever
// — claimNextJob only claims 'pending'/'failed_retry' jobs, so nothing would
// ever dispatch it again short of a full daemon restart. Verifies the job is
// now moved to 'failed_retry' (retryable) when under the strike tolerance.
func TestRecordCommitFailure_UnderTolerance(t *testing.T) {
	d := openTestDB(t)
	job := seedRunningJob(t, d)
	ctx := context.Background()

	err := recordCommitFailure(ctx, d, job, 1, "raw output", time.Now(), 0, 2, errAssertCommitFailure)
	if err != nil {
		t.Fatalf("recordCommitFailure: %v", err)
	}

	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	if status != "failed_retry" {
		t.Errorf("expected status 'failed_retry' (job must remain retryable), got %q", status)
	}

	var validationResult string
	if err := d.QueryRowContext(ctx,
		`SELECT validation_result FROM handoff_attempts WHERE job_id = ?`, job.ID,
	).Scan(&validationResult); err != nil {
		t.Fatalf("query attempt: %v", err)
	}
	if validationResult == "valid" {
		t.Errorf("a Commit failure must not be recorded as a valid attempt")
	}
}

// TestRecordCommitFailure_EscalatesAtTolerance verifies that once strikes
// reach tolerance, a Commit failure escalates the job (visible to an
// operator) instead of retrying forever.
func TestRecordCommitFailure_EscalatesAtTolerance(t *testing.T) {
	d := openTestDB(t)
	job := seedRunningJob(t, d)
	ctx := context.Background()

	err := recordCommitFailure(ctx, d, job, 1, "raw output", time.Now(), 2, 2, errAssertCommitFailure)
	if err != nil {
		t.Fatalf("recordCommitFailure: %v", err)
	}

	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	if status != "escalated" {
		t.Errorf("expected status 'escalated' at tolerance, got %q", status)
	}
}
