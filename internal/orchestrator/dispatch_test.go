package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
	"ratchet/internal/verbs"
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

var errAssertRunFailure = errors.New("no test file paths for bead 42")

// TestRecordRunFailure_UnderTolerance reproduces the Stage 6 audit scenario:
// handler.Run returns a deterministic error (e.g. REFINE_TESTS_WRITE's "no
// test file paths" when a bead's output_files lost its test file) that can
// never resolve on retry. Before this fix, dispatch reset the job straight
// back to 'pending' unconditionally — no attempt recorded, no strike
// counted, no cap — so the job retried forever and never escalated. Verifies
// a first Run failure is recorded as a retryable attempt.
func TestRecordRunFailure_UnderTolerance(t *testing.T) {
	d := openTestDB(t)
	job := seedRunningJob(t, d)
	ctx := context.Background()

	err := recordRunFailure(ctx, d, job, time.Now(), errAssertRunFailure)
	if err == nil {
		t.Fatal("recordRunFailure: expected the original runErr to be returned")
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
		t.Errorf("a Run failure must not be recorded as a valid attempt")
	}
}

// TestRecordRunFailure_EscalatesAtTolerance verifies that a Run error which
// keeps recurring (the same deterministic bug on every retry) escalates once
// strikes reach tolerance, instead of retrying forever with zero visibility.
func TestRecordRunFailure_EscalatesAtTolerance(t *testing.T) {
	d := openTestDB(t)
	job := seedRunningJob(t, d)
	ctx := context.Background()

	// Seed two prior failed attempts to bring strikes to tolerance.
	for i := 1; i <= 2; i++ {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at, ended_at)
			VALUES (?, ?, '', 'run_error: prior failure', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			job.ID, i,
		); err != nil {
			t.Fatalf("seed prior attempt: %v", err)
		}
	}

	err := recordRunFailure(ctx, d, job, time.Now(), errAssertRunFailure)
	if err == nil {
		t.Fatal("recordRunFailure: expected the original runErr to be returned")
	}

	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	if status != "escalated" {
		t.Errorf("expected status 'escalated' at tolerance, got %q — a permanently failing Run() must not retry forever", status)
	}
}

// TestCompleteExecuteBeadJob_DoesNotClobberInfraFailureRetry reproduces the
// Stage 4 audit bug: internal/execution/window.go's handleInfraFailure moves
// an EXECUTE_BEAD job to 'pending' (to retry, under infraFailureCap) or
// 'escalated' (at cap) itself, then returns nil. Before the fix,
// completeExecuteBeadJob's write was unconditional and always clobbered that
// decision back to 'complete' — silently stranding the bead, since no
// ANALYZE_EXECUTION job exists on the infra-failure path to pick it back up.
// Verifies the status='running' guard leaves a job handleInfraFailure already
// moved untouched.
func TestCompleteExecuteBeadJob_DoesNotClobberInfraFailureRetry(t *testing.T) {
	for _, finalStatus := range []string{"pending", "escalated"} {
		t.Run(finalStatus, func(t *testing.T) {
			d := openTestDB(t)
			job := seedRunningJob(t, d)
			ctx := context.Background()

			// Simulate handleInfraFailure already having moved the job out of
			// 'running' before RunExecutionWindow returned nil.
			if _, err := d.ExecContext(ctx,
				`UPDATE handoff_jobs SET status = ? WHERE id = ?`, finalStatus, job.ID,
			); err != nil {
				t.Fatalf("simulate handleInfraFailure write: %v", err)
			}

			if err := completeExecuteBeadJob(ctx, d, job.ID); err != nil {
				t.Fatalf("completeExecuteBeadJob: %v", err)
			}

			var status string
			if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
				t.Fatalf("query job status: %v", err)
			}
			if status != finalStatus {
				t.Errorf("expected completeExecuteBeadJob to leave status %q untouched, got %q — bug reproduced: infra-failure decision clobbered", finalStatus, status)
			}
		})
	}
}

// TestCompleteExecuteBeadJob_CompletesRunningJob verifies the normal
// completion path still works: a job still 'running' (the state
// RunExecutionWindow leaves it in on a real successful execution) is marked
// 'complete'.
func TestCompleteExecuteBeadJob_CompletesRunningJob(t *testing.T) {
	d := openTestDB(t)
	job := seedRunningJob(t, d)
	ctx := context.Background()

	if err := completeExecuteBeadJob(ctx, d, job.ID); err != nil {
		t.Fatalf("completeExecuteBeadJob: %v", err)
	}

	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	if status != "complete" {
		t.Errorf("expected status 'complete' for a normally-completed running job, got %q", status)
	}
}

// assertStrikeRecorded checks that a run_error attempt was recorded for jobID
// and that the job's status matches wantStatus, for the "no accounting at
// all" bugs this Stage 7 pass fixed: before the fix, none of these three
// dispatch() failure paths (model-lookup, ollama warmup, EXECUTE_BEAD's
// RunExecutionWindow error) wrote a handoff_attempts row or counted a strike
// at all — they just reset the job status unconditionally, forever, with no
// escalation ever possible.
func assertStrikeRecorded(t *testing.T, d *db.DB, jobID int64, wantStatus, wantResultPrefix string) {
	t.Helper()
	ctx := context.Background()

	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	if status != wantStatus {
		t.Errorf("expected status %q, got %q", wantStatus, status)
	}

	var validationResult string
	if err := d.QueryRowContext(ctx,
		`SELECT validation_result FROM handoff_attempts WHERE job_id = ?`, jobID,
	).Scan(&validationResult); err != nil {
		t.Fatalf("expected a handoff_attempts row to be written (bug: failure was never recorded at all): %v", err)
	}
	if !strings.HasPrefix(validationResult, wantResultPrefix) {
		t.Errorf("expected validation_result to start with %q, got %q", wantResultPrefix, validationResult)
	}
}

// TestDispatch_ModelLookupFailureAppliesStrikeAccounting reproduces the Stage
// 7 audit finding: a missing verb_model_assignments row (or any DB error
// looking one up) made dispatch() reset the job straight to 'pending' with
// no attempt recorded and no strike counted — looping forever if the
// assignment is never fixed. Verifies the failure is now recorded and
// retryable under tolerance.
func TestDispatch_ModelLookupFailureAppliesStrikeAccounting(t *testing.T) {
	d := openTestDB(t)
	job := seedRunningJob(t, d) // RECONCILE_DECOMPOSITION, project 1 — no verb_model_assignments row seeded
	ctx := context.Background()

	oc := ollama.New("http://127.0.0.1:1") // never reached: lookup fails before any HTTP call
	handlers := verbs.All(oc.BaseURL)

	if err := dispatch(ctx, d, oc, handlers, job); err == nil {
		t.Fatal("dispatch: expected an error from the missing model assignment")
	}

	assertStrikeRecorded(t, d, job.ID, "failed_retry", "run_error: model lookup:")
}

// TestDispatch_OllamaWarmupFailureAppliesStrikeAccounting reproduces the same
// gap for the Ollama warmup call itself (e.g. Ollama unreachable, or a model
// never pulled on the host — a persistent, non-transient condition).
func TestDispatch_OllamaWarmupFailureAppliesStrikeAccounting(t *testing.T) {
	d := openTestDB(t)
	job := seedRunningJob(t, d)
	ctx := context.Background()

	if _, err := d.ExecContext(ctx, `
		INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (1, ?, 'fake-model')`,
		db.VerbReconcileDecomposition,
	); err != nil {
		t.Fatalf("seed verb_model_assignments: %v", err)
	}

	oc := ollama.New("http://127.0.0.1:1") // connection refused — Warmup fails fast
	handlers := verbs.All(oc.BaseURL)

	if err := dispatch(ctx, d, oc, handlers, job); err == nil {
		t.Fatal("dispatch: expected an error from the unreachable Ollama warmup call")
	}

	assertStrikeRecorded(t, d, job.ID, "failed_retry", "run_error: ollama warmup for model fake-model:")
}

// TestDispatch_ExecuteBeadRunErrorAppliesStrikeAccounting reproduces the
// Stage 6/7 audit finding for EXECUTE_BEAD specifically: any error
// RunExecutionWindow returns before ever starting the execute-bead subprocess
// (missing bead revision, DB failure, disk-full creating the trace file,
// fork/exec failure) reset the job to 'failed_retry' unconditionally, with no
// strike ever counted and no cap — looping forever. Uses a bead with no
// current_revision_id so RunExecutionWindow's first query fails immediately
// (no matching bead_revisions row), without needing a real subprocess or
// Ollama call.
func TestDispatch_ExecuteBeadRunErrorAppliesStrikeAccounting(t *testing.T) {
	d := openTestDB(t)
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
	beadRes, err := d.ExecContext(ctx, `
		INSERT INTO beads (project_id, status, current_revision_id) VALUES (1, 'pending', NULL)`)
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	beadID, _ := beadRes.LastInsertId()

	res, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (1, ?, ?, 'running', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		db.VerbExecuteBead, beadID)
	if err != nil {
		t.Fatalf("seed handoff_jobs: %v", err)
	}
	jobID, _ := res.LastInsertId()
	job := &db.HandoffJob{ID: jobID, ProjectID: 1, Verb: db.VerbExecuteBead, BeadID: sql.NullInt64{Int64: beadID, Valid: true}}

	oc := ollama.New("http://127.0.0.1:1")
	handlers := verbs.All(oc.BaseURL)

	if err := dispatch(ctx, d, oc, handlers, job); err == nil {
		t.Fatal("dispatch: expected an error — bead has no current_revision_id")
	}

	assertStrikeRecorded(t, d, job.ID, "failed_retry", "run_error: load bead revision:")
}
