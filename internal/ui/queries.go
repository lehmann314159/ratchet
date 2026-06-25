package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"ratchet/internal/db"
)

type ProjectRow struct {
	ID         int64
	Label      string
	Status     string
	FolderPath string
	DesignDoc  string
	CreatedAt  string
}

type BeadRow struct {
	ID             int64
	Status         string
	Title          string
	Attempts       int
	Budget         int    // execution_budget from current revision
	ElapsedSeconds int    // seconds since execution started; 0 if not executing
}

type JobRow struct {
	ID        int64
	Verb      string
	BeadID    sql.NullInt64
	BeadTitle string
	Status    string
	UpdatedAt string
}

type EscalatedRow struct {
	ID               int64
	ProjectID        int64
	Verb             string
	BeadID           sql.NullInt64
	BeadTitle        string
	Strikes          int
	ValidationResult string
	RawOutput        sql.NullString
	UpdatedAt        string
	Budget           int // current execution_budget from bead_revisions; 0 if not bead-scoped
}

func queryActiveProject(ctx context.Context, d *db.DB) (*ProjectRow, error) {
	row := d.QueryRowContext(ctx, `
		SELECT id, label, status, folder_path, design_doc_path, created_at
		FROM projects WHERE status = 'active'
		ORDER BY id LIMIT 1`)
	p := &ProjectRow{}
	if err := row.Scan(&p.ID, &p.Label, &p.Status, &p.FolderPath, &p.DesignDoc, &p.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("active project: %w", err)
	}
	return p, nil
}

func queryAllProjects(ctx context.Context, d *db.DB) ([]ProjectRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, label, status, folder_path, design_doc_path, created_at
		FROM projects ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("all projects: %w", err)
	}
	defer rows.Close()
	var out []ProjectRow
	for rows.Next() {
		var p ProjectRow
		if err := rows.Scan(&p.ID, &p.Label, &p.Status, &p.FolderPath, &p.DesignDoc, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func queryBeads(ctx context.Context, d *db.DB, projectID int64) ([]BeadRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT b.id, b.status, COALESCE(br.full_text, '{}'),
		       COALESCE(br.execution_budget, 0),
		       (SELECT COUNT(*) FROM executions e WHERE e.bead_id = b.id AND e.termination_cause IS NOT NULL),
		       COALESCE((
		         SELECT CAST((julianday('now') - julianday(e2.started_at)) * 86400 AS INTEGER)
		         FROM executions e2
		         WHERE e2.bead_id = b.id AND e2.termination_cause IS NULL
		         ORDER BY e2.started_at DESC LIMIT 1
		       ), 0)
		FROM beads b
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.project_id = ?
		ORDER BY b.id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("beads: %w", err)
	}
	defer rows.Close()

	var out []BeadRow
	for rows.Next() {
		var r BeadRow
		var fullText string
		if err := rows.Scan(&r.ID, &r.Status, &fullText, &r.Budget, &r.Attempts, &r.ElapsedSeconds); err != nil {
			return nil, err
		}
		var parsed struct {
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(fullText), &parsed) == nil && parsed.Title != "" {
			r.Title = parsed.Title
		} else {
			r.Title = fmt.Sprintf("bead-%d", r.ID)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func queryRecentJobs(ctx context.Context, d *db.DB, projectID int64, limit int) ([]JobRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT hj.id, hj.verb, hj.bead_id,
		       COALESCE(json_extract(br.full_text, '$.title'), ''),
		       hj.status, hj.updated_at
		FROM handoff_jobs hj
		LEFT JOIN beads b ON b.id = hj.bead_id
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE hj.project_id = ?
		ORDER BY hj.updated_at DESC
		LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent jobs: %w", err)
	}
	defer rows.Close()

	var out []JobRow
	for rows.Next() {
		var r JobRow
		if err := rows.Scan(&r.ID, &r.Verb, &r.BeadID, &r.BeadTitle, &r.Status, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func queryEscalatedJobs(ctx context.Context, d *db.DB) ([]EscalatedRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT hj.id, hj.project_id, hj.verb, hj.bead_id,
		       COALESCE(json_extract(br.full_text, '$.title'), ''),
		       (SELECT COUNT(*) FROM handoff_attempts ha
		        WHERE ha.job_id = hj.id AND ha.validation_result != 'valid') AS strikes,
		       COALESCE(
		         (SELECT ha.validation_result FROM handoff_attempts ha
		          WHERE ha.job_id = hj.id ORDER BY ha.attempt_number DESC LIMIT 1),
		         ''),
		       (SELECT ha.raw_output FROM handoff_attempts ha
		        WHERE ha.job_id = hj.id ORDER BY ha.attempt_number DESC LIMIT 1),
		       hj.updated_at,
		       COALESCE(br.execution_budget, 0)
		FROM handoff_jobs hj
		LEFT JOIN beads b ON b.id = hj.bead_id
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE hj.status = 'escalated'
		ORDER BY hj.updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("escalated jobs: %w", err)
	}
	defer rows.Close()

	var out []EscalatedRow
	for rows.Next() {
		var r EscalatedRow
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Verb, &r.BeadID, &r.BeadTitle,
			&r.Strikes, &r.ValidationResult, &r.RawOutput, &r.UpdatedAt, &r.Budget); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func queryEscalatedJobByID(ctx context.Context, d *db.DB, id int64) (*EscalatedRow, error) {
	r := &EscalatedRow{}
	err := d.QueryRowContext(ctx, `
		SELECT hj.id, hj.project_id, hj.verb, hj.bead_id,
		       COALESCE(json_extract(br.full_text, '$.title'), ''),
		       (SELECT COUNT(*) FROM handoff_attempts ha
		        WHERE ha.job_id = hj.id AND ha.validation_result != 'valid') AS strikes,
		       COALESCE(
		         (SELECT ha.validation_result FROM handoff_attempts ha
		          WHERE ha.job_id = hj.id ORDER BY ha.attempt_number DESC LIMIT 1),
		         ''),
		       (SELECT ha.raw_output FROM handoff_attempts ha
		        WHERE ha.job_id = hj.id ORDER BY ha.attempt_number DESC LIMIT 1),
		       hj.updated_at,
		       COALESCE(br.execution_budget, 0)
		FROM handoff_jobs hj
		LEFT JOIN beads b ON b.id = hj.bead_id
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE hj.id = ?`, id,
	).Scan(&r.ID, &r.ProjectID, &r.Verb, &r.BeadID, &r.BeadTitle,
		&r.Strikes, &r.ValidationResult, &r.RawOutput, &r.UpdatedAt, &r.Budget)
	if err != nil {
		return nil, fmt.Errorf("escalated job %d: %w", id, err)
	}
	return r, nil
}

type ExecutionRow struct {
	ID               int64
	AttemptNum       int
	TerminationCause string
	BudgetSeconds    int
	ElapsedSeconds   int
	MonitorFired     bool
	StartedAt        string
	Decision         string // adjudication decision, empty if none yet
	DecisionReasoning string
	TracePath        string
}

type RevisionRow struct {
	RevisionNumber  int
	ExecutionBudget int
	CreatedByVerb   string
	CreatedAt       string
	FullText        string
}

type beadDetailData struct {
	baseData
	BeadID     int64
	BeadTitle  string
	Executions []ExecutionRow
	Revisions  []RevisionRow
}

func queryBeadDetail(ctx context.Context, d *db.DB, beadID int64) (*beadDetailData, error) {
	out := &beadDetailData{BeadID: beadID}

	// Title from current revision.
	var fullText string
	_ = d.QueryRowContext(ctx, `
		SELECT COALESCE(br.full_text, '{}') FROM beads b
		LEFT JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID).Scan(&fullText)
	var parsed struct{ Title string `json:"title"` }
	if json.Unmarshal([]byte(fullText), &parsed) == nil && parsed.Title != "" {
		out.BeadTitle = parsed.Title
	} else {
		out.BeadTitle = fmt.Sprintf("bead-%d", beadID)
	}

	// Execution history.
	rows, err := d.QueryContext(ctx, `
		SELECT e.id,
		       ROW_NUMBER() OVER (ORDER BY e.started_at) AS attempt_num,
		       COALESCE(e.termination_cause, 'running'),
		       br.execution_budget,
		       CAST((julianday(COALESCE(e.ended_at, 'now')) - julianday(e.started_at)) * 86400 AS INTEGER),
		       COALESCE(e.monitor_fired, 0),
		       e.started_at,
		       COALESCE(adj.decision, ''),
		       COALESCE(adj.reasoning_text, ''),
		       e.trace_path
		FROM executions e
		JOIN bead_revisions br ON br.bead_id = e.bead_id
		  AND br.revision_number = (
		    SELECT MAX(br2.revision_number) FROM bead_revisions br2
		    WHERE br2.bead_id = e.bead_id AND br2.created_at <= e.started_at
		  )
		LEFT JOIN adjudications adj ON adj.execution_id = e.id
		WHERE e.bead_id = ?
		ORDER BY e.started_at`, beadID)
	if err != nil {
		return nil, fmt.Errorf("execution history: %w", err)
	}
	defer rows.Close()
	var attemptNum int
	for rows.Next() {
		var r ExecutionRow
		var monitorFired int
		if err := rows.Scan(&r.ID, &attemptNum, &r.TerminationCause,
			&r.BudgetSeconds, &r.ElapsedSeconds, &monitorFired,
			&r.StartedAt, &r.Decision, &r.DecisionReasoning, &r.TracePath); err != nil {
			return nil, err
		}
		r.AttemptNum = attemptNum
		r.MonitorFired = monitorFired == 1
		out.Executions = append(out.Executions, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Revision log.
	revRows, err := d.QueryContext(ctx, `
		SELECT revision_number, execution_budget, created_by_verb, created_at, full_text
		FROM bead_revisions WHERE bead_id = ? ORDER BY revision_number`, beadID)
	if err != nil {
		return nil, fmt.Errorf("revisions: %w", err)
	}
	defer revRows.Close()
	for revRows.Next() {
		var r RevisionRow
		if err := revRows.Scan(&r.RevisionNumber, &r.ExecutionBudget, &r.CreatedByVerb, &r.CreatedAt, &r.FullText); err != nil {
			return nil, err
		}
		out.Revisions = append(out.Revisions, r)
	}
	return out, revRows.Err()
}

func queryEscalatedCount(ctx context.Context, d *db.DB) int {
	var n int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM handoff_jobs WHERE status = 'escalated'`).Scan(&n)
	return n
}

func queryTracePath(ctx context.Context, d *db.DB, execID int64) (string, error) {
	var path string
	err := d.QueryRowContext(ctx, `SELECT trace_path FROM executions WHERE id = ?`, execID).Scan(&path)
	return path, err
}
