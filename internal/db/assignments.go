package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SetVerbModelAssignment upserts a verb→model assignment for a project,
// enforcing the two model-assignment constraints from the architecture:
//
//  1. model(DECOMPOSE_SPEC) == model(RECONCILE_DECOMPOSITION)
//     The reconciling model must be the decomposing model; otherwise a
//     DISAGREE outcome is not self-review at all, just a second opinion.
//
//  2. model(AUDIT_DECOMPOSITION) != model(DECOMPOSE_SPEC)
//     The auditing model must differ from the decomposing model to
//     constitute a real cross-review (OQ-049's basis).
func (db *DB) SetVerbModelAssignment(ctx context.Context, projectID int64, verb, model string) error {
	existing, err := db.verbModelMap(ctx, projectID)
	if err != nil {
		return err
	}
	// Apply the proposed assignment in a local copy before checking constraints.
	proposed := make(map[string]string, len(existing))
	for k, v := range existing {
		proposed[k] = v
	}
	proposed[verb] = model

	if err := checkModelConstraints(proposed); err != nil {
		return fmt.Errorf("assignment rejected: %w", err)
	}

	_, err = db.ExecContext(ctx,
		`INSERT INTO verb_model_assignments (project_id, verb, model)
		 VALUES (?, ?, ?)
		 ON CONFLICT (project_id, verb) DO UPDATE SET model = excluded.model`,
		projectID, verb, model,
	)
	return err
}

// SeedVerbModelAssignments writes all 8 verb→model assignments for a new
// project in a single transaction, enforcing both model-assignment constraints.
// Assignments are the validated model fleet from Experiments 1–5.
//
// adjudicateModel is passed explicitly because both glm-4.7-flash and
// gemma4:31b are validated for ADJUDICATE_NEXT_EXECUTION (Experiment 5).
// The seeded project default is gemma4:31b (GLM showed an internal-consistency
// failure on the hardest Exp-5 case — a self-contradicting field declaration,
// exactly the failure mode the architecture's consistency check targets).
func SeedVerbModelAssignments(ctx context.Context, tx *sql.Tx, projectID int64) error {
	assignments := map[string]string{
		VerbDecomposeSpec:           "glm-4.7-flash",
		VerbAuditDecomposition:      "gemma4:31b",
		VerbReconcileDecomposition:  "glm-4.7-flash",
		VerbExecuteBead:             "",  // no model; EXECUTE_BEAD is a subprocess, not a model call
		VerbMonitorExecution:        "mistral-small3.2:24b",
		VerbAnalyzeExecution:        "Qwen3-Coder-30B-A3B",
		VerbCompressAnalysis:        "gemma4:31b",
		VerbAdjudicateNextExecution: "gemma4:31b",
	}

	// Validate constraints before writing anything.
	if err := checkModelConstraints(assignments); err != nil {
		return fmt.Errorf("seed rejected: %w", err)
	}

	for verb, model := range assignments {
		if model == "" {
			continue // EXECUTE_BEAD has no model assignment
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (?, ?, ?)`,
			projectID, verb, model,
		); err != nil {
			return fmt.Errorf("seed %s: %w", verb, err)
		}
	}
	return nil
}

// verbModelMap returns the current verb→model map for a project.
func (db *DB) verbModelMap(ctx context.Context, projectID int64) (map[string]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT verb, model FROM verb_model_assignments WHERE project_id = ?`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := make(map[string]string)
	for rows.Next() {
		var verb, model string
		if err := rows.Scan(&verb, &model); err != nil {
			return nil, err
		}
		m[verb] = model
	}
	return m, rows.Err()
}

// checkModelConstraints validates the two model-assignment constraints.
// A partial assignment (not all 8 verbs present) is allowed; constraints
// are only violated if both verbs in a pair are present and mismatched.
func checkModelConstraints(m map[string]string) error {
	decompose, hasDecompose := m[VerbDecomposeSpec]
	reconcile, hasReconcile := m[VerbReconcileDecomposition]
	audit, hasAudit := m[VerbAuditDecomposition]

	if hasDecompose && hasReconcile && decompose != reconcile {
		return errors.New(
			"DECOMPOSE_SPEC and RECONCILE_DECOMPOSITION must share the same model " +
				"(reconciling model must be the decomposing model for self-review framing to hold)",
		)
	}
	if hasDecompose && hasAudit && audit == decompose {
		return errors.New(
			"AUDIT_DECOMPOSITION must use a different model from DECOMPOSE_SPEC " +
				"(cross-review requires a distinct auditing model — OQ-049)",
		)
	}
	return nil
}
