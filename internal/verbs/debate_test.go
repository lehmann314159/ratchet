package verbs

// Step 4: audit/reconcile debate loop tests.
//
// Two categories:
//   - Unit tests for loadDebateHistory, buildReconcileUserMsg, formatReconcileResponses
//   - Multi-round smoke tests: the convergence comparator exercised across a
//     full 2-round sequence, which the individual-round Commit tests in
//     commit_test.go do not cover.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"ratchet/internal/db"
)

// --- loadDebateHistory ---

func TestLoadDebateHistoryEmpty(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, -1, "fixture: debate history — empty")

	history, err := loadDebateHistory(context.Background(), d, -1)
	if err != nil {
		t.Fatalf("loadDebateHistory: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("expected empty history, got %d rounds", len(history))
	}
}

func TestLoadDebateHistoryTwoRounds(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: debate history — two rounds")

	_, err := d.ExecContext(ctx, `
		INSERT INTO audit_reconcile_rounds
		  (project_id, round_number, critique_text, reconciliation, outcome, created_at)
		VALUES
		  (-1, 1, 'audit critique round 1', '{"responses":[{"bead_title":"B01","action":"disagree","reason":"wrong"}]}', 'disagreed_continuing', '2026-01-01T00:00:00Z'),
		  (-1, 2, 'audit critique round 2', '{"responses":[{"bead_title":"B01","action":"agree_and_fix","reason":"ok"}]}', 'converged', '2026-01-01T00:01:00Z')`)
	if err != nil {
		t.Fatalf("seed rounds: %v", err)
	}

	history, err := loadDebateHistory(ctx, d, -1)
	if err != nil {
		t.Fatalf("loadDebateHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 rounds, got %d", len(history))
	}
	if history[0].RoundNumber != 1 || history[0].Outcome != "disagreed_continuing" {
		t.Errorf("round 1 wrong: %+v", history[0])
	}
	if history[1].RoundNumber != 2 || history[1].Outcome != "converged" {
		t.Errorf("round 2 wrong: %+v", history[1])
	}
	if history[0].CritiqueText != "audit critique round 1" {
		t.Errorf("round 1 critique = %q", history[0].CritiqueText)
	}
}

// --- formatReconcileResponses ---

func TestFormatReconcileResponsesValid(t *testing.T) {
	json := `{"responses":[{"bead_title":"B01","action":"agree_and_fix","reason":"stride formula corrected"},{"bead_title":"B02","action":"disagree","reason":"named approach per §2.3"}],"updated_beads":[]}`
	got := formatReconcileResponses(json)
	if !strings.Contains(got, "B01: AGREE AND FIX") {
		t.Errorf("missing AGREE AND FIX line: %q", got)
	}
	if !strings.Contains(got, "B02: DISAGREE") {
		t.Errorf("missing DISAGREE line: %q", got)
	}
	if !strings.Contains(got, "stride formula corrected") {
		t.Errorf("missing reason text: %q", got)
	}
}

func TestFormatReconcileResponsesInvalidJSON(t *testing.T) {
	raw := "not valid json"
	got := formatReconcileResponses(raw)
	// Should fall back to the raw string.
	if got != raw {
		t.Errorf("expected raw fallback, got %q", got)
	}
}

// --- buildReconcileUserMsg ---

func TestBuildReconcileUserMsgNoHistory(t *testing.T) {
	beads := []beadState{{BeadID: 1, Title: "B01", FullText: "spec text"}}
	msg := buildReconcileUserMsg("design doc content", beads, nil, "audit critique text")

	if !strings.Contains(msg, "## Design Document") {
		t.Error("missing Design Document section")
	}
	if !strings.Contains(msg, "spec text") {
		t.Error("missing bead full_text")
	}
	if !strings.Contains(msg, "## Current Critique") {
		t.Error("missing Current Critique section")
	}
	if !strings.Contains(msg, "audit critique text") {
		t.Error("missing critique content")
	}
	// No history — should NOT contain debate history section.
	if strings.Contains(msg, "Previous Debate History") {
		t.Error("unexpected debate history section when history is empty")
	}
}

func TestBuildReconcileUserMsgWithHistory(t *testing.T) {
	beads := []beadState{{BeadID: 1, Title: "B01", FullText: "revised spec"}}
	history := []debateRound{
		{
			RoundNumber:    1,
			CritiqueText:   "round 1 critique: stride formula wrong",
			Reconciliation: `{"responses":[{"bead_title":"B01","action":"disagree","reason":"formula is correct per §3.1"}],"updated_beads":[]}`,
			Outcome:        "disagreed_continuing",
		},
	}
	msg := buildReconcileUserMsg("design doc", beads, history, "round 2 critique: still wrong")

	if !strings.Contains(msg, "## Previous Debate History") {
		t.Error("missing Previous Debate History section")
	}
	if !strings.Contains(msg, "Round 1 (outcome: disagreed_continuing)") {
		t.Error("missing round 1 header")
	}
	if !strings.Contains(msg, "round 1 critique: stride formula wrong") {
		t.Error("missing round 1 critique text")
	}
	// The reconciliation JSON should be formatted as bullet points.
	if !strings.Contains(msg, "B01: DISAGREE") {
		t.Error("missing formatted disagree bullet")
	}
	if !strings.Contains(msg, "## Current Critique") {
		t.Error("missing Current Critique section")
	}
	if !strings.Contains(msg, "round 2 critique: still wrong") {
		t.Error("missing round 2 critique text")
	}
	// History must appear BEFORE the current critique.
	historyIdx := strings.Index(msg, "Previous Debate History")
	critiqueIdx := strings.Index(msg, "Current Critique")
	if historyIdx > critiqueIdx {
		t.Error("debate history section appears after current critique (wrong order)")
	}
}

// --- Multi-round convergence smoke tests ---
//
// These exercise the full 2-round debate sequence: seed AUDIT output, call
// RECONCILE Commit, verify routing, then do it again for round 2. This is
// the "smoke-test convergence comparator specifically" from the build order.

// seedAuditComplete inserts a completed AUDIT handoff_job and a valid attempt
// with the given findings JSON. Returns the job ID.
func seedAuditComplete(t *testing.T, d *db.DB, projectID int64, findingsJSON, createdAt string) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, 'AUDIT_DECOMPOSITION', NULL, 'complete', ?, ?)`,
		projectID, createdAt, createdAt)
	if err != nil {
		t.Fatalf("seedAuditComplete job: %v", err)
	}
	jobID, _ := res.LastInsertId()
	_, err = d.ExecContext(ctx, `
		INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at)
		VALUES (?, 1, ?, 'valid', ?)`,
		jobID, findingsJSON, createdAt)
	if err != nil {
		t.Fatalf("seedAuditComplete attempt: %v", err)
	}
	return jobID
}

const auditFindingsJSON = `{"findings":[{"bead_title":"B01","issue":"stride formula uses 3 bytes/pixel; NRGBA is always 4","design_doc_reference":"§3.1"}],"overall_verdict":"issues_found"}`

// TestDebateLoopTwoRoundsConverged: round 1 disagrees, round 2 agrees → converged.
func TestDebateLoopTwoRoundsConverged(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: debate loop/2-round converged")
	beadID, _ := seedBead(t, d, -1, "B01")
	_ = beadID

	// --- Round 1: RECONCILE disagrees ---
	seedAuditComplete(t, d, -1, auditFindingsJSON, "2026-01-01T00:00:00Z")

	reconcile1Job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h1 := &ReconcileDecomposition{
		lastCritique:    auditFindingsJSON,
		lastRoundsSoFar: 0,
	}
	round1Output := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{
			{BeadTitle: "B01", Action: "disagree", Reason: "the formula is correct per §3.1 of the design doc"},
		},
		UpdatedBeads: []ParsedBead{
			{Title: "B01", FullText: "original spec unchanged", ExecutionBudget: 300, MonitorOverride: "honor"},
		},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h1.Commit(ctx, tx, reconcile1Job, round1Output) })

	// Verify round 1: disagreed_continuing, AUDIT re-enqueued.
	var round1Outcome string
	if err := d.QueryRowContext(ctx,
		`SELECT outcome FROM audit_reconcile_rounds WHERE project_id = -1 AND round_number = 1`,
	).Scan(&round1Outcome); err != nil {
		t.Fatalf("round 1 row missing: %v", err)
	}
	if round1Outcome != "disagreed_continuing" {
		t.Errorf("round 1 outcome = %q, want disagreed_continuing", round1Outcome)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = 'AUDIT_DECOMPOSITION' AND status = 'pending'`); n != 1 {
		t.Errorf("pending AUDIT jobs after round 1 = %d, want 1", n)
	}

	// --- Round 2: RECONCILE agrees (after seeing the debate history) ---
	// Seed a second AUDIT job (with a later timestamp so latestAuditCritique picks it up).
	seedAuditComplete(t, d, -1, auditFindingsJSON, "2026-01-01T00:01:00Z")

	// Verify debate history is now populated.
	history, err := loadDebateHistory(ctx, d, -1)
	if err != nil {
		t.Fatalf("loadDebateHistory: %v", err)
	}
	if len(history) != 1 || history[0].RoundNumber != 1 {
		t.Errorf("expected 1 history round before round 2, got %d", len(history))
	}

	reconcile2Job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h2 := &ReconcileDecomposition{
		lastCritique:    auditFindingsJSON,
		lastRoundsSoFar: 1, // one completed round
	}
	round2Output := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{
			{
				BeadTitle: "B01", Action: "agree_and_fix",
				Reason:      "correct — updating stride formula to 4 bytes/pixel",
				UpdatedBead: &ParsedBead{Title: "B01", FullText: "fixed spec: stride=4", ExecutionBudget: 300, MonitorOverride: "honor"},
			},
		},
		UpdatedBeads: []ParsedBead{
			{Title: "B01", FullText: "fixed spec: stride=4", ExecutionBudget: 300, MonitorOverride: "honor"},
		},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h2.Commit(ctx, tx, reconcile2Job, round2Output) })

	// Verify round 2: converged, EXECUTE_BEAD enqueued.
	var round2Outcome string
	if err := d.QueryRowContext(ctx,
		`SELECT outcome FROM audit_reconcile_rounds WHERE project_id = -1 AND round_number = 2`,
	).Scan(&round2Outcome); err != nil {
		t.Fatalf("round 2 row missing: %v", err)
	}
	if round2Outcome != "converged" {
		t.Errorf("round 2 outcome = %q, want converged", round2Outcome)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = 'EXECUTE_BEAD'`); n != 1 {
		t.Errorf("EXECUTE_BEAD jobs after convergence = %d, want 1", n)
	}
	// Bead B01 should now be at revision 2 (agree_and_fix created a new revision).
	if n := countRows(t, d, `SELECT COUNT(*) FROM bead_revisions WHERE bead_id = ? AND revision_number = 2`, beadID); n != 1 {
		t.Errorf("bead revision 2 after agree_and_fix = %d, want 1", n)
	}
}

// TestDebateLoopTwoRoundsEscalated: both rounds disagree → escalated at cap.
func TestDebateLoopTwoRoundsEscalated(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: debate loop/2-round escalated")
	seedBead(t, d, -1, "B01")

	// --- Round 1: RECONCILE disagrees ---
	seedAuditComplete(t, d, -1, auditFindingsJSON, "2026-01-01T00:00:00Z")

	reconcile1Job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h1 := &ReconcileDecomposition{lastCritique: auditFindingsJSON, lastRoundsSoFar: 0}
	inTx(t, d, func(tx *sql.Tx) error {
		return h1.Commit(ctx, tx, reconcile1Job, ReconcileDecompositionOutput{
			Responses:    []ReconcileResponse{{BeadTitle: "B01", Action: "disagree", Reason: "finding is wrong: §3.1 specifies this"}},
			UpdatedBeads: []ParsedBead{{Title: "B01", FullText: "spec", ExecutionBudget: 300, MonitorOverride: "honor"}},
		})
	})

	var r1outcome string
	_ = d.QueryRowContext(ctx, `SELECT outcome FROM audit_reconcile_rounds WHERE round_number=1 AND project_id=-1`).Scan(&r1outcome)
	if r1outcome != "disagreed_continuing" {
		t.Fatalf("expected disagreed_continuing after round 1, got %q", r1outcome)
	}

	// --- Round 2: RECONCILE STILL disagrees → escalated (round 2 == cap of 2) ---
	seedAuditComplete(t, d, -1, auditFindingsJSON, "2026-01-01T00:01:00Z")

	reconcile2Job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h2 := &ReconcileDecomposition{lastCritique: auditFindingsJSON, lastRoundsSoFar: 1}
	inTx(t, d, func(tx *sql.Tx) error {
		return h2.Commit(ctx, tx, reconcile2Job, ReconcileDecompositionOutput{
			Responses:    []ReconcileResponse{{BeadTitle: "B01", Action: "disagree", Reason: "still disagree: §3.1 is unambiguous"}},
			UpdatedBeads: []ParsedBead{{Title: "B01", FullText: "spec", ExecutionBudget: 300, MonitorOverride: "honor"}},
		})
	})

	var r2outcome string
	if err := d.QueryRowContext(ctx,
		`SELECT outcome FROM audit_reconcile_rounds WHERE round_number=2 AND project_id=-1`,
	).Scan(&r2outcome); err != nil {
		t.Fatalf("round 2 row missing: %v", err)
	}
	if r2outcome != "escalated" {
		t.Errorf("round 2 outcome = %q, want escalated", r2outcome)
	}
	// Reconcile job must be marked escalated.
	var jobStatus string
	if err := d.QueryRowContext(ctx,
		`SELECT status FROM handoff_jobs WHERE id = ?`, reconcile2Job.ID,
	).Scan(&jobStatus); err != nil {
		t.Fatalf("reconcile job row: %v", err)
	}
	if jobStatus != "escalated" {
		t.Errorf("reconcile job status = %q, want escalated", jobStatus)
	}
	// No EXECUTE_BEAD jobs should exist.
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = 'EXECUTE_BEAD'`); n != 0 {
		t.Errorf("EXECUTE_BEAD jobs = %d after escalation, want 0", n)
	}
	// Debate history has both rounds.
	history, _ := loadDebateHistory(ctx, d, -1)
	if len(history) != 2 {
		t.Errorf("debate history has %d rounds, want 2", len(history))
	}
}

// TestDebateLoopSingleRoundConverged: AUDIT finds issues, RECONCILE agrees to
// all → converged in one round, no re-audit needed.
func TestDebateLoopSingleRoundConverged(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: debate loop/1-round converged")
	beadID, _ := seedBead(t, d, -1, "B01")

	seedAuditComplete(t, d, -1, auditFindingsJSON, "2026-01-01T00:00:00Z")

	reconcileJob := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h := &ReconcileDecomposition{lastCritique: auditFindingsJSON, lastRoundsSoFar: 0}
	inTx(t, d, func(tx *sql.Tx) error {
		return h.Commit(ctx, tx, reconcileJob, ReconcileDecompositionOutput{
			Responses: []ReconcileResponse{
				{
					BeadTitle: "B01", Action: "agree_and_fix",
					Reason:      "corrected stride to 4 bytes/pixel",
					UpdatedBead: &ParsedBead{Title: "B01", FullText: "fixed", ExecutionBudget: 300, MonitorOverride: "honor"},
				},
			},
			UpdatedBeads: []ParsedBead{
				{Title: "B01", FullText: "fixed", ExecutionBudget: 300, MonitorOverride: "honor"},
			},
		})
	})

	var outcome string
	if err := d.QueryRowContext(ctx,
		`SELECT outcome FROM audit_reconcile_rounds WHERE project_id = -1`,
	).Scan(&outcome); err != nil {
		t.Fatalf("round row missing: %v", err)
	}
	if outcome != "converged" {
		t.Errorf("outcome = %q, want converged", outcome)
	}
	// EXECUTE_BEAD enqueued immediately (no second AUDIT needed).
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = 'EXECUTE_BEAD' AND bead_id = ?`, beadID); n != 1 {
		t.Errorf("EXECUTE_BEAD jobs = %d, want 1", n)
	}
	// No re-enqueued AUDIT job.
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = 'AUDIT_DECOMPOSITION' AND status = 'pending'`); n != 0 {
		t.Errorf("pending AUDIT jobs after convergence = %d, want 0", n)
	}
}
