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

// idMap tracks an old row ID -> new row ID remapping for one table, built up
// as clone-project inserts each row and reads back its fresh autoincrement ID.
type idMap map[int64]int64

// RunCloneProjectMain is the entry point for the `ratchet clone-project` subcommand.
func RunCloneProjectMain(args []string) {
	flags := flag.NewFlagSet("clone-project", flag.ExitOnError)
	dbPath := flags.String("db", "ratchet.db", "path to SQLite database")
	fromID := flags.Int64("from", 0, "project ID to clone from — positive for a live project, negative for a fixture (required)")
	label := flags.String("label", "", "label for the new project (required)")
	folder := flags.String("folder", "", "folder path for the new project — must not already exist (required)")
	designDoc := flags.String("design-doc", "", "path to a design doc whose content replaces the source's at the same design_doc_path (optional; defaults to copying the source's design doc unchanged)")
	_ = flags.Parse(args)

	if *fromID == 0 {
		slog.Error("clone-project: --from is required")
		os.Exit(1)
	}
	if *label == "" {
		slog.Error("clone-project: --label is required")
		os.Exit(1)
	}
	if *folder == "" {
		slog.Error("clone-project: --folder is required")
		os.Exit(1)
	}

	d, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("clone-project: open db", "error", err)
		os.Exit(1)
	}
	defer d.Close()

	newID, lineageRootID, iterationNumber, err := cloneProject(context.Background(), d, *fromID, *label, *folder, *designDoc)
	if err != nil {
		slog.Error("clone-project", "error", err)
		os.Exit(1)
	}

	folderAbs, _ := filepath.Abs(*folder)
	fmt.Printf("project cloned\n")
	fmt.Printf("  from id:    %d\n", *fromID)
	fmt.Printf("  new id:     %d\n", newID)
	fmt.Printf("  label:      %s\n", *label)
	fmt.Printf("  folder:     %s\n", folderAbs)
	fmt.Printf("  lineage:    root project %d, iteration %d\n", lineageRootID, iterationNumber)
	if *designDoc != "" {
		fmt.Printf("  design doc: replaced with %s\n", *designDoc)
		fmt.Printf("  cascade:    started — AUDIT_DECOMPOSITION queued against project %d's beads\n", *fromID)
	}
}

// cloneProject makes a true deep copy of fromID (positive live project or
// negative fixture — structurally identical either way, per the design):
// a fresh folder tree on disk plus a fresh set of DB rows across every
// project-scoped table, with every internal foreign key remapped to the new
// row IDs. Unlike saveFixture's in-place renumber, the source is left
// completely untouched — the whole point is to run the same starting point
// repeatedly without mutating it. One transaction: either every table's rows
// land correctly remapped, or nothing is written.
//
// Lineage-aware (loop-mode iteration model): the clone continues the
// source's lineage rather than starting a new one — it inherits the
// source's lineage_root_id and takes iteration_number = source's + 1. This
// is exactly clone-project's original stated purpose ("run the same
// starting point repeatedly") applied to the lineage columns; a genuinely
// unrelated fork should use new-project instead. Refuses to create a
// duplicate iteration if one already exists for this lineage — a lineage
// must have at most one project per iteration_number, or "does iteration N
// exist" stops being a well-defined question.
//
// designDocOverride, if non-empty, replaces the design doc's *content* at
// its existing path within the cloned folder — the file at
// design_doc_path is overwritten with designDocOverride's content once the
// folder tree is copied; design_doc_path itself is never renamed, so the
// override file's own name is irrelevant. This is how a human's edited or
// appended design doc (see the loop-mode iteration model's cascade design)
// actually gets into a new iteration: clone carries everything else
// forward unchanged, this is the one deliberate injection point.
//
// Preconditions: the source project must exist, must have zero 'running'
// handoff_jobs (a copied 'running' row would be orphaned in the new project —
// nothing is actually executing it there), newFolder must not already exist,
// designDocOverride (if given) must exist, and no project may already claim
// the computed next iteration number in this lineage.
func cloneProject(ctx context.Context, d *db.DB, fromID int64, newLabel, newFolder, designDocOverride string) (newID, lineageRootID int64, iterationNumber int, err error) {
	if designDocOverride != "" {
		if _, statErr := os.Stat(designDocOverride); statErr != nil {
			return 0, 0, 0, fmt.Errorf("design doc override not found: %s", designDocOverride)
		}
	}
	var src struct {
		DesignDocPath          string
		MonitorOverrideDefault string
		ExecutionBudgetDefault int
		AuditReconcileRoundCap int
		MaxExecutionAttempts   int
		Language               string
		PauseAfterReconcile    bool
		PauseAfterVerb         sql.NullString
		PauseAfterBeadID       sql.NullInt64
		ReconcileSelfResolve   bool
		LineageRootID          sql.NullInt64
		IterationNumber        int
		FolderPath             string
	}
	if err = d.QueryRowContext(ctx, `
		SELECT design_doc_path, monitor_override_default, execution_budget_default,
		       audit_reconcile_round_cap, max_execution_attempts, language,
		       pause_after_reconcile, pause_after_verb, pause_after_bead_id,
		       reconcile_self_resolve, lineage_root_id, iteration_number, folder_path
		FROM projects WHERE id = ?`, fromID,
	).Scan(&src.DesignDocPath, &src.MonitorOverrideDefault, &src.ExecutionBudgetDefault,
		&src.AuditReconcileRoundCap, &src.MaxExecutionAttempts, &src.Language,
		&src.PauseAfterReconcile, &src.PauseAfterVerb, &src.PauseAfterBeadID,
		&src.ReconcileSelfResolve, &src.LineageRootID, &src.IterationNumber, &src.FolderPath,
	); err == sql.ErrNoRows {
		return 0, 0, 0, fmt.Errorf("project not found: %d", fromID)
	} else if err != nil {
		return 0, 0, 0, fmt.Errorf("query project: %w", err)
	}
	if !src.LineageRootID.Valid {
		// Should be unreachable: project.Create and the backfillLineageRootID
		// migration both guarantee every project has a lineage_root_id. Caught
		// here rather than silently propagating NULL into the clone, which
		// would leave it lineage-untracked with no way to find it later.
		return 0, 0, 0, fmt.Errorf("project %d has no lineage_root_id — data invariant violated", fromID)
	}
	nextIterationNumber := src.IterationNumber + 1

	var runningCount int
	if err = d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM handoff_jobs WHERE project_id = ? AND status = 'running'`, fromID,
	).Scan(&runningCount); err != nil {
		return 0, 0, 0, fmt.Errorf("count running jobs: %w", err)
	}
	if runningCount > 0 {
		return 0, 0, 0, fmt.Errorf("project %d has %d running job(s) — wait for them to finish before cloning", fromID, runningCount)
	}

	// A design-doc override starts a cascade: clone-project enqueues a single
	// AUDIT_DECOMPOSITION job for the new project below, with created_at set
	// to now. handoff_jobs are otherwise cloned wholesale with their original
	// created_at (cloneHandoffJobs), so any pending/failed_retry job already
	// sitting in the source — necessarily older than that new job — would
	// dispatch first once the clone goes active, running against pre-edit
	// specs before the re-audit the design-doc edit exists to trigger even
	// starts. Requiring the source to be at a genuine rest point (complete,
	// paused, escalated, or full_stopped — not mid-dispatch) closes this off
	// entirely: nothing pending gets cloned in, so the injected
	// AUDIT_DECOMPOSITION job is guaranteed to be the only pending job.
	if designDocOverride != "" {
		var restartableCount int
		if err = d.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM handoff_jobs WHERE project_id = ? AND status IN ('pending', 'failed_retry')`, fromID,
		).Scan(&restartableCount); err != nil {
			return 0, 0, 0, fmt.Errorf("count pending jobs: %w", err)
		}
		if restartableCount > 0 {
			return 0, 0, 0, fmt.Errorf("project %d has %d pending/failed_retry job(s) — a design-doc-override clone starts a cascade review and requires the source at a clean rest point (complete, paused, escalated, or full_stopped)", fromID, restartableCount)
		}
	}

	newFolderAbs, err := filepath.Abs(newFolder)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("resolve new folder path: %w", err)
	}
	if _, statErr := os.Stat(newFolderAbs); statErr == nil {
		return 0, 0, 0, fmt.Errorf("folder already exists: %s", newFolderAbs)
	}

	oldFolderAbs, err := filepath.Abs(src.FolderPath)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("resolve source folder path: %w", err)
	}

	// Go-formatted (RFC3339), not SQLite's datetime('now') — every reader of
	// these timestamps elsewhere in the codebase (e.g. queue.go's
	// activeProject) parses with time.Parse(time.RFC3339, ...) and silently
	// discards the error, so a datetime('now')-formatted value ("2026-08-29
	// 04:13:17", no T/Z) parses to the zero time instead of failing loudly.
	// Confirmed live (2026-08-29): a cloned project's cascade-starting
	// AUDIT_DECOMPOSITION job had exactly this malformed timestamp, the only
	// row in the whole table not matching every other insert's format.
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Checked inside the transaction (not before it started) so a concurrent
	// clone of the same lineage can't both pass this check before either
	// commits — mirrors save-fixture's same-transaction ID allocation for the
	// same reason. Deliberately before copyDir below: there's no reason to
	// pay for a full folder-tree copy only to discard it on a conflict this
	// check would have caught for free.
	var conflictID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM projects WHERE lineage_root_id = ? AND iteration_number = ?`,
		src.LineageRootID.Int64, nextIterationNumber,
	).Scan(&conflictID)
	if err == nil {
		return 0, 0, 0, fmt.Errorf("lineage %d already has an iteration %d (project %d) — clone from that project to continue the lineage, or use new-project to start an unrelated one",
			src.LineageRootID.Int64, nextIterationNumber, conflictID)
	} else if err != sql.ErrNoRows {
		return 0, 0, 0, fmt.Errorf("check existing iteration: %w", err)
	}

	if err = copyDir(oldFolderAbs, newFolderAbs); err != nil {
		return 0, 0, 0, fmt.Errorf("copy folder tree: %w", err)
	}

	if designDocOverride != "" {
		overrideData, readErr := os.ReadFile(designDocOverride)
		if readErr != nil {
			return 0, 0, 0, fmt.Errorf("read design doc override: %w", readErr)
		}
		destPath := filepath.Join(newFolderAbs, src.DesignDocPath)
		if writeErr := os.WriteFile(destPath, overrideData, 0o644); writeErr != nil {
			return 0, 0, 0, fmt.Errorf("write design doc override: %w", writeErr)
		}
	}

	// cascade_baseline_project_id is set only for a design-doc-override
	// clone — it marks the new project as a cascade iteration and points
	// enqueueDecompositionApproved at fromID to diff against once
	// AUDIT_DECOMPOSITION/RECONCILE_DECOMPOSITION converge (cascade_review.go).
	// NULL for an ordinary clone.
	var cascadeBaselineProjectID sql.NullInt64
	if designDocOverride != "" {
		cascadeBaselineProjectID = sql.NullInt64{Int64: fromID, Valid: true}
	}

	res, err := tx.ExecContext(ctx, `
		INSERT INTO projects
		  (label, folder_path, design_doc_path, status,
		   monitor_override_default, execution_budget_default,
		   audit_reconcile_round_cap, max_execution_attempts,
		   language, pause_after_reconcile, pause_after_verb, pause_after_bead_id,
		   reconcile_self_resolve, lineage_root_id, iteration_number,
		   cascade_baseline_project_id,
		   created_at, updated_at)
		SELECT ?, ?, ?, 'active',
		       monitor_override_default, execution_budget_default,
		       audit_reconcile_round_cap, max_execution_attempts,
		       language, pause_after_reconcile, pause_after_verb, pause_after_bead_id,
		       reconcile_self_resolve, lineage_root_id, ?,
		       ?,
		       ?, ?
		FROM projects WHERE id = ?`,
		newLabel, newFolderAbs, src.DesignDocPath, nextIterationNumber, cascadeBaselineProjectID, now, now, fromID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("insert cloned project: %w", err)
	}
	newID, err = res.LastInsertId()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("cloned project id: %w", err)
	}

	if err = cloneVerbModelAssignments(ctx, tx, fromID, newID); err != nil {
		return 0, 0, 0, err
	}

	beadIDs, revivedBeadIDs, err := cloneBeads(ctx, tx, fromID, newID)
	if err != nil {
		return 0, 0, 0, err
	}

	revisionIDs, err := cloneBeadRevisions(ctx, tx, fromID, newID, beadIDs)
	if err != nil {
		return 0, 0, 0, err
	}

	if err = fixupCurrentRevisionIDs(ctx, tx, fromID, beadIDs, revisionIDs); err != nil {
		return 0, 0, 0, err
	}

	// Skipped for a cascade clone: those rounds debated fit against the
	// source's design doc, which this clone just replaced. Carrying them
	// forward would both feed AUDIT/RECONCILE stale "Previous Debate History"
	// about defects in a doc that no longer exists, and inflate
	// nextRoundNumber's MAX(round_number)+1 (project-wide, no notion of
	// "rounds before vs. after the cascade started") so the fresh re-audit's
	// own first round could already be at or past audit_reconcile_round_cap
	// purely from inherited history. A cascade project starts with a clean
	// round-cap counter instead.
	if designDocOverride == "" {
		if err = cloneAuditReconcileRounds(ctx, tx, fromID, newID); err != nil {
			return 0, 0, 0, err
		}
	}

	executionIDs, err := cloneExecutions(ctx, tx, fromID, newID, beadIDs, revisionIDs, oldFolderAbs, newFolderAbs)
	if err != nil {
		return 0, 0, 0, err
	}

	if err = cloneAnalyses(ctx, tx, fromID, newID, executionIDs); err != nil {
		return 0, 0, 0, err
	}

	if err = cloneCompressedHistory(ctx, tx, fromID, newID, beadIDs); err != nil {
		return 0, 0, 0, err
	}

	if err = cloneAdjudications(ctx, tx, fromID, newID, beadIDs, executionIDs); err != nil {
		return 0, 0, 0, err
	}

	if err = cloneSpecRevisions(ctx, tx, fromID, newID, beadIDs, revisionIDs); err != nil {
		return 0, 0, 0, err
	}

	jobIDs, err := cloneHandoffJobs(ctx, tx, fromID, newID, beadIDs)
	if err != nil {
		return 0, 0, 0, err
	}

	if err = cloneHandoffAttempts(ctx, tx, jobIDs); err != nil {
		return 0, 0, 0, err
	}

	verifyAttemptIDs, err := cloneVerifyAttempts(ctx, tx, fromID, newID, jobIDs)
	if err != nil {
		return 0, 0, 0, err
	}

	if err = cloneCertifications(ctx, tx, fromID, newID, verifyAttemptIDs); err != nil {
		return 0, 0, 0, err
	}

	if err = cloneTestRefinements(ctx, tx, fromID, newID, beadIDs); err != nil {
		return 0, 0, 0, err
	}

	// Any bead revived from full_stopped (see cloneBeads) needs its
	// pre-stop on-disk/DB state cleared, not just its status column flipped
	// — otherwise a bead that was mid-flight when the source stopped (not
	// merely never-started, like the case that surfaced this) would carry
	// forward a half-written test file or stale test_refinements/
	// compressed_history narration referencing content that's about to be
	// treated as if it never existed. Deliberately after cloneTestRefinements/
	// cloneCompressedHistory above (clear what was just copied, not before
	// it exists) and after fixupCurrentRevisionIDs (needs each bead's
	// current output_files to know what to reset).
	if err = reviveFullStoppedBeads(ctx, tx, newID, revivedBeadIDs, newFolderAbs); err != nil {
		return 0, 0, 0, err
	}

	// A design-doc override starts the cascade review immediately: the new
	// project already has beads (cloned above), so it enters the
	// AUDIT_DECOMPOSITION/RECONCILE_DECOMPOSITION loop directly against the
	// replaced doc rather than going through DECOMPOSE_SPEC again. This is
	// the only pending job in the new project (guaranteed by the
	// pending/failed_retry precondition above), so it's the first thing the
	// orchestrator dispatches once the project goes active.
	if designDocOverride != "" {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, NULL, 'pending', ?, ?)`,
			newID, db.VerbAuditDecomposition, now, now); err != nil {
			return 0, 0, 0, fmt.Errorf("enqueue cascade AUDIT_DECOMPOSITION: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return 0, 0, 0, fmt.Errorf("commit: %w", err)
	}

	return newID, src.LineageRootID.Int64, nextIterationNumber, nil
}

func cloneVerbModelAssignments(ctx context.Context, tx *sql.Tx, fromID, newID int64) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO verb_model_assignments (project_id, verb, model)
		SELECT ?, verb, model FROM verb_model_assignments WHERE project_id = ?`,
		newID, fromID); err != nil {
		return fmt.Errorf("clone verb_model_assignments: %w", err)
	}
	return nil
}

// cloneBeads copies every bead row, leaving current_revision_id NULL for now
// (bead_revisions don't exist under their new IDs yet — fixupCurrentRevisionIDs
// fills it in afterward). Returns the old bead ID -> new bead ID map, plus the
// new IDs of any bead whose status was 'full_stopped' in the source.
//
// full_stopped is rewritten to 'pending' rather than copied verbatim.
// full_stopped describes the *source project's* history (something stopped
// it — full-stop-project, a project-wide cap) — it was never a decision
// about that specific bead, and a clone is supposed to represent a fresh
// starting point ready to make progress, not perpetuate whatever administrative
// state the source happened to be in when it stopped. Confirmed live
// (2026-08-29, tictactoe-v1-cascade-2): a bead full-stopped mid-project
// (never actually executed) stayed full_stopped straight through a cascade
// clone since nothing about its own spec had changed for AUDIT/RECONCILE to
// react to — and the project-completion check only watches for 'pending'
// beads, so the project declared itself complete having silently skipped
// that bead's real work entirely.
func cloneBeads(ctx context.Context, tx *sql.Tx, fromID, newID int64) (beadIDs idMap, revivedNewIDs []int64, err error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, status, rewound_at, execution_attempts_override
		FROM beads WHERE project_id = ? ORDER BY id`, fromID)
	if err != nil {
		return nil, nil, fmt.Errorf("query beads: %w", err)
	}

	type row struct {
		oldID                     int64
		status                    string
		rewoundAt                 sql.NullString
		executionAttemptsOverride sql.NullInt64
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.oldID, &r.status, &r.rewoundAt, &r.executionAttemptsOverride); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("scan beads: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("beads rows: %w", err)
	}

	out := make(idMap, len(buf))
	for _, r := range buf {
		status := r.status
		wasFullStopped := status == "full_stopped"
		if wasFullStopped {
			status = "pending"
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO beads (project_id, status, current_revision_id, rewound_at, execution_attempts_override)
			VALUES (?, ?, NULL, ?, ?)`,
			newID, status, r.rewoundAt, r.executionAttemptsOverride)
		if err != nil {
			return nil, nil, fmt.Errorf("insert bead: %w", err)
		}
		newBeadID, err := res.LastInsertId()
		if err != nil {
			return nil, nil, fmt.Errorf("new bead id: %w", err)
		}
		if wasFullStopped {
			revivedNewIDs = append(revivedNewIDs, newBeadID)
		}
		out[r.oldID] = newBeadID
	}
	return out, revivedNewIDs, nil
}

// reviveFullStoppedBeads resets the on-disk and DB state of every bead
// cloneBeads flipped from full_stopped back to pending, so each one starts
// clean rather than carrying forward whatever partial state it was in when
// the source project stopped. Mirrors rewind-bead's own reset shape (delete
// test files, restore impl files to their scaffold stub, clear
// test_refinements/compressed_history) rather than inventing a new one —
// same problem, same fix, just triggered by a clone instead of a rewind.
func reviveFullStoppedBeads(ctx context.Context, tx *sql.Tx, projectID int64, beadIDs []int64, folderPath string) error {
	for _, beadID := range beadIDs {
		var outputFilesJSON string
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(json_extract(br.full_text, '$.output_files'), '[]')
			FROM beads b JOIN bead_revisions br ON br.id = b.current_revision_id
			WHERE b.id = ?`, beadID,
		).Scan(&outputFilesJSON); err != nil {
			return fmt.Errorf("load output_files for revived bead %d: %w", beadID, err)
		}
		var outputFiles []string
		if err := json.Unmarshal([]byte(outputFilesJSON), &outputFiles); err != nil {
			return fmt.Errorf("parse output_files for revived bead %d: %w", beadID, err)
		}

		for _, f := range outputFiles {
			if !strings.HasSuffix(f, "_test.go") {
				continue
			}
			path := filepath.Join(folderPath, f)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				slog.Warn("clone-project: delete test file on revived bead", "bead_id", beadID, "path", path, "error", err)
			}
		}

		if _, _, err := verbs.WriteScaffoldStubsTx(ctx, tx, projectID, folderPath, outputFiles); err != nil {
			return fmt.Errorf("restore scaffold stubs for revived bead %d: %w", beadID, err)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM test_refinements WHERE bead_id = ?`, beadID); err != nil {
			return fmt.Errorf("clear test_refinements for revived bead %d: %w", beadID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM compressed_history WHERE bead_id = ?`, beadID); err != nil {
			return fmt.Errorf("clear compressed_history for revived bead %d: %w", beadID, err)
		}
	}
	return nil
}

func cloneBeadRevisions(ctx context.Context, tx *sql.Tx, fromID, newID int64, beadIDs idMap) (idMap, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, bead_id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at
		FROM bead_revisions WHERE project_id = ? ORDER BY id`, fromID)
	if err != nil {
		return nil, fmt.Errorf("query bead_revisions: %w", err)
	}

	type row struct {
		oldID           int64
		beadID          int64
		revisionNumber  int
		fullText        string
		executionBudget int
		monitorOverride string
		createdByVerb   string
		createdAt       string
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.oldID, &r.beadID, &r.revisionNumber, &r.fullText,
			&r.executionBudget, &r.monitorOverride, &r.createdByVerb, &r.createdAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan bead_revisions: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bead_revisions rows: %w", err)
	}

	out := make(idMap, len(buf))
	for _, r := range buf {
		newBeadID, ok := beadIDs[r.beadID]
		if !ok {
			return nil, fmt.Errorf("bead_revisions: no remapped bead for old bead_id %d", r.beadID)
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO bead_revisions
			  (project_id, bead_id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			newID, newBeadID, r.revisionNumber, r.fullText, r.executionBudget, r.monitorOverride, r.createdByVerb, r.createdAt)
		if err != nil {
			return nil, fmt.Errorf("insert bead_revisions: %w", err)
		}
		newRevID, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("new bead_revisions id: %w", err)
		}
		out[r.oldID] = newRevID
	}
	return out, nil
}

// fixupCurrentRevisionIDs sets each new bead's current_revision_id now that
// bead_revisions exist under their new IDs. cloneBeads always inserts NULL
// there (new bead_revisions rows don't exist yet at that point), so this
// reads the *source* project's current_revision_id values and remaps them
// through both beadIDs and revisionIDs onto the freshly-inserted rows. A
// source bead with no revision yet (current_revision_id NULL) is skipped,
// leaving its clone NULL too.
func fixupCurrentRevisionIDs(ctx context.Context, tx *sql.Tx, fromID int64, beadIDs, revisionIDs idMap) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, current_revision_id FROM beads WHERE project_id = ? AND current_revision_id IS NOT NULL`, fromID)
	if err != nil {
		return fmt.Errorf("query beads for fixup: %w", err)
	}
	type row struct {
		oldBeadID     int64
		oldRevisionID int64
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.oldBeadID, &r.oldRevisionID); err != nil {
			rows.Close()
			return fmt.Errorf("scan beads for fixup: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("beads for fixup rows: %w", err)
	}

	for _, r := range buf {
		newBeadID, ok := beadIDs[r.oldBeadID]
		if !ok {
			return fmt.Errorf("fixup current_revision_id: no remapped bead for old bead_id %d", r.oldBeadID)
		}
		newRevID, ok := revisionIDs[r.oldRevisionID]
		if !ok {
			return fmt.Errorf("fixup current_revision_id: no remapped bead_revision for old revision_id %d", r.oldRevisionID)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE beads SET current_revision_id = ? WHERE id = ?`, newRevID, newBeadID); err != nil {
			return fmt.Errorf("fixup current_revision_id: %w", err)
		}
	}
	return nil
}

func cloneAuditReconcileRounds(ctx context.Context, tx *sql.Tx, fromID, newID int64) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_reconcile_rounds (project_id, round_number, critique_text, reconciliation, outcome, created_at)
		SELECT ?, round_number, critique_text, reconciliation, outcome, created_at
		FROM audit_reconcile_rounds WHERE project_id = ?`,
		newID, fromID); err != nil {
		return fmt.Errorf("clone audit_reconcile_rounds: %w", err)
	}
	return nil
}

// cloneExecutions copies every execution row, remapping bead_id and
// bead_revision_id and rewriting trace_path's folder prefix from the source
// project's folder to the clone's. The filename portion (which embeds the
// *old* bead ID, e.g. "bead-42-attempt-1.log") is left untouched — that is
// the literal file copyDir already placed under the new folder.
func cloneExecutions(ctx context.Context, tx *sql.Tx, fromID, newID int64, beadIDs, revisionIDs idMap, oldFolderAbs, newFolderAbs string) (idMap, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, bead_id, bead_revision_id, trace_path, termination_cause,
		       monitor_fired, monitor_honored, started_at, ended_at, infra_failure, test_first_attempt
		FROM executions WHERE project_id = ? ORDER BY id`, fromID)
	if err != nil {
		return nil, fmt.Errorf("query executions: %w", err)
	}

	type row struct {
		oldID            int64
		beadID           int64
		beadRevisionID   int64
		tracePath        string
		terminationCause sql.NullString
		monitorFired     sql.NullBool
		monitorHonored   sql.NullBool
		startedAt        string
		endedAt          sql.NullString
		infraFailure     bool
		testFirstAttempt bool
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.oldID, &r.beadID, &r.beadRevisionID, &r.tracePath, &r.terminationCause,
			&r.monitorFired, &r.monitorHonored, &r.startedAt, &r.endedAt, &r.infraFailure, &r.testFirstAttempt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan executions: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("executions rows: %w", err)
	}

	out := make(idMap, len(buf))
	for _, r := range buf {
		newBeadID, ok := beadIDs[r.beadID]
		if !ok {
			return nil, fmt.Errorf("executions: no remapped bead for old bead_id %d", r.beadID)
		}
		newRevID, ok := revisionIDs[r.beadRevisionID]
		if !ok {
			return nil, fmt.Errorf("executions: no remapped bead_revision for old bead_revision_id %d", r.beadRevisionID)
		}
		newTracePath := r.tracePath
		if rel, ok := strings.CutPrefix(r.tracePath, oldFolderAbs); ok {
			newTracePath = newFolderAbs + rel
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO executions
			  (project_id, bead_id, bead_revision_id, trace_path, termination_cause,
			   monitor_fired, monitor_honored, started_at, ended_at, infra_failure, test_first_attempt)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newID, newBeadID, newRevID, newTracePath, r.terminationCause,
			r.monitorFired, r.monitorHonored, r.startedAt, r.endedAt, r.infraFailure, r.testFirstAttempt)
		if err != nil {
			return nil, fmt.Errorf("insert executions: %w", err)
		}
		newExecID, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("new executions id: %w", err)
		}
		out[r.oldID] = newExecID
	}
	return out, nil
}

func cloneAnalyses(ctx context.Context, tx *sql.Tx, fromID, newID int64, executionIDs idMap) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, execution_id, mechanical_findings, analyzer_interpretation, created_at
		FROM analyses WHERE project_id = ? ORDER BY id`, fromID)
	if err != nil {
		return fmt.Errorf("query analyses: %w", err)
	}

	type row struct {
		oldID                  int64
		executionID            int64
		mechanicalFindings     string
		analyzerInterpretation sql.NullString
		createdAt              string
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.oldID, &r.executionID, &r.mechanicalFindings, &r.analyzerInterpretation, &r.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan analyses: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("analyses rows: %w", err)
	}

	for _, r := range buf {
		newExecID, ok := executionIDs[r.executionID]
		if !ok {
			return fmt.Errorf("analyses: no remapped execution for old execution_id %d", r.executionID)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO analyses (project_id, execution_id, mechanical_findings, analyzer_interpretation, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			newID, newExecID, r.mechanicalFindings, r.analyzerInterpretation, r.createdAt); err != nil {
			return fmt.Errorf("insert analyses: %w", err)
		}
	}
	return nil
}

func cloneCompressedHistory(ctx context.Context, tx *sql.Tx, fromID, newID int64, beadIDs idMap) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT bead_id, compressed_text, updated_at
		FROM compressed_history WHERE project_id = ? ORDER BY bead_id`, fromID)
	if err != nil {
		return fmt.Errorf("query compressed_history: %w", err)
	}

	type row struct {
		beadID         int64
		compressedText string
		updatedAt      string
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.beadID, &r.compressedText, &r.updatedAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan compressed_history: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("compressed_history rows: %w", err)
	}

	for _, r := range buf {
		newBeadID, ok := beadIDs[r.beadID]
		if !ok {
			return fmt.Errorf("compressed_history: no remapped bead for old bead_id %d", r.beadID)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO compressed_history (bead_id, project_id, compressed_text, updated_at)
			VALUES (?, ?, ?, ?)`,
			newBeadID, newID, r.compressedText, r.updatedAt); err != nil {
			return fmt.Errorf("insert compressed_history: %w", err)
		}
	}
	return nil
}

func cloneAdjudications(ctx context.Context, tx *sql.Tx, fromID, newID int64, beadIDs, executionIDs idMap) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, bead_id, execution_id, trend, bead_spec_fit, reasoning_text,
		       attempt_budget_cost, monitor_escalation_status, decision, created_at
		FROM adjudications WHERE project_id = ? ORDER BY id`, fromID)
	if err != nil {
		return fmt.Errorf("query adjudications: %w", err)
	}

	type row struct {
		oldID                   int64
		beadID                  int64
		executionID             int64
		trend                   string
		beadSpecFit             string
		reasoningText           string
		attemptBudgetCost       float64
		monitorEscalationStatus bool
		decision                string
		createdAt               string
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.oldID, &r.beadID, &r.executionID, &r.trend, &r.beadSpecFit, &r.reasoningText,
			&r.attemptBudgetCost, &r.monitorEscalationStatus, &r.decision, &r.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan adjudications: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("adjudications rows: %w", err)
	}

	for _, r := range buf {
		newBeadID, ok := beadIDs[r.beadID]
		if !ok {
			return fmt.Errorf("adjudications: no remapped bead for old bead_id %d", r.beadID)
		}
		newExecID, ok := executionIDs[r.executionID]
		if !ok {
			return fmt.Errorf("adjudications: no remapped execution for old execution_id %d", r.executionID)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO adjudications
			  (project_id, bead_id, execution_id, trend, bead_spec_fit, reasoning_text,
			   attempt_budget_cost, monitor_escalation_status, decision, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newID, newBeadID, newExecID, r.trend, r.beadSpecFit, r.reasoningText,
			r.attemptBudgetCost, r.monitorEscalationStatus, r.decision, r.createdAt); err != nil {
			return fmt.Errorf("insert adjudications: %w", err)
		}
	}
	return nil
}

func cloneSpecRevisions(ctx context.Context, tx *sql.Tx, fromID, newID int64, beadIDs, revisionIDs idMap) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, trigger_bead_id, revised_bead_id, old_revision_id, new_revision_id, created_at
		FROM spec_revisions WHERE project_id = ? ORDER BY id`, fromID)
	if err != nil {
		return fmt.Errorf("query spec_revisions: %w", err)
	}

	type row struct {
		oldID         int64
		triggerBeadID int64
		revisedBeadID int64
		oldRevisionID int64
		newRevisionID sql.NullInt64
		createdAt     string
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.oldID, &r.triggerBeadID, &r.revisedBeadID, &r.oldRevisionID, &r.newRevisionID, &r.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan spec_revisions: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("spec_revisions rows: %w", err)
	}

	for _, r := range buf {
		newTriggerBeadID, ok := beadIDs[r.triggerBeadID]
		if !ok {
			return fmt.Errorf("spec_revisions: no remapped bead for old trigger_bead_id %d", r.triggerBeadID)
		}
		newRevisedBeadID, ok := beadIDs[r.revisedBeadID]
		if !ok {
			return fmt.Errorf("spec_revisions: no remapped bead for old revised_bead_id %d", r.revisedBeadID)
		}
		newOldRevisionID, ok := revisionIDs[r.oldRevisionID]
		if !ok {
			return fmt.Errorf("spec_revisions: no remapped bead_revision for old old_revision_id %d", r.oldRevisionID)
		}
		var newNewRevisionID sql.NullInt64
		if r.newRevisionID.Valid {
			remapped, ok := revisionIDs[r.newRevisionID.Int64]
			if !ok {
				return fmt.Errorf("spec_revisions: no remapped bead_revision for old new_revision_id %d", r.newRevisionID.Int64)
			}
			newNewRevisionID = sql.NullInt64{Int64: remapped, Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO spec_revisions (project_id, trigger_bead_id, revised_bead_id, old_revision_id, new_revision_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			newID, newTriggerBeadID, newRevisedBeadID, newOldRevisionID, newNewRevisionID, r.createdAt); err != nil {
			return fmt.Errorf("insert spec_revisions: %w", err)
		}
	}
	return nil
}

func cloneHandoffJobs(ctx context.Context, tx *sql.Tx, fromID, newID int64, beadIDs idMap) (idMap, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at
		FROM handoff_jobs WHERE project_id = ? ORDER BY id`, fromID)
	if err != nil {
		return nil, fmt.Errorf("query handoff_jobs: %w", err)
	}

	type row struct {
		oldID             int64
		verb              string
		beadID            sql.NullInt64
		status            string
		refinementCycleID sql.NullInt64
		createdAt         string
		updatedAt         string
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.oldID, &r.verb, &r.beadID, &r.status, &r.refinementCycleID, &r.createdAt, &r.updatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan handoff_jobs: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("handoff_jobs rows: %w", err)
	}

	out := make(idMap, len(buf))
	for _, r := range buf {
		var newBeadID sql.NullInt64
		if r.beadID.Valid {
			remapped, ok := beadIDs[r.beadID.Int64]
			if !ok {
				return nil, fmt.Errorf("handoff_jobs: no remapped bead for old bead_id %d", r.beadID.Int64)
			}
			newBeadID = sql.NullInt64{Int64: remapped, Valid: true}
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			newID, r.verb, newBeadID, r.status, r.refinementCycleID, r.createdAt, r.updatedAt)
		if err != nil {
			return nil, fmt.Errorf("insert handoff_jobs: %w", err)
		}
		newJobID, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("new handoff_jobs id: %w", err)
		}
		out[r.oldID] = newJobID
	}
	return out, nil
}

// cloneHandoffAttempts copies attempt rows keyed by job_id — handoff_attempts
// has no project_id column of its own (confirmed against schema.sql), so it
// is scoped entirely through the jobIDs map built by cloneHandoffJobs.
func cloneHandoffAttempts(ctx context.Context, tx *sql.Tx, jobIDs idMap) error {
	for oldJobID, newJobID := range jobIDs {
		rows, err := tx.QueryContext(ctx, `
			SELECT attempt_number, raw_output, validation_result, created_at, ended_at
			FROM handoff_attempts WHERE job_id = ? ORDER BY id`, oldJobID)
		if err != nil {
			return fmt.Errorf("query handoff_attempts: %w", err)
		}

		type row struct {
			attemptNumber    int
			rawOutput        sql.NullString
			validationResult string
			createdAt        string
			endedAt          sql.NullString
		}
		var buf []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.attemptNumber, &r.rawOutput, &r.validationResult, &r.createdAt, &r.endedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan handoff_attempts: %w", err)
			}
			buf = append(buf, r)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return fmt.Errorf("handoff_attempts rows: %w", rowsErr)
		}

		for _, r := range buf {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO handoff_attempts (job_id, attempt_number, raw_output, validation_result, created_at, ended_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				newJobID, r.attemptNumber, r.rawOutput, r.validationResult, r.createdAt, r.endedAt); err != nil {
				return fmt.Errorf("insert handoff_attempts: %w", err)
			}
		}
	}
	return nil
}

func cloneVerifyAttempts(ctx context.Context, tx *sql.Tx, fromID, newID int64, jobIDs idMap) (idMap, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, job_id, attempt_number, file_presence_pass, no_behavioral_tests_pass,
		       compile_pass, api_check_pass, stub_purity_pass, violations, verifier_interpretation, created_at
		FROM verify_attempts WHERE project_id = ? ORDER BY id`, fromID)
	if err != nil {
		return nil, fmt.Errorf("query verify_attempts: %w", err)
	}

	type row struct {
		oldID                  int64
		jobID                  int64
		attemptNumber          int
		filePresencePass       bool
		noBehavioralTestsPass  bool
		compilePass            bool
		apiCheckPass           bool
		stubPurityPass         bool
		violations             sql.NullString
		verifierInterpretation sql.NullString
		createdAt              string
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.oldID, &r.jobID, &r.attemptNumber, &r.filePresencePass, &r.noBehavioralTestsPass,
			&r.compilePass, &r.apiCheckPass, &r.stubPurityPass, &r.violations, &r.verifierInterpretation, &r.createdAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan verify_attempts: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("verify_attempts rows: %w", err)
	}

	out := make(idMap, len(buf))
	for _, r := range buf {
		newJobID, ok := jobIDs[r.jobID]
		if !ok {
			return nil, fmt.Errorf("verify_attempts: no remapped job for old job_id %d", r.jobID)
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO verify_attempts
			  (project_id, job_id, attempt_number, file_presence_pass, no_behavioral_tests_pass,
			   compile_pass, api_check_pass, stub_purity_pass, violations, verifier_interpretation, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newID, newJobID, r.attemptNumber, r.filePresencePass, r.noBehavioralTestsPass,
			r.compilePass, r.apiCheckPass, r.stubPurityPass, r.violations, r.verifierInterpretation, r.createdAt)
		if err != nil {
			return nil, fmt.Errorf("insert verify_attempts: %w", err)
		}
		newAttemptID, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("new verify_attempts id: %w", err)
		}
		out[r.oldID] = newAttemptID
	}
	return out, nil
}

func cloneCertifications(ctx context.Context, tx *sql.Tx, fromID, newID int64, verifyAttemptIDs idMap) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT verify_attempt_id, preliminary_decision, model_reasoning, final_decision, feedback, created_at
		FROM certifications WHERE project_id = ? ORDER BY id`, fromID)
	if err != nil {
		return fmt.Errorf("query certifications: %w", err)
	}

	type row struct {
		verifyAttemptID     int64
		preliminaryDecision string
		modelReasoning      sql.NullString
		finalDecision       string
		feedback            sql.NullString
		createdAt           string
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.verifyAttemptID, &r.preliminaryDecision, &r.modelReasoning, &r.finalDecision, &r.feedback, &r.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan certifications: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("certifications rows: %w", err)
	}

	for _, r := range buf {
		newVerifyAttemptID, ok := verifyAttemptIDs[r.verifyAttemptID]
		if !ok {
			return fmt.Errorf("certifications: no remapped verify_attempt for old verify_attempt_id %d", r.verifyAttemptID)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO certifications (project_id, verify_attempt_id, preliminary_decision, model_reasoning, final_decision, feedback, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			newID, newVerifyAttemptID, r.preliminaryDecision, r.modelReasoning, r.finalDecision, r.feedback, r.createdAt); err != nil {
			return fmt.Errorf("insert certifications: %w", err)
		}
	}
	return nil
}

func cloneTestRefinements(ctx context.Context, tx *sql.Tx, fromID, newID int64, beadIDs idMap) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT bead_id, cycle_id, turn, verb, changed, summary, decision, created_at
		FROM test_refinements WHERE project_id = ? ORDER BY id`, fromID)
	if err != nil {
		return fmt.Errorf("query test_refinements: %w", err)
	}

	type row struct {
		beadID    int64
		cycleID   int
		turn      int
		verb      string
		changed   bool
		summary   sql.NullString
		decision  string
		createdAt string
	}
	var buf []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.beadID, &r.cycleID, &r.turn, &r.verb, &r.changed, &r.summary, &r.decision, &r.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan test_refinements: %w", err)
		}
		buf = append(buf, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("test_refinements rows: %w", err)
	}

	for _, r := range buf {
		newBeadID, ok := beadIDs[r.beadID]
		if !ok {
			return fmt.Errorf("test_refinements: no remapped bead for old bead_id %d", r.beadID)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO test_refinements (project_id, bead_id, cycle_id, turn, verb, changed, summary, decision, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newID, newBeadID, r.cycleID, r.turn, r.verb, r.changed, r.summary, r.decision, r.createdAt); err != nil {
			return fmt.Errorf("insert test_refinements: %w", err)
		}
	}
	return nil
}

// copyDir recursively copies every file and subdirectory from src into dst.
// dst must not already exist; it (and every subdirectory) is created as the
// walk encounters it. Pure Go rather than shelling out to `cp -r`, matching
// the existing copyFile helper's style (see restart.go) — no exec.Command
// dependency, portable, and works the same in a sandboxed environment.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}
