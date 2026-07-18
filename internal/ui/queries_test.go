package ui

import (
	"context"
	"testing"
)

// TestQueryBeads_AttemptsExcludesTestFirstAttempts reproduces the
// checkers-try-1 bead-684 "attempt 9/10" UI discrepancy: the dashboard's
// Attempts/MaxAttempts count excluded infra_failure=1 rows but not
// test_first_attempt=1 rows — unlike atExecutionCap (the actual mechanical
// escalation gate in adjudicate_next_execution.go), which excludes both. A
// bead that had gone through REFINE_TESTS-adjacent test-first executions
// showed a higher, more alarming attempt count in the UI than the number
// that actually counts toward escalation.
func TestQueryBeads_AttemptsExcludesTestFirstAttempts(t *testing.T) {
	_, d := openTestServer(t)
	projectID := seedProject(t, d)
	beadID := seedBead(t, d, projectID, 900)
	ctx := context.Background()

	insertExecution := func(infraFailure, testFirst int) {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO executions
			  (project_id, bead_id, bead_revision_id, trace_path,
			   termination_cause, monitor_honored, started_at, infra_failure, test_first_attempt)
			VALUES (?, ?, (SELECT current_revision_id FROM beads WHERE id = ?), '/tmp/trace.log',
			        'success', 1, '2026-01-01T00:00:00Z', ?, ?)`,
			projectID, beadID, beadID, infraFailure, testFirst); err != nil {
			t.Fatalf("insert execution: %v", err)
		}
	}

	insertExecution(0, 0) // a real attempt
	insertExecution(0, 1) // test-first attempt — must not count
	insertExecution(0, 1) // test-first attempt — must not count
	insertExecution(1, 0) // infra failure — must not count
	insertExecution(0, 0) // a real attempt

	beads, err := queryBeads(ctx, d, projectID)
	if err != nil {
		t.Fatalf("queryBeads: %v", err)
	}
	if len(beads) != 1 {
		t.Fatalf("expected 1 bead, got %d", len(beads))
	}
	if beads[0].Attempts != 2 {
		t.Errorf("Attempts = %d, want 2 (2 real attempts; test-first and infra-failure rows excluded)", beads[0].Attempts)
	}
	if beads[0].InfraFailures != 1 {
		t.Errorf("InfraFailures = %d, want 1", beads[0].InfraFailures)
	}
}