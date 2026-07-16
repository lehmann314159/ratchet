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
// project (and every project-scoped table's rows) in place to a fresh
// negative ID — cheap, no row duplication, no folder copy — rather than
// copying data. Returns the new (negative) ID and the fixture's label.
//
// The new ID is allocated sequentially — one less than the current lowest
// (most negative) fixture ID, or -1 if there are no fixtures yet — rather
// than derived from projectID itself. An earlier version negated projectID
// directly (N -> -N); that scheme collided in practice, because
// projects.id has no AUTOINCREMENT, so SQLite reuses the lowest freed id,
// and save-fixture is exactly the operation that frees one (renumbering N
// to -N leaves N available again). A later project reusing that freed N
// would then try to claim the same -N a prior fixture already occupied —
// confirmed live twice, 2026-07-16 (chess-v4 reusing checkers-v9's freed
// id 99; goban-v3 reusing chess-v5's freed id 100). Sequential allocation
// makes this collision structurally impossible instead of merely detected:
// the new ID is always strictly less than every existing fixture ID by
// construction. The one tradeoff is that a fixture's ID no longer directly
// reveals its source project's original positive ID — the label carries
// that context instead (e.g. "fixture: checkers post-RECONCILE").
//
// Preconditions: projectID must be positive (a fixture ID passed back in is
// almost certainly a mistake), the project must exist, and it must have zero
// 'running' handoff_jobs (renumbering out from under an in-flight job would
// corrupt whichever row the running job later tries to write back to).
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

	var runningCount int
	if err = d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM handoff_jobs WHERE project_id = ? AND status = 'running'`, projectID,
	).Scan(&runningCount); err != nil {
		return 0, "", fmt.Errorf("count running jobs: %w", err)
	}
	if runningCount > 0 {
		return 0, "", fmt.Errorf("project %d has %d running job(s) — wait for them to finish before saving a fixture", projectID, runningCount)
	}

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

	// Allocate the new fixture ID inside the same transaction as the rename,
	// so a concurrent save-fixture can't compute the same "next" ID twice —
	// SQLite's single-writer setup (db.go's SetMaxOpenConns(1)) serializes
	// this with any other in-flight write anyway, but computing it here
	// keeps the invariant obviously true rather than incidentally true.
	var minFixtureID sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT MIN(id) FROM projects WHERE id < 0`).Scan(&minFixtureID); err != nil {
		_ = tx.Rollback()
		return 0, "", fmt.Errorf("compute next fixture id: %w", err)
	}
	newID = -1
	if minFixtureID.Valid {
		newID = minFixtureID.Int64 - 1
	}

	if err = renumberFixtureID(ctx, tx, projectID, newID, now); err != nil {
		_ = tx.Rollback()
		return 0, "", err
	}

	if _, err = tx.ExecContext(ctx,
		`UPDATE projects SET label = ?, status = 'fixture', updated_at = ? WHERE id = ?`,
		label, now, newID,
	); err != nil {
		_ = tx.Rollback()
		return 0, "", fmt.Errorf("set fixture label/status: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("commit: %w", err)
	}

	return newID, label, nil
}

// renumberFixtureID moves oldID's project row (and every project-scoped
// table's rows, plus any recovered_from_project_id backreference) to newID
// within tx. Callers must run this inside a transaction with
// PRAGMA foreign_keys=OFF already toggled (SQLite refuses to toggle it
// mid-transaction), and must handle any other fields on the projects row
// (label, status) themselves, in the same transaction — this only moves the
// ID and everything that has a live FK into it.
func renumberFixtureID(ctx context.Context, tx *sql.Tx, oldID, newID int64, now string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET id = ?, updated_at = ? WHERE id = ?`,
		newID, now, oldID,
	); err != nil {
		return fmt.Errorf("renumber project row: %w", err)
	}

	for _, table := range fixtureScopedTables {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET project_id = ? WHERE project_id = ?`, table),
			newID, oldID,
		); err != nil {
			return fmt.Errorf("renumber %s: %w", table, err)
		}
	}

	// Repoint any other project's restart lineage so it still resolves —
	// mirrors the care internal/ui/handlers.go's project-delete handler
	// already takes for this same backreference (it nulls it out there since
	// the referenced project is gone; here it's just renumbered, so repoint
	// instead of null).
	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET recovered_from_project_id = ? WHERE recovered_from_project_id = ?`,
		newID, oldID,
	); err != nil {
		return fmt.Errorf("repoint recovered_from_project_id: %w", err)
	}

	return nil
}
