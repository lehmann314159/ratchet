package ui

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"time"
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
	// For REFINE_TESTS jobs, increment refinement_cycle_id so the cap check resets.
	res, err := tx.ExecContext(ctx, `
		UPDATE handoff_jobs
		SET status = 'pending', updated_at = ?,
		    refinement_cycle_id = CASE
		        WHEN verb LIKE 'REFINE_TESTS%' THEN COALESCE(refinement_cycle_id, 1) + 1
		        ELSE refinement_cycle_id
		    END
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
	ctx := r.Context()
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("begin tx: %v", err), http.StatusInternalServerError)
		return
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET status = 'active', updated_at = ? WHERE id = ? AND status = 'paused'`,
		now, id,
	); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("resume project: %v", err), http.StatusInternalServerError)
		return
	}

	var beadID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM beads WHERE project_id = ? AND status = 'pending' ORDER BY id LIMIT 1`, id,
	).Scan(&beadID); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("find first pending bead: %v", err), http.StatusInternalServerError)
		return
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, 'EXECUTE_BEAD', ?, 'pending', ?, ?)`,
		id, beadID, now, now,
	); err != nil {
		_ = tx.Rollback()
		http.Error(w, fmt.Sprintf("enqueue first bead: %v", err), http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, fmt.Sprintf("commit: %v", err), http.StatusInternalServerError)
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
