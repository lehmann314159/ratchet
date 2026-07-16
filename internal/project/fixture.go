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

// fixtureScopedTables lists every table with a direct project_id column that
// must be renumbered when a project becomes a fixture. Cross-checked against
// schema.sql directly (not copied from an earlier summary): handoff_attempts
// is deliberately excluded — it has no project_id column of its own, only
// job_id referencing handoff_jobs(id), so it moves with its job automatically
// without needing a row update of its own.
var fixtureScopedTables = []string{
	"verb_model_assignments",
	"beads",
	"bead_revisions",
	"audit_reconcile_rounds",
	"executions",
	"analyses",
	"compressed_history",
	"adjudications",
	"spec_revisions",
	"handoff_jobs",
	"verify_attempts",
	"certifications",
	"test_refinements",
}

// RunSaveFixtureMain is the entry point for the `ratchet save-fixture` subcommand.
func RunSaveFixtureMain(args []string) {
	flags := flag.NewFlagSet("save-fixture", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	projectID := flags.Int64("project", 0, "project ID to save as a fixture (required)")
	label := flags.String("label", "", "stage descriptor for the fixture label, e.g. \"checkers post-RECONCILE\" (optional; defaults to the project's own label)")
	_ = flags.Parse(args)

	if *projectID == 0 {
		slog.Error("save-fixture: --project is required")
		os.Exit(1)
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("save-fixture: open db", "error", err)
		os.Exit(1)
	}
	defer d.Close()

	newID, newLabel, err := saveFixture(context.Background(), d, *projectID, *label)
	if err != nil {
		slog.Error("save-fixture", "error", err)
		os.Exit(1)
	}

	fmt.Printf("fixture saved\n")
	fmt.Printf("  id:    %d\n", newID)
	fmt.Printf("  label: %s\n", newLabel)
}

// saveFixture converts a live project into a fixture: a frozen, reusable
// starting point the orchestrator will never dispatch again. It renumbers the
// project (and every project-scoped table's rows) in place to the negative of
// its own ID — cheap, no row duplication, no folder copy — rather than
// copying data. Returns the new (negative) ID and the fixture's label.
//
// Preconditions: projectID must be positive (a fixture ID passed back in is
// almost certainly a mistake — negating it again would produce a positive ID
// that could collide with a real project), the project must exist, it must
// have zero 'running' handoff_jobs (renumbering out from under an in-flight
// job would corrupt whichever row the running job later tries to write back
// to), and -projectID must not already be taken by an earlier fixture.
//
// That last check matters because projects.id has no AUTOINCREMENT: SQLite
// reuses the lowest freed id, and save-fixture is exactly the operation that
// frees one (renumbering N to -N leaves N available again). A later project
// can land back on the same N a prior fixture was saved from — confirmed
// live (chess-v4, 2026-07-16, reused id 99 freed by checkers-v9's -99
// fixture). Without this check, the renumber UPDATE below would fail on the
// table's own UNIQUE constraint mid-transaction (rolled back safely, but
// with a raw SQL error instead of an actionable one).
func saveFixture(ctx context.Context, d *db.DB, projectID int64, labelSuffix string) (newID int64, label string, err error) {
	if projectID <= 0 {
		return 0, "", fmt.Errorf("project ID must be positive (got %d) — already a fixture?", projectID)
	}

	var origLabel, status string
	if err = d.QueryRowContext(ctx,
		`SELECT label, status FROM projects WHERE id = ?`, projectID,
	).Scan(&origLabel, &status); err == sql.ErrNoRows {
		return 0, "", fmt.Errorf("project not found: %d", projectID)
	} else if err != nil {
		return 0, "", fmt.Errorf("query project: %w", err)
	}

	var conflictLabel string
	if err = d.QueryRowContext(ctx,
		`SELECT label FROM projects WHERE id = ?`, -projectID,
	).Scan(&conflictLabel); err == nil {
		return 0, "", fmt.Errorf("fixture id %d is already taken by %q — a project previously freed id %d by becoming this fixture, and a later project reused it; rename or remove the existing fixture first", -projectID, conflictLabel, projectID)
	} else if err != sql.ErrNoRows {
		return 0, "", fmt.Errorf("check existing fixture: %w", err)
	}

	var runningCount int
	if err = d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM handoff_jobs WHERE project_id = ? AND status = 'running'`, projectID,
	).Scan(&runningCount); err != nil {
		return 0, "", fmt.Errorf("count running jobs: %w", err)
	}
	if runningCount > 0 {
		return 0, "", fmt.Errorf("project %d has %d running job(s) — wait for them to finish before saving a fixture", projectID, runningCount)
	}

	newID = -projectID
	descriptor := origLabel
	if labelSuffix != "" {
		descriptor = labelSuffix
	}
	label = "fixture: " + descriptor

	now := time.Now().UTC().Format(time.RFC3339)

	// The rename touches projects.id itself, which every scoped table (plus
	// projects.recovered_from_project_id) has a live FK into — same
	// PRAGMA-toggle-outside-the-transaction requirement as the schema
	// rename+recreate+copy+drop migrations in db.go (SQLite refuses to
	// toggle foreign_keys while a transaction is open).
	if _, err = d.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return 0, "", fmt.Errorf("disable foreign_keys: %w", err)
	}
	defer func() {
		if _, ferr := d.ExecContext(ctx, `PRAGMA foreign_keys = ON`); ferr != nil {
			slog.Error("save-fixture: re-enable foreign_keys failed", "error", ferr)
		}
	}()

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", fmt.Errorf("begin tx: %w", err)
	}

	if _, err = tx.ExecContext(ctx,
		`UPDATE projects SET id = ?, label = ?, status = 'fixture', updated_at = ? WHERE id = ?`,
		newID, label, now, projectID,
	); err != nil {
		_ = tx.Rollback()
		return 0, "", fmt.Errorf("renumber project row: %w", err)
	}

	for _, table := range fixtureScopedTables {
		if _, err = tx.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET project_id = ? WHERE project_id = ?`, table),
			newID, projectID,
		); err != nil {
			_ = tx.Rollback()
			return 0, "", fmt.Errorf("renumber %s: %w", table, err)
		}
	}

	// Repoint any other project's restart lineage so it still resolves —
	// mirrors the care internal/ui/handlers.go's project-delete handler
	// already takes for this same backreference (it nulls it out there since
	// the referenced project is gone; here it's just renumbered, so repoint
	// instead of null).
	if _, err = tx.ExecContext(ctx,
		`UPDATE projects SET recovered_from_project_id = ? WHERE recovered_from_project_id = ?`,
		newID, projectID,
	); err != nil {
		_ = tx.Rollback()
		return 0, "", fmt.Errorf("repoint recovered_from_project_id: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("commit: %w", err)
	}

	return newID, label, nil
}
