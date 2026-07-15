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

// RunResumeProjectMain is the entry point for the `ratchet resume-project` subcommand.
func RunResumeProjectMain(args []string) {
	flags := flag.NewFlagSet("resume-project", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	projectID := flags.Int64("project", 0, "project ID to resume (required)")
	_ = flags.Parse(args)

	if *projectID == 0 {
		slog.Error("resume-project: --project is required")
		os.Exit(1)
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("resume-project: open db", "error", err)
		os.Exit(1)
	}
	defer d.Close()

	label, nextVerb, nextBeadID, err := resumeProject(context.Background(), d, *projectID)
	if err != nil {
		slog.Error("resume-project", "error", err)
		os.Exit(1)
	}

	fmt.Printf("project resumed\n")
	fmt.Printf("  id:        %d\n", *projectID)
	fmt.Printf("  label:     %s\n", label)
	fmt.Printf("  status:    active\n")
	switch {
	case nextVerb == "":
		fmt.Printf("  next job:  none pending — check handoff_jobs\n")
	case nextBeadID.Valid:
		fmt.Printf("  next job:  %s (bead %d)\n", nextVerb, nextBeadID.Int64)
	default:
		fmt.Printf("  next job:  %s\n", nextVerb)
	}
}

// resumeProject transitions a paused project back to active.
//
// Every pause point (pause_after_reconcile, pause_after_verb,
// pause_after_bead_id) always enqueues its normal next handoff_job *before*
// pausing — the job is left sitting 'pending' but inert, since the
// orchestrator only dispatches jobs for status='active' projects. So resuming
// is nothing more than this status flip: there is no next-job state to
// reconstruct, regardless of which pause point the project stopped at.
//
// The returned nextVerb/nextBeadID are informational only (for the CLI
// printout) — the pending job that was already enqueued right before the
// project paused, found by querying rather than reconstructed.
func resumeProject(ctx context.Context, d *db.DB, projectID int64) (label, nextVerb string, nextBeadID sql.NullInt64, err error) {
	var status string
	if err = d.QueryRowContext(ctx,
		`SELECT label, status FROM projects WHERE id = ?`, projectID,
	).Scan(&label, &status); err == sql.ErrNoRows {
		return "", "", sql.NullInt64{}, fmt.Errorf("project not found: %d", projectID)
	} else if err != nil {
		return "", "", sql.NullInt64{}, fmt.Errorf("query project: %w", err)
	}

	if status != "paused" {
		return "", "", sql.NullInt64{}, fmt.Errorf("project %d is not paused (status=%s)", projectID, status)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return "", "", sql.NullInt64{}, fmt.Errorf("begin tx: %w", err)
	}

	if _, err = tx.ExecContext(ctx,
		`UPDATE projects SET status = 'active', updated_at = ? WHERE id = ?`,
		now, projectID,
	); err != nil {
		_ = tx.Rollback()
		return "", "", sql.NullInt64{}, fmt.Errorf("update project status: %w", err)
	}

	err = tx.QueryRowContext(ctx, `
		SELECT verb, bead_id FROM handoff_jobs
		WHERE project_id = ? AND status = 'pending'
		ORDER BY created_at LIMIT 1`, projectID,
	).Scan(&nextVerb, &nextBeadID)
	if err != nil && err != sql.ErrNoRows {
		_ = tx.Rollback()
		return "", "", sql.NullInt64{}, fmt.Errorf("query next job: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return "", "", sql.NullInt64{}, fmt.Errorf("commit: %w", err)
	}

	return label, nextVerb, nextBeadID, nil
}
