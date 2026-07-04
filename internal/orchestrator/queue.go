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
// All verbs share the same tolerance — 2 bad outputs before escalating —
// so that a single malformed model response auto-retries rather than
// requiring human intervention.
// AUDIT_DECOMPOSITION and RECONCILE_DECOMPOSITION share the round-cap
// counter rather than this mechanism (see queue handling in dispatch.go).
func verbTolerance(verb string) int {
	return 2
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

// nextPendingJob returns the oldest actionable job for projectID, or nil if
// none. Both 'pending' (never attempted) and 'failed_retry' (failed validation
// but within strike tolerance) are dispatched — 'failed_retry' jobs accumulate
// strikes until they exceed verbTolerance and escalate.
// claimNextJob atomically finds and marks the oldest pending/failed_retry job
// as 'running'. The UPDATE subquery executes under SQLite's write lock, so two
// concurrent processes cannot claim the same job: whichever process wins the
// lock claims the row; the other finds no matching pending row and gets nil.
func claimNextJob(ctx context.Context, d *db.DB, projectID int64) (*db.HandoffJob, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	row := d.QueryRowContext(ctx, `
		UPDATE handoff_jobs
		SET status = 'running', updated_at = ?
		WHERE id = (
			SELECT id FROM handoff_jobs
			WHERE project_id = ? AND status IN ('pending', 'failed_retry')
			ORDER BY created_at ASC
			LIMIT 1
		)
		RETURNING id, project_id, verb, bead_id, status, created_at, updated_at`,
		now, projectID)

	j := &db.HandoffJob{}
	var createdAt, updatedAt string
	if err := row.Scan(
		&j.ID, &j.ProjectID, &j.Verb, &j.BeadID, &j.Status, &createdAt, &updatedAt,
	); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("claim next job: %w", err)
	}
	j.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	j.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return j, nil
}

// recoverOrphanedExecutions handles executions left open by a previous
// orchestrator crash (termination_cause IS NULL AND ended_at IS NULL).
// Marks them as infrastructure failures so they don't count against the
// model's attempt budget, resets their beads to pending, and resets the
// corresponding EXECUTE_BEAD jobs to pending (not failed_retry; the model
// was never at fault). Must be called before resetStaleRunning.
func recoverOrphanedExecutions(ctx context.Context, d *db.DB) error {
	rows, err := d.QueryContext(ctx,
		`SELECT id, bead_id FROM executions
		 WHERE termination_cause IS NULL AND ended_at IS NULL`)
	if err != nil {
		return fmt.Errorf("query orphaned executions: %w", err)
	}
	defer rows.Close()

	type orphan struct{ execID, beadID int64 }
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.execID, &o.beadID); err != nil {
			return err
		}
		orphans = append(orphans, o)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, o := range orphans {
		slog.Warn("startup: recovering orphaned execution (orchestrator was restarted mid-run)",
			"execution_id", o.execID, "bead_id", o.beadID)

		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin recovery tx: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE executions
			SET infra_failure = 1, termination_cause = 'success', ended_at = ?
			WHERE id = ?`, now, o.execID,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("mark orphaned execution %d: %w", o.execID, err)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET status = 'pending' WHERE id = ? AND status = 'executing'`,
			o.beadID,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("reset bead %d: %w", o.beadID, err)
		}

		// Reset to pending, not failed_retry — model was not at fault.
		if _, err := tx.ExecContext(ctx, `
			UPDATE handoff_jobs SET status = 'pending', updated_at = ?
			WHERE bead_id = ? AND verb = ? AND status = 'running'`,
			now, o.beadID, db.VerbExecuteBead,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("reset execute job for bead %d: %w", o.beadID, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit recovery for execution %d: %w", o.execID, err)
		}
	}
	return nil
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
// all within tx. startedAt is captured before the model call; ended_at is now.
func commitAttempt(ctx context.Context, tx *sql.Tx, jobID int64, attemptNum int, rawOutput, validationResult, nextStatus string, startedAt time.Time) error {
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		jobID, attemptNum, rawOutput, validationResult, startedAt.UTC().Format(time.RFC3339), now,
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

