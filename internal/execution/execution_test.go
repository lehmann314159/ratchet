package execution

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ratchet/internal/db"
)

// --- parseDecision ---

func TestParseDecision(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"FIRE", "DECISION: FIRE\nREASON: same failure repeating three times", "FIRE"},
		{"NO_FIRE", "DECISION: NO_FIRE\nREASON: each attempt targets a different bug", "NO_FIRE"},
		{"extra whitespace", "DECISION:  FIRE \nREASON: loop detected", "FIRE"},
		{"FIRE after prose", "some preamble\nDECISION: FIRE\nREASON: yes", "FIRE"},
		{"malformed — no DECISION line", "I think this is a loop.", "NO_FIRE"},
		{"malformed — unknown value", "DECISION: MAYBE\nREASON: unsure", "NO_FIRE"},
		{"empty", "", "NO_FIRE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDecision(tc.input)
			if got != tc.want {
				t.Errorf("parseDecision(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// --- isTestsLockedMode ---

// TestIsTestsLockedMode_PureTestBeadNeverLocks reproduces the Stage 4 audit
// bug: a bead whose output_files is entirely *_test.go (e.g. an
// integration-test bead with no implementation files of its own — 72 such
// beads exist in production data) must never enter "tests locked" mode, even
// if its own test file already exists on disk from a prior attempt. Before
// the fix, this bead shape tripped isTestsLockedMode (built for a REFINE_TESTS
// impl bead retrying against a separately-owned, pre-certified test file),
// producing a contradictory prompt (the file was simultaneously the only
// legal Output File and explicitly "LOCKED, do NOT write") and an empty
// expectedFiles slice.
func TestIsTestsLockedMode_PureTestBeadNeverLocks(t *testing.T) {
	dir := t.TempDir()
	outputFiles := []string{"integration_test.go"}
	if err := os.WriteFile(filepath.Join(dir, "integration_test.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if isTestsLockedMode(dir, outputFiles) {
		t.Error("expected isTestsLockedMode=false for a pure-test bead (no non-test output files) even with its test file already on disk")
	}

	// The user message must not be contradictory: the model must be allowed
	// to write its own test file, and must not see a LOCKED section for it.
	msg := buildBeadUserMsg("spec text", outputFiles, []string{"go test ./..."}, "", "", "", dir)
	if strings.Contains(msg, "LOCKED") {
		t.Errorf("expected no Tests Locked section for a pure-test bead, got:\n%s", msg)
	}
	if !strings.Contains(msg, "integration_test.go") {
		t.Errorf("expected integration_test.go to still be listed as a writable Output File, got:\n%s", msg)
	}
}

// TestIsTestsLockedMode_ImplBeadStillLocks confirms the fix doesn't break the
// intended case: an impl bead with both test and non-test output files, whose
// test file was pre-certified by REFINE_TESTS (already on disk), must still
// lock the test file and restrict writes to the implementation file.
func TestIsTestsLockedMode_ImplBeadStillLocks(t *testing.T) {
	dir := t.TempDir()
	outputFiles := []string{"game.go", "game_test.go"}
	if err := os.WriteFile(filepath.Join(dir, "game_test.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !isTestsLockedMode(dir, outputFiles) {
		t.Error("expected isTestsLockedMode=true for an impl bead whose pre-certified test file already exists on disk")
	}

	msg := buildBeadUserMsg("spec text", outputFiles, []string{"go test ./..."}, "", "", "", dir)
	if !strings.Contains(msg, "LOCKED") {
		t.Errorf("expected a Tests Locked section for an impl bead with a pre-certified test file, got:\n%s", msg)
	}
}

// --- buildMissingPathWarning ---

// TestBuildMissingPathWarning_EmptyExpectedFilesDoesNotPanic guards the crash
// found alongside the isTestsLockedMode bug: expectedFiles[0] was indexed
// unconditionally, which panicked (crashing execute-bead with no
// termination_cause written) whenever expectedFiles was empty. Kept as a
// defensive backstop even though the isTestsLockedMode fix removes the only
// known way to reach this with an empty slice.
func TestBuildMissingPathWarning_EmptyExpectedFilesDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("buildMissingPathWarning(nil) panicked: %v", r)
		}
	}()
	msg := buildMissingPathWarning(nil)
	if msg == "" {
		t.Error("expected a non-empty warning message even with no expected files")
	}
}

// --- DB helpers shared across execution tests ---

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedProject(t *testing.T, d *db.DB, id int64) {
	t.Helper()
	_, err := d.ExecContext(context.Background(), `
		INSERT INTO projects
		  (id, label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, created_at, updated_at)
		VALUES (?, 'fixture: execution test', '/tmp', 'design.md',
		        'active', 'honor', 300, 2,
		        '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id)
	if err != nil {
		t.Fatalf("seedProject: %v", err)
	}
}

func seedBeadWithRevision(t *testing.T, d *db.DB, projectID int64, budget int) (beadID, revID int64) {
	t.Helper()
	ctx := context.Background()
	res, err := d.ExecContext(ctx,
		`INSERT INTO beads (project_id, status, current_revision_id) VALUES (?, 'executing', NULL)`, projectID)
	if err != nil {
		t.Fatalf("seedBead: %v", err)
	}
	beadID, _ = res.LastInsertId()
	res, err = d.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text,
		   execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (?, ?, 1, '{"title":"B01","full_text":"spec"}', ?, 'honor',
		        'DECOMPOSE_SPEC', '2026-01-01T00:00:00Z')`,
		projectID, beadID, budget)
	if err != nil {
		t.Fatalf("seedRevision: %v", err)
	}
	revID, _ = res.LastInsertId()
	_, _ = d.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID)
	return beadID, revID
}

func seedExecution(t *testing.T, d *db.DB, projectID, beadID, revID int64) int64 {
	t.Helper()
	res, err := d.ExecContext(context.Background(), `
		INSERT INTO executions
		  (project_id, bead_id, bead_revision_id, trace_path,
		   monitor_honored, started_at)
		VALUES (?, ?, ?, '/tmp/test-trace.log', 1, '2026-01-01T00:00:00Z')`,
		projectID, beadID, revID)
	if err != nil {
		t.Fatalf("seedExecution: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// --- handleInfraFailure ---
//
// No test exercised this function before the Stage 4 audit, even though it's
// the function whose job-status writes the dispatch.go clobbering bug
// stomped. These tests cover handleInfraFailure in isolation; the clobbering
// itself is covered by TestCompleteExecuteBeadJob_DoesNotClobberInfraFailureRetry
// in internal/orchestrator.

func TestHandleInfraFailure_UnderCapRetriesJob(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, -1)
	beadID, revID := seedBeadWithRevision(t, d, -1, 300)
	execID := seedExecution(t, d, -1, beadID, revID)

	res, err := d.ExecContext(context.Background(), `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (-1, ?, ?, 'running', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		db.VerbExecuteBead, beadID)
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	jobID, _ := res.LastInsertId()
	job := &db.HandoffJob{ID: jobID, ProjectID: -1, Verb: db.VerbExecuteBead, BeadID: sql.NullInt64{Int64: beadID, Valid: true}}

	if err := handleInfraFailure(context.Background(), d, execID, beadID, job); err != nil {
		t.Fatalf("handleInfraFailure: %v", err)
	}

	var execInfraFailure int
	var terminationCause string
	if err := d.QueryRowContext(context.Background(),
		`SELECT infra_failure, termination_cause FROM executions WHERE id = ?`, execID,
	).Scan(&execInfraFailure, &terminationCause); err != nil {
		t.Fatalf("query execution: %v", err)
	}
	if execInfraFailure != 1 || terminationCause != "success" {
		t.Errorf("execution: infra_failure=%d termination_cause=%q, want 1/\"success\"", execInfraFailure, terminationCause)
	}

	var beadStatus string
	if err := d.QueryRowContext(context.Background(),
		`SELECT status FROM beads WHERE id = ?`, beadID,
	).Scan(&beadStatus); err != nil {
		t.Fatalf("query bead: %v", err)
	}
	if beadStatus != "pending" {
		t.Errorf("bead status = %q, want pending", beadStatus)
	}

	var jobStatus string
	if err := d.QueryRowContext(context.Background(),
		`SELECT status FROM handoff_jobs WHERE id = ?`, jobID,
	).Scan(&jobStatus); err != nil {
		t.Fatalf("query job: %v", err)
	}
	if jobStatus != "pending" {
		t.Errorf("job status = %q, want pending (under infraFailureCap, should retry)", jobStatus)
	}
}

func TestHandleInfraFailure_AtCapEscalates(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedProject(t, d, -1)
	beadID, revID := seedBeadWithRevision(t, d, -1, 300)

	// Two prior infra failures already recorded, each with a distinct
	// started_at so the consecutive-failure count query orders correctly.
	for i, startedAt := range []string{"2026-01-01T00:00:00Z", "2026-01-01T00:05:00Z"} {
		res, err := d.ExecContext(ctx, `
			INSERT INTO executions
			  (project_id, bead_id, bead_revision_id, trace_path, monitor_honored,
			   started_at, infra_failure, termination_cause, ended_at)
			VALUES (-1, ?, ?, ?, 1, ?, 1, 'success', ?)`,
			beadID, revID, "/tmp/prior-trace.log", startedAt, startedAt)
		if err != nil {
			t.Fatalf("seed prior infra failure %d: %v", i, err)
		}
		_ = res
	}

	execID := seedExecution(t, d, -1, beadID, revID) // started_at 2026-01-01T00:00:00Z per helper; fine, only ordering vs a real attempt matters

	res, err := d.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (-1, ?, ?, 'running', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		db.VerbExecuteBead, beadID)
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	jobID, _ := res.LastInsertId()
	job := &db.HandoffJob{ID: jobID, ProjectID: -1, Verb: db.VerbExecuteBead, BeadID: sql.NullInt64{Int64: beadID, Valid: true}}

	if err := handleInfraFailure(ctx, d, execID, beadID, job); err != nil {
		t.Fatalf("handleInfraFailure: %v", err)
	}

	var jobStatus string
	if err := d.QueryRowContext(ctx,
		`SELECT status FROM handoff_jobs WHERE id = ?`, jobID,
	).Scan(&jobStatus); err != nil {
		t.Fatalf("query job: %v", err)
	}
	if jobStatus != "escalated" {
		t.Errorf("job status = %q, want escalated (3rd consecutive infra failure, at cap)", jobStatus)
	}
}

// --- writeMonitorFired ---

func TestWriteMonitorFired(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, -1)
	beadID, revID := seedBeadWithRevision(t, d, -1, 300)
	execID := seedExecution(t, d, -1, beadID, revID)

	t.Run("write fired=true", func(t *testing.T) {
		if err := writeMonitorFired(d, execID, true); err != nil {
			t.Fatalf("writeMonitorFired: %v", err)
		}
		var v sql.NullInt64
		_ = d.QueryRowContext(context.Background(),
			`SELECT monitor_fired FROM executions WHERE id = ?`, execID).Scan(&v)
		if !v.Valid || v.Int64 != 1 {
			t.Errorf("monitor_fired = %v, want 1", v)
		}
	})

	t.Run("write fired=false", func(t *testing.T) {
		if err := writeMonitorFired(d, execID, false); err != nil {
			t.Fatalf("writeMonitorFired: %v", err)
		}
		var v sql.NullInt64
		_ = d.QueryRowContext(context.Background(),
			`SELECT monitor_fired FROM executions WHERE id = ?`, execID).Scan(&v)
		if !v.Valid || v.Int64 != 0 {
			t.Errorf("monitor_fired = %v, want 0", v)
		}
	})
}

// --- writeTerminationCause ---

func TestWriteTerminationCause(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, -1)
	beadID, revID := seedBeadWithRevision(t, d, -1, 300)
	execID := seedExecution(t, d, -1, beadID, revID)

	cases := []string{"success", "timeout", "monitor_terminated", "monitor_force_killed"}
	for _, cause := range cases {
		t.Run(cause, func(t *testing.T) {
			if err := writeTerminationCause(d, execID, cause); err != nil {
				t.Fatalf("writeTerminationCause(%q): %v", cause, err)
			}
			var got string
			_ = d.QueryRowContext(context.Background(),
				`SELECT termination_cause FROM executions WHERE id = ?`, execID).Scan(&got)
			if got != cause {
				t.Errorf("termination_cause = %q, want %q", got, cause)
			}
		})
	}
}

// --- readMonitorFired ---

func TestReadMonitorFired(t *testing.T) {
	d := openTestDB(t)
	seedProject(t, d, -1)
	beadID, revID := seedBeadWithRevision(t, d, -1, 300)
	execID := seedExecution(t, d, -1, beadID, revID)

	t.Run("NULL → not fired", func(t *testing.T) {
		fired, err := readMonitorFired(d, execID)
		if err != nil {
			t.Fatal(err)
		}
		if fired {
			t.Error("expected not-fired when monitor_fired is NULL")
		}
	})

	t.Run("0 → not fired", func(t *testing.T) {
		_ = writeMonitorFired(d, execID, false)
		fired, err := readMonitorFired(d, execID)
		if err != nil {
			t.Fatal(err)
		}
		if fired {
			t.Error("expected not-fired when monitor_fired=0")
		}
	})

	t.Run("1 → fired", func(t *testing.T) {
		_ = writeMonitorFired(d, execID, true)
		fired, err := readMonitorFired(d, execID)
		if err != nil {
			t.Fatal(err)
		}
		if !fired {
			t.Error("expected fired when monitor_fired=1")
		}
	})
}

// --- stubLine ---

func TestStubLine(t *testing.T) {
	t.Run("loop mode returns identical content each step", func(t *testing.T) {
		line1 := stubLine("loop", 1)
		line2 := stubLine("loop", 2)
		line3 := stubLine("loop", 10)
		// Timestamps differ, but the error description must be identical.
		// Strip timestamp prefix for comparison.
		trim := func(s string) string {
			idx := 0
			for i, c := range s {
				if c == ']' {
					idx = i + 2
					break
				}
			}
			return s[idx:]
		}
		if trim(line1) != trim(line2) || trim(line2) != trim(line3) {
			t.Errorf("loop mode lines differ:\n  %q\n  %q\n  %q", line1, line2, line3)
		}
	})

	t.Run("success mode returns distinct steps", func(t *testing.T) {
		line1 := stubLine("success", 1)
		line2 := stubLine("success", 2)
		if line1 == line2 {
			t.Error("success mode step 1 and step 2 are identical")
		}
	})
}
