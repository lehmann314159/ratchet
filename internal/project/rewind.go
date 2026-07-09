package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ratchet/internal/db"
)

// RunRewindBeadMain is the entry point for `ratchet rewind-bead`.
// It resets an escalated bead so the pipeline can retry it without restarting
// the whole project. Two modes:
//
//	--to=execute  keep certified test files, delete implementation files,
//	              re-enqueue EXECUTE_BEAD with a fresh attempt budget
//	--to=refine   delete all output files, clear test_refinements,
//	              re-enqueue REFINE_TESTS_WRITE from cycle 1
func RunRewindBeadMain(args []string) {
	flags := flag.NewFlagSet("rewind-bead", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	beadID := flags.Int64("bead-id", 0, "bead ID to rewind (required)")
	to := flags.String("to", "execute", "rewind target: execute or refine")
	_ = flags.Parse(args)

	if *beadID == 0 {
		slog.Error("rewind-bead: --bead-id is required")
		os.Exit(1)
	}
	if *to != "execute" && *to != "refine" {
		slog.Error("rewind-bead: --to must be 'execute' or 'refine'")
		os.Exit(1)
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("rewind-bead: open db", "error", err)
		os.Exit(1)
	}
	defer d.Close()

	ctx := context.Background()

	var projectID int64
	var beadStatus string
	var currentRevisionID sql.NullInt64
	if err := d.QueryRowContext(ctx,
		`SELECT project_id, status, current_revision_id FROM beads WHERE id = ?`, *beadID,
	).Scan(&projectID, &beadStatus, &currentRevisionID); err == sql.ErrNoRows {
		slog.Error("rewind-bead: bead not found", "id", *beadID)
		os.Exit(1)
	} else if err != nil {
		slog.Error("rewind-bead: query bead", "error", err)
		os.Exit(1)
	}

	if beadStatus != "escalated" && beadStatus != "full_stopped" {
		slog.Error("rewind-bead: bead must be in 'escalated' or 'full_stopped' state", "status", beadStatus)
		os.Exit(1)
	}

	var projectFolder, projectStatus string
	var maxAttempts int
	if err := d.QueryRowContext(ctx,
		`SELECT folder, status, max_execution_attempts FROM projects WHERE id = ?`, projectID,
	).Scan(&projectFolder, &projectStatus, &maxAttempts); err != nil {
		slog.Error("rewind-bead: query project", "error", err)
		os.Exit(1)
	}

	// Count existing valid executions so we can grant a fresh budget on top.
	var existingExecutions int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM executions WHERE bead_id = ? AND infra_failure = 0 AND test_first_attempt = 0`,
		*beadID,
	).Scan(&existingExecutions); err != nil {
		slog.Error("rewind-bead: count executions", "error", err)
		os.Exit(1)
	}

	// Get output_files from the current bead revision.
	var outputFiles []string
	if currentRevisionID.Valid {
		var fullText string
		if err := d.QueryRowContext(ctx,
			`SELECT full_text FROM bead_revisions WHERE id = ?`, currentRevisionID.Int64,
		).Scan(&fullText); err == nil {
			var parsed struct {
				OutputFiles []string `json:"output_files"`
			}
			if json.Unmarshal([]byte(fullText), &parsed) == nil {
				outputFiles = parsed.OutputFiles
			}
		}
	}

	targetVerb := "EXECUTE_BEAD"
	if *to == "refine" {
		targetVerb = "REFINE_TESTS_WRITE"
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("rewind-bead: begin tx", "error", err)
		os.Exit(1)
	}

	// Cancel any escalated/pending jobs for this bead.
	if _, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'complete', updated_at = ?
		 WHERE bead_id = ? AND status IN ('escalated', 'pending', 'failed_retry')`,
		now, *beadID,
	); err != nil {
		_ = tx.Rollback()
		slog.Error("rewind-bead: cancel jobs", "error", err)
		os.Exit(1)
	}

	// Reset bead to pending and grant a fresh execution budget on top of past count.
	newAttemptCap := existingExecutions + maxAttempts
	if _, err := tx.ExecContext(ctx,
		`UPDATE beads SET status = 'pending', execution_attempts_override = ? WHERE id = ?`,
		newAttemptCap, *beadID,
	); err != nil {
		_ = tx.Rollback()
		slog.Error("rewind-bead: reset bead", "error", err)
		os.Exit(1)
	}

	// Clear test_refinements when rewinding to refine so WRITE starts at cycle 1.
	if *to == "refine" {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM test_refinements WHERE bead_id = ?`, *beadID,
		); err != nil {
			_ = tx.Rollback()
			slog.Error("rewind-bead: clear test_refinements", "error", err)
			os.Exit(1)
		}
	}

	// Enqueue the target verb job.
	var refinementCycleID sql.NullInt64
	if *to == "refine" {
		refinementCycleID = sql.NullInt64{Int64: 1, Valid: true}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO handoff_jobs
		   (project_id, verb, bead_id, status, created_at, updated_at, refinement_cycle_id)
		 VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		projectID, targetVerb, *beadID, now, now, refinementCycleID,
	); err != nil {
		_ = tx.Rollback()
		slog.Error("rewind-bead: enqueue job", "error", err)
		os.Exit(1)
	}

	// Re-activate the project if it was full_stopped.
	if projectStatus != "active" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE projects SET status = 'active', updated_at = ? WHERE id = ?`,
			now, projectID,
		); err != nil {
			_ = tx.Rollback()
			slog.Error("rewind-bead: reactivate project", "error", err)
			os.Exit(1)
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("rewind-bead: commit", "error", err)
		os.Exit(1)
	}

	// Delete files after the DB transaction commits.
	var deleted, kept []string
	for _, f := range outputFiles {
		isTest := strings.HasSuffix(f, "_test.go")
		if *to == "execute" && isTest {
			kept = append(kept, f)
			continue
		}
		path := filepath.Join(projectFolder, f)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("rewind-bead: delete file", "path", path, "error", err)
		} else {
			deleted = append(deleted, f)
		}
	}

	fmt.Printf("bead rewound\n")
	fmt.Printf("  bead-id:        %d\n", *beadID)
	fmt.Printf("  project-id:     %d\n", projectID)
	fmt.Printf("  rewound to:     %s\n", targetVerb)
	fmt.Printf("  attempt budget: %d → %d\n", existingExecutions, newAttemptCap)
	if len(deleted) > 0 {
		fmt.Printf("  files deleted:\n")
		for _, f := range deleted {
			fmt.Printf("    %s\n", f)
		}
	}
	if len(kept) > 0 {
		fmt.Printf("  test files kept (certified):\n")
		for _, f := range kept {
			fmt.Printf("    %s\n", f)
		}
	}
}