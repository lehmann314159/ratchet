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

	// Warm up the model before the real Chat() call. A trivial "hello" request
	// forces the model into VRAM so a cold swap costs at most 1 minute here
	// instead of silently burning the full 30-minute job timeout.
	// Model-free verbs (VERIFY_MANIFEST) skip this step entirely.
	if !verbSkipsModelWarmup(job.Verb) {
		var model string
		if err := d.QueryRowContext(ctx,
			`SELECT model FROM verb_model_assignments WHERE project_id = ? AND verb = ?`,
			job.ProjectID, job.Verb,
		).Scan(&model); err != nil {
			slog.Error("warmup: model lookup failed, will retry", "verb", job.Verb, "job_id", job.ID, "error", err)
			_, _ = d.ExecContext(ctx, `UPDATE handoff_jobs SET status = 'pending' WHERE id = ? AND status = 'running'`, job.ID)
			return err
		}
		if err := oc.Warmup(ctx, model); err != nil {
			slog.Error("ollama warmup failed, will retry", "verb", job.Verb, "job_id", job.ID, "model", model, "error", err)
			_, _ = d.ExecContext(ctx, `UPDATE handoff_jobs SET status = 'pending' WHERE id = ? AND status = 'running'`, job.ID)
			return err
		}
		slog.Info("ollama warmup ok", "verb", job.Verb, "job_id", job.ID, "model", model)
	}

	// Run: infrastructure error → return without counting as a strike.
	startedAt := time.Now().UTC()
	rawOutput, runErr := handler.Run(ctx, d, oc, job)
	if runErr != nil {
		slog.Error("verb Run failed (infrastructure error, will retry)",
			"verb", job.Verb, "job_id", job.ID, "error", runErr)
		_, _ = d.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = 'pending' WHERE id = ? AND status = 'running'`, job.ID)
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
	var commitFailed bool
	txErr := withTx(ctx, d, func(tx *sql.Tx) error {
		if err := commitAttempt(ctx, tx, job.ID, attemptNum, rawOutput, validationResult, nextStatus, startedAt); err != nil {
			return err
		}
		if isValid {
			if err := handler.Commit(ctx, tx, job, parsed); err != nil {
				commitFailed = true
				return fmt.Errorf("commit result: %w", err)
			}
		}
		return nil
	})
	if txErr != nil {
		if !commitFailed {
			// commitAttempt itself failed — a plain DB write error, unrelated to
			// the model's output. Same treatment as a Run() failure: retry
			// without counting a strike.
			return txErr
		}
		// handler.Commit failed after Validate already accepted the output as
		// well-formed (e.g. RECONCILE's applyFixes hitting a bead lookup that
		// doesn't exist). The whole transaction — including the attempt row —
		// rolled back, so without this the job is left in 'running' forever:
		// claimNextJob only claims 'pending'/'failed_retry' jobs, and the only
		// reset path (resetStaleRunning) runs once at daemon startup. Recording
		// this as a failed attempt reuses the existing strike/tolerance math so
		// the job retries, then escalates, instead of silently wedging the
		// orchestrator's one execution slot until a human restarts the daemon.
		return recordCommitFailure(ctx, d, job, attemptNum, rawOutput, startedAt, strikes, tolerance, txErr)
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

// verbSkipsModelWarmup returns true for verbs that are model-free and must not
// have their model assignment looked up (VERIFY_MANIFEST has none).
func verbSkipsModelWarmup(verb string) bool {
	return verb == db.VerbVerifyManifest
}

// recordCommitFailure runs in a fresh transaction after handler.Commit has
// failed and the original attempt transaction has rolled back. It writes the
// attempt as failed (validation_result carries the Commit error, distinct
// from a Validate failure) and applies the same strike/tolerance decision
// dispatch already computed for a Validate failure, so a Commit error is
// retried and eventually escalated exactly like any other bad attempt,
// instead of leaving the job stuck in 'running' indefinitely.
func recordCommitFailure(ctx context.Context, d *db.DB, job *db.HandoffJob, attemptNum int, rawOutput string, startedAt time.Time, strikes, tolerance int, commitErr error) error {
	slog.Error("verb Commit failed", "verb", job.Verb, "job_id", job.ID, "project_id", job.ProjectID, "error", commitErr)

	nextStatus := "failed_retry"
	shouldEscalate := strikes+1 > tolerance
	if shouldEscalate {
		nextStatus = "escalated"
	}
	now := time.Now().UTC().Format(time.RFC3339)

	txErr := withTx(ctx, d, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at, ended_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			job.ID, attemptNum, rawOutput, "commit_error: "+commitErr.Error(),
			startedAt.UTC().Format(time.RFC3339), now,
		); err != nil {
			return fmt.Errorf("record commit-failure attempt: %w", err)
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = ?, updated_at = ? WHERE id = ?`,
			nextStatus, now, job.ID)
		return err
	})
	if txErr != nil {
		return txErr
	}

	if shouldEscalate {
		slog.Error("ESCALATION — requires human review (Commit failure)",
			"project_id", job.ProjectID, "job_id", job.ID, "verb", job.Verb,
			"bead_id", job.BeadID, "strikes", strikes+1, "tolerance", tolerance,
		)
	} else {
		slog.Warn("job attempt failed at Commit, will retry",
			"verb", job.Verb, "job_id", job.ID, "strikes", strikes+1, "next_status", nextStatus)
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
