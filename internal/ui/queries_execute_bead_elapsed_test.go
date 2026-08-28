package ui

import (
	"context"
	"testing"
)

// TestQueryRecentJobs_ExecuteBeadElapsed locks in the fix for a real live
// gap (connect-four-v5 bead 71, 2026-08-28): EXECUTE_BEAD never writes to
// handoff_attempts at all — unlike every other verb, its timing lives in
// the executions table. The UI showed no elapsed time whatsoever for an
// EXECUTE_BEAD job (not merely a wrong one) until queryRecentJobs learned
// to look there instead, matching an execution to its job positionally
// (there's no direct FK): the Nth EXECUTE_BEAD job for a bead (by id)
// corresponds to the Nth executions row for that bead (by started_at).
func TestQueryRecentJobs_ExecuteBeadElapsed(t *testing.T) {
	_, d := openTestServer(t)
	ctx := context.Background()
	projectID := seedProject(t, d)
	beadID := seedBead(t, d, projectID, 900)

	insertExecution := func(startedAt, endedAt string) {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO executions
			  (project_id, bead_id, bead_revision_id, trace_path,
			   termination_cause, monitor_honored, started_at, ended_at)
			VALUES (?, ?, (SELECT current_revision_id FROM beads WHERE id = ?), '/tmp/trace.log',
			        'timeout', 1, ?, ?)`,
			projectID, beadID, beadID, startedAt, endedAt); err != nil {
			t.Fatalf("insert execution: %v", err)
		}
	}
	insertJob := func(status string) int64 {
		id := seedJob(t, d, projectID, beadID, "EXECUTE_BEAD", status)
		return id
	}

	// First EXECUTE_BEAD attempt: 5 minutes (300s), retried after a failure.
	firstJobID := insertJob("complete")
	insertExecution("2026-01-01T00:00:00Z", "2026-01-01T00:05:00Z")

	// Second EXECUTE_BEAD attempt (a later job id, same bead): 12 minutes
	// (720s) — must match the *second* execution row, not double-count or
	// pick up the first one again.
	secondJobID := insertJob("complete")
	insertExecution("2026-01-01T01:00:00Z", "2026-01-01T01:12:00Z")

	jobs, err := queryRecentJobs(ctx, d, projectID)
	if err != nil {
		t.Fatalf("queryRecentJobs: %v", err)
	}

	byID := map[int64]JobRow{}
	for _, j := range jobs {
		byID[j.ID] = j
	}

	if got := byID[firstJobID].ElapsedFormatted; got != "5m0s" {
		t.Errorf("first EXECUTE_BEAD job elapsed = %q, want \"5m0s\"", got)
	}
	if got := byID[secondJobID].ElapsedFormatted; got != "12m0s" {
		t.Errorf("second EXECUTE_BEAD job elapsed = %q, want \"12m0s\"", got)
	}
}
