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
	"ratchet/internal/verbs"
)

// RunRewindBeadMain is the entry point for `ratchet rewind-bead`.
//
// It resets an escalated (or stuck) bead to a clean state:
//   - spec rolled back to revision 1
//   - execution attempt budget extended by max_execution_attempts
//   - test files deleted and test_refinements cleared
//   - impl files replaced with scaffold stubs (always compilable baseline)
//   - re-enqueues REFINE_TESTS_WRITE to regenerate tests from scratch
//
// Rewind always restarts from REFINE_TESTS_WRITE. If the impl just needed more
// attempts, extending the budget is sufficient — rewind is reserved for cases
// where something went wrong enough to warrant human intervention, and in those
// cases the tests must be re-examined too.
func RunRewindBeadMain(args []string) {
	flags := flag.NewFlagSet("rewind-bead", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	beadID := flags.Int64("bead-id", 0, "bead ID to rewind (required)")
	_ = flags.Parse(args)

	if *beadID == 0 {
		slog.Error("rewind-bead: --bead-id is required")
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
	if err := d.QueryRowContext(ctx,
		`SELECT project_id, status FROM beads WHERE id = ?`, *beadID,
	).Scan(&projectID, &beadStatus); err == sql.ErrNoRows {
		slog.Error("rewind-bead: bead not found", "id", *beadID)
		os.Exit(1)
	} else if err != nil {
		slog.Error("rewind-bead: query bead", "error", err)
		os.Exit(1)
	}

	if beadStatus == "succeeded" {
		slog.Error("rewind-bead: bead has already succeeded", "status", beadStatus)
		os.Exit(1)
	}

	var projectFolder, projectStatus string
	var maxAttempts int
	if err := d.QueryRowContext(ctx,
		`SELECT folder_path, status, max_execution_attempts FROM projects WHERE id = ?`, projectID,
	).Scan(&projectFolder, &projectStatus, &maxAttempts); err != nil {
		slog.Error("rewind-bead: query project", "error", err)
		os.Exit(1)
	}

	// Find the first (original DECOMPOSE) revision for this bead.
	var firstRevisionID int64
	var firstRevisionFullText string
	if err := d.QueryRowContext(ctx,
		`SELECT id, full_text FROM bead_revisions WHERE bead_id = ? ORDER BY revision_number ASC LIMIT 1`,
		*beadID,
	).Scan(&firstRevisionID, &firstRevisionFullText); err != nil {
		slog.Error("rewind-bead: find first revision", "error", err)
		os.Exit(1)
	}

	var outputFiles []string
	var parsed struct {
		OutputFiles []string `json:"output_files"`
	}
	if json.Unmarshal([]byte(firstRevisionFullText), &parsed) == nil {
		outputFiles = parsed.OutputFiles
	}

	// Count existing valid executions to grant a fresh budget on top.
	var existingExecutions int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM executions WHERE bead_id = ? AND infra_failure = 0 AND test_first_attempt = 0`,
		*beadID,
	).Scan(&existingExecutions); err != nil {
		slog.Error("rewind-bead: count executions", "error", err)
		os.Exit(1)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("rewind-bead: begin tx", "error", err)
		os.Exit(1)
	}

	// Cancel any active jobs for this bead.
	if _, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'complete', updated_at = ?
		 WHERE bead_id = ? AND status IN ('escalated', 'pending', 'failed_retry', 'running')`,
		now, *beadID,
	); err != nil {
		_ = tx.Rollback()
		slog.Error("rewind-bead: cancel jobs", "error", err)
		os.Exit(1)
	}

	// Reset spec to revision 1 and grant a fresh execution budget. rewound_at
	// records the boundary so loadBeadRevisionLog/currentLineageRevisionIDs
	// can tell pre-rewind revisions apart from post-rewind ones — revision_number
	// alone can't do this anymore, since every bead_revisions insert site now
	// uses a bead-wide MAX(revision_number)+1 to avoid numbering collisions,
	// which means revision numbers never repeat or go backwards across a rewind
	// for the lineage filter to detect (see currentLineage in inputs.go).
	newAttemptCap := existingExecutions + maxAttempts
	if _, err := tx.ExecContext(ctx,
		`UPDATE beads SET status = 'pending', current_revision_id = ?, execution_attempts_override = ?, rewound_at = ? WHERE id = ?`,
		firstRevisionID, newAttemptCap, now, *beadID,
	); err != nil {
		_ = tx.Rollback()
		slog.Error("rewind-bead: reset bead", "error", err)
		os.Exit(1)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM test_refinements WHERE bead_id = ?`, *beadID,
	); err != nil {
		_ = tx.Rollback()
		slog.Error("rewind-bead: clear test_refinements", "error", err)
		os.Exit(1)
	}

	// Clear the rolling COMPRESS_ANALYSIS summary too — it's fed back to the
	// model on every future COMPRESS_ANALYSIS call as "Existing Compressed
	// History", so left alone it keeps narrating pre-rewind attempts (e.g.
	// "TestPlaceStone/KoCreation [RECURRING x5]") against a test file that no
	// longer exists after this rewind.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM compressed_history WHERE bead_id = ?`, *beadID,
	); err != nil {
		_ = tx.Rollback()
		slog.Error("rewind-bead: clear compressed_history", "error", err)
		os.Exit(1)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs
		  (project_id, verb, bead_id, status, created_at, updated_at, refinement_cycle_id)
		VALUES (?, 'REFINE_TESTS_WRITE', ?, 'pending', ?, ?, 1)`,
		projectID, *beadID, now, now,
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

	var deletedTests []string
	for _, f := range outputFiles {
		if strings.HasSuffix(f, "_test.go") {
			path := filepath.Join(projectFolder, f)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				slog.Warn("rewind-bead: delete test file", "path", path, "error", err)
			}
			deletedTests = append(deletedTests, f)
		}
	}

	// Reset all non-test impl files to a clean baseline: manifest-backed .go
	// files are overwritten with their scaffold stub; anything else (never
	// scaffolded by SURVEY, so no stub baseline exists) is deleted instead.
	stubbedFiles, deletedFiles, err := verbs.WriteScaffoldStubs(ctx, d, projectID, projectFolder, outputFiles)
	if err != nil {
		slog.Error("rewind-bead: write scaffold stubs", "error", err)
		os.Exit(1)
	}

	fmt.Printf("bead rewound\n")
	fmt.Printf("  bead-id:        %d\n", *beadID)
	fmt.Printf("  project-id:     %d\n", projectID)
	fmt.Printf("  spec reset to:  revision 1\n")
	fmt.Printf("  attempt budget: %d → %d\n", existingExecutions, newAttemptCap)
	fmt.Printf("  next verb:      REFINE_TESTS_WRITE\n")
	if len(deletedTests) > 0 {
		fmt.Printf("  test files deleted:\n")
		for _, f := range deletedTests {
			fmt.Printf("    %s\n", f)
		}
	}
	if len(stubbedFiles) > 0 {
		fmt.Printf("  impl files stubbed:\n")
		for _, f := range stubbedFiles {
			fmt.Printf("    %s\n", f)
		}
	}
	if len(deletedFiles) > 0 {
		fmt.Printf("  impl files deleted (not in SURVEY manifest, no stub baseline):\n")
		for _, f := range deletedFiles {
			fmt.Printf("    %s\n", f)
		}
	}
}

