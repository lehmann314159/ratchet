package verbs

import (
	"context"
	"database/sql"
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
		       language, pause_after_reconcile, pause_after_verb, pause_after_bead_id,
		       created_at, updated_at
		FROM projects WHERE id = ?`, projectID)
	p := &db.Project{}
	var createdAt, updatedAt string
	if err := row.Scan(
		&p.ID, &p.Label, &p.FolderPath, &p.DesignDocPath, &p.Status,
		&p.RecoveredFromProjectID,
		&p.MonitorOverrideDefault, &p.ExecutionBudgetDefault,
		&p.AuditReconcileRoundCap, &p.MaxExecutionAttempts,
		&p.Language, &p.PauseAfterReconcile, &p.PauseAfterVerb, &p.PauseAfterBeadID,
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
	OutputFiles     []string
	ExitCriteria    []string
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
			Title        string   `json:"title"`
			OutputFiles  []string `json:"output_files"`
			ExitCriteria []string `json:"exit_criteria"`
		}
		if json.Unmarshal([]byte(s.FullText), &tmp) == nil {
			if tmp.Title != "" {
				s.Title = tmp.Title
			}
			s.OutputFiles = tmp.OutputFiles
			s.ExitCriteria = tmp.ExitCriteria
		}
		if s.Title == "" {
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
		`SELECT COUNT(*) FROM audit_reconcile_rounds WHERE project_id = ? AND outcome NOT IN ('redecompose', 'reconcile_rejected')`,
		projectID,
	).Scan(&roundsSoFar); err != nil {
		return "", 0, fmt.Errorf("round count: %w", err)
	}
	return critique, roundsSoFar, nil
}

// nextRoundNumber returns the next collision-free round_number for projectID:
// a single project-wide sequence spanning every audit_reconcile_rounds row
// regardless of outcome (mechanical 'redecompose' rows and real debate rounds
// alike), so every row for a project gets a number no earlier row ever used.
//
// This is deliberately NOT the same value as the round-cap comparison (see
// latestAuditCritique's roundsSoFar / ReconcileDecomposition.Commit's
// nextRound), which intentionally counts only real debate rounds so a
// mechanical redecompose retry doesn't eat into the debate round budget.
// Conflating the two — as both call sites did before this fix, each computing
// its own COUNT-based value for the stored round_number column — let two
// different rows land on the same round_number: once between a redecompose
// row and a real round (they used disjoint COUNT scopes), and once between
// two real rounds (COUNT-based numbering isn't self-healing once any row's
// stored round_number has ever drifted from its true ordinal position, e.g.
// from a requeue after escalation). MAX+1 is collision-proof by construction
// and self-heals from any prior drift, so this value is now used only for the
// column DECOMPOSE_SPEC/RECONCILE_DECOMPOSITION actually store.
func nextRoundNumber(ctx context.Context, tx *sql.Tx, projectID int64) (int, error) {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(round_number), 0) + 1 FROM audit_reconcile_rounds WHERE project_id = ?`,
		projectID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("next round number: %w", err)
	}
	return n, nil
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

// loadLastValidationFailure returns the validation_result string from the most
// recent invalid handoff_attempt for jobID, or empty string if no prior
// failures exist. Used to inject rejection context into retry prompts.
func loadLastValidationFailure(ctx context.Context, d *db.DB, jobID int64) (string, error) {
	var result string
	err := d.QueryRowContext(ctx, `
		SELECT validation_result FROM handoff_attempts
		WHERE job_id = ? AND validation_result != 'valid'
		ORDER BY attempt_number DESC LIMIT 1`, jobID,
	).Scan(&result)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("last validation failure for job %d: %w", jobID, err)
	}
	return result, nil
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
	ID              int64
	RevisionNumber  int
	FullText        string
	ExecutionBudget int
	MonitorOverride string
	CreatedByVerb   string
	CreatedAt       string
}

// latestSurveyManifest returns the parsed output of the most recent completed
// SURVEY_SPEC job for projectID. Used by VERIFY and CERTIFY.
func latestSurveyManifest(ctx context.Context, d *db.DB, projectID int64) (*SurveySpecOutput, error) {
	var raw string
	if err := d.QueryRowContext(ctx, `
		SELECT ha.raw_output
		FROM handoff_jobs hj
		JOIN handoff_attempts ha ON ha.job_id = hj.id
		WHERE hj.project_id = ? AND hj.verb = ?
		  AND hj.status = 'complete'
		  AND ha.validation_result = 'valid'
		ORDER BY hj.created_at DESC
		LIMIT 1`,
		projectID, db.VerbSurveySpec,
	).Scan(&raw); err != nil {
		return nil, fmt.Errorf("latest survey manifest for project %d: %w", projectID, err)
	}
	var out SurveySpecOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("parse survey manifest: %w", err)
	}
	return &out, nil
}

// latestVerifyAttempt returns the parsed output of the most recent completed
// VERIFY_MANIFEST job for projectID. Used by CERTIFY.
func latestVerifyAttempt(ctx context.Context, d *db.DB, projectID int64) (*VerifyManifestOutput, error) {
	var raw string
	if err := d.QueryRowContext(ctx, `
		SELECT ha.raw_output
		FROM handoff_jobs hj
		JOIN handoff_attempts ha ON ha.job_id = hj.id
		WHERE hj.project_id = ? AND hj.verb = ?
		  AND hj.status = 'complete'
		  AND ha.validation_result = 'valid'
		ORDER BY hj.created_at DESC
		LIMIT 1`,
		projectID, db.VerbVerifyManifest,
	).Scan(&raw); err != nil {
		return nil, fmt.Errorf("latest verify attempt for project %d: %w", projectID, err)
	}
	var out VerifyManifestOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse verify output: %w", err)
	}
	return &out, nil
}

// latestCertifyFeedback returns the feedback string from the most recent
// CERTIFY_MANIFEST rejection for projectID, or "" if no rejection exists.
// Used by SURVEY to include retry guidance.
func latestCertifyFeedback(ctx context.Context, d *db.DB, projectID int64) (string, error) {
	var feedback string
	err := d.QueryRowContext(ctx, `
		SELECT COALESCE(feedback, '') FROM certifications
		WHERE project_id = ? AND final_decision = 'reject'
		ORDER BY created_at DESC
		LIMIT 1`,
		projectID,
	).Scan(&feedback)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("latest certify feedback for project %d: %w", projectID, err)
	}
	return feedback, nil
}

// loadVerbModelOrFallback returns the model assigned to verb for projectID,
// falling back to fallbackVerb if verb has no assignment. Used for verbs added
// after a project was created; the table migration seeds the assignment, but
// this fallback guards against any gap.
func loadVerbModelOrFallback(ctx context.Context, d *db.DB, projectID int64, verb, fallbackVerb string) (string, error) {
	model, err := loadVerbModel(ctx, d, projectID, verb)
	if err == nil {
		return model, nil
	}
	return loadVerbModel(ctx, d, projectID, fallbackVerb)
}

// enqueueBeadExecution enqueues REFINE_TESTS_A if the bead has *_test.go output
// files (so tests are certified before implementation starts), or EXECUTE_BEAD
// otherwise. Called from reconcile_decomposition, revise_pending, and resume.
func enqueueBeadExecution(ctx context.Context, tx *sql.Tx, projectID, beadID int64, now string) error {
	var fullText string
	if err := tx.QueryRowContext(ctx, `
		SELECT br.full_text FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID,
	).Scan(&fullText); err != nil {
		return fmt.Errorf("load bead %d for enqueue decision: %w", beadID, err)
	}
	verb := db.VerbExecuteBead
	if beadSpecHasTestFiles(fullText) {
		verb = db.VerbRefineTestsWrite
	}
	var err error
	if verb == db.VerbRefineTestsWrite {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, refinement_cycle_id, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', 1, ?, ?)`,
			projectID, verb, beadID, now, now)
	} else {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			projectID, verb, beadID, now, now)
	}
	return err
}

// shouldPauseAfterVerb reports whether projectID's pause_after_verb matches verb.
// Callers must enqueue the verb's normal next handoff_job *before* calling this
// and, if it returns true, call pauseProject instead of returning nil — the
// orchestrator only polls status='active' projects, so a 'pending' job left
// sitting in a paused project is inert until the project resumes.
func shouldPauseAfterVerb(ctx context.Context, tx *sql.Tx, projectID int64, verb string) (bool, error) {
	var pauseAfterVerb sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT pause_after_verb FROM projects WHERE id = ?`, projectID,
	).Scan(&pauseAfterVerb); err != nil {
		return false, fmt.Errorf("load pause_after_verb: %w", err)
	}
	return pauseAfterVerb.Valid && pauseAfterVerb.String == verb, nil
}

// shouldPauseAfterBead reports whether projectID's pause_after_bead_id matches
// beadID. Same enqueue-then-check calling convention as shouldPauseAfterVerb.
func shouldPauseAfterBead(ctx context.Context, tx *sql.Tx, projectID, beadID int64) (bool, error) {
	var pauseAfterBeadID sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT pause_after_bead_id FROM projects WHERE id = ?`, projectID,
	).Scan(&pauseAfterBeadID); err != nil {
		return false, fmt.Errorf("load pause_after_bead_id: %w", err)
	}
	return pauseAfterBeadID.Valid && pauseAfterBeadID.Int64 == beadID, nil
}

// pauseProject sets projectID's status to 'paused'. Called after the normal
// next handoff_job has already been enqueued, so resuming (a plain status
// flip back to 'active') needs no reconstruction logic.
func pauseProject(ctx context.Context, tx *sql.Tx, projectID int64, now string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE projects SET status = 'paused', updated_at = ? WHERE id = ?`,
		now, projectID)
	return err
}

// beadSpecHasTestFiles returns true when the bead spec's output_files includes
// at least one *_test.go file — indicating REFINE_TESTS should run first.
func beadSpecHasTestFiles(fullText string) bool {
	var spec struct {
		OutputFiles []string `json:"output_files"`
	}
	if json.Unmarshal([]byte(fullText), &spec) != nil {
		return false
	}
	return hasTestGoFile(spec.OutputFiles)
}

// beadHasRefinements returns true if any test_refinements rows exist for beadID.
// Used to prevent the old test-first machinery from firing on post-REFINE_TESTS beads.
func beadHasRefinements(ctx context.Context, d *db.DB, beadID int64) bool {
	var count int
	_ = d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM test_refinements WHERE bead_id = ?`, beadID,
	).Scan(&count)
	return count > 0
}

// loadBeadByID returns a single bead's current state by bead ID.
func loadBeadByID(ctx context.Context, d *db.DB, beadID int64) (*beadState, error) {
	var s beadState
	if err := d.QueryRowContext(ctx, `
		SELECT b.id, br.full_text, br.execution_budget, br.monitor_override, br.revision_number
		FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.id = ?`, beadID,
	).Scan(&s.BeadID, &s.FullText, &s.ExecutionBudget, &s.MonitorOverride, &s.RevisionNumber); err != nil {
		return nil, fmt.Errorf("load bead %d: %w", beadID, err)
	}
	var tmp struct {
		Title        string   `json:"title"`
		OutputFiles  []string `json:"output_files"`
		ExitCriteria []string `json:"exit_criteria"`
	}
	if json.Unmarshal([]byte(s.FullText), &tmp) == nil {
		if tmp.Title != "" {
			s.Title = tmp.Title
		}
		s.OutputFiles = tmp.OutputFiles
		s.ExitCriteria = tmp.ExitCriteria
	}
	if s.Title == "" {
		s.Title = fmt.Sprintf("bead-%d", beadID)
	}
	return &s, nil
}

// loadPendingBeads returns all pending beads for projectID with their current revision.
func loadPendingBeads(ctx context.Context, d *db.DB, projectID int64) ([]beadState, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT b.id, br.full_text, br.execution_budget, br.monitor_override, br.revision_number
		FROM beads b
		JOIN bead_revisions br ON br.id = b.current_revision_id
		WHERE b.project_id = ? AND b.status = 'pending'
		ORDER BY b.id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("load pending beads: %w", err)
	}
	defer rows.Close()

	var out []beadState
	for rows.Next() {
		var s beadState
		if err := rows.Scan(&s.BeadID, &s.FullText, &s.ExecutionBudget, &s.MonitorOverride, &s.RevisionNumber); err != nil {
			return nil, err
		}
		var tmp struct {
			Title        string   `json:"title"`
			OutputFiles  []string `json:"output_files"`
			ExitCriteria []string `json:"exit_criteria"`
		}
		if json.Unmarshal([]byte(s.FullText), &tmp) == nil {
			if tmp.Title != "" {
				s.Title = tmp.Title
			}
			s.OutputFiles = tmp.OutputFiles
			s.ExitCriteria = tmp.ExitCriteria
		}
		if s.Title == "" {
			s.Title = fmt.Sprintf("bead-%d", s.BeadID)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// loadBeadRevisionLog returns the revision log for beadID, showing only the
// current lineage. rewind-bead resets a bead's current_revision_id back to
// revision 1 without deleting the higher-numbered revisions that led to the
// escalation — those rows stay in the table (other rows, like old executions,
// still reference them by id) but are no longer relevant context. Left
// unfiltered, ADJUDICATE would see the full pre-rewind history as if it were
// still live, which is both misleading (a stale "current" spec gets treated
// as authoritative) and bloats the prompt unnecessarily.
//
// The lineage boundary is tracked explicitly via beads.rewound_at, set by
// rewind-bead. An earlier version of this function tried to infer the
// boundary from revision_number alone (a row whose number doesn't exceed the
// running max), but every bead_revisions insert site (ADJUDICATE's
// execute_revised/test_reject, REVISE_PENDING, RECONCILE) numbers new
// revisions via a bead-wide MAX(revision_number)+1 specifically to avoid
// collisions with stale post-rewind rows — which means revision_number is
// always strictly increasing across the whole history, rewinds included, and
// the old heuristic could never actually fire. Found during the stage-5 audit
// (2026-07-14): confirmed by reproduction that the old currentLineage always
// returned the full, unfiltered history. rewound_at fixes this durably and
// also lifts the old "only catches the first rewind" limitation, since it's
// overwritten on every rewind rather than inferred from data written for an
// unrelated purpose.
func loadBeadRevisionLog(ctx context.Context, d *db.DB, beadID int64) ([]revisionEntry, error) {
	var rewoundAt sql.NullString
	if err := d.QueryRowContext(ctx,
		`SELECT rewound_at FROM beads WHERE id = ?`, beadID,
	).Scan(&rewoundAt); err != nil {
		return nil, fmt.Errorf("load rewound_at for bead %d: %w", beadID, err)
	}

	rows, err := d.QueryContext(ctx, `
		SELECT id, revision_number, full_text, execution_budget, monitor_override, created_by_verb, created_at
		FROM bead_revisions
		WHERE bead_id = ?
		ORDER BY id`, beadID)
	if err != nil {
		return nil, fmt.Errorf("revision log for bead %d: %w", beadID, err)
	}
	defer rows.Close()

	var all []revisionEntry
	for rows.Next() {
		var e revisionEntry
		if err := rows.Scan(&e.ID, &e.RevisionNumber, &e.FullText, &e.ExecutionBudget, &e.MonitorOverride, &e.CreatedByVerb, &e.CreatedAt); err != nil {
			return nil, err
		}
		all = append(all, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return currentLineage(all, rewoundAt), nil
}

// currentLineage trims a bead's revisions (ordered by id, i.e. creation
// order) down to the segment created since the most recent rewind. Revision 1
// is always kept regardless of rewoundAt — it's the bead's origin story and
// rewind-bead's own merged revision (created_by_verb='REWIND_BEAD') already
// falls inside the created_at >= rewoundAt window on its own, so this isn't
// load-bearing for reaching the post-rewind spec, just deliberately always
// visible. A bead that was never rewound (rewoundAt not valid) returns the
// full history unchanged.
func currentLineage(all []revisionEntry, rewoundAt sql.NullString) []revisionEntry {
	if !rewoundAt.Valid {
		return all
	}
	lineage := make([]revisionEntry, 0, len(all))
	for _, e := range all {
		if e.RevisionNumber == 1 || e.CreatedAt >= rewoundAt.String {
			lineage = append(lineage, e)
		}
	}
	return lineage
}

// currentLineageRevisionIDs returns the bead_revisions.id set for beadID's
// current lineage (see currentLineage) — used to filter executions/analyses
// history down to attempts against the current lineage, the same way
// loadBeadRevisionLog filters the spec text itself. Without this, a rewound
// bead's diff signal and compressed history would keep citing pre-rewind
// attempts as if they were still live.
func currentLineageRevisionIDs(ctx context.Context, d *db.DB, beadID int64) (map[int64]bool, error) {
	entries, err := loadBeadRevisionLog(ctx, d, beadID)
	if err != nil {
		return nil, err
	}
	ids := make(map[int64]bool, len(entries))
	for _, e := range entries {
		ids[e.ID] = true
	}
	return ids, nil
}
