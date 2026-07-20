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
	seedRecurringFailureExecutionWithOutput(t, d, dir, beadID, revID, order, nil, failNames...)
}

// seedRecurringFailureExecutionWithOutput is seedRecurringFailureExecution
// plus an optional map of subtest name -> the "=== RUN"-block output text
// go test -v would have printed for that subtest, so tests can exercise
// extractTestOutput's byte-identical-content comparison. A name with no
// entry in outputByName gets no "=== RUN" block at all (matching the
// no-output-captured shape the original tests seed).
func seedRecurringFailureExecutionWithOutput(t *testing.T, d *db.DB, dir string, beadID, revID int64, order int, outputByName map[string]string, failNames ...string) {
	t.Helper()
	seedRecurringFailureExecutionWithOutputAndImpl(t, d, dir, beadID, revID, order, outputByName, nil, failNames...)
}

// seedRecurringFailureExecutionWithOutputAndImpl is
// seedRecurringFailureExecutionWithOutput plus an optional map of relative
// path -> full file content, embedded in checkOutputFiles' exact format
// ("<path>: present (N bytes)\n```\n<content>\n```\n") so tests can exercise
// implementationChangedBetweenAttempts.
func seedRecurringFailureExecutionWithOutputAndImpl(t *testing.T, d *db.DB, dir string, beadID, revID int64, order int, outputByName, implContent map[string]string, failNames ...string) {
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
		if out, ok := outputByName[name]; ok {
			findings.WriteString("=== RUN   " + name + "\n" + out + "\n")
		}
	}
	for _, name := range failNames {
		findings.WriteString("--- FAIL: " + name + "\n")
	}
	if len(implContent) > 0 {
		findings.WriteString("\n\n## Output Files (at analysis time)\n\n")
		for path, content := range implContent {
			fmt.Fprintf(&findings, "%s: present (%d bytes)\n```\n%s\n```\n", path, len(content), content)
		}
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

		note, _, _ := recurringTestFailureNote(context.Background(), d, beadID)
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

		note, _, _ := recurringTestFailureNote(context.Background(), d, beadID)
		if note != "" {
			t.Errorf("note = %q, want empty — the two attempts share no failing subtest", note)
		}
	})

	t.Run("byte-identical failure output across two attempts gets the stronger note", func(t *testing.T) {
		d := openTestDB(t)
		seedProject(t, d, -1, "recurring-failure-fixture-3")
		beadID, revID := seedBead(t, d, -1, "B01")
		dir := t.TempDir()

		sameOutput := map[string]string{
			"TestAllValidMoves/BlockedPieces": "    game_test.go:194: expected no moves for blocked piece, got [{{4 1} {3 0} []}]",
		}
		seedRecurringFailureExecutionWithOutput(t, d, dir, beadID, revID, 1, sameOutput, "TestAllValidMoves/BlockedPieces")
		seedRecurringFailureExecutionWithOutput(t, d, dir, beadID, revID, 2, sameOutput, "TestAllValidMoves/BlockedPieces")

		note, _, _ := recurringTestFailureNote(context.Background(), d, beadID)
		if !strings.Contains(note, "identical output") {
			t.Errorf("note = %q, want the stronger [Recurring test failure — identical output] variant", note)
		}
		if !strings.Contains(note, "TestAllValidMoves/BlockedPieces") {
			t.Errorf("note = %q, want it to name the recurring subtest", note)
		}
		if !strings.Contains(note, "expected no moves for blocked piece") {
			t.Errorf("note = %q, want it to quote the actual repeated failure text", note)
		}
	})

	t.Run("same subtest name but different failure output gets the weaker advisory note", func(t *testing.T) {
		d := openTestDB(t)
		seedProject(t, d, -1, "recurring-failure-fixture-4")
		beadID, revID := seedBead(t, d, -1, "B01")
		dir := t.TempDir()

		seedRecurringFailureExecutionWithOutput(t, d, dir, beadID, revID, 1,
			map[string]string{"TestFoo/Bar": "    game_test.go:10: got 1, want 2"},
			"TestFoo/Bar")
		seedRecurringFailureExecutionWithOutput(t, d, dir, beadID, revID, 2,
			map[string]string{"TestFoo/Bar": "    game_test.go:10: got 3, want 2"},
			"TestFoo/Bar")

		note, _, _ := recurringTestFailureNote(context.Background(), d, beadID)
		if strings.Contains(note, "identical output") {
			t.Errorf("note = %q, want the weaker variant since the failure text differs between attempts", note)
		}
		if !strings.Contains(note, "TestFoo/Bar") {
			t.Errorf("note = %q, want it to name the recurring subtest", note)
		}
	})

	// The next two cases cover the 2026-07-20 exprvm-web-v1 bead 33 incident
	// and the reconciliation with the earlier (Stage 6 audit) walk-back of an
	// unconditional "issue decision=re_refine" command — see
	// implementationChangedBetweenAttempts' doc comment. Byte-identical test
	// failure output alone must not be enough to force anything; it must also
	// be paired with genuinely different generated implementation code.

	t.Run("identical failure with genuinely different implementation is eligible for forcing", func(t *testing.T) {
		d := openTestDB(t)
		seedProject(t, d, -1, "recurring-failure-fixture-5")
		beadID, revID := seedBead(t, d, -1, "B01")
		dir := t.TempDir()

		sameOutput := map[string]string{
			"TestTemplates/OutputOnly": "    templates_test.go:103: expected \"1 + 1\" in output",
		}
		seedRecurringFailureExecutionWithOutputAndImpl(t, d, dir, beadID, revID, 1, sameOutput,
			map[string]string{"templates.go": "package main\n\nfunc InitTemplates() {}\n"},
			"TestTemplates/OutputOnly")
		seedRecurringFailureExecutionWithOutputAndImpl(t, d, dir, beadID, revID, 2, sameOutput,
			map[string]string{"templates.go": "package main\n\n// rewritten from scratch\nfunc InitTemplates() { _ = 1 }\n"},
			"TestTemplates/OutputOnly")

		note, names, text := recurringTestFailureNote(context.Background(), d, beadID)
		if !strings.Contains(note, "identical output") {
			t.Fatalf("note = %q, want the stronger identical-output variant", note)
		}
		if len(names) != 1 || names[0] != "TestTemplates/OutputOnly" {
			t.Errorf("forced names = %v, want exactly [TestTemplates/OutputOnly] since the implementation genuinely differs", names)
		}
		if text["TestTemplates/OutputOnly"] == "" {
			t.Error("forced text map missing the subtest's captured failure text")
		}
	})

	t.Run("identical failure with unchanged implementation is not eligible for forcing", func(t *testing.T) {
		d := openTestDB(t)
		seedProject(t, d, -1, "recurring-failure-fixture-6")
		beadID, revID := seedBead(t, d, -1, "B01")
		dir := t.TempDir()

		sameOutput := map[string]string{
			"TestTemplates/OutputOnly": "    templates_test.go:103: expected \"1 + 1\" in output",
		}
		sameImpl := map[string]string{"templates.go": "package main\n\nfunc InitTemplates() {}\n"}
		seedRecurringFailureExecutionWithOutputAndImpl(t, d, dir, beadID, revID, 1, sameOutput, sameImpl, "TestTemplates/OutputOnly")
		seedRecurringFailureExecutionWithOutputAndImpl(t, d, dir, beadID, revID, 2, sameOutput, sameImpl, "TestTemplates/OutputOnly")

		note, names, _ := recurringTestFailureNote(context.Background(), d, beadID)
		if !strings.Contains(note, "identical output") {
			t.Fatalf("note = %q, want the stronger identical-output variant (the note itself is unaffected by the impl gate)", note)
		}
		if len(names) != 0 {
			t.Errorf("forced names = %v, want none — a real unfixed implementation bug the model keeps "+
				"regenerating unchanged must stay advisory-only, per the Stage 6 audit walk-back", names)
		}
	})
}

func TestImplementationChangedBetweenAttempts(t *testing.T) {
	blockA := "templates.go: present (10 bytes)\n```\npackage main\n```\n"
	blockBSame := "templates.go: present (10 bytes)\n```\npackage main\n```\n"
	blockBDiff := "templates.go: present (14 bytes)\n```\npackage other\n```\n"

	if implementationChangedBetweenAttempts(blockA, blockBSame) {
		t.Error("identical content across attempts should report unchanged")
	}
	if !implementationChangedBetweenAttempts(blockA, blockBDiff) {
		t.Error("differing content across attempts should report changed")
	}
	if implementationChangedBetweenAttempts("", "") {
		t.Error("no extractable blocks on either side must default to unchanged (safe default), not changed")
	}
	if implementationChangedBetweenAttempts(blockA, "") {
		t.Error("one side unextractable must default to unchanged (safe default), not changed")
	}
}
