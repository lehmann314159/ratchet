package ui

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ratchet/internal/project"
)

// baseData is included in every page render so the layout can show the
// escalation badge count in the nav without a separate query per page.
type baseData struct {
	EscalatedCount int
}

func (s *server) base(r *http.Request) baseData {
	return baseData{EscalatedCount: queryEscalatedCount(r.Context(), s.db)}
}

func (s *server) render(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *server) renderPartial(w http.ResponseWriter, tmpl *template.Template, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// --- Dashboard ---

type dashboardData struct {
	baseData
	Project     *ProjectRow
	Beads       []BeadRow
	Jobs        []JobRow
	AllProjects []ProjectRow
}

func (s *server) dashboardData(r *http.Request) dashboardData {
	ctx := r.Context()
	d := dashboardData{baseData: s.base(r)}
	project, _ := queryActiveProject(ctx, s.db)
	d.Project = project
	if project != nil {
		d.Beads, _ = queryBeads(ctx, s.db, project.ID)
		d.Jobs, _ = queryRecentJobs(ctx, s.db, project.ID)
	}
	d.AllProjects, _ = queryAllProjects(ctx, s.db)
	return d
}

func (s *server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, s.tmpl.dashboard, s.dashboardData(r))
}

func (s *server) handleStatusPartial(w http.ResponseWriter, r *http.Request) {
	s.renderPartial(w, s.tmpl.dashboard, "status", s.dashboardData(r))
}

// --- Escalations list ---

type escalationsData struct {
	baseData
	Jobs []EscalatedRow
}

func (s *server) handleEscalations(w http.ResponseWriter, r *http.Request) {
	jobs, _ := queryEscalatedJobs(r.Context(), s.db)
	s.render(w, s.tmpl.escalations, escalationsData{
		baseData: s.base(r),
		Jobs:     jobs,
	})
}

// --- Escalation detail ---

type escalationData struct {
	baseData
	Job *EscalatedRow
}

func (s *server) handleEscalationDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	job, err := queryEscalatedJobByID(r.Context(), s.db, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("job not found: %v", err), http.StatusNotFound)
		return
	}
	s.render(w, s.tmpl.escalation, escalationData{baseData: s.base(r), Job: job})
}

// --- Requeue ---

func (s *server) handleRequeue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("begin tx: %v", err), http.StatusInternalServerError)
		return
	}

	// Claim the job atomically: only an escalated job may be requeued. Guards
	// against a stale escalation-detail page (or a duplicate/retried request)
	// requeuing a job that's already been resolved or is currently 'running'
	// under the orchestrator — without this, that write would race the
	// orchestrator's own status writes with no coordination at all.
	//
	// refinement_cycle_id is deliberately left untouched. It used to be
	// bumped here on the theory that a fresh cycle number "resets the cap
	// check" — but every REFINE_TESTS_JUDGE/WRITE cap check
	// (refine_tests.go's RefineTestsJudge.Commit "revise" branch) computes
	// its next cycle from the job's *own* cycle_id, so bumping it here only
	// pushes that job further past the cap, never resets anything. Worse,
	// REFINE_TESTS_JUDGE's critique-findings lookup (and REFINE_TESTS_WRITE's
	// prior-JUDGE lookup) is scoped by exact cycle_id equality against a
	// sibling job/row created earlier in the *same* cycle — bumping only the
	// requeued job's own row orphans it from that sibling data, silently
	// degrading to an empty lookup. Confirmed live (connect-four-v2 bead 56,
	// 2026-08-27): requeuing an escalated REFINE_TESTS_JUDGE this way made it
	// lose ADJUDICATE_NEXT_EXECUTION's injected re_refine diagnosis entirely,
	// so it judged an empty critique section and wrongly approved a test file
	// that was never actually revised. A plain requeue — same cycle, same
	// job, same input, fresh model call — is both correct and sufficient.
	res, err := tx.ExecContext(ctx, `
		UPDATE handoff_jobs
		SET status = 'pending', updated_at = ?
		WHERE id = ? AND status = 'escalated'`, now, id)
	if err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("requeue failed: %v", err), http.StatusInternalServerError)
		return
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		_ = tx.Rollback()
		http.Error(w, "job is no longer escalated (already resolved, or picked up by the orchestrator) — reload the escalations list", http.StatusConflict)
		return
	}

	// Delete prior failed attempts so the strike count resets to zero.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM handoff_attempts WHERE job_id = ? AND validation_result != 'valid'`, id,
	); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("requeue failed: %v", err), http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, fmt.Sprintf("commit: %v", err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/escalations", http.StatusSeeOther)
}

// --- Close ---

func (s *server) handleClose(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// Guarded on status='escalated' so a stale page or duplicate request can't
	// mark a job that's already moved on (resolved, or currently 'running'
	// under the orchestrator) as complete out from under it.
	res, err := s.db.ExecContext(r.Context(),
		`UPDATE handoff_jobs SET status = 'complete', updated_at = ? WHERE id = ? AND status = 'escalated'`, now, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("close failed: %v", err), http.StatusInternalServerError)
		return
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		http.Error(w, "job is no longer escalated (already resolved, or picked up by the orchestrator) — reload the escalations list", http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/escalations", http.StatusSeeOther)
}

// --- Requeue with budget override ---

func (s *server) handleRequeuWithBudget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	budget, err := strconv.Atoi(r.FormValue("budget"))
	if err != nil || budget < 60 {
		http.Error(w, "budget must be an integer >= 60", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	now := time.Now().UTC().Format(time.RFC3339)

	// Look up the bead associated with this job.
	var beadID int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT bead_id FROM handoff_jobs WHERE id = ?`, id,
	).Scan(&beadID); err != nil || beadID == 0 {
		http.Error(w, "job has no bead — budget override only applies to bead-scoped jobs", http.StatusBadRequest)
		return
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("begin tx: %v", err), http.StatusInternalServerError)
		return
	}

	// Insert a new bead revision with the updated budget.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at)
		SELECT project_id, bead_id, revision_number + 1,
		       json_set(full_text, '$.execution_budget', ?),
		       ?, monitor_override, 'ADJUDICATE_NEXT_EXECUTION', ?
		FROM bead_revisions WHERE bead_id = ?
		ORDER BY revision_number DESC LIMIT 1`,
		budget, budget, now, beadID,
	); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("insert revision: %v", err), http.StatusInternalServerError)
		return
	}

	// Point the bead at the new revision.
	if _, err := tx.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = (
		   SELECT id FROM bead_revisions WHERE bead_id = ? ORDER BY revision_number DESC LIMIT 1
		 ) WHERE id = ?`, beadID, beadID,
	); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("update bead revision: %v", err), http.StatusInternalServerError)
		return
	}

	// Delete invalid attempts.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM handoff_attempts WHERE job_id = ? AND validation_result != 'valid'`, id,
	); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("requeue failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Claim the job atomically as the final write: only an escalated job may
	// be requeued. If another request already resolved this job (stale page,
	// duplicate submission), this affects zero rows and the whole
	// transaction — including the new revision — rolls back instead of
	// silently applying a budget change to a job that's moved on.
	res, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'pending', updated_at = ? WHERE id = ? AND status = 'escalated'`, now, id)
	if err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("requeue failed: %v", err), http.StatusInternalServerError)
		return
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		_ = tx.Rollback()
		http.Error(w, "job is no longer escalated (already resolved, or picked up by the orchestrator) — reload the escalations list", http.StatusConflict)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, fmt.Sprintf("commit: %v", err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/escalations", http.StatusSeeOther)
}

// --- Grant Additional Attempts ---

func (s *server) handleGrantAttempts(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	extra, err := strconv.Atoi(r.FormValue("attempts"))
	if err != nil || extra < 1 || extra > 10 {
		http.Error(w, "attempts must be an integer between 1 and 10", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	now := time.Now().UTC().Format(time.RFC3339)

	var beadID sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT bead_id FROM handoff_jobs WHERE id = ?`, id,
	).Scan(&beadID); err != nil || !beadID.Valid {
		http.Error(w, "job not found or not bead-scoped", http.StatusNotFound)
		return
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("begin tx: %v", err), http.StatusInternalServerError)
		return
	}

	// Increment the per-bead override (seeding from the project default if not yet set),
	// so only this bead gets extra attempts rather than raising the cap project-wide.
	if _, err := tx.ExecContext(ctx, `
		UPDATE beads
		SET execution_attempts_override = COALESCE(
			execution_attempts_override,
			(SELECT max_execution_attempts FROM projects WHERE id = (SELECT project_id FROM beads WHERE id = ?))
		) + ?
		WHERE id = ?`, beadID.Int64, extra, beadID.Int64,
	); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("grant attempts: %v", err), http.StatusInternalServerError)
		return
	}

	// Clear invalid attempts so ADJUDICATE retries cleanly.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM handoff_attempts WHERE job_id = ? AND validation_result != 'valid'`, id,
	); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("grant attempts: %v", err), http.StatusInternalServerError)
		return
	}

	// Claim the job atomically as the final write: only an escalated job may
	// have attempts granted. If another request already resolved this job,
	// this affects zero rows and the whole transaction — including the
	// attempts-override bump — rolls back instead of silently applying it to
	// a job that's moved on.
	res, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'pending', updated_at = ? WHERE id = ? AND status = 'escalated'`, now, id)
	if err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("requeue: %v", err), http.StatusInternalServerError)
		return
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		_ = tx.Rollback()
		http.Error(w, "job is no longer escalated (already resolved, or picked up by the orchestrator) — reload the escalations list", http.StatusConflict)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, fmt.Sprintf("commit: %v", err), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/escalations", http.StatusSeeOther)
}

// --- Full-Stop Project ---

func (s *server) handleCloseProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid project id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("begin tx: %v", err), http.StatusInternalServerError)
		return
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET status = 'full_stopped', updated_at = ?
		 WHERE id = ? AND status IN ('active', 'paused')`,
		now, id,
	); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("full-stop project: %v", err), http.StatusInternalServerError)
		return
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE beads SET status = 'full_stopped' WHERE project_id = ? AND status = 'pending'`, id,
	); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("stop beads: %v", err), http.StatusInternalServerError)
		return
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'complete', updated_at = ?
		 WHERE project_id = ? AND status IN ('pending', 'running', 'failed_retry')`,
		now, id,
	); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("cancel jobs: %v", err), http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, fmt.Sprintf("commit: %v", err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- Resume Project ---

func (s *server) handleResumeProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid project id", http.StatusBadRequest)
		return
	}

	// Delegate to the same logic as `ratchet resume-project`: every pause
	// point always enqueues its normal next handoff_job *before* pausing, so
	// resuming is nothing more than a status flip — there is no next-job
	// state to reconstruct. The previous version of this handler assumed a
	// bead already existed and tried to hand-pick one to enqueue EXECUTE_BEAD
	// for, which broke for any pause before DECOMPOSE_SPEC has run (no beads
	// exist yet) and duplicated a job the pause mechanism had already queued
	// for every other pause point.
	if _, _, _, err := project.ResumeProject(r.Context(), s.db, id); err != nil {
		http.Error(w, fmt.Sprintf("resume project: %v", err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- Remove Project ---

func (s *server) handleRemoveProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid project id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	// Guard: only full_stopped projects may be removed.
	var status string
	if err := s.db.QueryRowContext(ctx,
		`SELECT status FROM projects WHERE id = ?`, id,
	).Scan(&status); err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	if status != "full_stopped" && status != "complete" {
		http.Error(w, "only full_stopped or complete projects can be removed", http.StatusBadRequest)
		return
	}

	// Guard: a project still referenced by another project's loop-mode FK
	// columns can't be deleted — the final `DELETE FROM projects` would hit a
	// live foreign-key violation (foreign_keys is ON, db.go) and roll the whole
	// transaction back. Both columns were added on the loop-mode branch after
	// this handler was written; the steps list below only ever learned to
	// null out the older recovered_from_project_id. Rather than orphan the
	// referencing rows, block the removal and say what's in the way.
	var laterIterations int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM projects WHERE lineage_root_id = ? AND id != ?`, id, id,
	).Scan(&laterIterations); err != nil {
		http.Error(w, fmt.Sprintf("check lineage: %v", err), http.StatusInternalServerError)
		return
	}
	if laterIterations > 0 {
		http.Error(w, fmt.Sprintf(
			"project %d is the root of a lineage with %d later iteration(s) — remove those first",
			id, laterIterations), http.StatusBadRequest)
		return
	}
	var cascadeChildren int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM projects WHERE cascade_baseline_project_id = ?`, id,
	).Scan(&cascadeChildren); err != nil {
		http.Error(w, fmt.Sprintf("check cascade baseline: %v", err), http.StatusInternalServerError)
		return
	}
	if cascadeChildren > 0 {
		http.Error(w, fmt.Sprintf(
			"project %d is the cascade baseline for %d other project(s) — remove those first",
			id, cascadeChildren), http.StatusBadRequest)
		return
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("begin tx: %v", err), http.StatusInternalServerError)
		return
	}

	// Delete in topological order to satisfy FK constraints.
	// beads ↔ bead_revisions have a circular FK; break it with a NULL-out first.
	steps := []string{
		`DELETE FROM certifications   WHERE project_id = ?`,
		`DELETE FROM verify_attempts  WHERE project_id = ?`,
		`DELETE FROM handoff_attempts WHERE job_id IN (SELECT id FROM handoff_jobs WHERE project_id = ?)`,
		`DELETE FROM analyses         WHERE project_id = ?`,
		`DELETE FROM adjudications    WHERE project_id = ?`,
		`DELETE FROM executions       WHERE project_id = ?`,
		`DELETE FROM spec_revisions   WHERE project_id = ?`,
		`DELETE FROM compressed_history WHERE project_id = ?`,
		`DELETE FROM handoff_jobs     WHERE project_id = ?`,
		`DELETE FROM audit_reconcile_rounds WHERE project_id = ?`,
		`DELETE FROM verb_model_assignments WHERE project_id = ?`,
		`DELETE FROM test_refinements WHERE project_id = ?`,
		`UPDATE beads SET current_revision_id = NULL WHERE project_id = ?`,
		`DELETE FROM bead_revisions   WHERE project_id = ?`,
		`DELETE FROM beads            WHERE project_id = ?`,
		`UPDATE projects SET recovered_from_project_id = NULL WHERE recovered_from_project_id = ?`,
		`DELETE FROM projects         WHERE id = ?`,
	}
	for _, q := range steps {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			_ = tx.Rollback()
			http.Error(w, fmt.Sprintf("remove project: %v", err), http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, fmt.Sprintf("commit: %v", err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- Bead detail ---

func (s *server) handleBeadDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid bead id", http.StatusBadRequest)
		return
	}
	d, err := queryBeadDetail(r.Context(), s.db, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("bead detail: %v", err), http.StatusInternalServerError)
		return
	}
	if folder, ferr := queryBeadProjectFolder(r.Context(), s.db, id); ferr == nil {
		d.RewindSnapshots = listRewindSnapshots(folder, id)
	}
	d.baseData = s.base(r)
	s.render(w, s.tmpl.beadDetail, d)
}

// --- Trace viewer ---

type traceData struct {
	baseData
	Path    string
	Content string
}

// handleTrace serves a trace file by execution ID, never a client-supplied
// path — the path is resolved server-side via queryTracePath, so a request
// can only ever read a path this application itself wrote to the executions
// table, not an arbitrary file on disk.
func (s *server) handleTrace(w http.ResponseWriter, r *http.Request) {
	execID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid execution id", http.StatusBadRequest)
		return
	}
	path, err := queryTracePath(r.Context(), s.db, execID)
	if err != nil {
		http.Error(w, "execution not found", http.StatusNotFound)
		return
	}
	b, err := os.ReadFile(path)
	content := ""
	if err == nil {
		content = string(b)
	}
	s.render(w, s.tmpl.trace, traceData{
		baseData: s.base(r),
		Path:     path,
		Content:  content,
	})
}

// --- Reports ---
//
// bead-report.md and project-report.md are written mechanically (no model
// call — see docs/post-execution-report-spec.md) whenever a bead or project
// reaches a terminal state, including escalation. These handlers just find
// and display them; the report content itself is the forensic record.

type reportData struct {
	baseData
	Title   string
	Path    string
	Content string
}

// handleBeadReport serves traces/bead-{id}-report.md for a bead. The path is
// resolved server-side from the bead's project folder_path, never
// client-supplied, mirroring handleTrace's path-resolution pattern.
func (s *server) handleBeadReport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid bead id", http.StatusBadRequest)
		return
	}
	folder, err := queryBeadProjectFolder(r.Context(), s.db, id)
	if err != nil {
		http.Error(w, "bead not found", http.StatusNotFound)
		return
	}
	path := filepath.Join(folder, "traces", fmt.Sprintf("bead-%d-report.md", id))
	b, err := os.ReadFile(path)
	content := ""
	if err == nil {
		content = string(b)
	}
	s.render(w, s.tmpl.report, reportData{
		baseData: s.base(r),
		Title:    fmt.Sprintf("Bead %d Report", id),
		Path:     path,
		Content:  content,
	})
}

// handleProjectReport serves traces/project-report.md for a project.
func (s *server) handleProjectReport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid project id", http.StatusBadRequest)
		return
	}
	folder, err := queryProjectFolder(r.Context(), s.db, id)
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	path := filepath.Join(folder, "traces", "project-report.md")
	b, err := os.ReadFile(path)
	content := ""
	if err == nil {
		content = string(b)
	}
	s.render(w, s.tmpl.report, reportData{
		baseData: s.base(r),
		Title:    "Project Report",
		Path:     path,
		Content:  content,
	})
}

// --- Rewind snapshots ---
//
// rewindBead (internal/project/rewind.go) preserves a bead's pre-rewind file
// content under traces/_bead-{id}-rewind-{n}/ before deleting tests or
// stubbing impl files. These handlers list and display those snapshots —
// on-disk only, not tracked in the DB, so listing means scanning the
// project's traces/ directory rather than querying a table. The leading
// underscore keeps `go test ./...` from picking up the copied .go files as
// a second package (see SnapshotBeadFiles's doc comment) — must match here.

// listRewindSnapshots returns the sorted snapshot numbers found for beadID
// under folder/traces, or nil if none exist (including if folder can't be read).
func listRewindSnapshots(folder string, beadID int64) []int {
	entries, err := os.ReadDir(filepath.Join(folder, "traces"))
	if err != nil {
		return nil
	}
	prefix := fmt.Sprintf("_bead-%d-rewind-", beadID)
	var nums []int
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), prefix)); err == nil {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	return nums
}

// renderSnapshotDir concatenates a rewind snapshot's README.md manifest and
// every preserved file's content into one display string, README first.
func renderSnapshotDir(dir string) (string, error) {
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.Write(readme)

	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil || rel == "README.md" {
			return relErr
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		fmt.Fprintf(&sb, "\n\n## %s\n\n```\n%s\n```\n", rel, content)
		return nil
	})
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}

// handleBeadSnapshot serves a single rewind snapshot's contents. The path is
// built server-side from the bead's project folder plus the bead/snapshot
// IDs, never client-supplied.
func (s *server) handleBeadSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid bead id", http.StatusBadRequest)
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		http.Error(w, "invalid snapshot number", http.StatusBadRequest)
		return
	}
	folder, err := queryBeadProjectFolder(r.Context(), s.db, id)
	if err != nil {
		http.Error(w, "bead not found", http.StatusNotFound)
		return
	}
	dir := filepath.Join(folder, "traces", fmt.Sprintf("_bead-%d-rewind-%d", id, n))
	content, _ := renderSnapshotDir(dir)
	s.render(w, s.tmpl.report, reportData{
		baseData: s.base(r),
		Title:    fmt.Sprintf("Bead %d Rewind Snapshot %d", id, n),
		Path:     dir,
		Content:  content,
	})
}
