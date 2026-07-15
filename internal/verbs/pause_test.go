package verbs

import (
	"context"
	"database/sql"
	"testing"

	"ratchet/internal/db"
)

// Regression tests for the generalized pause points (Phase 1 of the
// fixture/clone project): pause_after_verb, checked at each verb's
// enqueue-next branch, and pause_after_bead_id (see revise_pending_test.go).
// Every case below asserts the pipeline's normal next handoff_job is still
// enqueued even while pausing — pausing only gates the project's status,
// never the job, so resuming is a pure status flip back to 'active'.

func setPauseAfterVerb(t *testing.T, d *db.DB, projectID int64, verb string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(),
		`UPDATE projects SET pause_after_verb = ? WHERE id = ?`, verb, projectID,
	); err != nil {
		t.Fatalf("set pause_after_verb: %v", err)
	}
}

func projectStatus(t *testing.T, d *db.DB, projectID int64) string {
	t.Helper()
	var status string
	if err := d.QueryRowContext(context.Background(),
		`SELECT status FROM projects WHERE id = ?`, projectID,
	).Scan(&status); err != nil {
		t.Fatalf("query project status: %v", err)
	}
	return status
}

// --- RECONCILE_DECOMPOSITION ---

func TestReconcileDecompositionCommitConvergedPausesOnPauseAfterVerb(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: RECONCILE_DECOMPOSITION converged/pause_after_verb")
	setPauseAfterVerb(t, d, -1, db.VerbReconcileDecomposition)
	beadID, _ := seedBead(t, d, -1, "B01")

	job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h := &ReconcileDecomposition{lastCritique: "no issues", lastRoundsSoFar: 0}
	out := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{{BeadTitle: "B01", Action: "agree", Reason: "no finding"}},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ? AND bead_id = ?`, db.VerbExecuteBead, beadID); n != 1 {
		t.Errorf("EXECUTE_BEAD jobs = %d, want 1 (still enqueued even though pausing)", n)
	}
	if status := projectStatus(t, d, -1); status != "paused" {
		t.Errorf("project status = %q, want paused", status)
	}
}

// TestReconcileDecompositionCommitConvergedPauseAfterReconcileStillWorks confirms
// the pre-existing pause_after_reconcile flag still pauses the project after the
// enqueue-then-gate refactor (it used to short-circuit before enqueueing).
func TestReconcileDecompositionCommitConvergedPauseAfterReconcileStillWorks(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: RECONCILE_DECOMPOSITION converged/pause_after_reconcile")
	if _, err := d.ExecContext(ctx, `UPDATE projects SET pause_after_reconcile = 1 WHERE id = -1`); err != nil {
		t.Fatalf("set pause_after_reconcile: %v", err)
	}
	beadID, _ := seedBead(t, d, -1, "B01")

	job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h := &ReconcileDecomposition{lastCritique: "no issues", lastRoundsSoFar: 0}
	out := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{{BeadTitle: "B01", Action: "agree", Reason: "no finding"}},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ? AND bead_id = ?`, db.VerbExecuteBead, beadID); n != 1 {
		t.Errorf("EXECUTE_BEAD jobs = %d, want 1 (still enqueued even though pausing)", n)
	}
	if status := projectStatus(t, d, -1); status != "paused" {
		t.Errorf("project status = %q, want paused", status)
	}
}

func TestReconcileDecompositionCommitConvergedNoPauseByDefault(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: RECONCILE_DECOMPOSITION converged/no pause configured")
	seedBead(t, d, -1, "B01")

	job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h := &ReconcileDecomposition{lastCritique: "no issues", lastRoundsSoFar: 0}
	out := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{{BeadTitle: "B01", Action: "agree", Reason: "no finding"}},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if status := projectStatus(t, d, -1); status != "active" {
		t.Errorf("project status = %q, want active (no pause configured)", status)
	}
}

// --- VERIFY_MANIFEST ---

func TestVerifyManifestCommitPausesOnPauseAfterVerb(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: VERIFY_MANIFEST commit/pause_after_verb")
	setPauseAfterVerb(t, d, -1, db.VerbVerifyManifest)

	job := seedJob(t, d, -1, db.VerbVerifyManifest, sql.NullInt64{})
	out := VerifyManifestOutput{
		FilePresencePass: true, NoBehavioralTestsPass: true, CompilePass: true,
		APICheckPass: true, StubPurityPass: true,
	}
	h := &VerifyManifest{}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ?`, db.VerbCertifyManifest); n != 1 {
		t.Errorf("CERTIFY_MANIFEST jobs = %d, want 1 (still enqueued even though pausing)", n)
	}
	if status := projectStatus(t, d, -1); status != "paused" {
		t.Errorf("project status = %q, want paused", status)
	}
}

// --- CERTIFY_MANIFEST ---

func seedVerifyAttempt(t *testing.T, d *db.DB, projectID int64) {
	t.Helper()
	ctx := context.Background()
	res, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'complete', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		projectID, db.VerbVerifyManifest)
	if err != nil {
		t.Fatalf("seed VERIFY_MANIFEST job: %v", err)
	}
	jobID, _ := res.LastInsertId()
	if _, err := d.ExecContext(ctx, `
		INSERT INTO verify_attempts
		  (project_id, job_id, attempt_number, file_presence_pass, no_behavioral_tests_pass,
		   compile_pass, api_check_pass, stub_purity_pass, created_at)
		VALUES (?, ?, 1, 1, 1, 1, 1, 1, '2026-01-01T00:00:00Z')`,
		projectID, jobID); err != nil {
		t.Fatalf("seed verify_attempts: %v", err)
	}
}

func TestCertifyManifestCommitApprovePausesOnPauseAfterVerb(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: CERTIFY_MANIFEST approve/pause_after_verb")
	setPauseAfterVerb(t, d, -1, db.VerbCertifyManifest)
	seedSurveyManifest(t, d, -1, SurveySpecOutput{Module: "m", Package: "p"})
	seedVerifyAttempt(t, d, -1)

	job := seedJob(t, d, -1, db.VerbCertifyManifest, sql.NullInt64{})
	out := CertifyManifestOutput{PreliminaryDecision: "approve", FinalDecision: "approve"}
	h := &CertifyManifest{folderPath: t.TempDir()}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ?`, db.VerbDecomposeSpec); n != 1 {
		t.Errorf("DECOMPOSE_SPEC jobs = %d, want 1 (still enqueued even though pausing)", n)
	}
	if status := projectStatus(t, d, -1); status != "paused" {
		t.Errorf("project status = %q, want paused", status)
	}
}

// TestCertifyManifestCommitRejectIgnoresPauseAfterVerb confirms the reject/retry
// path never pauses, even if pause_after_verb=CERTIFY_MANIFEST — only the
// approve path (the actual "verb complete, advancing" branch) is gated.
func TestCertifyManifestCommitRejectIgnoresPauseAfterVerb(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: CERTIFY_MANIFEST reject/pause_after_verb should not fire")
	setPauseAfterVerb(t, d, -1, db.VerbCertifyManifest)
	seedVerifyAttempt(t, d, -1)

	job := seedJob(t, d, -1, db.VerbCertifyManifest, sql.NullInt64{})
	out := CertifyManifestOutput{PreliminaryDecision: "reject", FinalDecision: "reject"}
	h := &CertifyManifest{folderPath: t.TempDir()}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ?`, db.VerbSurveySpec); n != 1 {
		t.Errorf("SURVEY_SPEC retry jobs = %d, want 1", n)
	}
	if status := projectStatus(t, d, -1); status != "active" {
		t.Errorf("project status = %q, want active (reject path must not pause)", status)
	}
}

// --- ADJUDICATE_NEXT_EXECUTION ---

func TestAdjudicateNextExecutionCommitDeclareSuccessPartialPausesOnPauseAfterVerb(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE declare_success partial/pause_after_verb")
	setPauseAfterVerb(t, d, -1, db.VerbAdjudicateNextExecution)
	beadID1, revID1 := seedBead(t, d, -1, "B01")
	seedBead(t, d, -1, "B02")
	zero := 0
	seedExecution(t, d, -1, beadID1, revID1, "success", &zero)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID1, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend: "not_applicable", BeadSpecFit: "not_applicable",
		Reasoning: "All exit criteria confirmed met for B01.",
		Decision:  "declare_success",
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{}).Commit(ctx, tx, job, out)
	})

	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ?`, db.VerbRevisePending); n != 1 {
		t.Errorf("REVISE_PENDING jobs = %d, want 1 (still enqueued even though pausing)", n)
	}
	if status := projectStatus(t, d, -1); status != "paused" {
		t.Errorf("project status = %q, want paused", status)
	}
}

// TestAdjudicateNextExecutionCommitDeclareSuccessCompleteIgnoresPauseAfterVerb
// confirms a project that just went complete (last bead) is left 'complete',
// never overwritten to 'paused', even if pause_after_verb=ADJUDICATE_NEXT_EXECUTION —
// REVISE_PENDING never fires for the last bead, so there is nothing to pause before.
func TestAdjudicateNextExecutionCommitDeclareSuccessCompleteIgnoresPauseAfterVerb(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: ADJUDICATE declare_success complete/pause_after_verb should not fire")
	setPauseAfterVerb(t, d, -1, db.VerbAdjudicateNextExecution)
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0
	seedExecution(t, d, -1, beadID, revID, "success", &zero)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend: "not_applicable", BeadSpecFit: "not_applicable",
		Reasoning: "All exit criteria confirmed met.",
		Decision:  "declare_success",
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{}).Commit(ctx, tx, job, out)
	})

	if status := projectStatus(t, d, -1); status != "complete" {
		t.Errorf("project status = %q, want complete", status)
	}
}

// --- REFINE_TESTS_JUDGE ---

func TestRefineTestsJudgeCommitApprovedPausesOnPauseAfterVerb(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: REFINE_TESTS_JUDGE approved/pause_after_verb")
	setPauseAfterVerb(t, d, -1, db.VerbRefineTestsJudge)
	beadID, _ := seedBead(t, d, -1, "B01")

	job := seedJob(t, d, -1, db.VerbRefineTestsJudge, sql.NullInt64{Int64: beadID, Valid: true})
	out := RefineTestsJudgeOutput{Decision: "approved", Summary: "tests look good"}
	h := &RefineTestsJudge{}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ? AND bead_id = ?`, db.VerbExecuteBead, beadID); n != 1 {
		t.Errorf("EXECUTE_BEAD jobs = %d, want 1 (still enqueued even though pausing)", n)
	}
	if status := projectStatus(t, d, -1); status != "paused" {
		t.Errorf("project status = %q, want paused", status)
	}
}

// TestRefineTestsJudgeCommitReviseIgnoresPauseAfterVerb confirms the revise/retry
// path never pauses, even if pause_after_verb=REFINE_TESTS_JUDGE.
func TestRefineTestsJudgeCommitReviseIgnoresPauseAfterVerb(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: REFINE_TESTS_JUDGE revise/pause_after_verb should not fire")
	setPauseAfterVerb(t, d, -1, db.VerbRefineTestsJudge)
	beadID, _ := seedBead(t, d, -1, "B01")

	job := seedJob(t, d, -1, db.VerbRefineTestsJudge, sql.NullInt64{Int64: beadID, Valid: true})
	out := RefineTestsJudgeOutput{Decision: "revise", Instructions: "fix the boundary case"}
	h := &RefineTestsJudge{}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ? AND bead_id = ?`, db.VerbRefineTestsWrite, beadID); n != 1 {
		t.Errorf("REFINE_TESTS_WRITE retry jobs = %d, want 1", n)
	}
	if status := projectStatus(t, d, -1); status != "active" {
		t.Errorf("project status = %q, want active (revise path must not pause)", status)
	}
}

// --- REVISE_PENDING: bead-level pause (pause_after_bead_id) ---

func setPauseAfterBead(t *testing.T, d *db.DB, projectID, beadID int64) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(),
		`UPDATE projects SET pause_after_bead_id = ? WHERE id = ?`, beadID, projectID,
	); err != nil {
		t.Fatalf("set pause_after_bead_id: %v", err)
	}
}

// TestRevisePendingCommitPausesOnTriggerBeadMatch confirms that when the bead
// that just succeeded (the REVISE_PENDING trigger bead) matches
// pause_after_bead_id, REVISE_PENDING still does its normal housekeeping
// (dispatching the next pending bead for execution) and only the project's
// status is gated — resuming later is then a pure status flip, since the
// correct next job is already sitting there pending.
func TestRevisePendingCommitPausesOnTriggerBeadMatch(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: REVISE_PENDING dispatch-next/pause_after_bead_id matches trigger")
	triggerBeadID, _ := seedBead(t, d, -1, "B01")
	nextBeadID, _ := seedBead(t, d, -1, "B02")
	setPauseAfterBead(t, d, -1, triggerBeadID)

	job := seedJob(t, d, -1, db.VerbRevisePending, sql.NullInt64{Int64: triggerBeadID, Valid: true})
	out := RevisePendingOutput{} // no revisions — every pending bead treated as no_change
	h := &RevisePending{}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND bead_id = ? AND verb IN (?, ?)`,
		nextBeadID, db.VerbExecuteBead, db.VerbRefineTestsWrite); n != 1 {
		t.Errorf("next-bead execution jobs for B02 = %d, want 1 (still dispatched even though pausing)", n)
	}
	if status := projectStatus(t, d, -1); status != "paused" {
		t.Errorf("project status = %q, want paused", status)
	}
}

// TestRevisePendingCommitNoPauseWhenBeadIDDiffers confirms pause_after_bead_id
// set to a bead other than the trigger bead never pauses this dispatch.
func TestRevisePendingCommitNoPauseWhenBeadIDDiffers(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: REVISE_PENDING dispatch-next/pause_after_bead_id differs")
	triggerBeadID, _ := seedBead(t, d, -1, "B01")
	seedBead(t, d, -1, "B02")
	setPauseAfterBead(t, d, -1, triggerBeadID+1000) // some unrelated bead ID

	job := seedJob(t, d, -1, db.VerbRevisePending, sql.NullInt64{Int64: triggerBeadID, Valid: true})
	out := RevisePendingOutput{}
	h := &RevisePending{}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if status := projectStatus(t, d, -1); status != "active" {
		t.Errorf("project status = %q, want active (pause_after_bead_id does not match trigger bead)", status)
	}
}

// TestRevisePendingCommitNoPauseByDefault confirms an unconfigured
// pause_after_bead_id (NULL) never pauses.
func TestRevisePendingCommitNoPauseByDefault(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: REVISE_PENDING dispatch-next/no pause configured")
	triggerBeadID, _ := seedBead(t, d, -1, "B01")
	seedBead(t, d, -1, "B02")

	job := seedJob(t, d, -1, db.VerbRevisePending, sql.NullInt64{Int64: triggerBeadID, Valid: true})
	out := RevisePendingOutput{}
	h := &RevisePending{}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	if status := projectStatus(t, d, -1); status != "active" {
		t.Errorf("project status = %q, want active", status)
	}
}
