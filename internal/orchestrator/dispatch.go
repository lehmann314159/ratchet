package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/execution"
	"ratchet/internal/ollama"
	"ratchet/internal/verbs"
)

// dispatch executes one handoff job end-to-end:
//  1. Marks the job running.
//  2. Calls the verb handler's Run (HTTP call to Ollama — outside any transaction).
//  3. Validates the raw output.
//  4. In a single atomic transaction: records the attempt, writes the result
//     (if valid), enqueues next jobs, and updates the job's final status.
//
// Infrastructure errors (DB, network) are returned to the caller for
// logging and backoff; they do not count as strikes.
func dispatch(ctx context.Context, d *db.DB, oc *ollama.Client, handlers map[string]verbs.Handler, job *db.HandoffJob) error {
	// EXECUTE_BEAD runs two subprocesses concurrently and manages its own
	// lifecycle; it does not go through the one-shot Run/Validate/Commit path.
	if job.Verb == db.VerbExecuteBead {
		if err := markJobRunning(ctx, d, job.ID); err != nil {
			return fmt.Errorf("mark running: %w", err)
		}
		if err := execution.RunExecutionWindow(ctx, d, oc.BaseURL, job); err != nil {
			slog.Error("execution window failed",
				"job_id", job.ID, "bead_id", job.BeadID, "error", err)
			_, _ = d.ExecContext(ctx,
				`UPDATE handoff_jobs SET status = 'failed_retry', updated_at = ? WHERE id = ?`,
				time.Now().UTC().Format(time.RFC3339), job.ID)
			return err
		}
		_, err := d.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = 'complete', updated_at = ? WHERE id = ?`,
			time.Now().UTC().Format(time.RFC3339), job.ID)
		return err
	}

	handler, ok := handlers[job.Verb]
	if !ok {
		slog.Warn("no handler for verb — skipping", "verb", job.Verb, "job_id", job.ID)
		return nil
	}

	if err := markJobRunning(ctx, d, job.ID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}

	// Run: infrastructure error → return without counting as a strike.
	rawOutput, runErr := handler.Run(ctx, d, oc, job)
	if runErr != nil {
		slog.Error("verb Run failed (infrastructure error, will retry)",
			"verb", job.Verb, "job_id", job.ID, "error", runErr)
		// Reset to pending so the next poll retries it.
		_, _ = d.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = 'pending' WHERE id = ?`, job.ID)
		return runErr
	}

	validationResult, parsed := handler.Validate(rawOutput)
	isValid := validationResult == "valid"

	// Determine next job status and whether to escalate, before the transaction.
	strikes, err := strikeCount(ctx, d, job.ID)
	if err != nil {
		return fmt.Errorf("count strikes: %w", err)
	}
	attemptNum, err := nextAttemptNumber(ctx, d, job.ID)
	if err != nil {
		return fmt.Errorf("next attempt number: %w", err)
	}
	tolerance := verbTolerance(job.Verb)

	// Determine next job status.
	var nextStatus string
	shouldEscalate := false
	switch {
	case isValid:
		nextStatus = "complete"
	case !isValid && strikes+1 > tolerance:
		nextStatus = "escalated"
		shouldEscalate = true
	default:
		nextStatus = "failed_retry"
	}

	// Single atomic transaction: write attempt + result (if valid) + next jobs + job status.
	txErr := withTx(ctx, d, func(tx *sql.Tx) error {
		if err := commitAttempt(ctx, tx, job.ID, attemptNum, rawOutput, validationResult, nextStatus); err != nil {
			return err
		}
		if isValid {
			if err := handler.Commit(ctx, tx, job, parsed); err != nil {
				return fmt.Errorf("commit result: %w", err)
			}
		}
		return nil
	})
	if txErr != nil {
		return txErr
	}

	if shouldEscalate {
		slog.Error("ESCALATION — requires human review",
			"project_id", job.ProjectID,
			"job_id", job.ID,
			"verb", job.Verb,
			"bead_id", job.BeadID,
			"strikes", strikes+1,
			"tolerance", tolerance,
			"last_validation", validationResult,
		)
	}

	if isValid {
		slog.Info("job complete", "verb", job.Verb, "job_id", job.ID, "project_id", job.ProjectID)
	} else {
		slog.Warn("job attempt invalid",
			"verb", job.Verb, "job_id", job.ID, "strikes", strikes+1,
			"validation", validationResult, "next_status", nextStatus)
	}
	return nil
}

// withTx executes fn within a new transaction, rolling back on error.
func withTx(ctx context.Context, d *db.DB, fn func(*sql.Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
