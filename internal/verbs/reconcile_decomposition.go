package verbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ratchet/internal/db"
	"ratchet/internal/ollama"
)

const reconcileDecompositionSystemPrompt = `You receive a specific critique of a decomposition you authored. For each finding, respond with one of:
- agree_and_fix: the finding is correct; provide the corrected Bead in updated_bead
- disagree: the finding is wrong; provide a specific, stated reason in the reason field

Vague or blanket defenses ("this is by design", "not applicable") are not acceptable for a disagree.
Your updated_beads field must contain the complete decomposition after all fixes are applied,
even if no beads changed (so the next audit has the full current state).

Respond with JSON only, no prose before or after:
{
  "responses": [
    {
      "bead_title": "<title of the affected Bead>",
      "action": "agree_and_fix" | "disagree",
      "reason": "<your reasoning>",
      "updated_bead": { "title": "...", "full_text": "...", "execution_budget": <int>, "monitor_override": "honor"|"ignore" }
    }
  ],
  "updated_beads": [
    { "title": "...", "full_text": "...", "execution_budget": <int>, "monitor_override": "honor"|"ignore" }
  ]
}`

// ReconcileDecomposition stores critique context between Run and Commit so
// Commit can write the round row without a second in-transaction query.
// Safe because the orchestrator runs one job at a time.
type ReconcileDecomposition struct {
	lastCritique    string
	lastRoundsSoFar int
}

func (h *ReconcileDecomposition) Verb() string { return db.VerbReconcileDecomposition }

func (h *ReconcileDecomposition) Run(ctx context.Context, d *db.DB, oc *ollama.Client, job *db.HandoffJob) (string, error) {
	doc, err := loadDesignDoc(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	beads, err := loadCurrentBeads(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	critique, roundsSoFar, err := latestAuditCritique(ctx, d, job.ProjectID)
	if err != nil {
		return "", err
	}
	model, err := loadVerbModel(ctx, d, job.ProjectID, db.VerbReconcileDecomposition)
	if err != nil {
		return "", err
	}

	// Cache for Commit (single-goroutine orchestrator; no race).
	h.lastCritique = critique
	h.lastRoundsSoFar = roundsSoFar

	var sb strings.Builder
	sb.WriteString("## Design Document\n\n")
	sb.WriteString(doc)
	sb.WriteString("\n\n## Current Decomposition\n\n")
	for _, b := range beads {
		fmt.Fprintf(&sb, "### %s\n\n%s\n\n", b.Title, b.FullText)
	}
	sb.WriteString("## Critique to Reconcile\n\n")
	sb.WriteString(critique)

	return oc.Chat(ctx, model, []ollama.Message{
		{Role: "system", Content: reconcileDecompositionSystemPrompt},
		{Role: "user", Content: sb.String()},
	}, nil)
}

func (h *ReconcileDecomposition) Validate(raw string) (string, any) {
	var out ReconcileDecompositionOutput
	if err := json.Unmarshal([]byte(ollama.ExtractJSON(raw)), &out); err != nil {
		return fmt.Sprintf("malformed: JSON parse error: %v", err), nil
	}
	if len(out.Responses) == 0 {
		return "malformed: responses array is empty", nil
	}
	for i, r := range out.Responses {
		if r.Action != "agree_and_fix" && r.Action != "disagree" {
			return fmt.Sprintf("malformed: responses[%d] action must be \"agree_and_fix\" or \"disagree\", got %q", i, r.Action), nil
		}
		if r.Action == "agree_and_fix" && r.UpdatedBead == nil {
			return fmt.Sprintf("malformed: responses[%d] action is agree_and_fix but updated_bead is absent", i), nil
		}
		if r.Action == "disagree" && strings.TrimSpace(r.Reason) == "" {
			return fmt.Sprintf("malformed: responses[%d] action is disagree but reason is empty", i), nil
		}
	}
	if len(out.UpdatedBeads) == 0 {
		return "malformed: updated_beads array is empty", nil
	}
	for i, b := range out.UpdatedBeads {
		if b.ExecutionBudget <= 0 {
			return fmt.Sprintf("malformed: updated_beads[%d] (%s) execution_budget must be a positive integer", i, b.Title), nil
		}
		if b.MonitorOverride != "honor" && b.MonitorOverride != "ignore" {
			return fmt.Sprintf("malformed: updated_beads[%d] (%s) monitor_override must be \"honor\" or \"ignore\", got %q", i, b.Title, b.MonitorOverride), nil
		}
	}
	return "valid", out
}

// Commit writes the audit_reconcile_rounds row, applies any agree_and_fix
// updates to bead_revisions, and enqueues the next job.
//
// Convergence detection (Step 4): the proper check is whether the next AUDIT
// round raises genuinely new findings. For now, all-agree-and-fix declares
// convergence; any disagree continues the loop (or escalates at the cap).
func (h *ReconcileDecomposition) Commit(ctx context.Context, tx *sql.Tx, job *db.HandoffJob, parsed any) error {
	out := parsed.(ReconcileDecompositionOutput)
	now := time.Now().UTC().Format(time.RFC3339)

	var roundCap int
	if err := tx.QueryRowContext(ctx,
		`SELECT audit_reconcile_round_cap FROM projects WHERE id = ?`,
		job.ProjectID,
	).Scan(&roundCap); err != nil {
		return fmt.Errorf("load round cap: %w", err)
	}

	nextRound := h.lastRoundsSoFar + 1
	hasDisagree := false
	for _, r := range out.Responses {
		if r.Action == "disagree" {
			hasDisagree = true
			break
		}
	}

	outcome := "converged"
	if hasDisagree {
		if nextRound >= roundCap {
			outcome = "escalated"
		} else {
			outcome = "disagreed_continuing"
		}
	}

	reconciliationJSON, _ := json.Marshal(out)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_reconcile_rounds
		  (project_id, round_number, critique_text, reconciliation, outcome, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		job.ProjectID, nextRound, h.lastCritique, string(reconciliationJSON), outcome, now,
	); err != nil {
		return fmt.Errorf("insert audit_reconcile_round: %w", err)
	}

	if err := h.applyFixes(ctx, tx, job.ProjectID, out, now); err != nil {
		return err
	}

	switch outcome {
	case "converged":
		return enqueueAllBeadsForExecution(ctx, tx, job.ProjectID, now)
	case "disagreed_continuing":
		_, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, NULL, 'pending', ?, ?)`,
			job.ProjectID, db.VerbAuditDecomposition, now, now)
		return err
	case "escalated":
		// Mark the current job escalated; the orchestrator notifies Mike.
		_, err := tx.ExecContext(ctx,
			`UPDATE handoff_jobs SET status = 'escalated', updated_at = ? WHERE id = ?`,
			now, job.ID)
		return err
	}
	return nil
}

func (h *ReconcileDecomposition) applyFixes(ctx context.Context, tx *sql.Tx, projectID int64, out ReconcileDecompositionOutput, now string) error {
	for _, r := range out.Responses {
		if r.Action != "agree_and_fix" || r.UpdatedBead == nil {
			continue
		}
		var beadID int64
		var currentRevNum int
		if err := tx.QueryRowContext(ctx, `
			SELECT b.id, br.revision_number
			FROM beads b
			JOIN bead_revisions br ON br.id = b.current_revision_id
			WHERE b.project_id = ?
			  AND json_extract(br.full_text, '$.title') = ?`,
			projectID, r.BeadTitle,
		).Scan(&beadID, &currentRevNum); err != nil {
			return fmt.Errorf("find bead %q for fix: %w", r.BeadTitle, err)
		}

		fullText, _ := json.Marshal(r.UpdatedBead)
		res, err := tx.ExecContext(ctx, `
			INSERT INTO bead_revisions
			  (project_id, bead_id, revision_number, full_text,
			   execution_budget, monitor_override, created_by_verb, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, beadID, currentRevNum+1, string(fullText),
			r.UpdatedBead.ExecutionBudget, r.UpdatedBead.MonitorOverride,
			db.VerbReconcileDecomposition, now)
		if err != nil {
			return fmt.Errorf("insert revision for bead %q: %w", r.BeadTitle, err)
		}
		revID, _ := res.LastInsertId()

		if _, err := tx.ExecContext(ctx,
			`UPDATE beads SET current_revision_id = ? WHERE id = ?`, revID, beadID); err != nil {
			return fmt.Errorf("update bead %q current_revision_id: %w", r.BeadTitle, err)
		}
	}
	return nil
}

// enqueueAllBeadsForExecution creates EXECUTE_BEAD jobs for every bead in
// the project. The handler is implemented in Step 3.
func enqueueAllBeadsForExecution(ctx context.Context, tx *sql.Tx, projectID int64, now string) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM beads WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, beadID := range ids {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO handoff_jobs (project_id, verb, bead_id, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', ?, ?)`,
			projectID, db.VerbExecuteBead, beadID, now, now); err != nil {
			return fmt.Errorf("enqueue %s for bead %d: %w", db.VerbExecuteBead, beadID, err)
		}
	}
	return nil
}
