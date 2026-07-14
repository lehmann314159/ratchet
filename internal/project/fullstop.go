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

// RunFullStopProjectMain is the entry point for the `ratchet full-stop-project` subcommand.
// It transitions a project to full_stopped, marks all pending beads full_stopped, and
// cancels any pending/running handoff jobs so the orchestrator ignores the project.
func RunFullStopProjectMain(args []string) {
	flags := flag.NewFlagSet("full-stop-project", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	projectID := flags.Int64("project", 0, "project ID to full-stop (required)")
	_ = flags.Parse(args)

	if *projectID == 0 {
		slog.Error("full-stop-project: --project is required")
		os.Exit(1)
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("full-stop-project: open db", "error", err)
		os.Exit(1)
	}
	defer d.Close()

	label, beadsStopped, jobsCancelled, err := fullStopProject(context.Background(), d, *projectID)
	if err != nil {
		slog.Error("full-stop-project", "error", err)
		os.Exit(1)
	}

	fmt.Printf("project full-stopped\n")
	fmt.Printf("  id:              %d\n", *projectID)
	fmt.Printf("  label:           %s\n", label)
	fmt.Printf("  beads stopped:   %d\n", beadsStopped)
	fmt.Printf("  jobs cancelled:  %d\n", jobsCancelled)
}

// fullStopProject transitions a project to full_stopped, marks its pending
// beads full_stopped, and cancels any pending/running/failed_retry handoff
// jobs so the orchestrator ignores it going forward. Shared by
// RunFullStopProjectMain and RunRestartProjectMain so both stop a project the
// same way. Returns an error (rather than exiting) if the project doesn't
// exist or is already in a terminal status, leaving the decision of how to
// report that to the caller.
func fullStopProject(ctx context.Context, d *db.DB, projectID int64) (label string, beadsStopped, jobsCancelled int64, err error) {
	var status string
	if err = d.QueryRowContext(ctx,
		`SELECT label, status FROM projects WHERE id = ?`, projectID,
	).Scan(&label, &status); err == sql.ErrNoRows {
		return "", 0, 0, fmt.Errorf("project not found: %d", projectID)
	} else if err != nil {
		return "", 0, 0, fmt.Errorf("query project: %w", err)
	}

	if status == "full_stopped" || status == "complete" {
		return "", 0, 0, fmt.Errorf("project %d is already terminal (status=%s)", projectID, status)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, 0, fmt.Errorf("begin tx: %w", err)
	}

	if _, err = tx.ExecContext(ctx,
		`UPDATE projects SET status = 'full_stopped', updated_at = ? WHERE id = ?`,
		now, projectID,
	); err != nil {
		_ = tx.Rollback()
		return "", 0, 0, fmt.Errorf("update project status: %w", err)
	}

	beadRes, err := tx.ExecContext(ctx,
		`UPDATE beads SET status = 'full_stopped' WHERE project_id = ? AND status = 'pending'`,
		projectID,
	)
	if err != nil {
		_ = tx.Rollback()
		return "", 0, 0, fmt.Errorf("mark beads full_stopped: %w", err)
	}
	beadsStopped, _ = beadRes.RowsAffected()

	jobRes, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'complete', updated_at = ?
		 WHERE project_id = ? AND status IN ('pending', 'running', 'failed_retry')`,
		now, projectID,
	)
	if err != nil {
		_ = tx.Rollback()
		return "", 0, 0, fmt.Errorf("cancel pending jobs: %w", err)
	}
	jobsCancelled, _ = jobRes.RowsAffected()

	if err = tx.Commit(); err != nil {
		return "", 0, 0, fmt.Errorf("commit: %w", err)
	}

	return label, beadsStopped, jobsCancelled, nil
}
