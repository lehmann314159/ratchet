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

	ctx := context.Background()

	var label, status string
	if err := d.QueryRowContext(ctx,
		`SELECT label, status FROM projects WHERE id = ?`, *projectID,
	).Scan(&label, &status); err == sql.ErrNoRows {
		slog.Error("full-stop-project: project not found", "id", *projectID)
		os.Exit(1)
	} else if err != nil {
		slog.Error("full-stop-project: query project", "error", err)
		os.Exit(1)
	}

	if status == "full_stopped" || status == "complete" {
		slog.Error("full-stop-project: project is already terminal", "id", *projectID, "status", status)
		os.Exit(1)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("full-stop-project: begin tx", "error", err)
		os.Exit(1)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET status = 'full_stopped', updated_at = ? WHERE id = ?`,
		now, *projectID,
	); err != nil {
		_ = tx.Rollback()
		slog.Error("full-stop-project: update project status", "error", err)
		os.Exit(1)
	}

	beadRes, err := tx.ExecContext(ctx,
		`UPDATE beads SET status = 'full_stopped' WHERE project_id = ? AND status = 'pending'`,
		*projectID,
	)
	if err != nil {
		_ = tx.Rollback()
		slog.Error("full-stop-project: mark beads full_stopped", "error", err)
		os.Exit(1)
	}
	beadsUpdated, _ := beadRes.RowsAffected()

	jobRes, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'complete', updated_at = ?
		 WHERE project_id = ? AND status IN ('pending', 'running', 'failed_retry')`,
		now, *projectID,
	)
	if err != nil {
		_ = tx.Rollback()
		slog.Error("full-stop-project: cancel pending jobs", "error", err)
		os.Exit(1)
	}
	jobsCancelled, _ := jobRes.RowsAffected()

	if err := tx.Commit(); err != nil {
		slog.Error("full-stop-project: commit", "error", err)
		os.Exit(1)
	}

	fmt.Printf("project full-stopped\n")
	fmt.Printf("  id:              %d\n", *projectID)
	fmt.Printf("  label:           %s\n", label)
	fmt.Printf("  beads stopped:   %d\n", beadsUpdated)
	fmt.Printf("  jobs cancelled:  %d\n", jobsCancelled)
}
