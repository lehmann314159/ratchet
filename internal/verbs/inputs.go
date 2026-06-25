package verbs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

// loadVerbModel returns the Ollama model assigned to verb for projectID.
func loadVerbModel(ctx context.Context, d *db.DB, projectID int64, verb string) (string, error) {
	var model string
	err := d.QueryRowContext(ctx,
		`SELECT model FROM verb_model_assignments WHERE project_id = ? AND verb = ?`,
		projectID, verb,
	).Scan(&model)
	if err != nil {
		return "", fmt.Errorf("model for %s: %w", verb, err)
	}
	return model, nil
}

// loadProject returns the projects row for projectID.
func loadProject(ctx context.Context, d *db.DB, projectID int64) (*db.Project, error) {
	row := d.QueryRowContext(ctx, `
		SELECT id, label, folder_path, design_doc_path, status,
		       recovered_from_project_id,
		       monitor_override_default, execution_budget_default,
		       audit_reconcile_round_cap, max_execution_attempts,
		       created_at, updated_at
		FROM projects WHERE id = ?`, projectID)
	p := &db.Project{}
	var createdAt, updatedAt string
	if err := row.Scan(
		&p.ID, &p.Label, &p.FolderPath, &p.DesignDocPath, &p.Status,
		&p.RecoveredFromProjectID,
		&p.MonitorOverrideDefault, &p.ExecutionBudgetDefault,
		&p.AuditReconcileRoundCap, &p.MaxExecutionAttempts,
		&createdAt, &updatedAt,
	); err != nil {
		return nil, fmt.Errorf("load project %d: %w", projectID, err)
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return p, nil
}

// loadDesignDoc reads the design doc file for a project.
func loadDesignDoc(ctx context.Context, d *db.DB, projectID int64) (string, error) {
	p, err := loadProject(ctx, d, projectID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(p.FolderPath, p.DesignDocPath)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read design doc %s: %w", path, err)
	}
	return string(b), nil
}

// beadState is a Bead joined with its current revision's data.
type beadState struct {
	BeadID          int64
	Title           string
	FullText        string
	ExecutionBudget int
	MonitorOverride string
	RevisionNumber  int
}

// loadCurrentBeads returns all beads for projectID with their current revision.
func loadCurrentBeads(ctx context.Context, d *db.DB, projectID int64) ([]beadState, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT b.id, br.full_text, br.execution_budget, br.monitor_override, br.revision_number
		FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.project_id = ?
		ORDER BY b.id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("load beads: %w", err)
	}
	defer rows.Close()

	var out []beadState
	for rows.Next() {
		var s beadState
		if err := rows.Scan(&s.BeadID, &s.FullText, &s.ExecutionBudget, &s.MonitorOverride, &s.RevisionNumber); err != nil {
			return nil, err
		}
		var tmp struct {
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(s.FullText), &tmp) == nil && tmp.Title != "" {
			s.Title = tmp.Title
		} else {
			s.Title = fmt.Sprintf("bead-%d", s.BeadID)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// debateRound is one completed audit/reconcile exchange, loaded from
// audit_reconcile_rounds for inclusion in RECONCILE's round-2 context.
type debateRound struct {
	RoundNumber    int
	CritiqueText   string // AUDIT's raw output for this round
	Reconciliation string // RECONCILE's JSON output for this round
	Outcome        string // 'converged' | 'disagreed_continuing' | 'escalated'
}

// loadDebateHistory returns all completed audit/reconcile rounds for projectID,
// oldest first. Empty slice is normal on the first round.
func loadDebateHistory(ctx context.Context, d *db.DB, projectID int64) ([]debateRound, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT round_number, critique_text, reconciliation, outcome
		FROM audit_reconcile_rounds
		WHERE project_id = ?
		ORDER BY round_number`, projectID)
	if err != nil {
		return nil, fmt.Errorf("load debate history: %w", err)
	}
	defer rows.Close()

	var out []debateRound
	for rows.Next() {
		var r debateRound
		if err := rows.Scan(&r.RoundNumber, &r.CritiqueText, &r.Reconciliation, &r.Outcome); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// latestAuditCritique returns the raw output and completed-round count for
// the most recent valid AUDIT_DECOMPOSITION attempt in this project.
//
// RECONCILE reads the critique here rather than from audit_reconcile_rounds
// because audit_reconcile_rounds is written atomically by RECONCILE's Commit
// (not AUDIT's Commit): we avoid a nullable column in the schema by staging
// the critique text in handoff_attempts until both sides of the round are
// available.
func latestAuditCritique(ctx context.Context, d *db.DB, projectID int64) (critique string, roundsSoFar int, err error) {
	if err = d.QueryRowContext(ctx, `
		SELECT ha.raw_output
		FROM handoff_jobs hj
		JOIN handoff_attempts ha ON ha.job_id = hj.id
		WHERE hj.project_id = ? AND hj.verb = ?
		  AND hj.status = 'complete'
		  AND ha.validation_result = 'valid'
		ORDER BY hj.created_at DESC
		LIMIT 1`,
		projectID, db.VerbAuditDecomposition,
	).Scan(&critique); err != nil {
		return "", 0, fmt.Errorf("latest audit critique: %w", err)
	}
	if err = d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_reconcile_rounds WHERE project_id = ?`,
		projectID,
	).Scan(&roundsSoFar); err != nil {
		return "", 0, fmt.Errorf("round count: %w", err)
	}
	return critique, roundsSoFar, nil
}

// loadLatestAnalysis returns the most recent valid ANALYZE_EXECUTION output
// for beadID.
func loadLatestAnalysis(ctx context.Context, d *db.DB, beadID int64) (*AnalyzeExecutionOutput, error) {
	var raw string
	if err := d.QueryRowContext(ctx, `
		SELECT ha.raw_output
		FROM handoff_jobs hj
		JOIN handoff_attempts ha ON ha.job_id = hj.id
		WHERE hj.bead_id = ? AND hj.verb = ?
		  AND hj.status = 'complete'
		  AND ha.validation_result = 'valid'
		ORDER BY hj.created_at DESC
		LIMIT 1`,
		beadID, db.VerbAnalyzeExecution,
	).Scan(&raw); err != nil {
		return nil, fmt.Errorf("latest analysis for bead %d: %w", beadID, err)
	}
	var out AnalyzeExecutionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("parse analysis output: %w", err)
	}
	return &out, nil
}

// loadCompressedHistory returns the compressed history for beadID, or empty
// string if no history exists yet (normal on the first attempt).
func loadCompressedHistory(ctx context.Context, d *db.DB, beadID int64) (string, error) {
	var text string
	err := d.QueryRowContext(ctx,
		`SELECT compressed_text FROM compressed_history WHERE bead_id = ?`, beadID,
	).Scan(&text)
	if err != nil {
		return "", nil // no history yet
	}
	return text, nil
}

// revisionEntry is one row from bead_revisions, ordered oldest first.
type revisionEntry struct {
	RevisionNumber  int
	FullText        string
	ExecutionBudget int
	MonitorOverride string
	CreatedByVerb   string
}

// loadBeadRevisionLog returns the full revision log for beadID.
func loadBeadRevisionLog(ctx context.Context, d *db.DB, beadID int64) ([]revisionEntry, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT revision_number, full_text, execution_budget, monitor_override, created_by_verb
		FROM bead_revisions
		WHERE bead_id = ?
		ORDER BY revision_number`, beadID)
	if err != nil {
		return nil, fmt.Errorf("revision log for bead %d: %w", beadID, err)
	}
	defer rows.Close()

	var out []revisionEntry
	for rows.Next() {
		var e revisionEntry
		if err := rows.Scan(&e.RevisionNumber, &e.FullText, &e.ExecutionBudget, &e.MonitorOverride, &e.CreatedByVerb); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
