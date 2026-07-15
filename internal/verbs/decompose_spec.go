package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/guidance"
	"ratchet/internal/ollama"
)

// decomposeRedecomposeCap bounds how many times DECOMPOSE_SPEC will reject
// its own output and retry after forwardFileReferenceChecks finds a
// bead-ordering violation, before giving up and full-stopping the project.
const decomposeRedecomposeCap = 3

type DecomposeSpec struct {
	budgetDefault int
	folderPath    string
}

func (h *DecomposeSpec) Verb() string { return db.VerbDecomposeSpec }

func (h *DecomposeSpec) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	doc, err := loadDesignDoc(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbDecomposeSpec)
	if err != nil {
		return "", err
	}
	project, err := loadProject(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	h.budgetDefault = project.ExecutionBudgetDefault
	h.folderPath = project.FolderPath

	surveyDocPath := filepath.Join(project.FolderPath, "survey.md")
	surveyDoc, err := os.ReadFile(surveyDocPath)
	if err != nil {
		return "", fmt.Errorf("read survey.md at %s: %w", surveyDocPath, err)
	}

	redecomposeFeedback, err := latestRedecomposeFeedback(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}

	userMsg := buildDecomposeUserMsg(doc, string(surveyDoc), redecomposeFeedback)
	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: guidance.InjectForVerbPath(decomposeSpecSystemPrompt(project.Language), project.FolderPath, db.VerbDecomposeSpec, "")},
		{Role: "user", Content: userMsg},
	}, nil)
}

// latestRedecomposeFeedback returns the critique_text of the most recent
// 'redecompose' audit_reconcile_rounds row for projectID, or "" if this
// project's decomposition has never been mechanically rejected.
func latestRedecomposeFeedback(ctx context.Context, d *db.DB, projectID int64) (string, error) {
	var text string
	err := d.QueryRowContext(ctx,
		`SELECT critique_text FROM audit_reconcile_rounds
		 WHERE project_id = ? AND outcome = 'redecompose'
		 ORDER BY id DESC LIMIT 1`, projectID,
	).Scan(&text)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load redecompose feedback: %w", err)
	}
	return text, nil
}

func buildDecomposeUserMsg(designDoc, surveyDoc, redecomposeFeedback string) string {
	var sb strings.Builder
	if redecomposeFeedback != "" {
		sb.WriteString("## Previous Decomposition Attempt Was Rejected\n\n")
		sb.WriteString("Your last decomposition had bead-ordering violations that made beads structurally ")
		sb.WriteString("unable to pass no matter how many times they were executed. Fix these in this attempt:\n\n")
		sb.WriteString(redecomposeFeedback)
		sb.WriteString("\n\n")
	}
	sb.WriteString("## Survey Document\n\n")
	sb.WriteString(surveyDoc)
	sb.WriteString("\n\n## Design Document\n\n")
	sb.WriteString(designDoc)
	return sb.String()
}

func (h *DecomposeSpec) Validate(raw string) (string, any) {
	var out DecomposeSpecOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if len(out.Beads) == 0 {
		return "malformed: beads array is empty", nil
	}
	seenTitles := make(map[string]int, len(out.Beads))
	for i, b := range out.Beads {
		if b.Title == "" {
			return fmt.Sprintf("malformed: bead[%d] missing title", i), nil
		}
		if b.FullText == "" {
			return fmt.Sprintf("malformed: bead[%d] (%s) missing full_text", i, b.Title), nil
		}
		if b.MonitorOverride != "honor" && b.MonitorOverride != "ignore" {
			return fmt.Sprintf("malformed: bead[%d] (%s) monitor_override must be \"honor\" or \"ignore\", got %q", i, b.Title, b.MonitorOverride), nil
		}
		if len(b.OutputFiles) == 0 {
			return fmt.Sprintf("malformed: bead[%d] (%s) output_files is missing or empty", i, b.Title), nil
		}
		if len(b.ExitCriteria) == 0 {
			return fmt.Sprintf("malformed: bead[%d] (%s) exit_criteria is missing or empty", i, b.Title), nil
		}
		// Every downstream title-keyed lookup (RevisePending's revisionMap,
		// AUDIT/RECONCILE's own per-title maps) assumes titles are unique
		// within a project — bead titles aren't a separate DB column, so this
		// is the only point where a collision can be caught mechanically.
		if prev, dup := seenTitles[b.Title]; dup {
			return fmt.Sprintf("malformed: bead[%d] and bead[%d] both use title %q — every bead title must be unique", prev, i, b.Title), nil
		}
		seenTitles[b.Title] = i
	}
	return "valid", out
}

func (h *DecomposeSpec) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(DecomposeSpecOutput)
	now := time.Now().UTC().Format(time.RFC3339)

	if violations := forwardFileReferenceChecks(out.Beads); len(violations) > 0 {
		return h.commitRedecompose(ctx, tx, job, violations, now)
	}

	var allOutputFiles []string
	for _, pb := range out.Beads {
		allOutputFiles = append(allOutputFiles, pb.OutputFiles...)
	}
	lang := detectLang(h.folderPath, allOutputFiles)
	for _, pb := range out.Beads {
		applyMechanicalBeadFixes(lang, &pb)

		// Write the bead row first (current_revision_id NULL until revision exists).
		res, err := tx.ExecContext(ctx, `
			INSERT INTO beads (project_id, status, current_revision_id)
			VALUES (?, 'pending', NULL)`, job.ProjectID)
		if err != nil {
			return fmt.Errorf("insert bead %q: %w", pb.Title, err)
		}
		beadID, _ := res.LastInsertId()

		// Write full_text as a JSON object so the title is preserved alongside
		// the spec text (the title field isn't a separate column in bead_revisions).
		fullText, err := json.Marshal(pb)
		if err != nil {
			return fmt.Errorf("marshal bead %q: %w", pb.Title, err)
		}

		res, err = tx.ExecContext(ctx, `
			INSERT INTO bead_revisions
			  (project_id, bead_id, revision_number, full_text,
			   execution_budget, monitor_override, created_by_verb, created_at)
			VALUES (?, ?, 1, ?, ?, ?, ?, ?)`,
			job.ProjectID, beadID, string(fullText),
			h.budgetDefault, pb.MonitorOverride,
			db.VerbDecomposeSpec, now)
		if err != nil {
			return fmt.Errorf("insert revision for bead %q: %w", pb.Title, err)
		}
		revID, _ := res.LastInsertId()

		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
			return fmt.Errorf("set current_revision_id for bead %q: %w", pb.Title, err)
		}
	}

	// Enqueue AUDIT_DECOMPOSITION (project-scoped, bead_id NULL).
	_, err := tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		job.ProjectID, db.VerbAuditDecomposition, now, now)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", db.VerbAuditDecomposition, err)
	}

	// Log ambiguities if any.
	if len(out.Ambiguities) > 0 {
		// Store as a project-level note in the project label for now;
		// a dedicated table is a future enhancement.
		note := "AMBIGUITIES: " + strings.Join(out.Ambiguities, "; ")
		if _, err := tx.ExecContext(ctx,
			`UPDATE projects SET label = label || ? WHERE id = ?`,
			" | "+note, job.ProjectID); err != nil {
			return fmt.Errorf("record ambiguities: %w", err)
		}
	}

	return nil
}

// commitRedecompose rejects a decomposition that failed forwardFileReferenceChecks:
// no bead row is written for this attempt, a 'redecompose' audit_reconcile_rounds
// row records the violations (read back by latestRedecomposeFeedback on the next
// attempt), and either another DECOMPOSE_SPEC job is enqueued or, once
// decomposeRedecomposeCap is reached, the project is full-stopped for human
// review — mirroring CertifyManifest.commitReject's SURVEY_SPEC retry-then-give-up
// pattern.
func (h *DecomposeSpec) commitRedecompose(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, violations []string, now string) error {
	var attemptCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_reconcile_rounds WHERE project_id = ? AND outcome = 'redecompose'`,
		job.ProjectID,
	).Scan(&attemptCount); err != nil {
		return fmt.Errorf("count redecompose attempts: %w", err)
	}
	attemptCount++

	roundNumber, err := nextRoundNumber(ctx, tx, job.ProjectID)
	if err != nil {
		return err
	}

	critique := "Bead ordering violations (structural, mechanically detected — not a model judgment call):\n- " +
		strings.Join(violations, "\n- ")

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_reconcile_rounds (project_id, round_number, critique_text, reconciliation, outcome, created_at)
		VALUES (?, ?, ?, '', 'redecompose', ?)`,
		job.ProjectID, roundNumber, critique, now,
	); err != nil {
		return fmt.Errorf("insert redecompose round: %w", err)
	}

	if attemptCount >= decomposeRedecomposeCap {
		slog.Error("ESCALATION — DECOMPOSE_SPEC: bead-ordering violations persisted after cap",
			"project_id", job.ProjectID, "cap", decomposeRedecomposeCap, "violations", violations)
		_, err := tx.ExecContext(ctx,
			`UPDATE projects SET status = 'full_stopped', updated_at = ? WHERE id = ?`,
			now, job.ProjectID)
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
		VALUES (?, ?, NULL, 'pending', ?, ?)`,
		job.ProjectID, db.VerbDecomposeSpec, now, now)
	if err != nil {
		return fmt.Errorf("enqueue retry %s: %w", db.VerbDecomposeSpec, err)
	}
	return nil
}
