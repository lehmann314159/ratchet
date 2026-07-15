package verbs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ratchet/internal/db"
)

// seedRecurringFailureExecution inserts one execution + analyses row for
// beadID with a trace showing a successful non-test write (so
// recurringTestFailureNote's wroteImpl gate passes) and mechanical_findings
// containing the given "--- FAIL:" subtest names. order controls creation
// order (and thus id, which the query sorts DESC on) across calls.
func seedRecurringFailureExecution(t *testing.T, d *db.DB, dir string, beadID, revID int64, order int, failNames ...string) {
	t.Helper()
	ctx := context.Background()

	tracePath := filepath.Join(dir, fmt.Sprintf("trace-%d.log", order))
	trace := "[TURN 1]\n" +
		"[tool: write_file map[content:package main] path:game.go]]\n" +
		"[result]\n" +
		"ok: wrote game.go\n"
	if err := os.WriteFile(tracePath, []byte(trace), 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	var findings strings.Builder
	for _, name := range failNames {
		findings.WriteString("--- FAIL: " + name + "\n")
	}

	startedAt := fmt.Sprintf("2026-01-%02dT00:00:00Z", order)
	res, err := d.ExecContext(ctx, `
		INSERT INTO executions
		  (project_id, bead_id, bead_revision_id, trace_path, termination_cause,
		   monitor_fired, monitor_honored, started_at, ended_at)
		VALUES (-1, ?, ?, ?, 'success', 0, 1, ?, ?)`,
		beadID, revID, tracePath, startedAt, startedAt)
	if err != nil {
		t.Fatalf("seed execution: %v", err)
	}
	execID, _ := res.LastInsertId()

	if _, err := d.ExecContext(ctx, `
		INSERT INTO analyses (project_id, execution_id, mechanical_findings, analyzer_interpretation, created_at)
		VALUES (-1, ?, ?, '', ?)`,
		execID, findings.String(), startedAt); err != nil {
		t.Fatalf("seed analysis: %v", err)
	}
}

func TestRecurringTestFailureNote(t *testing.T) {
	t.Run("identical subtest failing twice produces an advisory note, not a command", func(t *testing.T) {
		d := openTestDB(t)
		seedProject(t, d, -1, "recurring-failure-fixture")
		beadID, revID := seedBead(t, d, -1, "B01")
		dir := t.TempDir()

		// order 1 then 2 — later order sorts later (higher id), matching the
		// query's ORDER BY e.id DESC LIMIT 2, so the two most recent attempts
		// are exactly these two, both failing the same subtest.
		seedRecurringFailureExecution(t, d, dir, beadID, revID, 1, "TestFoo/Bar")
		seedRecurringFailureExecution(t, d, dir, beadID, revID, 2, "TestFoo/Bar")

		note := recurringTestFailureNote(context.Background(), d, beadID)
		if note == "" {
			t.Fatal("expected a non-empty note for an identically-recurring subtest failure")
		}
		if !strings.Contains(note, "TestFoo/Bar") {
			t.Errorf("note = %q, want it to name the recurring subtest", note)
		}
		if strings.Contains(note, "Action: issue decision=re_refine, not execute_revised") {
			t.Error("note still contains the old unconditional command — should be advisory")
		}
		if !strings.Contains(note, "execute_revised") {
			t.Error("note should still mention execute_revised as a valid option when an implementation fix can be named")
		}
	})

	t.Run("no shared failure across the two most recent revising attempts produces no note", func(t *testing.T) {
		d := openTestDB(t)
		seedProject(t, d, -1, "recurring-failure-fixture-2")
		beadID, revID := seedBead(t, d, -1, "B01")
		dir := t.TempDir()

		seedRecurringFailureExecution(t, d, dir, beadID, revID, 1, "TestFoo/Bar")
		seedRecurringFailureExecution(t, d, dir, beadID, revID, 2, "TestFoo/Baz")

		note := recurringTestFailureNote(context.Background(), d, beadID)
		if note != "" {
			t.Errorf("note = %q, want empty — the two attempts share no failing subtest", note)
		}
	})
}
