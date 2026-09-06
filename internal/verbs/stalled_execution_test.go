package verbs

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"ratchet/internal/db"
)

func TestCountTrailingStalls(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: trailing stalls")
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0

	if n := countTrailingStalls(ctx, d, beadID); n != 0 {
		t.Fatalf("no executions yet: countTrailingStalls = %d, want 0", n)
	}

	seedExecution(t, d, -1, beadID, revID, "stalled", &zero)
	seedExecution(t, d, -1, beadID, revID, "stalled", &zero)
	if n := countTrailingStalls(ctx, d, beadID); n != 2 {
		t.Errorf("two consecutive stalls: countTrailingStalls = %d, want 2", n)
	}

	// A newer non-stall execution breaks the run.
	seedExecution(t, d, -1, beadID, revID, "success", &zero)
	if n := countTrailingStalls(ctx, d, beadID); n != 0 {
		t.Errorf("latest is success: countTrailingStalls = %d, want 0", n)
	}
}

func TestCountTrailingTimeoutsIgnoresStalled(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: stalled vs timeout")
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0
	seedExecution(t, d, -1, beadID, revID, "timeout", &zero)
	seedExecution(t, d, -1, beadID, revID, "stalled", &zero) // latest

	if n := countTrailingTimeouts(ctx, d, beadID); n != 0 {
		t.Errorf("countTrailingTimeouts = %d, want 0 — a stalled latest execution must not count as a timeout", n)
	}
}

func TestStalledExecutionNote(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: stalled note")
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0

	if note := stalledExecutionNote(ctx, d, beadID); note != "" {
		t.Fatalf("no executions: note = %q, want empty", note)
	}
	seedExecution(t, d, -1, beadID, revID, "timeout", &zero)
	if note := stalledExecutionNote(ctx, d, beadID); note != "" {
		t.Errorf("latest is timeout: note should be empty, got %q", note)
	}
	seedExecution(t, d, -1, beadID, revID, "stalled", &zero)
	note := stalledExecutionNote(ctx, d, beadID)
	if !strings.Contains(note, "Stalled execution") || !strings.Contains(note, "must not be") {
		t.Errorf("stalled note missing expected guidance:\n%s", note)
	}
}

func TestAdjudicateEscalatesOnSecondConsecutiveStall(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: repeated stall escalation")
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0
	seedExecution(t, d, -1, beadID, revID, "stalled", &zero)
	seedExecution(t, d, -1, beadID, revID, "stalled", &zero)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend: "same", BeadSpecFit: "bead_problem",
		Reasoning: "stalled again; narrowing the spec",
		Decision:  "execute_revised",
		RevisedBead: &ParsedBead{
			Title: "B01", FullText: "narrower spec", ExecutionBudget: 300, MonitorOverride: "honor",
			OutputFiles: []string{"game.go"}, ExitCriteria: []string{"test -f game.go"},
		},
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{trailingStalls: 2, budgetDefault: 300}).Commit(ctx, tx, job, out)
	})

	var status string
	if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
		t.Fatalf("job row missing: %v", err)
	}
	if status != "escalated" {
		t.Errorf("job status = %q, want escalated", status)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM bead_revisions WHERE bead_id = ? AND revision_number = 2`, beadID); n != 0 {
		t.Errorf("no revised bead_revision should be written on escalation, got %d", n)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE verb = ? AND bead_id = ?`, db.VerbExecuteBead, beadID); n != 0 {
		t.Errorf("no EXECUTE_BEAD job should be enqueued on escalation, got %d", n)
	}
}

func TestAdjudicateSingleStallRetriesWithoutBudgetDoubling(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: single stall retry")
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0
	seedExecution(t, d, -1, beadID, revID, "stalled", &zero)

	job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
	out := AdjudicateNextExecutionOutput{
		Trend: "narrower", BeadSpecFit: "bead_problem",
		Reasoning: "first stall; narrowing the spec to the parser only",
		Decision:  "execute_revised",
		RevisedBead: &ParsedBead{
			Title: "B01", FullText: "narrower spec", ExecutionBudget: 300, MonitorOverride: "honor",
			OutputFiles: []string{"game.go"}, ExitCriteria: []string{"test -f game.go"},
		},
	}
	inTx(t, d, func(tx *sql.Tx) error {
		return (&AdjudicateNextExecution{trailingStalls: 1, budgetDefault: 300}).Commit(ctx, tx, job, out)
	})

	var status string
	_ = d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&status)
	if status == "escalated" {
		t.Fatalf("a single stall must not escalate")
	}
	var budget int
	if err := d.QueryRowContext(ctx, `
		SELECT br.execution_budget FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id WHERE b.id = ?`, beadID).Scan(&budget); err != nil {
		t.Fatalf("current revision budget: %v", err)
	}
	if budget != 300 {
		t.Errorf("execution_budget = %d, want 300 — a stall must NOT double the budget like a timeout", budget)
	}
	if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE verb = ? AND bead_id = ?`, db.VerbExecuteBead, beadID); n != 1 {
		t.Errorf("EXECUTE_BEAD jobs = %d, want 1", n)
	}
}
