package verbs

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ratchet/internal/db"
)

// traceWithWrite is a minimal trace containing one write_file call.
const traceWithWrite = "[TURN 1]\n" +
	"[tool: write_file map[content:package main] path:game.go]]\n" +
	"[result]\nok: wrote game.go\n"

// traceNoWrite is a trace where the agent only read and never wrote — the
// planning-spiral / unsatisfiable-locked-test shape.
const traceNoWrite = "[TURN 1]\n" +
	"[tool: read_file map[path:grammar_test.go]]\n" +
	"[result]\n(file contents)\n"

// seedStalledExecTrace inserts a stalled execution whose trace file at
// dir/name holds the given content, and returns nothing (the note reads the
// latest execution by id).
func seedStalledExecTrace(t *testing.T, d *db.DB, projectID, beadID, revID int64, dir, name, traceContent string) {
	t.Helper()
	tracePath := filepath.Join(dir, name)
	if err := os.WriteFile(tracePath, []byte(traceContent), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO executions
		  (project_id, bead_id, bead_revision_id, trace_path,
		   termination_cause, monitor_fired, monitor_honored, infra_failure, test_first_attempt,
		   started_at, ended_at)
		VALUES (?, ?, ?, ?, 'stalled', NULL, 0, 0, 0,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z')`,
		projectID, beadID, revID, tracePath); err != nil {
		t.Fatalf("seedStalledExecTrace: %v", err)
	}
}

func seedRefinementRow(t *testing.T, d *db.DB, projectID, beadID int64) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
		INSERT INTO test_refinements
		  (project_id, bead_id, cycle_id, turn, verb, changed, summary, decision, created_at)
		VALUES (?, ?, 1, 1, 'REFINE_TESTS_CRITIQUE', 0, 'x', '', '2026-01-01T00:00:00Z')`,
		projectID, beadID); err != nil {
		t.Fatalf("seedRefinementRow: %v", err)
	}
}

const reRefineBranchMarker = "choose re_refine and put in re_refine_guidance"

func TestStalledExecutionNote_ReRefineBranch(t *testing.T) {
	newFixture := func(t *testing.T) (*db.DB, int64, int64, string) {
		d := openTestDB(t)
		seedProject(t, d, -1, "fixture: re_refine branch")
		beadID, revID := seedBead(t, d, -1, "grammar")
		return d, beadID, revID, t.TempDir()
	}
	ctx := context.Background()

	t.Run("first stall + refinement bead + nothing written -> branch present", func(t *testing.T) {
		d, beadID, revID, dir := newFixture(t)
		seedRefinementRow(t, d, -1, beadID)
		seedStalledExecTrace(t, d, -1, beadID, revID, dir, "t.log", traceNoWrite)
		note := stalledExecutionNote(ctx, d, beadID)
		if !strings.Contains(note, reRefineBranchMarker) {
			t.Errorf("expected the re_refine branch, got:\n%s", note)
		}
	})

	t.Run("second consecutive stall -> branch suppressed", func(t *testing.T) {
		d, beadID, revID, dir := newFixture(t)
		seedRefinementRow(t, d, -1, beadID)
		seedStalledExecTrace(t, d, -1, beadID, revID, dir, "t1.log", traceNoWrite)
		seedStalledExecTrace(t, d, -1, beadID, revID, dir, "t2.log", traceNoWrite)
		note := stalledExecutionNote(ctx, d, beadID)
		if strings.Contains(note, reRefineBranchMarker) {
			t.Errorf("branch must be suppressed on the second consecutive stall:\n%s", note)
		}
		if !strings.Contains(note, "Stalled execution") {
			t.Errorf("base note should still be present:\n%s", note)
		}
	})

	t.Run("not a refinement bead -> branch absent", func(t *testing.T) {
		d, beadID, revID, dir := newFixture(t)
		seedStalledExecTrace(t, d, -1, beadID, revID, dir, "t.log", traceNoWrite)
		if strings.Contains(stalledExecutionNote(ctx, d, beadID), reRefineBranchMarker) {
			t.Error("branch must not appear for a non-REFINE_TESTS bead")
		}
	})

	t.Run("refinement bead but a write_file happened -> branch absent", func(t *testing.T) {
		d, beadID, revID, dir := newFixture(t)
		seedRefinementRow(t, d, -1, beadID)
		seedStalledExecTrace(t, d, -1, beadID, revID, dir, "t.log", traceWithWrite)
		if strings.Contains(stalledExecutionNote(ctx, d, beadID), reRefineBranchMarker) {
			t.Error("branch must not appear when the agent began writing")
		}
	})
}

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
	if !strings.Contains(note, "Stalled execution") || !strings.Contains(note, "NOT a wall-clock problem") {
		t.Errorf("stalled note missing expected guidance:\n%s", note)
	}
}

func TestTimeoutExecutionNote(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1, "fixture: timeout note")
	beadID, revID := seedBead(t, d, -1, "B01")
	zero := 0

	if note := timeoutExecutionNote(ctx, d, beadID); note != "" {
		t.Fatalf("no executions: note = %q, want empty", note)
	}
	seedExecution(t, d, -1, beadID, revID, "stalled", &zero)
	if note := timeoutExecutionNote(ctx, d, beadID); note != "" {
		t.Errorf("latest is stalled: timeout note should be empty, got %q", note)
	}
	seedExecution(t, d, -1, beadID, revID, "timeout", &zero)
	note := timeoutExecutionNote(ctx, d, beadID)
	if !strings.Contains(note, "Timed-out execution") ||
		!strings.Contains(note, "SCOPE") ||
		!strings.Contains(note, "no budget to increase") {
		t.Errorf("timeout note missing expected guidance:\n%s", note)
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

// TestAdjudicateClassifiesCeilingEscalation (B3c): when the specificity ratchet
// has already run (>=2 ADJUDICATE-authored bead_revisions) and the bead stalls
// again, the escalation is tagged "exceeds the EXECUTE model's ceiling" in the
// bead report — a self-labelling signal for burn-in triage. Control flow is
// unchanged (still escalates, no revision, no EXECUTE job).
func TestAdjudicateClassifiesCeilingEscalation(t *testing.T) {
	run := func(t *testing.T, adjRevs int) string {
		d := openTestDB(t)
		ctx := context.Background()
		seedProject(t, d, -1, "fixture: ceiling escalation classification")
		beadID, revID := seedBead(t, d, -1, "B01")
		zero := 0
		seedExecution(t, d, -1, beadID, revID, "stalled", &zero)
		seedExecution(t, d, -1, beadID, revID, "stalled", &zero)
		for i := 0; i < adjRevs; i++ {
			if _, err := d.ExecContext(ctx, `
				INSERT INTO bead_revisions
				  (project_id, bead_id, revision_number, full_text, execution_budget,
				   monitor_override, created_by_verb, created_at)
				VALUES (?, ?, ?, '{"title":"B01"}', 300, 'honor', ?, '2026-01-01T00:00:00Z')`,
				-1, beadID, 100+i, db.VerbAdjudicateNextExecution); err != nil {
				t.Fatal(err)
			}
		}
		folder := t.TempDir()
		job := seedJob(t, d, -1, db.VerbAdjudicateNextExecution, sql.NullInt64{Int64: beadID, Valid: true})
		out := AdjudicateNextExecutionOutput{
			Trend: "same", BeadSpecFit: "bead_problem",
			Reasoning: "stalled again",
			Decision:  "execute_revised",
			RevisedBead: &ParsedBead{
				Title: "B01", FullText: "narrower spec", ExecutionBudget: 300, MonitorOverride: "honor",
				OutputFiles: []string{"game.go"}, ExitCriteria: []string{"test -f game.go"},
			},
		}
		inTx(t, d, func(tx *sql.Tx) error {
			return (&AdjudicateNextExecution{trailingStalls: 2, budgetDefault: 300, folderPath: folder}).Commit(ctx, tx, job, out)
		})

		var status string
		if err := d.QueryRowContext(ctx, `SELECT status FROM handoff_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "escalated" {
			t.Fatalf("job status = %q, want escalated", status)
		}
		if n := countRows(t, d, `SELECT COUNT(*) FROM handoff_jobs WHERE verb = ? AND bead_id = ?`, db.VerbExecuteBead, beadID); n != 0 {
			t.Errorf("no EXECUTE_BEAD job should be enqueued on escalation, got %d", n)
		}
		data, err := os.ReadFile(filepath.Join(folder, "traces", "bead-1-report.md"))
		if err != nil {
			t.Fatalf("bead report not written: %v", err)
		}
		return string(data)
	}

	t.Run("ratchet ran → ceiling classification", func(t *testing.T) {
		if got := run(t, 2); !strings.Contains(got, "exceeds the EXECUTE model's ceiling") {
			t.Errorf("report missing the ceiling classification:\n%s", got)
		}
	})
	t.Run("ratchet did not run → plain escalation", func(t *testing.T) {
		got := run(t, 0)
		if strings.Contains(got, "exceeds the EXECUTE model's ceiling") {
			t.Errorf("report should not claim ceiling with <2 ADJUDICATE revisions:\n%s", got)
		}
		if !strings.Contains(got, "escalated") {
			t.Errorf("report should still record the escalation:\n%s", got)
		}
	})
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
