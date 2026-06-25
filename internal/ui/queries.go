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
	ID     int64
	Status string
	Title  string
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

func queryBeads(ctx context.Context, d *db.DB, projectID int64) ([]BeadRow, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT b.id, b.status, COALESCE(br.full_text, '{}')
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
		if err := rows.Scan(&r.ID, &r.Status, &fullText); err != nil {
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
