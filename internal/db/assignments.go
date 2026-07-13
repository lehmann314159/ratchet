package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SetVerbModelAssignment upserts a verb→model assignment for a project,
// enforcing the four model-assignment constraints from the architecture:
//
//  1. model(DECOMPOSE_SPEC) == model(RECONCILE_DECOMPOSITION)
//     The reconciling model must be the decomposing model; otherwise a
//     DISAGREE outcome is not self-review at all, just a second opinion.
//
//  2. model(AUDIT_DECOMPOSITION) != model(DECOMPOSE_SPEC)
//     The auditing model must differ from the decomposing model to
//     constitute a real cross-review (OQ-049's basis).
//
//  3. model(EXECUTE_BEAD) != model(ANALYZE_EXECUTION)
//     The executing model authors the work; the analyzing model reviews it.
//     Using the same model for both removes the independent review.
//
//  4. model(CERTIFY_MANIFEST) != model(SURVEY_SPEC)
//     The certifying model must differ from the surveying model; a model
//     cannot provide independent review of its own output.
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

// SeedVerbModelAssignments writes all verb→model assignments for a new
// project in a single transaction, enforcing all four model-assignment
// constraints.
func SeedVerbModelAssignments(ctx context.Context, tx *sql.Tx, projectID int64) error {
	assignments := map[string]string{
		VerbSurveySpec:              "gemma4:31b",
		VerbCertifyManifest:         "qwen3:32b",
		VerbDecomposeSpec:           "gemma4:31b",
		VerbAuditDecomposition:      "qwen3:32b",
		VerbReconcileDecomposition:  "gemma4:31b",
		VerbExecuteBead:             "gemma4:31b",
		VerbMonitorExecution:        "mistral-small3.2:24b",
		VerbAnalyzeExecution:        "qwen3:32b",
		VerbCompressAnalysis:        "mistral-small3.2:24b",
		VerbAdjudicateNextExecution: "gemma4:31b",
		VerbRevisePending:           "qwen3:32b",
		VerbRefineTestsWrite:        "gemma4:31b",
		VerbRefineTestsCritique:     "qwen3:32b",
		VerbRefineTestsJudge:        "gemma4:31b",
	}

	// Validate constraints before writing anything.
	if err := checkModelConstraints(assignments); err != nil {
		return fmt.Errorf("seed rejected: %w", err)
	}

	for verb, model := range assignments {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (?, ?, ?)`,
			projectID, verb, model,
		); err != nil {
			return fmt.Errorf("seed %s: %w", verb, err)
		}
	}
	return nil
}

// SeedVerbModelAssignmentsFromFleet writes verb→model assignments from an
// explicit fleet map, enforcing the same four constraints as the default seed.
// Every verb in AllVerbs must be present. Fleet files that predate the addition
// of SURVEY_SPEC and CERTIFY_MANIFEST automatically inherit fallback defaults:
// SURVEY_SPEC → same model as DECOMPOSE_SPEC; CERTIFY_MANIFEST → same model
// as AUDIT_DECOMPOSITION. Note: the fallback may violate constraint 4 if
// DECOMPOSE_SPEC == AUDIT_DECOMPOSITION; prefer explicit fleet entries.
func SeedVerbModelAssignmentsFromFleet(ctx context.Context, tx *sql.Tx, projectID int64, fleet map[string]string) error {
	// Copy to avoid mutating the caller's map.
	enriched := make(map[string]string, len(fleet)+2)
	for k, v := range fleet {
		enriched[k] = v
	}
	if _, ok := enriched[VerbSurveySpec]; !ok {
		if m, ok := enriched[VerbDecomposeSpec]; ok {
			enriched[VerbSurveySpec] = m
		}
	}
	if _, ok := enriched[VerbCertifyManifest]; !ok {
		if m, ok := enriched[VerbAuditDecomposition]; ok {
			enriched[VerbCertifyManifest] = m
		}
	}
	if _, ok := enriched[VerbRevisePending]; !ok {
		if m, ok := enriched[VerbAuditDecomposition]; ok {
			enriched[VerbRevisePending] = m
		}
	}
	// REFINE_TESTS_WRITE/JUDGE: writer and judge → EXECUTE model (gemma4:31b in live fleet).
	// REFINE_TESTS_CRITIQUE: critic → AUDIT model (qwen3:32b in live fleet).
	if _, ok := enriched[VerbRefineTestsWrite]; !ok {
		if m, ok := enriched[VerbExecuteBead]; ok {
			enriched[VerbRefineTestsWrite] = m
		}
	}
	if _, ok := enriched[VerbRefineTestsCritique]; !ok {
		if m, ok := enriched[VerbAuditDecomposition]; ok {
			enriched[VerbRefineTestsCritique] = m
		}
	}
	if _, ok := enriched[VerbRefineTestsJudge]; !ok {
		if m, ok := enriched[VerbExecuteBead]; ok {
			enriched[VerbRefineTestsJudge] = m
		}
	}
	for _, v := range AllVerbs {
		if _, ok := enriched[v]; !ok {
			return fmt.Errorf("fleet missing verb %q", v)
		}
	}
	if err := checkModelConstraints(enriched); err != nil {
		return fmt.Errorf("fleet rejected: %w", err)
	}
	for _, v := range AllVerbs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO verb_model_assignments (project_id, verb, model) VALUES (?, ?, ?)`,
			projectID, v, enriched[v],
		); err != nil {
			return fmt.Errorf("seed %s: %w", v, err)
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

// checkModelConstraints validates the four model-assignment constraints.
// A partial assignment (not all verbs present) is allowed; constraints
// are only violated if both verbs in a pair are present and conflict.
func checkModelConstraints(m map[string]string) error {
	decompose, hasDecompose := m[VerbDecomposeSpec]
	reconcile, hasReconcile := m[VerbReconcileDecomposition]
	audit, hasAudit := m[VerbAuditDecomposition]
	execute, hasExecute := m[VerbExecuteBead]
	analyze, hasAnalyze := m[VerbAnalyzeExecution]
	survey, hasSurvey := m[VerbSurveySpec]
	certify, hasCertify := m[VerbCertifyManifest]
	refineWrite, hasRefineWrite := m[VerbRefineTestsWrite]
	refineCtiq, hasRefineCtiq := m[VerbRefineTestsCritique]

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
	if hasExecute && hasAnalyze && execute == analyze {
		return errors.New(
			"EXECUTE_BEAD and ANALYZE_EXECUTION must use different models " +
				"(executing model authors the work; analyzing model reviews it independently)",
		)
	}
	if hasSurvey && hasCertify && certify == survey {
		return errors.New(
			"CERTIFY_MANIFEST must use a different model from SURVEY_SPEC " +
				"(a model cannot independently review its own manifest output)",
		)
	}
	if hasRefineWrite && hasRefineCtiq && refineWrite == refineCtiq {
		return errors.New(
			"REFINE_TESTS_WRITE and REFINE_TESTS_CRITIQUE must use different models " +
				"(writer and critic must be independent)",
		)
	}
	return nil
}
