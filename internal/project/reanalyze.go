package project

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"ratchet/internal/db"
)

// RunReanalyzeBeadMain is the entry point for `ratchet reanalyze-bead`.
//
// It re-runs ANALYZE_EXECUTION (and, via the pipeline's existing Commit
// chain, COMPRESS_ANALYSIS and ADJUDICATE_NEXT_EXECUTION after it) against a
// bead's already-completed execution, without touching test files, impl
// files, or the bead's spec at all.
//
// This exists for a narrower failure class than rewind-bead handles: a bead
// whose actual code and tests are fine, but whose *analysis* of a genuinely
// successful execution was corrupted by something outside the model's
// control — a framework bug, a transient infra hiccup that mangled a
// stored result, or similar. rewind-bead would "fix" that too, but only as
// a side effect of discarding the test file, stubbing the impl file, and
// forcing a full REFINE_TESTS_WRITE -> EXECUTE_BEAD -> ANALYZE_EXECUTION
// cycle from scratch — burning real compute and risking regression on work
// that was never actually wrong. Confirmed live, 2026-08-27
// (connect-four-v1, bead 47): a framework bug (rewind snapshots lacking a
// go.mod, so `go test ./...` silently ran the copied test file as a second
// package) doubled the size of the bead's mechanical findings, which pushed
// ADJUDICATE_NEXT_EXECUTION's prompt past available context. The actual
// EXECUTE_BEAD result and its tests were correct throughout; only the
// stored ANALYZE_EXECUTION output needed to be regenerated.
//
// ANALYZE_EXECUTION.Run already self-discovers "the most recent completed
// execution for this bead" (internal/verbs/analyze_execution.go) rather
// than reading an execution id off the handoff_job — so enqueuing a fresh
// job with nothing more than bead_id set is sufficient; no execution needs
// to be re-run, and the existing verb chain (ANALYZE_EXECUTION's Commit
// enqueues COMPRESS_ANALYSIS, whose Commit enqueues
// ADJUDICATE_NEXT_EXECUTION) proceeds exactly as it would have the first
// time, unmodified.
func RunReanalyzeBeadMain(args []string) {
	flags := flag.NewFlagSet("reanalyze-bead", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	beadID := flags.Int64("bead-id", 0, "bead ID to reanalyze (required)")
	_ = flags.Parse(args)

	if *beadID == 0 {
		slog.Error("reanalyze-bead: --bead-id is required")
		os.Exit(1)
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("reanalyze-bead: open db", "error", err)
		os.Exit(1)
	}
	defer d.Close()

	result, err := reanalyzeBead(context.Background(), d, *beadID)
	if err != nil {
		slog.Error("reanalyze-bead", "error", err)
		os.Exit(1)
	}

	fmt.Printf("bead reanalysis queued\n")
	fmt.Printf("  bead-id:            %d\n", *beadID)
	fmt.Printf("  project-id:         %d\n", result.ProjectID)
	fmt.Printf("  jobs cancelled:     %d\n", result.JobsCancelled)
	fmt.Printf("  compressed history: cleared\n")
	fmt.Printf("  test/impl files:    untouched\n")
	fmt.Printf("  next verb:          ANALYZE_EXECUTION\n")
}

// reanalyzeResult reports what reanalyzeBead actually did, for
// RunReanalyzeBeadMain to print.
type reanalyzeResult struct {
	ProjectID     int64
	JobsCancelled int64
}

// reanalyzeBead re-queues ANALYZE_EXECUTION for beadID against its most
// recent completed execution. Factored out as its own function (mirroring
// rewindBead/fullStopProject) so it can be exercised directly by tests
// instead of only through the os.Exit-based CLI entry point.
func reanalyzeBead(ctx context.Context, d *db.DB, beadID int64) (*reanalyzeResult, error) {
	var projectID int64
	var beadStatus string
	if err := d.QueryRowContext(ctx,
		`SELECT project_id, status FROM beads WHERE id = ?`, beadID,
	).Scan(&projectID, &beadStatus); err == sql.ErrNoRows {
		return nil, fmt.Errorf("bead not found: %d", beadID)
	} else if err != nil {
		return nil, fmt.Errorf("query bead: %w", err)
	}

	if beadStatus == "succeeded" {
		return nil, fmt.Errorf("bead %d has already succeeded — nothing to reanalyze", beadID)
	}

	var executionCount int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM executions WHERE bead_id = ? AND termination_cause IS NOT NULL`, beadID,
	).Scan(&executionCount); err != nil {
		return nil, fmt.Errorf("count executions: %w", err)
	}
	if executionCount == 0 {
		return nil, fmt.Errorf("bead %d has no completed execution to reanalyze", beadID)
	}

	var projectStatus string
	if err := d.QueryRowContext(ctx,
		`SELECT status FROM projects WHERE id = ?`, projectID,
	).Scan(&projectStatus); err != nil {
		return nil, fmt.Errorf("query project: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}

	// Cancel any active jobs for this bead — same guard rewindBead uses.
	// The stuck job this tool exists to recover from is typically escalated
	// ADJUDICATE_NEXT_EXECUTION, but a stray pending/failed_retry/running
	// row anywhere in the chain for this bead would otherwise race the
	// freshly-enqueued ANALYZE_EXECUTION job below.
	cancelRes, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'complete', updated_at = ?
		 WHERE bead_id = ? AND status IN ('escalated', 'pending', 'failed_retry', 'running')`,
		now, beadID,
	)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("cancel jobs: %w", err)
	}
	jobsCancelled, _ := cancelRes.RowsAffected()

	// Clear the rolling COMPRESS_ANALYSIS summary — it was built from
	// "existing history + latest analysis" (compress_analysis.go), and the
	// latest analysis is exactly what's being thrown out here. Leaving the
	// old summary in place would keep folding a corrupted analysis into
	// every future compression instead of starting clean from the fresh one
	// this reanalysis produces.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM compressed_history WHERE bead_id = ?`, beadID,
	); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("clear compressed_history: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, 'ANALYZE_EXECUTION', ?, 'pending', ?, ?)`,
		projectID, beadID, now, now,
	); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("enqueue job: %w", err)
	}

	if projectStatus != "active" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE projects SET status = 'active', updated_at = ? WHERE id = ?`,
			now, projectID,
		); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("reactivate project: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &reanalyzeResult{
		ProjectID:     projectID,
		JobsCancelled: jobsCancelled,
	}, nil
}
