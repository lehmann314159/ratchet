package verbs

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ratchet/internal/report"
)

// enqueueCascadeReview runs in place of enqueueFirstBeadForExecution when a
// cascade project's AUDIT_DECOMPOSITION/RECONCILE_DECOMPOSITION loop
// converges (see enqueueDecompositionApproved, inputs.go). projectID was
// materialized by clone-project --design-doc: every bead and its full
// execution/adjudication history was cloned wholesale from
// baselineProjectID, then the design doc was replaced and AUDIT/RECONCILE
// re-run against the inherited beads.
//
// This diffs every bead's now-approved spec against the corresponding bead
// (matched by title — RECONCILE cannot rename, add, or remove beads, so
// title identity is stable across the clone boundary; see
// ReconcileDecomposition.Validate) in baselineProjectID. Any textual
// difference in full_text, execution_budget, or monitor_override counts as
// changed — invalidation is deliberately liberal (see
// project_loop_mode_pivot memory): a stale artifact silently surviving a
// real change is worse than an unnecessary re-execution. A changed bead is
// reset to a clean, re-runnable state by resetBeadForRerun, including beads
// already 'succeeded' under the clone. An unchanged bead is left exactly as
// cloned — status, files, and execution/adjudication history untouched.
//
// Returns dispatched=true if a changed bead's execution was enqueued (the
// caller should still apply the normal pause-after-decomposition check), or
// false if no bead changed and the project was marked complete directly (no
// pause makes sense — there is nothing left to resume into).
func enqueueCascadeReview(ctx context.Context, tx *sql.Tx, projectID, baselineProjectID int64, now string) (dispatched bool, err error) {
	var folderPath string
	if err := tx.QueryRowContext(ctx,
		`SELECT folder_path FROM projects WHERE id = ?`, projectID,
	).Scan(&folderPath); err != nil {
		return false, fmt.Errorf("load cascade project folder: %w", err)
	}

	newBeads, err := loadCurrentBeadsTx(ctx, tx, projectID)
	if err != nil {
		return false, fmt.Errorf("load cascade project beads: %w", err)
	}
	baselineBeads, err := loadCurrentBeadsTx(ctx, tx, baselineProjectID)
	if err != nil {
		return false, fmt.Errorf("load cascade baseline beads: %w", err)
	}
	baselineByTitle := make(map[string]beadState, len(baselineBeads))
	for _, b := range baselineBeads {
		baselineByTitle[b.Title] = b
	}

	for _, nb := range newBeads {
		ob, ok := baselineByTitle[nb.Title]
		if ok && !beadSpecChanged(nb, ob) {
			continue
		}
		if err := resetBeadForRerun(ctx, tx, projectID, baselineProjectID, nb, folderPath, now); err != nil {
			return false, fmt.Errorf("reset changed bead %q: %w", nb.Title, err)
		}
	}

	var firstPendingBeadID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM beads WHERE project_id = ? AND status = 'pending' ORDER BY id LIMIT 1`,
		projectID,
	).Scan(&firstPendingBeadID)
	if err == sql.ErrNoRows {
		if _, err := tx.ExecContext(ctx,
			`UPDATE projects SET status = 'complete', updated_at = ? WHERE id = ?`,
			now, projectID,
		); err != nil {
			return false, fmt.Errorf("mark cascade project complete: %w", err)
		}
		report.WriteProject(ctx, tx, projectID, folderPath)
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find first changed bead: %w", err)
	}
	if err := EnqueueBeadExecution(ctx, tx, projectID, firstPendingBeadID, now); err != nil {
		return false, err
	}
	return true, nil
}

// beadSpecChanged reports whether two beads with the same title, one from
// the cascade project and one from its baseline, differ. full_text is the
// raw stored JSON blob (title, prose, output_files, exit_criteria all
// embedded), so comparing it as text already implements "any textual diff
// counts" for everything in the spec's prose and file/criteria lists;
// execution_budget and monitor_override are compared separately because
// they are the authoritative bead_revisions columns, not the vestigial
// copies embedded in full_text (see rewindBead's doc comment in
// internal/project/rewind.go for why the JSON copies are never trusted).
func beadSpecChanged(a, b beadState) bool {
	return a.FullText != b.FullText ||
		a.ExecutionBudget != b.ExecutionBudget ||
		a.MonitorOverride != b.MonitorOverride
}

// resetBeadForRerun resets bead nb (from the cascade project) to a clean,
// re-runnable state because the diff against its baseline counterpart proved
// its spec changed. Deliberately bypasses rewindBead's "already succeeded"
// guard: unlike a rewind (reacting to an execution failure), this reset has
// mechanical proof — the diff — that the spec under this bead's prior result
// no longer matches what was approved, so a 'succeeded' status here is stale
// by construction, not a mistake to protect against.
//
// Cancels any leftover active job for the bead (e.g. an 'escalated' job
// cloned from a baseline that was resting on exactly the defect the design
// doc edit fixed), clears execution_attempts_override back to the project
// default (a fresh budget, not rewindBead's existing-attempts-plus-more
// formula — the prior attempts belonged to a now-superseded spec, not
// legitimate spend against this one), deletes test files, and resets impl
// files to their scaffold baseline. Leaves executions/analyses/adjudications
// rows untouched as superseded history, matching rewindBead's own precedent
// of never deleting those tables — only test_refinements and
// compressed_history, both regenerated working state, are cleared.
//
// A pre-reset snapshot under traces/bead-{id}-cascade-{n}/ is taken before
// any destructive step, exactly mirroring rewindBead's own precondition —
// see SnapshotBeadFiles's doc comment.
func resetBeadForRerun(ctx context.Context, tx *sql.Tx, projectID, baselineProjectID int64, nb beadState, folderPath, now string) error {
	snapshotDir, preserved, err := SnapshotBeadFiles(folderPath, "cascade", nb.BeadID, nb.OutputFiles)
	if err != nil {
		return fmt.Errorf("snapshot pre-reset files: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'complete', updated_at = ?
		 WHERE bead_id = ? AND status IN ('escalated', 'pending', 'failed_retry', 'running')`,
		now, nb.BeadID,
	); err != nil {
		return fmt.Errorf("cancel active jobs: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE beads SET status = 'pending', execution_attempts_override = NULL WHERE id = ?`,
		nb.BeadID,
	); err != nil {
		return fmt.Errorf("reset bead status: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM test_refinements WHERE bead_id = ?`, nb.BeadID,
	); err != nil {
		return fmt.Errorf("clear test_refinements: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM compressed_history WHERE bead_id = ?`, nb.BeadID,
	); err != nil {
		return fmt.Errorf("clear compressed_history: %w", err)
	}

	var deletedTests []string
	for _, f := range nb.OutputFiles {
		if strings.HasSuffix(f, "_test.go") {
			path := filepath.Join(folderPath, f)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("delete test file %s: %w", f, err)
			}
			deletedTests = append(deletedTests, f)
		}
	}

	stubbedFiles, deletedFiles, err := WriteScaffoldStubsTx(ctx, tx, projectID, folderPath, nb.OutputFiles)
	if err != nil {
		return fmt.Errorf("write scaffold stubs: %w", err)
	}

	if err := writeCascadeResetManifest(snapshotDir, nb.BeadID, baselineProjectID, preserved, deletedTests, stubbedFiles, deletedFiles); err != nil {
		return fmt.Errorf("write cascade reset manifest: %w", err)
	}
	return nil
}

// writeCascadeResetManifest records why a bead was reset (diff-detected spec
// change against a named baseline project, not an execution failure) and
// each file's fate — the cascade counterpart to rewindBead's
// writeRewindManifest. Unlike that one, a write failure here is treated as a
// hard error rather than a warning: it runs inside the same tx as the
// destructive DB/file steps it documents, before that tx has committed, so
// propagating the error lets dispatch's commit-failure handling retry the
// whole reset instead of leaving an undocumented one behind silently.
func writeCascadeResetManifest(snapshotDir string, beadID, baselineProjectID int64, preserved, deletedTests, stubbedFiles, deletedFiles []string) error {
	var m strings.Builder
	fmt.Fprintf(&m, "# Bead %d cascade reset snapshot\n\n", beadID)
	fmt.Fprintf(&m, "Taken: %s\n\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&m, "Reset because a diff against baseline project %d proved this bead's spec changed.\n", baselineProjectID)

	m.WriteString("\n## Pre-reset content preserved\n\n")
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
