package ui

import (
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
		d.Jobs, _ = queryRecentJobs(ctx, s.db, project.ID, 20)
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
	now := time.Now().UTC().Format(time.RFC3339)
	// Delete prior failed attempts so the strike count resets to zero.
	_, _ = s.db.ExecContext(r.Context(),
		`DELETE FROM handoff_attempts WHERE job_id = ? AND validation_result != 'valid'`, id)
	_, err = s.db.ExecContext(r.Context(),
		`UPDATE handoff_jobs SET status = 'pending', updated_at = ? WHERE id = ?`, now, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("requeue failed: %v", err), http.StatusInternalServerError)
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
	_, err = s.db.ExecContext(r.Context(),
		`UPDATE handoff_jobs SET status = 'complete', updated_at = ? WHERE id = ?`, now, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("close failed: %v", err), http.StatusInternalServerError)
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

	// Insert a new bead revision with the updated budget.
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO bead_revisions
		  (project_id, bead_id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at)
		SELECT project_id, bead_id, revision_number + 1,
		       json_set(full_text, '$.execution_budget', ?),
		       ?, monitor_override, 'ADJUDICATE_NEXT_EXECUTION', ?
		FROM bead_revisions WHERE bead_id = ?
		ORDER BY revision_number DESC LIMIT 1`,
		budget, budget, now, beadID)
	if err != nil {
		http.Error(w, fmt.Sprintf("insert revision: %v", err), http.StatusInternalServerError)
		return
	}

	// Point the bead at the new revision.
	_, err = s.db.ExecContext(ctx,
		`UPDATE beads SET current_revision_id = (
		   SELECT id FROM bead_revisions WHERE bead_id = ? ORDER BY revision_number DESC LIMIT 1
		 ) WHERE id = ?`, beadID, beadID)
	if err != nil {
		http.Error(w, fmt.Sprintf("update bead revision: %v", err), http.StatusInternalServerError)
		return
	}

	// Reset job: delete invalid attempts, set pending.
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM handoff_attempts WHERE job_id = ? AND validation_result != 'valid'`, id)
	_, err = s.db.ExecContext(ctx,
		`UPDATE handoff_jobs SET status = 'pending', updated_at = ? WHERE id = ?`, now, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("requeue failed: %v", err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/escalations", http.StatusSeeOther)
}

// --- Close Project ---

func (s *server) handleCloseProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid project id", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(r.Context(),
		`UPDATE projects SET status = 'full_stopped', updated_at = ? WHERE id = ? AND status = 'active'`,
		now, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("close project failed: %v", err), http.StatusInternalServerError)
		return
	}
	_, _ = s.db.ExecContext(r.Context(),
		`UPDATE beads SET status = 'full_stopped' WHERE project_id = ? AND status = 'executing'`, id)
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

func (s *server) handleTrace(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path query parameter required", http.StatusBadRequest)
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
