package project

import (
	"context"
	"testing"

	"ratchet/internal/db"
)

func seedResumeProject(t *testing.T, d *db.DB, id int64, status string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   max_execution_attempts, created_at, updated_at)
		VALUES (?, 'resume-test', '/tmp/x', 'design.md', ?, 'honor', 300, 5,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, status); err != nil {
		t.Fatalf("seed project: %v", err)
	}
}

// TestResumeProject_PureStatusFlip confirms that resuming a paused project is
// nothing more than a status flip — the next job (already enqueued by
// whichever pause point stopped the project, per the enqueue-then-gate
// pattern) is left untouched and reported back for the CLI printout, not
// reconstructed.
func TestResumeProject_PureStatusFlip(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedResumeProject(t, d, 1, "paused")
	if _, err := d.ExecContext(ctx,
		`INSERT INTO beads (id, project_id, status) VALUES (7, 1, 'pending')`); err != nil {
		t.Fatalf("seed bead: %v", err)
	}

	if _, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (1, 'EXECUTE_BEAD', 7, 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed pending job: %v", err)
	}

	label, nextVerb, nextBeadID, err := resumeProject(ctx, d, 1)
	if err != nil {
		t.Fatalf("resumeProject: %v", err)
	}
	if label != "resume-test" {
		t.Errorf("label = %q, want resume-test", label)
	}
	if nextVerb != "EXECUTE_BEAD" {
		t.Errorf("nextVerb = %q, want EXECUTE_BEAD", nextVerb)
	}
	if !nextBeadID.Valid || nextBeadID.Int64 != 7 {
		t.Errorf("nextBeadID = %+v, want valid 7", nextBeadID)
	}

	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = 1`).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q, want active", status)
	}

	// The job itself must be untouched — no reconstruction, no re-enqueue.
	var jobCount int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM handoff_jobs WHERE project_id = 1`,
	).Scan(&jobCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobCount != 1 {
		t.Errorf("handoff_jobs count = %d, want 1 (resume must not enqueue a second job)", jobCount)
	}
}

// TestResumeProject_VerbLevelPauseNoBeadID exercises resuming from a
// project-scoped pause (e.g. pause_after_verb=CERTIFY_MANIFEST), where the
// pending job has no bead_id.
func TestResumeProject_VerbLevelPauseNoBeadID(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedResumeProject(t, d, 1, "paused")

	if _, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (1, 'DECOMPOSE_SPEC', NULL, 'pending', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed pending job: %v", err)
	}

	_, nextVerb, nextBeadID, err := resumeProject(ctx, d, 1)
	if err != nil {
		t.Fatalf("resumeProject: %v", err)
	}
	if nextVerb != "DECOMPOSE_SPEC" {
		t.Errorf("nextVerb = %q, want DECOMPOSE_SPEC", nextVerb)
	}
	if nextBeadID.Valid {
		t.Errorf("nextBeadID = %+v, want NULL", nextBeadID)
	}
}

// TestResumeProject_RejectsNonPausedProject confirms resume-project refuses to
// act on a project that isn't actually paused.
func TestResumeProject_RejectsNonPausedProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedResumeProject(t, d, 1, "active")

	if _, _, _, err := resumeProject(ctx, d, 1); err == nil {
		t.Error("expected error resuming an active (non-paused) project, got nil")
	}

	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = 1`).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q, want active (unchanged)", status)
	}
}

// TestResumeProject_NotFound confirms a clear error for an unknown project ID.
func TestResumeProject_NotFound(t *testing.T) {
	d := openTestDB(t)
	if _, _, _, err := resumeProject(context.Background(), d, 999); err == nil {
		t.Error("expected error for unknown project ID, got nil")
	}
}
