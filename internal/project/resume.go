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
// It transitions a paused project back to active and enqueues EXECUTE_BEAD for the
// first bead, resuming bead execution after a --pause-after-reconcile halt.
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

	ctx := context.Background()

	var label, status string
	if err := d.QueryRowContext(ctx,
		`SELECT label, status FROM projects WHERE id = ?`, *projectID,
	).Scan(&label, &status); err == sql.ErrNoRows {
		slog.Error("resume-project: project not found", "id", *projectID)
		os.Exit(1)
	} else if err != nil {
		slog.Error("resume-project: query project", "error", err)
		os.Exit(1)
	}

	if status != "paused" {
		slog.Error("resume-project: project is not paused", "id", *projectID, "status", status)
		os.Exit(1)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("resume-project: begin tx", "error", err)
		os.Exit(1)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET status = 'active', updated_at = ? WHERE id = ?`,
		now, *projectID,
	); err != nil {
		_ = tx.Rollback()
		slog.Error("resume-project: update status", "error", err)
		os.Exit(1)
	}

	var beadID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM beads WHERE project_id = ? ORDER BY id LIMIT 1`, *projectID,
	).Scan(&beadID); err != nil {
		_ = tx.Rollback()
		slog.Error("resume-project: find first bead", "error", err)
		os.Exit(1)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?)`,
		*projectID, db.VerbExecuteBead, beadID, now, now,
	); err != nil {
		_ = tx.Rollback()
		slog.Error("resume-project: enqueue first bead", "error", err)
		os.Exit(1)
	}

	if err := tx.Commit(); err != nil {
		slog.Error("resume-project: commit", "error", err)
		os.Exit(1)
	}

	fmt.Printf("project resumed\n")
	fmt.Printf("  id:        %d\n", *projectID)
	fmt.Printf("  label:     %s\n", label)
	fmt.Printf("  status:    active\n")
	fmt.Printf("  next job:  EXECUTE_BEAD (bead %d)\n", beadID)
}
