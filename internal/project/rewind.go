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
//   - prose (full_text) rolled back to the most recent revision NOT created
//     by ADJUDICATE_NEXT_EXECUTION — preserving DECOMPOSE_SPEC/
//     RECONCILE_DECOMPOSITION/REVISE_PENDING content, discarding only the
//     reactive patches ADJUDICATE_NEXT_EXECUTION accumulated in response to
//     this bead's own repeated failures; everything else (title,
//     output_files, exit_criteria, execution_budget, monitor_override) kept
//     from the current revision, since those can carry permanent corrections
//     (a RECONCILE-added missing test file, an ADJUDICATE stray-file cleanup
//     target, a budget doubled after a timeout) that an earlier revision
//     never had
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
	note := flags.String("note", "", "optional human guidance note to append for the next attempt")
	supersedes := flags.Int("supersedes", 0, "note number this note supersedes or retracts (optional)")
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

	result, err := rewindBead(context.Background(), d, *beadID, RewindOptions{Note: *note, Supersedes: *supersedes})
	if err != nil {
		slog.Error("rewind-bead", "error", err)
		os.Exit(1)
	}

	fmt.Printf("bead rewound\n")
	fmt.Printf("  bead-id:        %d\n", *beadID)
	fmt.Printf("  project-id:     %d\n", result.ProjectID)
	fmt.Printf("  spec reset to:  last pre-ADJUDICATE_NEXT_EXECUTION prose, current revision for everything else\n")
	fmt.Printf("  attempt budget: %d → %d\n", result.BudgetFrom, result.BudgetTo)
	fmt.Printf("  next verb:      REFINE_TESTS_WRITE\n")
	fmt.Printf("  pre-rewind snapshot: %s\n", result.SnapshotDir)
	if result.NewNoteNumber > 0 {
		fmt.Printf("  guidance note added: Note %d\n", result.NewNoteNumber)
	}
	if *supersedes != 0 {
		fmt.Printf("  supersedes:          Note %d\n", *supersedes)
	}
	if len(result.DeletedTests) > 0 {
		fmt.Printf("  test files deleted:\n")
		for _, f := range result.DeletedTests {
			fmt.Printf("    %s\n", f)
		}
	}
	if len(result.StubbedFiles) > 0 {
		fmt.Printf("  impl files stubbed:\n")
		for _, f := range result.StubbedFiles {
			fmt.Printf("    %s\n", f)
		}
	}
	if len(result.DeletedFiles) > 0 {
		fmt.Printf("  impl files deleted (not in SURVEY manifest, no stub baseline):\n")
		for _, f := range result.DeletedFiles {
			fmt.Printf("    %s\n", f)
		}
	}
}

// RewindOptions carries optional human input for a rewind, layered on top of
// the automatic prose/budget/exit-criteria merge every rewind performs.
type RewindOptions struct {
	// Note, if non-empty, is appended to the restored prose as a new,
	// numbered Guidance Log entry (see guidance_log.go). Appended, never
	// patched in place — earlier notes keep their original text forever.
	Note string
	// Supersedes, if non-zero, marks that earlier note number's status as
	// superseded (if Note is also given) or retracted (if not). The earlier
	// note's text is never edited or removed. Referencing a note number that
	// doesn't exist is an error, not a silent no-op.
	Supersedes int
}

// rewindResult reports what rewindBead actually did, for RunRewindBeadMain to print.
type rewindResult struct {
	ProjectID     int64
	BudgetFrom    int
	BudgetTo      int
	SnapshotDir   string
	NewNoteNumber int // 0 if no Note was given
	DeletedTests  []string
	StubbedFiles  []string
	DeletedFiles  []string
}

// rewindBead resets bead beadID to a clean, re-runnable state. See
// RunRewindBeadMain's doc comment for the full behavior; factored out as its
// own function (mirroring fullStopProject) so it can be exercised directly by
// tests instead of only through the os.Exit-based CLI entry point.
func rewindBead(ctx context.Context, d *db.DB, beadID int64, opts RewindOptions) (*rewindResult, error) {
	var projectID int64
	var beadStatus string
	var currentRevisionID int64
	if err := d.QueryRowContext(ctx,
		`SELECT project_id, status, current_revision_id FROM beads WHERE id = ?`, beadID,
	).Scan(&projectID, &beadStatus, &currentRevisionID); err == sql.ErrNoRows {
		return nil, fmt.Errorf("bead not found: %d", beadID)
	} else if err != nil {
		return nil, fmt.Errorf("query bead: %w", err)
	}

	if beadStatus == "succeeded" {
		return nil, fmt.Errorf("bead %d has already succeeded", beadID)
	}

	var projectFolder, projectStatus string
	var maxAttempts int
	if err := d.QueryRowContext(ctx,
		`SELECT folder_path, status, max_execution_attempts FROM projects WHERE id = ?`, projectID,
	).Scan(&projectFolder, &projectStatus, &maxAttempts); err != nil {
		return nil, fmt.Errorf("query project: %w", err)
	}

	// Find the most recent revision NOT created by ADJUDICATE_NEXT_EXECUTION —
	// full_text is the one field rewind actually needs to restore, since
	// that's what execute_revised's verbatim patches corrupt over repeated
	// attempts. ADJUDICATE_NEXT_EXECUTION is specifically the reactive,
	// compounding mechanism this is undoing: each of its revisions reacts to
	// its own prior revision's execution failure, which is how a spec
	// accumulates increasingly desperate patches (or, as with checkers bead 2,
	// a misdiagnosed requirement baked in permanently). DECOMPOSE_SPEC,
	// RECONCILE_DECOMPOSITION, and REVISE_PENDING revisions are not that kind
	// of thing — REVISE_PENDING in particular only ever revises a bead once,
	// informationally, while it is still pending, based on a sibling bead's
	// success, never in reaction to this bead's own failure. Rolling all the
	// way back to revision 1 unconditionally would discard that legitimate
	// cross-bead learning right alongside the corrupted content — confirmed
	// against real history before this change: every bead across the archived
	// project set that ever accumulated an ADJUDICATE_NEXT_EXECUTION revision
	// had all of its REVISE_PENDING/RECONCILE_DECOMPOSITION revisions strictly
	// before that point, never interleaved after (REVISE_PENDING cannot touch
	// a bead once it leaves 'pending', which happens no later than its first
	// execution attempt — the earliest an ADJUDICATE_NEXT_EXECUTION revision
	// could exist).
	var restoreFullText string
	if err := d.QueryRowContext(ctx,
		`SELECT full_text FROM bead_revisions
		 WHERE bead_id = ? AND created_by_verb != 'ADJUDICATE_NEXT_EXECUTION'
		 ORDER BY revision_number DESC LIMIT 1`,
		beadID,
	).Scan(&restoreFullText); err != nil {
		return nil, fmt.Errorf("find last pre-adjudication revision: %w", err)
	}

	// Load the current (pre-rewind) revision's full_text plus its real
	// execution_budget/monitor_override DB columns — not the same-named
	// fields embedded in the JSON full_text blob, which are vestigial: every
	// revision-creating verb writes the authoritative value to its own DB
	// column at INSERT time, and nothing downstream ever reads the JSON copy
	// back out. Everything about the current revision — output_files,
	// exit_criteria, title, budget, monitor_override — can carry permanent
	// corrections picked up after revision 1 (RECONCILE_DECOMPOSITION's
	// goFixBeadSpec adding a missing test file, ADJUDICATE's "workspace
	// repair" adding a stray-file cleanup target, a budget doubled after a
	// timeout) and none of that is the kind of prose drift rewind is meant to
	// undo. Reverting past it silently re-creates the exact bugs those fixes
	// addressed: a bead whose output_files no longer matches what
	// refine_tests.go/mechanical checks expect, or — the checkers-try-1
	// bead-684 incident — an execution_budget reset to a stale placeholder
	// value from the original DECOMPOSE_SPEC output, instantly timing out
	// every rewound bead's first EXECUTE_BEAD attempt. The merge below
	// therefore defaults to the current revision for every field and reverts
	// only full_text — the one field known to actually go stale — instead of
	// defaulting to revision 1 and hand-listing exceptions, which is what let
	// both of those bugs go unnoticed.
	var currentRevisionFullText, currentMonitorOverride string
	var currentExecutionBudget int
	if err := d.QueryRowContext(ctx,
		`SELECT full_text, execution_budget, monitor_override FROM bead_revisions WHERE id = ?`,
		currentRevisionID,
	).Scan(&currentRevisionFullText, &currentExecutionBudget, &currentMonitorOverride); err != nil {
		return nil, fmt.Errorf("load current revision: %w", err)
	}

	var restoreSpec, currentSpec verbs.ParsedBead
	if err := json.Unmarshal([]byte(restoreFullText), &restoreSpec); err != nil {
		return nil, fmt.Errorf("parse first revision: %w", err)
	}
	if err := json.Unmarshal([]byte(currentRevisionFullText), &currentSpec); err != nil {
		return nil, fmt.Errorf("parse current revision: %w", err)
	}

	// Merge: default to the current revision for every field, then revert
	// only full_text (prose) to the last pre-ADJUDICATE_NEXT_EXECUTION
	// revision found above. Keep the JSON blob's own
	// execution_budget/monitor_override fields in sync with the real DB
	// columns written below, so the stored spec text never disagrees with
	// the actual effective values.
	mergedSpec := currentSpec
	mergedSpec.FullText = restoreSpec.FullText
	mergedSpec.ExecutionBudget = currentExecutionBudget
	mergedSpec.MonitorOverride = currentMonitorOverride

	now := time.Now().UTC().Format(time.RFC3339)

	// Layer any human guidance for this attempt on top of the restored prose.
	// Appended as a new, numbered Guidance Log entry rather than patched into
	// the prose in place — see guidance_log.go's doc comment for why: that's
	// exactly the failure mode (ADJUDICATE_NEXT_EXECUTION's reactive patches)
	// this whole rewind mechanism already exists to undo. A bad --supersedes
	// value fails loudly here, before anything else about this rewind happens.
	updatedFullText, newNoteNumber, err := applyGuidance(mergedSpec.FullText, opts.Note, opts.Supersedes, now)
	if err != nil {
		return nil, fmt.Errorf("rewind-bead: %w", err)
	}
	mergedSpec.FullText = updatedFullText

	// Re-run the mechanical exit-criteria fixes (asterisk-escaping,
	// apiCheckTestFilename content-check stripping, bare-grep file assignment,
	// ...) over the inherited exit_criteria. The current revision's
	// exit_criteria may predate a mechanical fix landing in mechanical_checks.go
	// — carrying it forward unchanged would silently re-ship whatever bug that
	// fix addressed. This is what lets rewind-bead self-heal a bead like
	// checkers-try-1's 684 instead of requiring a manual DB patch.
	verbs.ApplyMechanicalBeadFixes(projectFolder, &mergedSpec)

	mergedFullText, err := json.Marshal(mergedSpec)
	if err != nil {
		return nil, fmt.Errorf("marshal merged revision: %w", err)
	}
	outputFiles := mergedSpec.OutputFiles

	// Preserve the pre-rewind file content before anything below deletes or
	// stubs it. This is a hard precondition, not best-effort: if we can't
	// confirm a snapshot exists, we don't proceed with destroying the
	// originals — no DB state changes yet at this point, so failing here
	// leaves the escalated bead untouched rather than half-rewound. The
	// bead-report.md written at escalation already captures file content as
	// text, but not a runnable/diffable tree — this is what makes attempt N's
	// actual broken code inspectable (or restorable) after rewind moves on.
	snapshotDir, preservedFiles, err := verbs.SnapshotBeadFiles(projectFolder, "rewind", beadID, outputFiles)
	if err != nil {
		return nil, fmt.Errorf("snapshot pre-rewind files: %w", err)
	}

	// Count existing valid executions to grant a fresh budget on top.
	var existingExecutions int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM executions WHERE bead_id = ? AND infra_failure = 0 AND test_first_attempt = 0`,
		beadID,
	).Scan(&existingExecutions); err != nil {
		return nil, fmt.Errorf("count executions: %w", err)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}

	// Cancel any active jobs for this bead.
	if _, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'complete', updated_at = ?
		 WHERE bead_id = ? AND status IN ('escalated', 'pending', 'failed_retry', 'running')`,
		now, beadID,
	); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("cancel jobs: %w", err)
	}

	// Insert the merged spec (revision-1 prose + current revision for
	// everything else) as a fresh revision, bead-wide MAX(revision_number)+1
	// like every other bead_revisions insert site, to avoid numbering collisions.
	var maxRevNum int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(revision_number), 0) FROM bead_revisions WHERE bead_id = ?`, beadID,
	).Scan(&maxRevNum); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("load max revision number: %w", err)
	}
	revRes, err := tx.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text,
		   execution_budget, monitor_override, created_by_verb, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'REWIND_BEAD', ?)`,
		projectID, beadID, maxRevNum+1, string(mergedFullText),
		currentExecutionBudget, currentMonitorOverride, now,
	)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("insert merged revision: %w", err)
	}
	newRevisionID, err := revRes.LastInsertId()
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("get merged revision id: %w", err)
	}

	// Point the bead at the merged revision and grant a fresh execution budget.
	// rewound_at records the boundary so loadBeadRevisionLog/currentLineageRevisionIDs
	// can tell pre-rewind revisions apart from post-rewind ones — revision_number
	// alone can't do this anymore, since every bead_revisions insert site now
	// uses a bead-wide MAX(revision_number)+1 to avoid numbering collisions,
	// which means revision numbers never repeat or go backwards across a rewind
	// for the lineage filter to detect (see currentLineage in inputs.go).
	newAttemptCap := existingExecutions + maxAttempts
	if _, err := tx.ExecContext(ctx,
		`UPDATE beads SET status = 'pending', current_revision_id = ?, execution_attempts_override = ?, rewound_at = ? WHERE id = ?`,
		newRevisionID, newAttemptCap, now, beadID,
	); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("reset bead: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM test_refinements WHERE bead_id = ?`, beadID,
	); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("clear test_refinements: %w", err)
	}

	// Clear the rolling COMPRESS_ANALYSIS summary too — it's fed back to the
	// model on every future COMPRESS_ANALYSIS call as "Existing Compressed
	// History", so left alone it keeps narrating pre-rewind attempts (e.g.
	// "TestPlaceStone/KoCreation [RECURRING x5]") against a test file that no
	// longer exists after this rewind.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM compressed_history WHERE bead_id = ?`, beadID,
	); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("clear compressed_history: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs
		  (project_id, verb, bead_id, status, created_at, updated_at, refinement_cycle_id)
		VALUES (?, 'REFINE_TESTS_WRITE', ?, 'pending', ?, ?, 1)`,
		projectID, beadID, now, now,
	); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("enqueue job: %w", err)
	}

	// Re-activate the project if it was full_stopped.
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
		return nil, fmt.Errorf("write scaffold stubs: %w", err)
	}

	// Finalize the snapshot's manifest now that every fact it reports
	// (guidance given, what each file's fate was) is actually known — writing
	// it here rather than up front at snapshotBeadFiles time means one
	// complete record instead of a partial one a reader has to reconcile
	// against rewindResult by hand. Purely documentation at this point (the
	// destructive steps above already happened), so a write failure is a
	// warning, not an error that unwinds an already-committed rewind.
	if err := writeRewindManifest(snapshotDir, beadID, opts, preservedFiles, deletedTests, stubbedFiles, deletedFiles); err != nil {
		slog.Warn("rewind-bead: write snapshot manifest", "dir", snapshotDir, "error", err)
	}

	return &rewindResult{
		ProjectID:     projectID,
		BudgetFrom:    existingExecutions,
		BudgetTo:      newAttemptCap,
		SnapshotDir:   snapshotDir,
		NewNoteNumber: newNoteNumber,
		DeletedTests:  deletedTests,
		StubbedFiles:  stubbedFiles,
		DeletedFiles:  deletedFiles,
	}, nil
}

// writeRewindManifest writes the snapshot dir's README.md once every fact it
// reports is known: the guidance given for the next attempt (if any), the
// files preserved, and each file's actual fate. This is the single forensic
// record for one rewind — what changed in the spec and what that cost on
// disk — rather than the two disconnected pieces (a pre-rewind file list and
// a CLI printout) it used to be split across.
func writeRewindManifest(snapshotDir string, beadID int64, opts RewindOptions, preserved, deletedTests, stubbedFiles, deletedFiles []string) error {
	var m strings.Builder
	fmt.Fprintf(&m, "# Bead %d rewind snapshot\n\n", beadID)
	fmt.Fprintf(&m, "Taken: %s\n\n", time.Now().UTC().Format(time.RFC3339))

	m.WriteString("## Guidance for next attempt\n\n")
	if opts.Note != "" {
		fmt.Fprintf(&m, "%s\n", opts.Note)
	} else {
		m.WriteString("(no new guidance note given)\n")
	}
	if opts.Supersedes != 0 {
		fmt.Fprintf(&m, "\nSupersedes/retracts: Note %d\n", opts.Supersedes)
	}

	m.WriteString("\n## Pre-rewind content preserved\n\n")
	if len(preserved) == 0 {
		m.WriteString("(no output files existed on disk yet)\n")
	}
	for _, f := range preserved {
		fmt.Fprintf(&m, "- %s\n", f)
	}

	m.WriteString("\n## What happened to each file\n\n")
	for _, f := range deletedTests {
		fmt.Fprintf(&m, "- %s: deleted (test file, regenerated by REFINE_TESTS_WRITE)\n", f)
	}
	for _, f := range stubbedFiles {
		fmt.Fprintf(&m, "- %s: reset to scaffold stub\n", f)
	}
	for _, f := range deletedFiles {
		fmt.Fprintf(&m, "- %s: deleted (not in SURVEY manifest, no stub baseline)\n", f)
	}

	return os.WriteFile(filepath.Join(snapshotDir, "README.md"), []byte(m.String()), 0o644)
}
