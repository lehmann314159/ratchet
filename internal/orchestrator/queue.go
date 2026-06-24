package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"ratchet/internal/db"
)

// verbTolerance returns the malformed-output strike limit for a verb.
// ADJUDICATE_NEXT_EXECUTION has zero tolerance (escalate immediately).
// AUDIT_DECOMPOSITION and RECONCILE_DECOMPOSITION share the round-cap
// counter rather than this mechanism (see queue handling in dispatch.go).
func verbTolerance(verb string) int {
	switch verb {
	case db.VerbAdjudicateNextExecution:
		return 0
	default:
		return 2
	}
}

// activeProject returns the single active project, or an error if none exists.
func activeProject(ctx context.Context, d *db.DB) (*db.Project, error) {
	row := d.QueryRowContext(ctx, `
		SELECT id, label, folder_path, design_doc_path, status,
		       recovered_from_project_id,
		       monitor_override_default, execution_budget_default,
		       audit_reconcile_round_cap, created_at, updated_at
		FROM projects WHERE status = 'active'
		ORDER BY id LIMIT 1`)
	p := &db.Project{}
	var createdAt, updatedAt string
	if err := row.Scan(
		&p.ID, &p.Label, &p.FolderPath, &p.DesignDocPath, &p.Status,
		&p.RecoveredFromProjectID,
		&p.MonitorOverrideDefault, &p.ExecutionBudgetDefault,
		&p.AuditReconcileRoundCap, &createdAt, &updatedAt,
	); err != nil {
		return nil, fmt.Errorf("active project: %w", err)
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return p, nil
}

// nextPendingJob returns the oldest pending job for projectID, or nil if none.
func nextPendingJob(ctx context.Context, d *db.DB, projectID int64) (*db.HandoffJob, error) {
	row := d.QueryRowContext(ctx, `
		SELECT id, project_id, verb, bead_id, status, created_at, updated_at
		FROM handoff_jobs
		WHERE project_id = ? AND status = 'pending'
		ORDER BY created_at ASC
		LIMIT 1`, projectID)

	j := &db.HandoffJob{}
	var createdAt, updatedAt string
	if err := row.Scan(
		&j.ID, &j.ProjectID, &j.Verb, &j.BeadID, &j.Status, &createdAt, &updatedAt,
	); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("next pending job: %w", err)
	}
	j.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	j.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return j, nil
}

// markJobRunning sets a job to 'running'. Called before the model HTTP call.
func markJobRunning(ctx context.Context, d *db.DB, jobID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'running', updated_at = ? WHERE id = ?`,
		now, jobID)
	return err
}

// resetStaleRunning resets jobs stuck in 'running' (from a crashed orchestrator)
// to 'failed_retry' so they are retried. Called on orchestrator startup.
func resetStaleRunning(ctx context.Context, d *db.DB) error {
	// Any job still 'running' after restart is stale — the process that started
	// it is gone. Reset to 'failed_retry' so the strike count increments.
	_, err := d.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'failed_retry', updated_at = ?
		 WHERE status = 'running'`,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("reset stale running jobs: %w", err)
	}
	return nil
}

// strikeCount returns the number of invalid (malformed) attempts for a job.
func strikeCount(ctx context.Context, d *db.DB, jobID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM handoff_attempts
		 WHERE job_id = ? AND validation_result != 'valid'`,
		jobID,
	).Scan(&n)
	return n, err
}

// nextAttemptNumber returns the next attempt_number for a job.
func nextAttemptNumber(ctx context.Context, d *db.DB, jobID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(attempt_number), 0) + 1 FROM handoff_attempts WHERE job_id = ?`,
		jobID,
	).Scan(&n)
	return n, err
}

// commitAttempt writes a handoff_attempts row and updates the job status,
// all within tx.
func commitAttempt(ctx context.Context, tx *sql.Tx, jobID int64, attemptNum int, rawOutput, validationResult, nextStatus string) error {
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		jobID, attemptNum, rawOutput, validationResult, now,
	); err != nil {
		return fmt.Errorf("insert handoff_attempt: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = ?, updated_at = ? WHERE id = ?`,
		nextStatus, now, jobID,
	); err != nil {
		return fmt.Errorf("update job status to %s: %w", nextStatus, err)
	}
	return nil
}

// escalate marks a job as escalated and logs a structured alert to stderr.
// The alert is the orchestrator's human notification mechanism for now;
// a UI hook can consume these log lines later.
func escalate(ctx context.Context, d *db.DB, job *db.HandoffJob, reason string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`,
		now, job.ID)
	if err != nil {
		return err
	}
	slog.Error("ESCALATION — requires human review",
		"project_id", job.ProjectID,
		"job_id", job.ID,
		"verb", job.Verb,
		"bead_id", job.BeadID,
		"reason", reason,
	)
	return nil
}
