package verbs

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"ratchet/internal/db"
)

// TestMergeProposedBeadsAppliesAgreeAndFixOnly confirms mergeProposedBeads
// substitutes only the beads with an agree_and_fix response, preserving dispatch
// order and leaving untouched beads (including a disagree response, which has
// no UpdatedBead) exactly as they were.
func TestMergeProposedBeadsAppliesAgreeAndFixOnly(t *testing.T) {
	current := []beadState{
		{Title: "templates", FullText: "old templates text", OutputFiles: []string{"templates/index.html", "templates/board.html", "templates_test.go"}},
		{Title: "http-handlers", FullText: "old http-handlers text", OutputFiles: []string{"handlers.go", "main.go"}},
	}
	out := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{
			{BeadTitle: "templates", Action: "agree_and_fix", UpdatedBead: &ParsedBead{
				Title: "templates", FullText: "new templates text calling templates/index.html", OutputFiles: []string{"templates_test.go"},
			}},
			{BeadTitle: "http-handlers", Action: "disagree", Reason: "no change needed"},
		},
	}

	merged := mergeProposedBeads(current, out)
	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2", len(merged))
	}
	if merged[0].Title != "templates" || merged[0].FullText != "new templates text calling templates/index.html" {
		t.Errorf("templates not substituted with updated_bead: %+v", merged[0])
	}
	if merged[1].Title != "http-handlers" || merged[1].FullText != "old http-handlers text" {
		t.Errorf("http-handlers (disagree, no updated_bead) should be unchanged: %+v", merged[1])
	}
}

// checkersV9Beads reconstructs the exact bead set (title/order/output_files)
// present just before checkers-v9's (project 99) real RECONCILE round 1 — see
// project_fixture_clone_design.md / the escalation investigation for how this
// was pulled from the live DB. templates (index 7) precedes http-handlers
// (index 8) and correctly owns the two html files at this point.
func checkersV9Beads() []beadState {
	return []beadState{
		{Title: "layout", FullText: "layout spec", OutputFiles: []string{"game.go", "ai.go", "handlers.go", "main.go", "go.mod", "do_not_use_this_test.go"}},
		{Title: "game-state", FullText: "game-state spec", OutputFiles: []string{"game.go", "game_test.go"}},
		{Title: "move-generation", FullText: "move-generation spec", OutputFiles: []string{"game.go", "game_test.go"}},
		{Title: "move-execution", FullText: "move-execution spec", OutputFiles: []string{"game.go", "game_test.go"}},
		{Title: "win-detection", FullText: "win-detection spec", OutputFiles: []string{"game.go", "game_test.go"}},
		{Title: "game-integration", FullText: "game-integration spec", OutputFiles: []string{"game_test.go"}},
		{Title: "ai", FullText: "ai spec", OutputFiles: []string{"ai.go", "ai_test.go"}},
		{Title: "templates", FullText: "Create templates/board.html and templates/index.html.", OutputFiles: []string{"templates/index.html", "templates/board.html", "templates_test.go"}},
		{Title: "http-handlers", FullText: "Load templates/index.html and templates/board.html from disk.", OutputFiles: []string{"handlers.go", "main.go", "handlers_test.go"}},
	}
}

// TestReconcileDecompositionCommitRejectsOwnForwardReferenceViolation is the
// direct regression test for the checkers-v9 escalation: RECONCILE proposes
// moving the two html files from "templates" onto "http-handlers" without
// reordering the beads, which reintroduces the exact forward-reference
// violation forwardFileReferenceChecks exists to catch. Commit must reject
// this before writing any bead_revisions, not commit it and let AUDIT
// discover the symptom rounds later.
func TestReconcileDecompositionCommitRejectsOwnForwardReferenceViolation(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: RECONCILE_DECOMPOSITION commit/reject own violation")
	templatesID, _ := seedBead(t, d, -1, "templates")
	handlersID, _ := seedBead(t, d, -1, "http-handlers")

	job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h := &ReconcileDecomposition{
		lastCritique:    "AUDIT claims http-handlers precedes templates",
		lastRoundsSoFar: 0,
		lastBeads:       checkersV9Beads(),
	}
	out := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{
			{BeadTitle: "http-handlers", Action: "agree_and_fix", Reason: "move template creation into http-handlers", UpdatedBead: &ParsedBead{
				Title: "http-handlers", FullText: "Create templates/board.html and templates/index.html, then load them.",
				OutputFiles: []string{"handlers.go", "main.go", "handlers_test.go", "templates/index.html", "templates/board.html"},
			}},
			{BeadTitle: "templates", Action: "agree_and_fix", Reason: "templates now only verifies parsing", UpdatedBead: &ParsedBead{
				Title: "templates", FullText: "Write TestTemplatesParse calling template.ParseFiles(\"templates/index.html\", \"templates/board.html\").",
				OutputFiles: []string{"templates_test.go"},
			}},
		},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	var outcome, critique string
	if err := d.QueryRowContext(ctx,
		`SELECT outcome, critique_text FROM audit_reconcile_rounds WHERE project_id = -1 ORDER BY id DESC LIMIT 1`,
	).Scan(&outcome, &critique); err != nil {
		t.Fatalf("expected a rejection row, found none: %v", err)
	}
	if outcome != "reconcile_rejected" {
		t.Errorf("outcome = %q, want reconcile_rejected", outcome)
	}
	if !strings.Contains(critique, "templates") || !strings.Contains(critique, "http-handlers") {
		t.Errorf("critique_text doesn't mention the violating beads: %q", critique)
	}

	// Neither bead should have gained a new revision — the rejected fix must
	// never reach applyFixes.
	if n := countRows(t, d, `SELECT COUNT(*) FROM bead_revisions WHERE bead_id IN (?, ?) AND revision_number > 1`, templatesID, handlersID); n != 0 {
		t.Errorf("bead_revisions beyond revision 1 = %d, want 0 (rejected fix must not be applied)", n)
	}

	// A retry RECONCILE_DECOMPOSITION job must be enqueued (not AUDIT_DECOMPOSITION).
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ? AND status = 'pending'`, db.VerbReconcileDecomposition); n != 1 {
		t.Errorf("pending RECONCILE_DECOMPOSITION retry jobs = %d, want 1", n)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ?`, db.VerbAuditDecomposition); n != 0 {
		t.Errorf("AUDIT_DECOMPOSITION jobs = %d, want 0 (this is not a real debate round)", n)
	}

	// The original job itself must not be left running or marked escalated below cap.
	var jobStatus string
	if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&jobStatus); err != nil {
		t.Fatalf("query original job status: %v", err)
	}
	if jobStatus == "escalated" {
		t.Errorf("original job escalated on the first rejection, want retry (cap is %d)", reconcileRejectCap)
	}
}

// TestReconcileDecompositionCommitRejectEscalatesAtCap confirms the retry
// loop is bounded: once reconcileRejectCap prior 'reconcile_rejected' rows
// already exist, the next violation escalates the job instead of enqueueing
// another retry.
func TestReconcileDecompositionCommitRejectEscalatesAtCap(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: RECONCILE_DECOMPOSITION commit/reject at cap")
	seedBead(t, d, -1, "templates")
	seedBead(t, d, -1, "http-handlers")

	for i := 0; i < reconcileRejectCap; i++ {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO audit_reconcile_rounds (project_id, round_number, critique_text, reconciliation, outcome, created_at)
			VALUES (-1, ?, 'prior rejection', '{}', 'reconcile_rejected', '2026-01-01T00:00:00Z')`, i+1); err != nil {
			t.Fatalf("seed prior rejection %d: %v", i, err)
		}
	}

	job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h := &ReconcileDecomposition{
		lastCritique:    "AUDIT claims http-handlers precedes templates",
		lastRoundsSoFar: 0,
		lastBeads:       checkersV9Beads(),
	}
	out := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{
			{BeadTitle: "http-handlers", Action: "agree_and_fix", Reason: "still wrong", UpdatedBead: &ParsedBead{
				Title: "http-handlers", FullText: "Create templates/board.html and templates/index.html, then load them.",
				OutputFiles: []string{"handlers.go", "main.go", "handlers_test.go", "templates/index.html", "templates/board.html"},
			}},
			{BeadTitle: "templates", Action: "agree_and_fix", Reason: "still wrong", UpdatedBead: &ParsedBead{
				Title: "templates", FullText: "Write TestTemplatesParse calling template.ParseFiles(\"templates/index.html\", \"templates/board.html\").",
				OutputFiles: []string{"templates_test.go"},
			}},
		},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	var jobStatus string
	if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&jobStatus); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	if jobStatus != "escalated" {
		t.Errorf("job status = %q, want escalated (cap %d reached)", jobStatus, reconcileRejectCap)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE project_id = -1 AND verb = ? AND status = 'pending'`, db.VerbReconcileDecomposition); n != 0 {
		t.Errorf("pending RECONCILE_DECOMPOSITION retry jobs = %d, want 0 (should escalate, not retry, at cap)", n)
	}
}

// TestReconcileDecompositionCommitCleanFixStillConverges is the regression
// guard: a normal agree_and_fix that does not introduce any forward-reference
// violation must still converge exactly as before this change, with
// lastBeads populated (unlike the pre-existing converged/disagreed tests,
// which leave it nil and so never exercise the new check at all).
func TestReconcileDecompositionCommitCleanFixStillConverges(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: RECONCILE_DECOMPOSITION commit/clean fix still converges")
	beadID, _ := seedBead(t, d, -1, "B01")

	job := seedJob(t, d, -1, db.VerbReconcileDecomposition, sql.NullInt64{})
	h := &ReconcileDecomposition{
		lastCritique:    "the stride formula uses 3 bytes/pixel; NRGBA is always 4",
		lastRoundsSoFar: 0,
		lastBeads:       []beadState{{Title: "B01", FullText: "spec for B01", OutputFiles: []string{"img.go"}}},
	}
	out := ReconcileDecompositionOutput{
		Responses: []ReconcileResponse{
			{BeadTitle: "B01", Action: "agree_and_fix", Reason: "correct finding", UpdatedBead: &ParsedBead{
				Title: "B01", FullText: "revised with stride=4", ExecutionBudget: 300, MonitorOverride: "honor", OutputFiles: []string{"img.go"},
			}},
		},
	}
	inTx(t, d, func(tx *sql.Tx) error { return h.Commit(ctx, tx, job, out) })

	var outcome string
	if err := d.QueryRowContext(ctx,
		`SELECT outcome FROM audit_reconcile_rounds WHERE project_id = -1 AND round_number = 1`,
	).Scan(&outcome); err != nil {
		t.Fatalf("audit_reconcile_rounds row missing: %v", err)
	}
	if outcome != "converged" {
		t.Errorf("outcome = %q, want converged (a clean fix must not be mistaken for a violation)", outcome)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM bead_revisions WHERE bead_id = ? AND revision_number = 2`, beadID); n != 1 {
		t.Errorf("bead_revisions rev2 = %d, want 1 (clean agree_and_fix must still be applied)", n)
	}
}